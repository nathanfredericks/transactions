/**
 * Human-like behavioral layer for cloakbrowser (JS/TS).
 *
 * Activated via humanize: true in launch() / launchContext().
 * Patches page methods to use Bezier mouse curves, realistic typing, and smooth scrolling.
 *
 * Stealth-aware (fixes #110):
 *   - isInputElement / isSelectorFocused use CDP Isolated Worlds instead of page.evaluate
 *   - Shift symbol typing uses CDP Input.dispatchKeyEvent for isTrusted=true events
 *   - Falls back to page.evaluate only when CDP session is unavailable
 *
 * Patches all interaction methods:
 * click, dblclick, hover, type, fill, check, uncheck, selectOption,
 * press, pressSequentially, tap, dragTo, clear + Frame-level equivalents.
 *
 * ELEMENTHANDLE-LEVEL:
 *   click, dblclick, hover, type, fill, press, selectOption,
 *   check, uncheck, setChecked, tap, focus
 *   + $, $$, waitForSelector (nested elements are also patched)
 *
 * page.$(), page.$$(), page.waitForSelector() and Frame equivalents
 * return patched ElementHandles automatically.
 */
import { rand, randRange, sleep } from './config.js';
import { humanMove, humanClick, clickTarget, humanIdle } from './mouse.js';
import { humanType } from './keyboard.js';
import { scrollToElement } from './scroll.js';
import { patchPageElementHandles, patchFrameElementHandles } from './elementhandle.js';
export { resolveConfig } from './config.js';
export { humanMove, humanClick, clickTarget, humanIdle } from './mouse.js';
export { humanType } from './keyboard.js';
export { scrollToElement } from './scroll.js';
export { patchSingleElementHandle } from './elementhandle.js';
// --- Platform-aware select-all shortcut (macOS uses Meta, others use Control) ---
const SELECT_ALL = process.platform === 'darwin' ? 'Meta+a' : 'Control+a';
// ============================================================================
// CDP Isolated World — stealth DOM evaluation
// ============================================================================
/**
 * Manages a CDP isolated execution context for DOM reads.
 * Produces clean Error.stack traces (no 'eval at evaluate :302:')
 * and is invisible to querySelector monkey-patches in the main world.
 *
 * Context ID is invalidated on navigation and auto-recreated on next call.
 */
class StealthEval {
    cdp = null;
    contextId = null;
    page;
    constructor(page) {
        this.page = page;
    }
    async ensureCdp() {
        if (!this.cdp) {
            this.cdp = await this.page.context().newCDPSession(this.page);
        }
        return this.cdp;
    }
    async createWorld() {
        const cdp = await this.ensureCdp();
        const tree = await cdp.send('Page.getFrameTree');
        const frameId = tree.frameTree.frame.id;
        const result = await cdp.send('Page.createIsolatedWorld', {
            frameId,
            worldName: '',
            grantUniveralAccess: true,
        });
        const ctxId = result.executionContextId;
        this.contextId = ctxId;
        return ctxId;
    }
    /**
     * Evaluate a JS expression in the isolated world.
     * Auto-recreates the world if the context was invalidated (navigation).
     * Returns the result value, or undefined on failure.
     */
    async evaluate(expression) {
        if (this.contextId === null) {
            await this.createWorld();
        }
        for (let attempt = 0; attempt < 2; attempt++) {
            try {
                const cdp = await this.ensureCdp();
                const result = await cdp.send('Runtime.evaluate', {
                    expression,
                    contextId: this.contextId,
                    returnByValue: true,
                });
                if (result.exceptionDetails) {
                    // Context was likely invalidated by navigation
                    if (attempt === 0) {
                        await this.createWorld();
                        continue;
                    }
                    return undefined;
                }
                return result.result?.value;
            }
            catch {
                if (attempt === 0) {
                    this.contextId = null;
                    try {
                        await this.createWorld();
                    }
                    catch {
                        return undefined;
                    }
                    continue;
                }
                return undefined;
            }
        }
        return undefined;
    }
    /** Mark context as stale — call after navigation. */
    invalidate() {
        this.contextId = null;
    }
    /** Get the underlying CDP session (reused for Input.dispatchKeyEvent etc.). */
    async getCdpSession() {
        return this.ensureCdp();
    }
}
// ============================================================================
// Cursor state
// ============================================================================
class CursorState {
    x = 0;
    y = 0;
    initialized = false;
}
// ============================================================================
// Stealth DOM queries — isolated world with evaluate fallback
// ============================================================================
/**
 * Check if selector matches an input/textarea/contenteditable element.
 * Uses CDP Isolated World when available — invisible to main world.
 */
async function isInputElement(stealth, page, selector) {
    if (stealth) {
        try {
            const escaped = JSON.stringify(selector);
            const result = await stealth.evaluate(`
        (() => {
          const el = document.querySelector(${escaped});
          if (!el) return false;
          const tag = el.tagName.toLowerCase();
          return tag === 'input' || tag === 'textarea'
            || el.getAttribute('contenteditable') === 'true';
        })()
      `);
            return !!result;
        }
        catch {
            // Fall through to page.evaluate
        }
    }
    // Fallback: page.evaluate (detectable — should only happen if CDP fails)
    return page.evaluate((sel) => {
        const el = document.querySelector(sel);
        if (!el)
            return false;
        const tag = el.tagName.toLowerCase();
        return tag === 'input' || tag === 'textarea'
            || el.getAttribute('contenteditable') === 'true';
    }, selector).catch(() => false);
}
/**
 * Check if the element matching selector is currently focused.
 * Uses CDP Isolated World when available — invisible to main world.
 */
async function isSelectorFocused(stealth, page, selector) {
    if (stealth) {
        try {
            const escaped = JSON.stringify(selector);
            const result = await stealth.evaluate(`
        (() => {
          const el = document.querySelector(${escaped});
          return el === document.activeElement;
        })()
      `);
            return !!result;
        }
        catch {
            // Fall through to page.evaluate
        }
    }
    return page.evaluate((sel) => {
        const el = document.querySelector(sel);
        return el === document.activeElement;
    }, selector).catch(() => false);
}
// ============================================================================
// Page-level patching
// ============================================================================
/**
 * Replace page methods with human-like implementations.
 */
function patchPage(page, cfg, cursor) {
    const originals = {
        click: page.click.bind(page),
        dblclick: page.dblclick.bind(page),
        hover: page.hover.bind(page),
        type: page.type.bind(page),
        fill: page.fill.bind(page),
        check: page.check.bind(page),
        uncheck: page.uncheck.bind(page),
        selectOption: page.selectOption.bind(page),
        press: page.press.bind(page),
        goto: page.goto.bind(page),
        isChecked: page.isChecked.bind(page),
        mouseMove: page.mouse.move.bind(page.mouse),
        mouseClick: page.mouse.click.bind(page.mouse),
        mouseDblclick: page.mouse.dblclick.bind(page.mouse),
        mouseWheel: page.mouse.wheel.bind(page.mouse),
        mouseDown: page.mouse.down.bind(page.mouse),
        mouseUp: page.mouse.up.bind(page.mouse),
        keyboardType: page.keyboard.type.bind(page.keyboard),
        keyboardDown: page.keyboard.down.bind(page.keyboard),
        keyboardUp: page.keyboard.up.bind(page.keyboard),
        keyboardPress: page.keyboard.press.bind(page.keyboard),
        keyboardInsertText: page.keyboard.insertText.bind(page.keyboard),
    };
    page._original = originals;
    page._humanCfg = cfg;
    // --- Stealth infrastructure ---
    const stealth = new StealthEval(page);
    page._stealth = stealth;
    // CDP session for shift symbol typing (lazy-initialized, reuses stealth's session)
    let cdpSession = null;
    const ensureCdp = async () => {
        if (!cdpSession) {
            try {
                cdpSession = await stealth.getCdpSession();
            }
            catch { }
        }
        return cdpSession;
    };
    const raw = {
        move: originals.mouseMove,
        down: originals.mouseDown,
        up: originals.mouseUp,
        wheel: originals.mouseWheel,
    };
    const rawKb = {
        down: originals.keyboardDown,
        up: originals.keyboardUp,
        type: originals.keyboardType,
        insertText: originals.keyboardInsertText,
    };
    async function ensureCursorInit() {
        if (!cursor.initialized) {
            cursor.x = rand(cfg.initial_cursor_x[0], cfg.initial_cursor_x[1]);
            cursor.y = rand(cfg.initial_cursor_y[0], cfg.initial_cursor_y[1]);
            await originals.mouseMove(cursor.x, cursor.y);
            cursor.initialized = true;
        }
    }
    // --- goto (invalidate isolated world on navigation) ---
    const humanGoto = async (url, options) => {
        const response = await originals.goto(url, options);
        stealth.invalidate();
        patchFrames(page, cfg, cursor, raw, rawKb, originals, stealth);
        return response;
    };
    // --- click ---
    const humanClickFn = async (selector, options) => {
        await ensureCursorInit();
        if (cfg.idle_between_actions) {
            await humanIdle(raw, rand(cfg.idle_between_duration[0], cfg.idle_between_duration[1]), cursor.x, cursor.y, cfg);
        }
        const { box, cursorX, cursorY } = await scrollToElement(page, raw, selector, cursor.x, cursor.y, cfg);
        cursor.x = cursorX;
        cursor.y = cursorY;
        const isInput = await isInputElement(stealth, page, selector);
        const target = clickTarget(box, isInput, cfg);
        await humanMove(raw, cursor.x, cursor.y, target.x, target.y, cfg);
        cursor.x = target.x;
        cursor.y = target.y;
        await humanClick(raw, isInput, cfg);
    };
    // --- dblclick ---
    const humanDblclickFn = async (selector, options) => {
        await ensureCursorInit();
        if (cfg.idle_between_actions) {
            await humanIdle(raw, rand(cfg.idle_between_duration[0], cfg.idle_between_duration[1]), cursor.x, cursor.y, cfg);
        }
        const { box, cursorX, cursorY } = await scrollToElement(page, raw, selector, cursor.x, cursor.y, cfg);
        cursor.x = cursorX;
        cursor.y = cursorY;
        const isInput = await isInputElement(stealth, page, selector);
        const target = clickTarget(box, isInput, cfg);
        await humanMove(raw, cursor.x, cursor.y, target.x, target.y, cfg);
        cursor.x = target.x;
        cursor.y = target.y;
        await raw.down({ clickCount: 2 });
        await sleep(rand(30, 60));
        await raw.up({ clickCount: 2 });
    };
    // --- hover ---
    const humanHoverFn = async (selector, options) => {
        await ensureCursorInit();
        if (cfg.idle_between_actions) {
            await humanIdle(raw, rand(cfg.idle_between_duration[0], cfg.idle_between_duration[1]), cursor.x, cursor.y, cfg);
        }
        const { box, cursorX, cursorY } = await scrollToElement(page, raw, selector, cursor.x, cursor.y, cfg);
        cursor.x = cursorX;
        cursor.y = cursorY;
        const target = clickTarget(box, false, cfg);
        await humanMove(raw, cursor.x, cursor.y, target.x, target.y, cfg);
        cursor.x = target.x;
        cursor.y = target.y;
    };
    // --- type ---
    const humanTypeFn = async (selector, text, options) => {
        await sleep(randRange(cfg.field_switch_delay));
        await humanClickFn(selector);
        await sleep(rand(100, 250));
        const cdp = await ensureCdp();
        await humanType(page, rawKb, text, cfg, cdp);
    };
    // --- fill (clears existing content first) ---
    const humanFillFn = async (selector, value, options) => {
        await sleep(randRange(cfg.field_switch_delay));
        await humanClickFn(selector);
        await sleep(rand(100, 250));
        await originals.keyboardPress(SELECT_ALL);
        await sleep(rand(30, 80));
        await originals.keyboardPress('Backspace');
        await sleep(rand(50, 150));
        const cdp = await ensureCdp();
        await humanType(page, rawKb, value, cfg, cdp);
    };
    // --- clear ---
    const humanClearFn = async (selector, options) => {
        if (!await isSelectorFocused(stealth, page, selector)) {
            await humanClickFn(selector);
        }
        await sleep(rand(50, 150));
        await originals.keyboardPress(SELECT_ALL);
        await sleep(rand(30, 80));
        await originals.keyboardPress('Backspace');
    };
    // --- check ---
    const humanCheckFn = async (selector, options) => {
        if (cfg.idle_between_actions) {
            await humanIdle(raw, rand(cfg.idle_between_duration[0], cfg.idle_between_duration[1]), cursor.x, cursor.y, cfg);
        }
        const checked = await originals.isChecked(selector).catch(() => false);
        if (!checked) {
            await humanClickFn(selector);
        }
    };
    // --- uncheck ---
    const humanUncheckFn = async (selector, options) => {
        if (cfg.idle_between_actions) {
            await humanIdle(raw, rand(cfg.idle_between_duration[0], cfg.idle_between_duration[1]), cursor.x, cursor.y, cfg);
        }
        const checked = await originals.isChecked(selector).catch(() => true);
        if (checked) {
            await humanClickFn(selector);
        }
    };
    // --- selectOption ---
    const humanSelectOptionFn = async (selector, values, options) => {
        await humanHoverFn(selector);
        await sleep(rand(100, 300));
        return originals.selectOption(selector, values, options);
    };
    // --- press (checks focus first — avoids redundant mouse moves) ---
    const humanPressFn = async (selector, key, options) => {
        if (!await isSelectorFocused(stealth, page, selector)) {
            await humanClickFn(selector);
        }
        await sleep(rand(50, 150));
        await originals.keyboardPress(key);
    };
    // --- pressSequentially ---
    const humanPressSequentiallyFn = async (selector, text, options) => {
        if (!await isSelectorFocused(stealth, page, selector)) {
            await humanClickFn(selector);
        }
        await sleep(rand(100, 250));
        const cdp = await ensureCdp();
        await humanType(page, rawKb, text, cfg, cdp);
    };
    // --- tap ---
    const humanTapFn = async (selector, options) => {
        await humanClickFn(selector, options);
    };
    // Assign page-level patches
    page.goto = humanGoto;
    page.click = humanClickFn;
    page.dblclick = humanDblclickFn;
    page.hover = humanHoverFn;
    page.type = humanTypeFn;
    page.fill = humanFillFn;
    page.check = humanCheckFn;
    page.uncheck = humanUncheckFn;
    page.selectOption = humanSelectOptionFn;
    page.press = humanPressFn;
    page.pressSequentially = humanPressSequentiallyFn;
    page.tap = humanTapFn;
    page.clear = humanClearFn;
    // --- mouse patches ---
    page.mouse.move = async (x, y, options) => {
        await ensureCursorInit();
        await humanMove(raw, cursor.x, cursor.y, x, y, cfg);
        cursor.x = x;
        cursor.y = y;
    };
    page.mouse.click = async (x, y, options) => {
        await ensureCursorInit();
        await humanMove(raw, cursor.x, cursor.y, x, y, cfg);
        cursor.x = x;
        cursor.y = y;
        await humanClick(raw, false, cfg);
    };
    // --- keyboard patches ---
    page.keyboard.type = async (text, options) => {
        const cdp = await ensureCdp();
        await humanType(page, rawKb, text, cfg, cdp);
    };
    // Store helpers for frame patching
    page._humanCursor = cursor;
    page._humanRaw = raw;
    page._humanRawKb = rawKb;
    page._humanOriginals = originals;
    page._humanClickFn = humanClickFn;
    page._humanHoverFn = humanHoverFn;
    page._humanClearFn = humanClearFn;
    page._humanPressFn = humanPressFn;
    page._humanPressSequentiallyFn = humanPressSequentiallyFn;
    page._humanTapFn = humanTapFn;
    page._ensureCursorInit = ensureCursorInit;
    // Initialize cursor immediately so it doesn't visibly jump from (0,0)
    cursor.x = rand(cfg.initial_cursor_x[0], cfg.initial_cursor_x[1]);
    cursor.y = rand(cfg.initial_cursor_y[0], cfg.initial_cursor_y[1]);
    originals.mouseMove(cursor.x, cursor.y).then(() => {
        cursor.initialized = true;
    }).catch(() => { });
    // --- Patch Frame-level methods (for sub-frames) ---
    patchFrames(page, cfg, cursor, raw, rawKb, originals, stealth);
    // --- Patch ElementHandle selectors (page.$, page.$$, page.waitForSelector) ---
    patchPageElementHandles(page, cfg, cursor, raw, rawKb, originals, stealth);
}
// ============================================================================
// Frame-level patching
// ============================================================================
/**
 * Patch Frame methods so Locator-based calls go through humanization.
 * All 13 methods patched: click, dblclick, hover, type, fill, check, uncheck,
 * selectOption, press, pressSequentially, tap, clear, dragAndDrop.
 */
function patchFrames(page, cfg, cursor, raw, rawKb, originals, stealth) {
    for (const frame of iterFrames(page)) {
        patchSingleFrame(frame, page, cfg, originals, stealth);
        // Patch frame-level ElementHandle selectors ($, $$, waitForSelector)
        patchFrameElementHandles(frame, page, cfg, cursor, raw, rawKb, originals, stealth);
    }
}
function patchSingleFrame(frame, page, cfg, originals, stealth) {
    if (frame._humanPatched)
        return;
    frame._humanPatched = true;
    // Save originals for methods that need fallback
    const origFrameSelectOption = frame.selectOption.bind(frame);
    const origFrameDragAndDrop = frame.dragAndDrop.bind(frame);
    frame.click = async (selector, options) => {
        await page.click(selector, options);
    };
    frame.dblclick = async (selector, options) => {
        await page.dblclick(selector, options);
    };
    frame.hover = async (selector, options) => {
        await page.hover(selector, options);
    };
    frame.type = async (selector, text, options) => {
        await page.type(selector, text, options);
    };
    frame.fill = async (selector, value, options) => {
        await page.fill(selector, value, options);
    };
    frame.check = async (selector, options) => {
        await page.check(selector, options);
    };
    frame.uncheck = async (selector, options) => {
        await page.uncheck(selector, options);
    };
    frame.selectOption = async (selector, values, options) => {
        await page.hover(selector);
        await sleep(rand(100, 300));
        return origFrameSelectOption(selector, values, options);
    };
    frame.press = async (selector, key, options) => {
        await page.press(selector, key, options);
    };
    frame.pressSequentially = async (selector, text, options) => {
        await page.pressSequentially(selector, text, options);
    };
    frame.tap = async (selector, options) => {
        await page.tap(selector, options);
    };
    frame.clear = async (selector, options) => {
        if (!await isSelectorFocused(stealth, page, selector)) {
            await page.click(selector);
        }
        await sleep(rand(50, 150));
        await originals.keyboardPress(SELECT_ALL);
        await sleep(rand(30, 80));
        await originals.keyboardPress('Backspace');
    };
    frame.dragAndDrop = async (source, target, options) => {
        const srcBox = await frame.locator(source).boundingBox().catch(() => null);
        const tgtBox = await frame.locator(target).boundingBox().catch(() => null);
        if (srcBox && tgtBox) {
            const sx = srcBox.x + srcBox.width / 2;
            const sy = srcBox.y + srcBox.height / 2;
            const tx = tgtBox.x + tgtBox.width / 2;
            const ty = tgtBox.y + tgtBox.height / 2;
            await page.mouse.move(sx, sy);
            await sleep(rand(100, 200));
            await originals.mouseDown();
            await sleep(rand(80, 150));
            await page.mouse.move(tx, ty);
            await sleep(rand(80, 150));
            await originals.mouseUp();
        }
        else {
            return origFrameDragAndDrop(source, target, options);
        }
    };
}
function* iterFrames(page) {
    try {
        const mainFrame = page.mainFrame();
        yield mainFrame;
        for (const child of mainFrame.childFrames()) {
            yield child;
        }
    }
    catch { }
}
// ============================================================================
// Context-level patching
// ============================================================================
function patchContext(context, cfg) {
    const cursor = new CursorState();
    for (const page of context.pages()) {
        patchPage(page, cfg, cursor);
    }
    context.on('page', (page) => {
        if (!page._original) {
            patchPage(page, cfg, new CursorState());
        }
    });
    const origNewPage = context.newPage.bind(context);
    context.newPage = async () => {
        const page = await origNewPage();
        if (!page._original) {
            patchPage(page, cfg, new CursorState());
        }
        return page;
    };
}
// ============================================================================
// Browser-level patching
// ============================================================================
export function patchBrowser(browser, cfg) {
    for (const context of browser.contexts()) {
        patchContext(context, cfg);
    }
    const origNewContext = browser.newContext.bind(browser);
    browser.newContext = async (options) => {
        const context = await origNewContext(options);
        patchContext(context, cfg);
        return context;
    };
    const origNewPage = browser.newPage.bind(browser);
    browser.newPage = async (options) => {
        const page = await origNewPage(options);
        if (!page._original) {
            const ctx = page.context();
            if (!ctx._humanPatched) {
                patchContext(ctx, cfg);
                ctx._humanPatched = true;
            }
            patchPage(page, cfg, new CursorState());
        }
        return page;
    };
}
export { patchContext, patchPage };
//# sourceMappingURL=index.js.map
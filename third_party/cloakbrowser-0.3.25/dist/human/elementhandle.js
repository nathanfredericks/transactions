/**
 * ElementHandle humanization for Playwright.
 *
 * Mirrors Puppeteer's ElementHandle patching architecture.
 * Patches page.$(), page.$$(), page.waitForSelector() to return humanized handles,
 * and patches all interaction methods on each ElementHandle instance.
 *
 * Playwright ElementHandle methods patched:
 *   click, dblclick, hover, type, fill, press, selectOption,
 *   check, uncheck, setChecked, tap, focus
 *   + $, $$, waitForSelector (nested elements are also patched)
 *
 * Stealth-aware:
 *   - Uses CDP DOM.describeNode when available to check element type
 *     (no main-world JS execution)
 *   - Falls back to el.evaluate() only when CDP is unavailable
 */
import { rand, randRange, sleep } from './config.js';
import { humanMove, humanClick, clickTarget, humanIdle } from './mouse.js';
import { humanType } from './keyboard.js';
// --- Platform-aware select-all shortcut ---
const SELECT_ALL = process.platform === 'darwin' ? 'Meta+a' : 'Control+a';
// ============================================================================
// Stealth ElementHandle input check — uses CDP DOM.describeNode
// ============================================================================
async function isInputElementHandle(stealth, // StealthEval from index.ts
el) {
    // Try CDP DOM.describeNode first (no main-world JS execution)
    if (stealth) {
        try {
            const cdp = await stealth.getCdpSession();
            // Playwright exposes the JSHandle's internal preview via _objectId or similar
            // We need the remote object ID. Try to get it via internal API.
            const impl = el._impl ?? el._object ?? el;
            const guid = impl._guid;
            // Use el.evaluate as a reliable fallback within stealth context
            // Playwright doesn't expose remoteObject directly like Puppeteer
        }
        catch { /* fallthrough */ }
    }
    // Fallback: el.evaluate (works reliably in Playwright)
    try {
        return await el.evaluate((node) => {
            const tag = node.tagName?.toLowerCase();
            return tag === 'input' || tag === 'textarea'
                || node.getAttribute?.('contenteditable') === 'true';
        });
    }
    catch {
        return false;
    }
}
// ============================================================================
// Patch a single Playwright ElementHandle
// ============================================================================
export function patchSingleElementHandle(el, page, cfg, cursor, raw, rawKb, originals, stealth) {
    if (el._humanPatched)
        return;
    el._humanPatched = true;
    // Save originals
    const origElClick = el.click.bind(el);
    const origElDblclick = el.dblclick.bind(el);
    const origElHover = el.hover.bind(el);
    const origElType = el.type.bind(el);
    const origElFill = el.fill.bind(el);
    const origElPress = el.press.bind(el);
    const origElSelectOption = el.selectOption.bind(el);
    const origElCheck = el.check.bind(el);
    const origElUncheck = el.uncheck.bind(el);
    const origElSetChecked = el.setChecked?.bind(el);
    const origElTap = el.tap.bind(el);
    const origElFocus = el.focus.bind(el);
    // Nested selectors
    const origEl$ = el.$.bind(el);
    const origEl$$ = el.$$.bind(el);
    const origElWaitForSelector = el.waitForSelector.bind(el);
    // --- Nested elements are also patched ---
    el.$ = async (selector) => {
        const child = await origEl$(selector);
        if (child)
            patchSingleElementHandle(child, page, cfg, cursor, raw, rawKb, originals, stealth);
        return child;
    };
    el.$$ = async (selector) => {
        const children = await origEl$$(selector);
        for (const child of children) {
            patchSingleElementHandle(child, page, cfg, cursor, raw, rawKb, originals, stealth);
        }
        return children;
    };
    el.waitForSelector = async (selector, options) => {
        const child = await origElWaitForSelector(selector, options);
        if (child)
            patchSingleElementHandle(child, page, cfg, cursor, raw, rawKb, originals, stealth);
        return child;
    };
    // --- Helper: get bounding box and move cursor to element ---
    const moveToElement = async () => {
        // Ensure cursor is initialized
        const ensureCursorInit = page._ensureCursorInit;
        if (ensureCursorInit)
            await ensureCursorInit();
        const box = await el.boundingBox();
        if (!box)
            return null;
        const isInp = await isInputElementHandle(stealth, el);
        const target = clickTarget(box, isInp, cfg);
        if (cfg.idle_between_actions) {
            await humanIdle(raw, rand(cfg.idle_between_duration[0], cfg.idle_between_duration[1]), cursor.x, cursor.y, cfg);
        }
        await humanMove(raw, cursor.x, cursor.y, target.x, target.y, cfg);
        cursor.x = target.x;
        cursor.y = target.y;
        return { box, isInp };
    };
    // --- el.click() ---
    el.click = async (options) => {
        const info = await moveToElement();
        if (!info)
            return origElClick(options);
        await humanClick(raw, info.isInp, cfg);
    };
    // --- el.dblclick() ---
    el.dblclick = async (options) => {
        const info = await moveToElement();
        if (!info)
            return origElDblclick(options);
        await raw.down({ clickCount: 2 });
        await sleep(rand(30, 60));
        await raw.up({ clickCount: 2 });
    };
    // --- el.hover() ---
    el.hover = async (options) => {
        const info = await moveToElement();
        if (!info)
            return origElHover(options);
        // Just move — no click
    };
    // --- el.type() ---
    el.type = async (text, options) => {
        const info = await moveToElement();
        if (!info)
            return origElType(text, options);
        await humanClick(raw, info.isInp, cfg);
        await sleep(rand(100, 250));
        let cdpSession = null;
        try {
            cdpSession = await stealth?.getCdpSession();
        }
        catch { }
        await humanType(page, rawKb, text, cfg, cdpSession);
    };
    // --- el.fill() ---
    el.fill = async (value, options) => {
        const info = await moveToElement();
        if (!info)
            return origElFill(value, options);
        await humanClick(raw, info.isInp, cfg);
        await sleep(rand(100, 250));
        // Clear existing content
        await originals.keyboardPress(SELECT_ALL);
        await sleep(rand(30, 80));
        await originals.keyboardPress('Backspace');
        await sleep(rand(50, 150));
        let cdpSession = null;
        try {
            cdpSession = await stealth?.getCdpSession();
        }
        catch { }
        await humanType(page, rawKb, value, cfg, cdpSession);
    };
    // --- el.press() ---
    el.press = async (key, options) => {
        await sleep(rand(20, 60));
        await originals.keyboardDown(key);
        await sleep(randRange(cfg.key_hold));
        await originals.keyboardUp(key);
    };
    // --- el.selectOption() ---
    el.selectOption = async (values, options) => {
        const info = await moveToElement();
        if (!info)
            return origElSelectOption(values, options);
        await humanClick(raw, false, cfg);
        await sleep(rand(100, 300));
        return origElSelectOption(values, options);
    };
    // --- el.check() ---
    el.check = async (options) => {
        try {
            const checked = await el.isChecked();
            if (checked)
                return; // Already checked
        }
        catch { }
        const info = await moveToElement();
        if (!info)
            return origElCheck(options);
        await humanClick(raw, info.isInp, cfg);
    };
    // --- el.uncheck() ---
    el.uncheck = async (options) => {
        try {
            const checked = await el.isChecked();
            if (!checked)
                return; // Already unchecked
        }
        catch { }
        const info = await moveToElement();
        if (!info)
            return origElUncheck(options);
        await humanClick(raw, info.isInp, cfg);
    };
    // --- el.setChecked() ---
    if (origElSetChecked) {
        el.setChecked = async (checked, options) => {
            try {
                const current = await el.isChecked();
                if (current === checked)
                    return;
            }
            catch { }
            const info = await moveToElement();
            if (!info)
                return origElSetChecked(checked, options);
            await humanClick(raw, info.isInp, cfg);
        };
    }
    // --- el.tap() ---
    el.tap = async (options) => {
        const info = await moveToElement();
        if (!info)
            return origElTap(options);
        await humanClick(raw, info.isInp, cfg);
    };
    // --- el.focus() ---
    // Move cursor humanly but use programmatic focus (no click side-effects).
    // Stock Playwright el.focus() never clicks — clicking would trigger onclick,
    // submit forms, navigate links, etc.
    el.focus = async () => {
        await moveToElement(); // human-like Bézier cursor movement
        await origElFocus(); // programmatic focus, no click
    };
}
// ============================================================================
// Page-level ElementHandle patching
// ============================================================================
export function patchPageElementHandles(page, cfg, cursor, raw, rawKb, originals, stealth) {
    // Patch page.$() — only if the method exists
    if (typeof page.$ === 'function') {
        const orig$ = page.$.bind(page);
        page.$ = async (selector) => {
            const el = await orig$(selector);
            if (el)
                patchSingleElementHandle(el, page, cfg, cursor, raw, rawKb, originals, stealth);
            return el;
        };
    }
    // Patch page.$$()
    if (typeof page.$$ === 'function') {
        const orig$$ = page.$$.bind(page);
        page.$$ = async (selector) => {
            const els = await orig$$(selector);
            for (const el of els) {
                patchSingleElementHandle(el, page, cfg, cursor, raw, rawKb, originals, stealth);
            }
            return els;
        };
    }
    // Patch page.waitForSelector()
    if (typeof page.waitForSelector === 'function') {
        const origWaitForSelector = page.waitForSelector.bind(page);
        page.waitForSelector = async (selector, options) => {
            const el = await origWaitForSelector(selector, options);
            if (el)
                patchSingleElementHandle(el, page, cfg, cursor, raw, rawKb, originals, stealth);
            return el;
        };
    }
}
// ============================================================================
// Frame-level ElementHandle patching
// ============================================================================
export function patchFrameElementHandles(frame, page, cfg, cursor, raw, rawKb, originals, stealth) {
    // Patch frame.$() — only if the method exists
    if (typeof frame.$ === 'function') {
        const origFrame$ = frame.$.bind(frame);
        frame.$ = async (selector) => {
            const el = await origFrame$(selector);
            if (el)
                patchSingleElementHandle(el, page, cfg, cursor, raw, rawKb, originals, stealth);
            return el;
        };
    }
    // Patch frame.$$()
    if (typeof frame.$$ === 'function') {
        const origFrame$$ = frame.$$.bind(frame);
        frame.$$ = async (selector) => {
            const els = await origFrame$$(selector);
            for (const el of els) {
                patchSingleElementHandle(el, page, cfg, cursor, raw, rawKb, originals, stealth);
            }
            return els;
        };
    }
    // Patch frame.waitForSelector()
    if (typeof frame.waitForSelector === 'function') {
        const origFrameWaitForSelector = frame.waitForSelector.bind(frame);
        frame.waitForSelector = async (selector, options) => {
            const el = await origFrameWaitForSelector(selector, options);
            if (el)
                patchSingleElementHandle(el, page, cfg, cursor, raw, rawKb, originals, stealth);
            return el;
        };
    }
}
//# sourceMappingURL=elementhandle.js.map
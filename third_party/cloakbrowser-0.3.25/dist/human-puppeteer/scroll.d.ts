/**
 * cloakbrowser-human — Human-like scrolling via mouse wheel events.
 * Adapted for Puppeteer API.
 *
 * Changes from Playwright version:
 *   - page.viewport() instead of page.viewportSize()
 *   - page.$(selector) + el.boundingBox() instead of page.locator().boundingBox()
 *   - No timeout parameter on boundingBox()
 */
import type { Page } from 'puppeteer-core';
import type { HumanConfig } from '../human/config.js';
import { RawMouse } from '../human/mouse.js';
interface ElementBounds {
    x: number;
    y: number;
    width: number;
    height: number;
}
export declare function smoothWheel(raw: RawMouse, delta: number, cfg: HumanConfig, axis?: 'x' | 'y'): Promise<void>;
export declare function scrollToElement(page: Page, raw: RawMouse, selector: string, cursorX: number, cursorY: number, cfg: HumanConfig): Promise<{
    box: ElementBounds;
    cursorX: number;
    cursorY: number;
}>;
export {};
//# sourceMappingURL=scroll.d.ts.map
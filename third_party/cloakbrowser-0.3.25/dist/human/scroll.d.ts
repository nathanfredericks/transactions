/**
 * cloakbrowser-human — Human-like scrolling via mouse wheel events.
 */
import type { Page } from 'playwright-core';
import { HumanConfig } from './config.js';
import { RawMouse } from './mouse.js';
interface ElementBounds {
    x: number;
    y: number;
    width: number;
    height: number;
}
export declare function scrollToElement(page: Page, raw: RawMouse, selector: string, cursorX: number, cursorY: number, cfg: HumanConfig): Promise<{
    box: ElementBounds;
    cursorX: number;
    cursorY: number;
}>;
export {};
//# sourceMappingURL=scroll.d.ts.map
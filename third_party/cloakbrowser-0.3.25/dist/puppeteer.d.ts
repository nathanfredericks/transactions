/**
 * Puppeteer launch wrapper for cloakbrowser.
 * NOW WITH HUMANIZE SUPPORT — humanize: true enables human-like
 * mouse curves, keyboard timing, and scroll patterns (same as Playwright).
 */
import type { Browser } from "puppeteer-core";
import type { LaunchOptions } from "./types.js";
/**
 * Launch stealth Chromium browser via Puppeteer.
 *
 * @example
 * ```ts
 * import { launch } from 'cloakbrowser/puppeteer';
 * * // With humanize — human-like mouse, keyboard, scroll
 * const browser = await launch({ humanize: true });
 * const page = await browser.newPage();
 * await page.goto('[https://example.com](https://example.com)');
 * await page.click('#login');  // Bézier curve mouse movement
 * await page.type('#email', 'user@example.com');  // Per-character timing
 * ```
 */
export declare function launch(options?: LaunchOptions): Promise<Browser>;
//# sourceMappingURL=puppeteer.d.ts.map
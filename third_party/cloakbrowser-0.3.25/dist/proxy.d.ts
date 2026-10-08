/**
 * Shared proxy URL parsing for Playwright and Puppeteer wrappers.
 */
export interface ParsedProxy {
    server: string;
    username?: string;
    password?: string;
}
/**
 * Prepend http:// to schemeless proxy URLs so parsers can extract hostname.
 * Used by geoip resolution which only needs a valid hostname, not auth fields.
 */
export declare function ensureProxyScheme(proxyUrl: string): string;
/**
 * Parse a proxy URL, extracting credentials into separate fields.
 *
 * Handles: "http://user:pass@host:port" -> { server: "http://host:port", username: "user", password: "pass" }
 * Also handles: no credentials, URL-encoded special chars, socks5://, missing port,
 * and bare proxy strings without a scheme (e.g. "user:pass@host:port" -> treated as http).
 */
/** Proxy dict shape accepted by Playwright/Puppeteer wrappers. */
export type ProxyDict = {
    server: string;
    bypass?: string;
    username?: string;
    password?: string;
};
/** Result of resolveProxyConfig — either Playwright dict OR Chrome arg, never both. */
export interface ProxyConfig {
    /** Playwright proxy option (for HTTP proxies). */
    proxyOption?: ParsedProxy;
    /** Chrome CLI args (for SOCKS5 proxies, e.g. ["--proxy-server=socks5://..."]). */
    proxyArgs: string[];
}
/**
 * Check if a proxy uses the SOCKS5 protocol.
 */
export declare function isSocksProxy(proxy: string | ProxyDict | undefined | null): boolean;
/**
 * Reconstruct a SOCKS5 URL with inline credentials from a proxy dict.
 */
export declare function reconstructSocksUrl(proxy: ProxyDict): string;
/**
 * Resolve proxy into Playwright option and/or Chrome args.
 *
 * Playwright rejects SOCKS5 proxies with credentials in its proxy dict,
 * so SOCKS5 is passed via --proxy-server Chrome arg instead.
 */
export declare function resolveProxyConfig(proxy: string | ProxyDict | undefined): ProxyConfig;
export declare function parseProxyUrl(proxy: string): ParsedProxy;
//# sourceMappingURL=proxy.d.ts.map
/**
 * Shared proxy URL parsing for Playwright and Puppeteer wrappers.
 */
/**
 * Prepend http:// to schemeless proxy URLs so parsers can extract hostname.
 * Used by geoip resolution which only needs a valid hostname, not auth fields.
 */
export function ensureProxyScheme(proxyUrl) {
    return proxyUrl.includes("://") ? proxyUrl : `http://${proxyUrl}`;
}
/**
 * Check if a proxy uses the SOCKS5 protocol.
 */
export function isSocksProxy(proxy) {
    if (!proxy)
        return false;
    const url = typeof proxy === "string" ? proxy : proxy.server;
    return /^socks5h?:\/\//i.test(url);
}
/**
 * Reconstruct a SOCKS5 URL with inline credentials from a proxy dict.
 */
export function reconstructSocksUrl(proxy) {
    const url = new URL(proxy.server);
    if (proxy.username) {
        url.username = encodeURIComponent(proxy.username);
        if (proxy.password)
            url.password = encodeURIComponent(proxy.password);
    }
    return url.href.replace(/\/$/, "");
}
/**
 * Resolve proxy into Playwright option and/or Chrome args.
 *
 * Playwright rejects SOCKS5 proxies with credentials in its proxy dict,
 * so SOCKS5 is passed via --proxy-server Chrome arg instead.
 */
export function resolveProxyConfig(proxy) {
    if (!proxy)
        return { proxyArgs: [] };
    if (isSocksProxy(proxy)) {
        // SOCKS5: bypass Playwright, pass directly to Chrome via --proxy-server.
        if (typeof proxy === "string") {
            return { proxyArgs: [`--proxy-server=${proxy}`] };
        }
        const socksUrl = reconstructSocksUrl(proxy);
        const args = [`--proxy-server=${socksUrl}`];
        if (proxy.bypass)
            args.push(`--proxy-bypass-list=${proxy.bypass}`);
        return { proxyArgs: args };
    }
    // HTTP/HTTPS: use Playwright's proxy dict
    if (typeof proxy === "string") {
        return { proxyOption: parseProxyUrl(proxy), proxyArgs: [] };
    }
    return { proxyOption: proxy, proxyArgs: [] };
}
export function parseProxyUrl(proxy) {
    let url;
    // Bare format: "user:pass@host:port" — new URL() throws without a scheme.
    const normalized = proxy.includes("@") && !proxy.includes("://") ? `http://${proxy}` : proxy;
    try {
        url = new URL(normalized);
    }
    catch {
        // Not a parseable URL (e.g. bare "host:port") — pass through as-is
        return { server: proxy };
    }
    if (!url.username) {
        return { server: proxy };
    }
    // Rebuild server URL without credentials
    const server = `${url.protocol}//${url.hostname}${url.port ? `:${url.port}` : ""}`;
    const result = {
        server,
        username: decodeURIComponent(url.username),
    };
    if (url.password) {
        result.password = decodeURIComponent(url.password);
    }
    return result;
}
//# sourceMappingURL=proxy.js.map
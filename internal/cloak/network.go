// Derived from CloakBrowser 0.3.25 proxy.js and geoip.js (MIT).
package cloak

import (
	"context"
	"errors"
	"github.com/oschwald/maxminddb-golang"
	"golang.org/x/net/proxy"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Proxy struct{ Server, Username, Password, Bypass string }

func ParseProxy(value string) (*Proxy, error) {
	normalized := value
	if !strings.Contains(normalized, "://") {
		normalized = "http://" + normalized
	}
	u, err := url.Parse(normalized)
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("invalid proxy")
	}
	p := &Proxy{Server: u.Scheme + "://" + u.Host}
	if u.User != nil {
		p.Username = u.User.Username()
		p.Password, _ = u.User.Password()
	}
	return p, nil
}
func (p *Proxy) URL() (*url.URL, error) {
	u, err := url.Parse(p.Server)
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("invalid proxy")
	}
	if p.Username != "" {
		u.User = url.UserPassword(p.Username, p.Password)
	}
	return u, nil
}
func (p *Proxy) args() ([]string, error) {
	u, err := p.URL()
	if err != nil {
		return nil, err
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" && scheme != "socks5" && scheme != "socks5h" {
		return nil, errors.New("unsupported proxy protocol")
	}
	server := p.Server
	if strings.HasPrefix(scheme, "socks5") {
		server = strings.TrimSuffix(u.String(), "/")
	}
	result := []string{"--proxy-server=" + server}
	if p.Bypass != "" {
		bypass := p.Bypass
		if !strings.HasPrefix(scheme, "socks5") {
			rules := strings.Split(bypass, ",")
			for i, rule := range rules {
				rule = strings.TrimSpace(rule)
				if strings.HasPrefix(rule, ".") {
					rule = "*" + rule
				}
				rules[i] = rule
			}
			bypass = strings.Join(rules, ";")
		}
		result = append(result, "--proxy-bypass-list="+bypass)
	}
	return result, nil
}

var countryLocale = map[string]string{"US": "en-US", "GB": "en-GB", "AU": "en-AU", "CA": "en-CA", "NZ": "en-NZ", "IE": "en-IE", "ZA": "en-ZA", "SG": "en-SG", "DE": "de-DE", "AT": "de-AT", "CH": "de-CH", "FR": "fr-FR", "BE": "fr-BE", "ES": "es-ES", "MX": "es-MX", "AR": "es-AR", "CO": "es-CO", "CL": "es-CL", "BR": "pt-BR", "PT": "pt-PT", "IT": "it-IT", "NL": "nl-NL", "JP": "ja-JP", "KR": "ko-KR", "CN": "zh-CN", "TW": "zh-TW", "HK": "zh-HK", "RU": "ru-RU", "UA": "uk-UA", "PL": "pl-PL", "CZ": "cs-CZ", "RO": "ro-RO", "IL": "he-IL", "TR": "tr-TR", "SA": "ar-SA", "AE": "ar-AE", "EG": "ar-EG", "IN": "hi-IN", "ID": "id-ID", "PH": "en-PH", "TH": "th-TH", "VN": "vi-VN", "MY": "ms-MY", "SE": "sv-SE", "NO": "nb-NO", "DK": "da-DK", "FI": "fi-FI", "GR": "el-GR", "HU": "hu-HU", "BG": "bg-BG"}

func proxyExitIP(ctx context.Context, p *Proxy) string {
	u, err := p.URL()
	if err != nil {
		return ""
	}
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	if strings.HasPrefix(u.Scheme, "socks5") {
		address := u.Host
		if u.Port() == "" {
			address = net.JoinHostPort(u.Hostname(), "1080")
		}
		var auth *proxy.Auth
		if p.Username != "" {
			auth = &proxy.Auth{User: p.Username, Password: p.Password}
		}
		dialer, e := proxy.SOCKS5("tcp", address, auth, &net.Dialer{Timeout: 10 * time.Second})
		if e != nil {
			return ""
		}
		cd, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return ""
		}
		transport.DialContext = cd.DialContext
	} else {
		transport.Proxy = http.ProxyURL(u)
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	for _, endpoint := range []string{"https://api.ipify.org", "https://checkip.amazonaws.com", "https://ifconfig.me/ip"} {
		req, e := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
		if e != nil {
			return ""
		}
		resp, e := client.Do(req)
		if e != nil {
			continue
		}
		data, e := io.ReadAll(io.LimitReader(resp.Body, 256))
		resp.Body.Close()
		if e == nil && resp.StatusCode == 200 {
			ip := strings.TrimSpace(string(data))
			if net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	return ""
}

var geoMu sync.Mutex

func geoDatabase(ctx context.Context) string {
	dir := os.Getenv("CLOAKBROWSER_CACHE_DIR")
	if dir == "" {
		home, e := os.UserHomeDir()
		if e != nil {
			return ""
		}
		dir = filepath.Join(home, ".cloakbrowser")
	}
	dest := filepath.Join(dir, "geoip", "GeoLite2-City.mmdb")
	geoMu.Lock()
	defer geoMu.Unlock()
	if stat, e := os.Stat(dest); e == nil {
		if time.Since(stat.ModTime()) >= 30*24*time.Hour {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				geoMu.Lock()
				defer geoMu.Unlock()
				_ = downloadGeo(ctx, dest)
			}()
		}
		return dest
	}
	if downloadGeo(ctx, dest) != nil {
		return ""
	}
	return dest
}
func downloadGeo(ctx context.Context, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(dest), "geoip-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	req, err := http.NewRequestWithContext(ctx, "GET", "https://github.com/P3TERX/GeoLite.mmdb/raw/download/GeoLite2-City.mmdb", nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("geo database unavailable")
	}
	n, err := io.Copy(file, io.LimitReader(resp.Body, 128*1024*1024+1))
	if err != nil {
		return err
	}
	if n > 128*1024*1024 {
		return errors.New("geo database oversized")
	}
	if err = file.Close(); err != nil {
		return err
	}
	db, err := maxminddb.Open(file.Name())
	if err != nil {
		return err
	}
	db.Close()
	return os.Rename(file.Name(), dest)
}
func resolveNetwork(ctx context.Context, o Options) (Options, error) {
	// Match the original no-proxy GeoIP no-op and remove unresolved WebRTC auto.
	needsIP := false
	for _, arg := range o.Args {
		needsIP = needsIP || arg == "--fingerprint-webrtc-ip=auto"
	}
	exit := ""
	if o.Proxy != nil {
		if o.GeoIP || needsIP {
			exit = proxyExitIP(ctx, o.Proxy)
		}
		if o.GeoIP && (o.Timezone == "" && o.TimezoneID == "" || o.Locale == "") {
			path := geoDatabase(ctx)
			ip := net.ParseIP(exit)
			if ip == nil {
				u, e := o.Proxy.URL()
				if e == nil {
					ips, e := net.DefaultResolver.LookupIP(ctx, "ip", u.Hostname())
					if e == nil && len(ips) > 0 {
						ip = ips[0]
					}
				}
			}
			if path != "" && ip != nil {
				db, e := maxminddb.Open(path)
				if e == nil {
					var record struct {
						Location struct {
							Timezone string `maxminddb:"time_zone"`
						} `maxminddb:"location"`
						Country struct {
							ISO string `maxminddb:"iso_code"`
						} `maxminddb:"country"`
					}
					if db.Lookup(ip, &record) == nil {
						if o.Timezone == "" && o.TimezoneID == "" {
							o.Timezone = record.Location.Timezone
						}
						if o.Locale == "" {
							o.Locale = countryLocale[record.Country.ISO]
						}
					}
					db.Close()
				}
			}
		}
	}
	args := []string{}
	explicit := false
	for _, arg := range o.Args {
		if arg == "--fingerprint-webrtc-ip=auto" {
			if exit != "" {
				args = append(args, "--fingerprint-webrtc-ip="+exit)
				explicit = true
			}
			continue
		}
		if strings.HasPrefix(arg, "--fingerprint-webrtc-ip") {
			explicit = true
		}
		args = append(args, arg)
	}
	if o.GeoIP && exit != "" && !explicit {
		args = append(args, "--fingerprint-webrtc-ip="+exit)
	}
	if o.Proxy != nil {
		proxyArgs, err := o.Proxy.args()
		if err != nil {
			return o, err
		}
		args = append(args, proxyArgs...)
	}
	o.Args = args
	return o, nil
}

// Derived from CloakBrowser 0.3.25 (MIT); see third_party for attribution.
// Package cloak ports the pinned CloakBrowser wrapper to Rod. Native Chromium
// fingerprint patches remain in the original binary. See third_party for sources.
package cloak

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"
	"github.com/nathanfredericks/transactions/internal/cloak/human"
	"math/rand/v2"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const WrapperVersion = "0.3.25"

//go:embed chromium-args.json
var chromiumArgs []byte

type Size struct{ Width, Height int }
type Options struct {
	Binary                                               string
	Headless                                             *bool
	StealthArgs                                          *bool
	Args                                                 []string
	Timezone, TimezoneID, Locale, UserAgent, ColorScheme string
	Viewport                                             *Size
	NoViewport                                           bool
	UserDataDir                                          string
	Humanize                                             bool
	HumanPreset                                          string
	HumanConfig                                          map[string]json.RawMessage
	Proxy                                                *Proxy
	GeoIP                                                bool
	Context                                              ContextOptions
}

// ContextOptions are explicit equivalents of the Playwright options supported
// by this port. Locale/timezone are accepted but ignored here, as in the source.
type ContextOptions struct {
	Viewport                   *Size
	NoViewport                 bool
	UserAgent, ColorScheme     string
	Locale, TimezoneID         string
	ExtraHTTPHeaders           map[string]string
	Cookies                    []*proto.NetworkCookieParam
	Storage                    map[string]map[string]string
	Permissions                []proto.BrowserPermissionType
	Geolocation                *proto.EmulationSetGeolocationOverride
	HTTPUsername, HTTPPassword string
	HTTPOrigin                 string
	DeviceScaleFactor          float64
	Screen                     *Size
}
type Browser struct {
	Raw         *rod.Browser
	root        *rod.Browser
	launch      *launcher.Launcher
	runtime     *runtimeState
	cancel      context.CancelFunc
	ownsProcess bool
}
type runtimeKey struct{}
type runtimeState struct {
	options Options
	cfg     human.Config
	pages   sync.Map
}

func Headed() *bool { v := false; return &v }
func BuildArgs(o Options) []string {
	seen := map[string]string{}
	order := []string{}
	add := func(arg string) {
		key := strings.SplitN(arg, "=", 2)[0]
		if _, ok := seen[key]; !ok {
			order = append(order, key)
		}
		seen[key] = arg
	}
	if o.StealthArgs == nil || *o.StealthArgs {
		add("--no-sandbox")
		add("--fingerprint=" + strconv.Itoa(rand.IntN(90000)+10000))
		platform := "windows"
		if runtime.GOOS == "darwin" {
			platform = "macos"
		}
		add("--fingerprint-platform=" + platform)
	}
	if o.Headless != nil && !*o.Headless || runtime.GOOS == "windows" {
		add("--ignore-gpu-blocklist")
	}
	for _, arg := range o.Args {
		add(arg)
	}
	timezone := o.Timezone
	if timezone == "" {
		timezone = o.TimezoneID
	}
	if timezone != "" {
		add("--fingerprint-timezone=" + timezone)
	}
	if o.Locale != "" {
		add("--lang=" + o.Locale)
		add("--fingerprint-locale=" + o.Locale)
	}
	result := make([]string, 0, len(order))
	for _, key := range order {
		result = append(result, seen[key])
	}
	return result
}
func cleanupLaunch(launch *launcher.Launcher, persistent bool) {
	if persistent {
		launch.Delete(flags.UserDataDir) // Wait for exit without deleting the caller-owned profile.
	}
	if launch.PID() == 0 {
		_ = os.RemoveAll(launch.Get(flags.UserDataDir))
		return
	}
	done := make(chan struct{})
	go func() { launch.Cleanup(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		launch.Kill()
	}
}
func Launch(ctx context.Context, o Options) (*Browser, error) {
	if o.UserAgent == "" {
		o.UserAgent = o.Context.UserAgent
	}
	if o.ColorScheme == "" {
		o.ColorScheme = o.Context.ColorScheme
	}
	if o.Binary == "" {
		o.Binary = os.Getenv("CLOAKBROWSER_BINARY_PATH")
	}
	if o.Binary == "" {
		o.Binary = os.Getenv("CLOAK_PATH")
	}
	if _, err := os.Stat(o.Binary); err != nil {
		return nil, errors.New("cloak binary unavailable")
	}
	var err error
	o, err = resolveNetwork(ctx, o)
	if err != nil {
		return nil, err
	}
	cfg, err := human.Resolve(o.HumanPreset, o.HumanConfig)
	if err != nil {
		return nil, err
	}
	launch := launcher.New().Context(ctx).Bin(o.Binary)
	// Remove browser flags inherited from Rod, then apply the exact pinned
	// Playwright launch defaults plus Cloak overrides. Keep only Rod's transport,
	// process ownership and profile controls.
	for flag := range launch.Flags {
		if flag != flags.Bin && flag != flags.Leakless && flag != flags.UserDataDir && flag != flags.RemoteDebuggingPort {
			launch.Delete(flag)
		}
	}
	var base []string
	if err = json.Unmarshal(chromiumArgs, &base); err != nil {
		return nil, err
	}
	headless := o.Headless == nil || *o.Headless
	if headless {
		base = append(base, "--headless", "--hide-scrollbars", "--mute-audio", "--blink-settings=primaryHoverType=2,availableHoverTypes=2,primaryPointerType=4,availablePointerTypes=4")
	}
	base = append(base, "--no-sandbox")
	base = append(base, BuildArgs(o)...)
	for _, arg := range base {
		parts := strings.SplitN(strings.TrimPrefix(arg, "--"), "=", 2)
		if len(parts) == 2 {
			launch.Set(flags.Flag(parts[0]), parts[1])
		} else {
			launch.Set(flags.Flag(parts[0]))
		}
	}
	launch.Set("no-startup-window")
	if o.UserDataDir != "" {
		launch.UserDataDir(o.UserDataDir)
	}
	control, err := launch.Launch()
	if err != nil {
		cleanupLaunch(launch, o.UserDataDir != "")
		return nil, errors.New("cloak launch failed")
	}
	root := rod.New().NoDefaultDevice().ControlURL(control).Context(ctx)
	if err = root.Connect(); err != nil {
		launch.Kill()
		cleanupLaunch(launch, o.UserDataDir != "")
		return nil, errors.New("cloak connection failed")
	}
	raw := root
	if o.UserDataDir == "" {
		raw, err = root.Incognito()
		if err != nil {
			_ = root.Close()
			cleanupLaunch(launch, o.UserDataDir != "")
			return nil, err
		}
	}
	state := &runtimeState{options: o, cfg: cfg}
	lifetime, cancel := context.WithCancel(context.WithValue(ctx, runtimeKey{}, state))
	raw = raw.Context(lifetime)
	result := &Browser{Raw: raw, root: root, launch: launch, runtime: state, cancel: cancel, ownsProcess: true}
	// Context-level configuration happens before any bank page is created.

	if err = configureContext(raw, o); err != nil {
		_ = result.Close()
		return nil, err
	}
	state.watchPages(raw)
	return result, nil
}
func (b *Browser) NewPage(ctx context.Context) (*rod.Page, error) {
	p, err := b.Raw.Context(context.WithValue(ctx, runtimeKey{}, b.runtime)).Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, err
	}
	_, err = SetupPage(p)
	return p, err
}

// NewContext creates an independently closable incognito context. Human behavior
// is inherited from launch; timezone/locale remain process-wide binary options.
func (b *Browser) NewContext(ctx context.Context, options ContextOptions) (*Browser, error) {
	raw, err := b.root.Context(ctx).Incognito()
	if err != nil {
		return nil, err
	}
	o := b.runtime.options
	o.Context = options
	o.UserDataDir = ""
	o.Viewport = options.Viewport
	o.NoViewport = options.NoViewport
	if o.Viewport == nil && !o.NoViewport {
		o.Viewport = &Size{1280, 720}
	}
	o.UserAgent, o.ColorScheme = options.UserAgent, options.ColorScheme
	state := &runtimeState{options: o, cfg: b.runtime.cfg}
	lifetime, cancel := context.WithCancel(context.WithValue(ctx, runtimeKey{}, state))
	raw = raw.Context(lifetime)
	child := &Browser{Raw: raw, root: b.root, launch: b.launch, runtime: state, cancel: cancel}
	if err = configureContext(raw, o); err != nil {
		_ = child.Close()
		return nil, err
	}
	state.watchPages(raw)
	return child, nil
}
func configureContext(raw *rod.Browser, o Options) error {
	if len(o.Context.Cookies) > 0 {
		if err := raw.SetCookies(o.Context.Cookies); err != nil {
			return err
		}
	}
	if len(o.Context.Permissions) > 0 {
		return (proto.BrowserGrantPermissions{BrowserContextID: raw.BrowserContextID, Permissions: o.Context.Permissions}).Call(raw)
	}
	return nil
}
func (s *runtimeState) watchPages(raw *rod.Browser) {
	go raw.EachEvent(func(e *proto.TargetTargetCreated) {
		if e.TargetInfo.Type == "page" && e.TargetInfo.BrowserContextID == raw.BrowserContextID {
			page, err := raw.PageFromTarget(e.TargetInfo.TargetID)
			if err == nil {
				_, _ = SetupPage(page)
			}
		}
	})()
}
func (b *Browser) Close() error {
	if !b.ownsProcess {
		defer b.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		return b.Raw.Context(ctx).Close()
	}
	defer b.cancel()
	defer cleanupLaunch(b.launch, b.runtime.options.UserDataDir != "")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	err := b.root.Context(ctx).Close()
	if err != nil {
		b.launch.Kill()
	}
	return err
}
func SetupPage(p *rod.Page) (*human.Page, error) {
	state, ok := p.GetContext().Value(runtimeKey{}).(*runtimeState)
	if !ok {
		return nil, nil
	}
	// One setup result per target, even when TargetCreated races newPage.
	entry := &pageEntry{}
	existing, _ := state.pages.LoadOrStore(p.TargetID, entry)
	entry = existing.(*pageEntry)
	entry.once.Do(func() { entry.page, entry.err = state.setup(p) })
	if entry.page == nil {
		return nil, entry.err
	}
	return entry.page.Context(p.GetContext()), entry.err
}

type pageEntry struct {
	once sync.Once
	page *human.Page
	err  error
	auth *pageAuth
}

func ForPage(p *rod.Page) (*human.Page, bool) {
	state, ok := p.GetContext().Value(runtimeKey{}).(*runtimeState)
	if !ok || !state.options.Humanize {
		return nil, false
	}
	result, err := SetupPage(p)
	return result, err == nil && result != nil
}
func (s *runtimeState) setup(p *rod.Page) (*human.Page, error) {
	o := s.options
	viewport := o.Viewport
	if viewport == nil && !o.NoViewport {
		viewport = &Size{1920, 947}
	}
	if viewport != nil {
		screen := viewport
		if o.Context.Screen != nil {
			screen = o.Context.Screen
		}
		scale := o.Context.DeviceScaleFactor
		if scale == 0 {
			scale = 1
		}
		if err := p.SetViewport(&proto.EmulationSetDeviceMetricsOverride{Width: viewport.Width, Height: viewport.Height, DeviceScaleFactor: scale, ScreenWidth: &screen.Width, ScreenHeight: &screen.Height, ScreenOrientation: &proto.EmulationScreenOrientation{Type: proto.EmulationScreenOrientationTypeLandscapePrimary, Angle: 0}}); err != nil {
			return nil, err
		}
		if o.Headless != nil && !*o.Headless {
			widthInset, heightInset := 8, 85
			if runtime.GOOS == "darwin" {
				widthInset, heightInset = 2, 80
			} else if runtime.GOOS == "windows" {
				widthInset, heightInset = 16, 88
			}
			if o.UserDataDir != "" {
				heightInset += 46
			}
			window, err := (proto.BrowserGetWindowForTarget{TargetID: p.TargetID}).Call(p)
			if err != nil {
				return nil, err
			}
			w, h := viewport.Width+widthInset, viewport.Height+heightInset
			if err = (proto.BrowserSetWindowBounds{WindowID: window.WindowID, Bounds: &proto.BrowserBounds{Width: &w, Height: &h}}).Call(p); err != nil {
				return nil, err
			}
		}
	}
	if o.UserAgent != "" {
		if err := (proto.EmulationSetUserAgentOverride{UserAgent: o.UserAgent}).Call(p); err != nil {
			return nil, err
		}
	}
	scheme := o.ColorScheme
	if scheme == "" {
		scheme = "light"
	}
	if err := (proto.EmulationSetEmulatedMedia{Features: []*proto.EmulationMediaFeature{{Name: "prefers-color-scheme", Value: scheme}}}).Call(p); err != nil {
		return nil, err
	}
	if o.Context.Geolocation != nil {
		if err := o.Context.Geolocation.Call(p); err != nil {
			return nil, err
		}
	}
	if len(o.Context.ExtraHTTPHeaders) > 0 {
		headers := map[string]interface{}{}
		for k, v := range o.Context.ExtraHTTPHeaders {
			headers[k] = v
		}
		data, _ := json.Marshal(headers)
		var h proto.NetworkHeaders
		_ = json.Unmarshal(data, &h)
		if err := (proto.NetworkSetExtraHTTPHeaders{Headers: h}).Call(p); err != nil {
			return nil, err
		}
	}
	if len(o.Context.Storage) > 0 {
		data, _ := json.Marshal(o.Context.Storage)
		if _, err := p.EvalOnNewDocument(`(()=>{const data=` + string(data) + `;for(const [key,value]of Object.entries(data[location.origin]||{}))localStorage.setItem(key,value)})()`); err != nil {
			return nil, err
		}
	}

	if handler, err := setupAuth(p, o); err != nil {
		return nil, err
	} else if handler != nil {
		entry, _ := s.pages.Load(p.TargetID)
		entry.(*pageEntry).auth = handler
	}
	if !o.Humanize {
		return nil, nil
	}
	return human.NewPage(p, s.cfg)
}

// LaunchContext is the explicit Go counterpart of the original banking entry point.
func LaunchContext(ctx context.Context, options Options) (*Browser, error) {
	return Launch(ctx, options)
}
func LaunchPersistentContext(ctx context.Context, options Options) (*Browser, error) {
	if options.UserDataDir == "" {
		return nil, errors.New("persistent profile directory required")
	}
	return Launch(ctx, options)
}

// LaunchBrowser uses the driver's ordinary new-page viewport; LaunchContext
// uses Cloak's wrapper viewport. Both support independent NewContext calls.
func LaunchBrowser(ctx context.Context, options Options) (*Browser, error) {
	if options.Viewport == nil && !options.NoViewport {
		options.Viewport = &Size{Width: 1280, Height: 720}
	}
	return Launch(ctx, options)
}

// Package chromium launches the bank browser through Playwright: stock
// Chromium for EQ, CloakBrowser for the banks that need its fingerprint.
package chromium

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// Options configure one launch.
type Options struct {
	Executable string
	// Cloak applies CloakBrowser's launch contract: its stealth arguments, the
	// vendor's ignored defaults and the licence status file.
	Cloak    bool
	Seed     int
	Platform string
	Timezone string
	Locale   string
	// Env is the browser process's whole environment.
	Env map[string]string
	// Timeout is the default for every Playwright action.
	Timeout time.Duration
}

// Instance is a running browser with its one page.
type Instance struct {
	Context playwright.BrowserContext
	Page    playwright.Page
	pw      *playwright.Playwright
	dir     string
	status  string
	stop    func() bool
}

// LaunchError means the browser did not start. Licence is the exit code
// CloakBrowser wrote when it refused its licence, or 0.
type LaunchError struct{ Licence int }

func (e *LaunchError) Error() string { return "chromium: browser did not start" }

// Launch starts the browser with a fresh profile. When ctx ends, the driver is
// stopped, which closes the browser and frees a CloakBrowser licence seat.
func Launch(ctx context.Context, o Options) (*Instance, error) {
	dir, err := os.MkdirTemp("", "bank-browser-")
	if err != nil {
		return nil, err
	}
	i := &Instance{dir: dir, status: filepath.Join(dir, "licence-status")}
	// The driver's output can carry the browser's arguments and stderr, so it
	// is discarded; a logger keeps playwright-go from redirecting the log package.
	i.pw, err = playwright.Run(&playwright.RunOptions{SkipInstallBrowsers: true, Verbose: false, Stdout: io.Discard, Stderr: io.Discard, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, &LaunchError{}
	}
	i.stop = context.AfterFunc(ctx, func() { _ = i.pw.Stop() })
	env := map[string]string{}
	maps.Copy(env, o.Env)
	opts := playwright.BrowserTypeLaunchPersistentContextOptions{
		ExecutablePath: new(o.Executable),
		Headless:       new(false),
		NoViewport:     new(true),
		Args:           args(o),
		Env:            env,
		HandleSIGINT:   new(false),
		HandleSIGTERM:  new(false),
		HandleSIGHUP:   new(false),
	}
	if o.Cloak {
		opts.IgnoreDefaultArgs = []string{"--enable-automation", "--enable-unsafe-swiftshader"}
		env["CLOAKBROWSER_LICENSE_STATUS_FILE"] = i.status
	}
	i.Context, err = i.pw.Chromium.LaunchPersistentContext(filepath.Join(dir, "profile"), opts)
	if err != nil {
		code, _ := i.LicenceCode()
		i.Close()
		return nil, &LaunchError{Licence: code}
	}
	i.Context.SetDefaultTimeout(float64(o.Timeout.Milliseconds()))
	if pages := i.Context.Pages(); len(pages) > 0 {
		i.Page = pages[0]
	} else if i.Page, err = i.Context.NewPage(); err != nil {
		code, _ := i.LicenceCode()
		i.Close()
		return nil, &LaunchError{Licence: code}
	}
	return i, nil
}

// args are CloakBrowser's stealth arguments, as the vendor's launcher builds
// them; stock Chromium gets Playwright's defaults alone.
func args(o Options) []string {
	if !o.Cloak {
		return nil
	}
	a := []string{"--no-sandbox", "--fingerprint=" + strconv.Itoa(o.Seed), "--fingerprint-platform=" + o.Platform, "--ignore-gpu-blocklist"}
	if o.Timezone != "" {
		a = append(a, "--fingerprint-timezone="+o.Timezone)
	}
	if o.Locale != "" {
		a = append(a, "--lang="+o.Locale, "--fingerprint-locale="+o.Locale)
	}
	return a
}

// Close shuts the browser and driver down within ten seconds and deletes the
// profile. It is safe to call more than once.
func (i *Instance) Close() {
	if i.stop != nil {
		i.stop()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if i.Context != nil {
			_ = i.Context.Close()
		}
		if i.pw != nil {
			_ = i.pw.Stop()
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		slog.Warn("browser did not close in time")
	}
	_ = os.RemoveAll(i.dir)
	i.Context, i.pw, i.stop = nil, nil, nil
}

// LicenceCode is the exit code CloakBrowser wrote when it refused its licence,
// including a refusal after start-up. Read it before Close.
func (i *Instance) LicenceCode() (int, bool) {
	b, err := os.ReadFile(i.status)
	if err != nil {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return code, err == nil
}

// ErrorKind names a Playwright error by its type alone; its text can carry
// URLs, arguments and browser output, so it is never logged.
func ErrorKind(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, playwright.ErrTimeout):
		return "timeout"
	case errors.Is(err, playwright.ErrTargetClosed):
		return "target-closed"
	case errors.Is(err, playwright.ErrPlaywright):
		return "playwright"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "context"
	default:
		return "other"
	}
}

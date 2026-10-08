// cloak-inspect records real browser input on a local non-secret fixture.
// It never loads AWS configuration, bank credentials or financial services.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/nathanfredericks/transactions/internal/cloak"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	auth := flag.Bool("auth", false, "Inspect repeated HTTP authentication and request routing")
	profiles := flag.Bool("profiles", false, "Inspect persistent profile reopening")
	full := flag.Bool("full", false, "Inspect handle/frame/composition/cancellation operations too")
	flag.Parse()
	body, err := os.ReadFile("scripts/cloak/inspection.html")
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/auth/") {
			user, password, ok := r.BasicAuth()
			if !ok || user != "fixture" || password != "sample" {
				w.Header().Set("WWW-Authenticate", `Basic realm="`+r.URL.Path+`"`)
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, "Local fixture requires authentication")
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(body)
	})
	mux.HandleFunc("/frame", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><html lang="en"><label for="child">Child text</label><input id="child" name="child"><script>for(const type of ['keydown','keyup','input'])document.addEventListener(type,e=>parent.events.push({type,trusted:e.isTrusted,key:e.key,code:e.code,target:'child',time:performance.now()}));</script></html>`)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go server.Serve(listener)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if *auth {
		return inspectAuth(ctx, "http://"+listener.Addr().String())
	}
	if *profiles {
		return inspectProfiles(ctx, "http://"+listener.Addr().String())
	}
	owned, err := cloak.Launch(ctx, cloak.Options{Headless: cloak.Headed(), Humanize: true, HumanPreset: "careful", GeoIP: true, Timezone: "America/Halifax"})
	if err != nil {
		return err
	}
	defer owned.Close()
	raw, err := owned.NewPage(ctx)
	if err != nil {
		return err
	}
	p, ok := cloak.ForPage(raw)
	if !ok {
		return errors.New("humanization missing")
	}
	if err = p.Navigate("http://" + listener.Addr().String()); err != nil {
		return err
	}
	if err = p.Fill("#text", "Ab9@!_é🙂"); err != nil {
		return fmt.Errorf("fill: %w", err)
	}
	if err = p.Click("button", "^Inspect$"); err != nil {
		return fmt.Errorf("click: %w", err)
	}
	if err = p.SetChecked("#check", true); err != nil {
		return err
	}
	if err = p.Fill("#far", "scroll"); err != nil {
		return fmt.Errorf("scroll: %w", err)
	}
	if *full {
		if err = p.Clear("#far"); err != nil {
			return err
		}
		if err = p.PressSequentially("#far", "append"); err != nil {
			return err
		}
		if err = p.Press("#far", "Tab"); err != nil {
			return err
		}
		el, err := p.Element("#text")
		if err != nil {
			return err
		}
		if err = el.Raw.ScrollIntoView(); err != nil {
			return err
		}
		if err = el.Fill("handle"); err != nil {
			return fmt.Errorf("handle fill: %w", err)
		}
		if err = el.Type("-type"); err != nil {
			return err
		}
		nested, err := raw.Element("main")
		if err != nil {
			return err
		}
		wrapped, err := p.Wrap(nested).Element("#focus")
		if err != nil {
			return err
		}
		before, err := raw.Eval(`()=>events.filter(e=>e.type==='click'&&e.target==='focus').length`)
		if err != nil {
			return err
		}
		if err = wrapped.Focus(); err != nil {
			return err
		}
		after, err := raw.Eval(`()=>events.filter(e=>e.type==='click'&&e.target==='focus').length`)
		if err != nil {
			return err
		}
		if before.Value.Int() != after.Value.Int() {
			return errors.New("focus clicked")
		}
		if err = p.SelectOption("#choice", []string{"two"}); err != nil {
			return fmt.Errorf("select: %w", err)
		}
		if err = p.SetChecked("#check", false); err != nil {
			return err
		}
		if err = p.DoubleClick("#text"); err != nil {
			return err
		}
		frameEl, err := raw.Element("#frame")
		if err != nil {
			return err
		}
		frame, err := p.Frame(frameEl)
		if err != nil {
			return err
		}
		if err = frame.Fill("#child", "frame"); err != nil {
			return fmt.Errorf("frame fill: %w", err)
		}
		child, err := frame.Raw.Element("#child")
		if err != nil {
			return err
		}
		value, err := child.Property("value")
		if err != nil || value.Str() != "frame" {
			return errors.New("frame coordinates/input mismatch")
		}
		if err = p.Drag("#drag", "#drop"); err != nil {
			return err
		}
		if err = p.Press("#text", "Control+a"); err != nil {
			return err
		}
		short, cancel := context.WithTimeout(ctx, 75*time.Millisecond)
		err = p.Context(short).KeyboardType("XYZ")
		cancel()
		if err == nil {
			return errors.New("cancellation did not stop typing")
		}

	}
	report, err := raw.Eval("()=>report()")
	if err != nil {
		return err
	}
	data := report.Value
	if !*full {
		if data.Get("text").Str() != "Ab9@!_é🙂" || data.Get("far").Str() != "scroll" || !data.Get("checked").Bool() || data.Get("clicks").Int() != 1 {
			return errors.New("inspection values differ")
		}
	}
	for _, event := range data.Get("events").Arr() {
		if !event.Get("trusted").Bool() && !(event.Get("target").Str() == "choice" && event.Get("type").Str() == "input") {
			return errors.New("untrusted input event")
		}
	}
	if len(data.Get("pressed").Arr()) != 0 {
		return errors.New("pressed key left after operation")
	}
	if *full {
		if data.Get("far").Str() != "append" || data.Get("choice").Str() != "two" || data.Get("checked").Bool() {
			return errors.New("composition values differ")
		}
		// A separate context must inherit humanization, isolate storage, and close
		// without closing the parent browser.
		childContext, err := owned.NewContext(ctx, cloak.ContextOptions{Viewport: &cloak.Size{Width: 900, Height: 600}, Storage: map[string]map[string]string{"http://" + listener.Addr().String(): {"sample": "child"}}})
		if err != nil {
			return err
		}
		childPage, err := childContext.NewPage(ctx)
		if err != nil {
			return err
		}
		childHuman, ok := cloak.ForPage(childPage)
		if !ok {
			return errors.New("new context not humanized")
		}
		if err = childHuman.Navigate("http://" + listener.Addr().String()); err != nil {
			return err
		}
		geometry, err := childHuman.Evaluate("({width:innerWidth,height:innerHeight,sample:localStorage.getItem('sample')})")
		if err != nil || geometry.Value.Get("width").Int() != 900 || geometry.Value.Get("height").Int() != 600 || geometry.Value.Get("sample").Str() != "child" {
			return errors.New("context settings mismatch")
		}
		if err = childContext.Close(); err != nil {
			return err
		}
		own, err := p.Evaluate("localStorage.getItem('sample')")
		if err != nil || !own.Value.Nil() {
			return errors.New("context storage not isolated")
		}
		// A popup created outside NewPage must receive the same setup automatically.
		if _, err = raw.Eval(`()=>{window.popup=window.open('/','inspection-popup');return true}`); err != nil {
			return err
		}
		deadline := time.Now().Add(10 * time.Second)
		found := false
		for !found && time.Now().Before(deadline) {
			pages, err := owned.Raw.Pages()
			if err != nil {
				return err
			}
			for _, candidate := range pages {
				if candidate.TargetID == raw.TargetID {
					continue
				}
				popup, ok := cloak.ForPage(candidate)
				if !ok {
					continue
				}
				identity, err := popup.Evaluate("({width:innerWidth,height:innerHeight})")
				if err == nil && identity.Value.Get("width").Int() == 1920 && identity.Value.Get("height").Int() == 947 {
					found = true
					_ = candidate.Close()
					break
				}
			}
			if !found {
				time.Sleep(50 * time.Millisecond)
			}
		}
		if !found {
			return errors.New("popup setup failed")
		}
		// Check cleanup before navigation discards the page's event history.
		if err = p.Navigate("http://" + listener.Addr().String()); err != nil {
			return err
		}
		object, err := p.Evaluate("document.querySelector('#text').id")
		if err != nil || object.Value.Str() != "text" {
			return errors.New("world recreation failed")
		}
	}
	return json.NewEncoder(os.Stdout).Encode(data)
}

func inspectProfiles(ctx context.Context, url string) error {
	dir, err := os.MkdirTemp("", "cloak-profile-inspection-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	profile := filepath.Join(dir, "profile")
	for iteration := 0; iteration < 2; iteration++ {
		owned, err := cloak.Launch(ctx, cloak.Options{Headless: cloak.Headed(), UserDataDir: profile, Humanize: true, HumanPreset: "careful"})
		if err != nil {
			return err
		}
		page, err := owned.NewPage(ctx)
		if err != nil {
			_ = owned.Close()
			return err
		}
		if err = page.Navigate(url); err != nil {
			_ = owned.Close()
			return err
		}
		if iteration == 0 {
			_, err = page.Eval(`()=>{localStorage.setItem('fixture','persisted');document.cookie='fixture=present; Max-Age=3600; Path=/';return true}`)
		} else {
			result, e := page.Eval(`()=>({storage:localStorage.getItem('fixture'),cookie:document.cookie.includes('fixture=present')})`)
			err = e
			if err == nil && (result.Value.Get("storage").Str() != "persisted" || !result.Value.Get("cookie").Bool()) {
				err = errors.New("persistent state not retained")
			}
		}
		closeErr := owned.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if _, err = os.Stat(profile); err != nil {
			return errors.New("persistent profile removed")
		}
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]bool{"persistentCookies": true, "persistentStorage": true, "profileRetained": true})
}

func inspectAuth(ctx context.Context, url string) error {
	owned, err := cloak.Launch(ctx, cloak.Options{Headless: cloak.Headed(), Context: cloak.ContextOptions{HTTPUsername: "fixture", HTTPPassword: "sample", HTTPOrigin: url}})
	if err != nil {
		return err
	}
	defer owned.Close()
	page, err := owned.NewPage(ctx)
	if err != nil {
		return err
	}
	router := cloak.NewRequestRouter(page)
	if err = router.Add("*/blocked", "", func(h *rod.Hijack) { h.Response.Fail(proto.NetworkErrorReasonBlockedByClient) }); err != nil {
		return err
	}
	if err = cloak.StartRequestRouter(page, router); err != nil {
		return err
	}
	defer cloak.StopRequestRouter(page, router)
	for _, path := range []string{"/auth/first", "/auth/second"} {
		if err = page.Navigate(url + path); err != nil {
			return err
		}
		title, err := page.Eval("()=>document.title")
		if err != nil || title.Value.Str() != "Cloak input inspection" {
			return errors.New("HTTP authentication failed")
		}
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]bool{"originAuthentication": true, "requestRouting": true, "repeatedChallenges": true})
}

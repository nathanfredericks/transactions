package signin

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"

	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/banks/rogers"
)

func signInRogers(ctx context.Context, d Dependencies, b *Browser) (session bank.Session, err error) {
	stage := "login"
	defer func() {
		if err != nil {
			f := bank.Classify(err)
			f.Operation = stage + "/" + f.Operation
			err = f
		}
	}()
	if previous := d.Previous; previous != nil {
		var saved rogers.Auth
		if json.Unmarshal(previous.Auth, &saved) != nil {
			return bank.Session{}, bank.Fail(bank.Invalid, "device-state")
		}
		data, _ := json.Marshal(saved.DeviceStorage)
		// Rogers uses SecureLS values plus its key metadata, not plain localStorage strings.
		script := `(()=>{if(location.origin==="https://selfserve.rogersbank.com"){for(const [key,value] of Object.entries(` + string(data) + `||{})){if(["deviceId","rememberedUsername","_secure__ls__metadata"].includes(key))localStorage.setItem(key,value)}}})()`
		if e := b.Page.AddInitScript(playwright.Script{Content: new(script)}); e != nil {
			return bank.Session{}, e
		}
	}
	o := Record(b.Page)
	// Rewrite the web channel to the app's and drop the reCAPTCHA token, as
	// the bank's own app does. Playwright's * does not cross slashes; ** does.
	e := b.Page.Route("https://selfserve.apis.rogersbank.com/**", func(route playwright.Route) {
		headers := map[string]string{}
		for k, v := range route.Request().Headers() {
			if strings.EqualFold(k, "channel") && v == "101" {
				v = "201"
			}
			headers[k] = v
		}
		body, _ := route.Request().PostDataBuffer()
		var data map[string]any
		if json.Unmarshal(body, &data) == nil {
			if data["channel"] == "101" {
				data["channel"] = "201"
			}
			delete(data, "recaptchaToken")
			body, _ = json.Marshal(data)
		}
		_ = route.Continue(playwright.RouteContinueOptions{Headers: headers, PostData: body})
	})
	if e != nil {
		return bank.Session{}, e
	}
	if _, e = b.Page.Goto("https://selfserve.rogersbank.com/home", commit); e != nil {
		return bank.Session{}, bank.Fail(bank.Temporary, "navigation")
	}
	if e = Input(ctx, b, `input[aria-label="Username"], input[formcontrolname="username"], input[name="username"]`, d.Credentials.Username); e != nil {
		return bank.Session{}, e
	}
	if e = Input(ctx, b, `input[type="password"]`, d.Credentials.Password); e != nil {
		return bank.Session{}, e
	}
	remember := b.Page.Locator(`input[name="rememberMe"]`).First()
	if e = remember.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateAttached, Timeout: playwright.Float(10000)}); e != nil {
		return bank.Session{}, bank.Fail(bank.Challenge, "remember-device")
	}
	checked, e := remember.IsChecked()
	if e != nil {
		return bank.Session{}, e
	}
	if !checked {
		if e = ClickLocator(ctx, b, remember); e != nil {
			return bank.Session{}, bank.Fail(bank.Challenge, "remember-device")
		}
	}
	if e = Click(ctx, b, "button", `^[Ss]ign in$`); e != nil {
		return bank.Session{}, e
	}
	stage = "login-response"
	login, e := o.Wait(ctx, func(v Exchange) bool {
		return v.Request.Method() == "POST" && strings.HasPrefix(v.Request.URL(), "https://selfserve.apis.rogersbank.com/v1/authenticate/user/") && v.Response != nil
	})
	if e != nil {
		return bank.Session{}, e
	}
	status := login.Response.Status()
	if status == 400 || status == 401 {
		return bank.Session{}, bank.Fail(bank.Credentials, "login")
	}
	if status == 429 {
		return bank.Session{}, bank.Throttle("login", SafeHeaders(login.Response.Headers())["retry-after"])
	}
	if status == 403 {
		return bank.Session{}, bank.Fail(bank.Challenge, "login")
	}
	if status != 200 && status != 412 {
		return bank.Session{}, bank.Fail(bank.Invalid, "login")
	}
	if status == 412 {
		stage = "email-verification"
		email := b.Page.Locator("mat-radio-button,label").Filter(playwright.LocatorFilterOptions{HasText: regexp.MustCompile(`[Ee]mail|@`)}).First()
		if e = email.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateAttached, Timeout: playwright.Float(30000)}); e != nil {
			return bank.Session{}, bank.Fail(bank.Challenge, "email-option")
		}
		if e = ClickLocator(ctx, b, email); e != nil {
			return bank.Session{}, e
		}
		after := time.Now()
		if e = Click(ctx, b, "button", `^[Ss]end code$`); e != nil {
			return bank.Session{}, e
		}
		code, e := ReadCode(ctx, d.Credentials.MailToken, MailChallenge{After: after, Sender: d.EmailSender, Subject: d.EmailSubject, Length: d.EmailCodeLength})
		if e != nil {
			return bank.Session{}, e
		}
		if e = Input(ctx, b, `input[id="verificationCode"], input[name="verificationCode"], input[autocomplete="one-time-code"]`, code); e != nil {
			return bank.Session{}, e
		}
		if e = Click(ctx, b, "button", `^Continue$`); e != nil {
			return bank.Session{}, e
		}
	}
	pattern := regexp.MustCompile(`^https://selfserve\.apis\.rogersbank\.com/corebank/v1/account/(\d+)/customer/(\d+)/detail$`)
	stage = "account-capture"
	discovery, e := o.Wait(ctx, func(v Exchange) bool {
		return v.Request.Method() == "GET" && pattern.MatchString(v.Request.URL()) && v.Response != nil && v.Response.Status() == 200
	})
	if e != nil {
		return bank.Session{}, e
	}
	parts := pattern.FindStringSubmatch(discovery.Request.URL())
	tokens, e := o.Wait(ctx, func(v Exchange) bool {
		return v.Request.Method() != "OPTIONS" && strings.HasPrefix(v.Request.URL(), "https://selfserve.apis.rogersbank.com/v1/authenticate/") && v.Response != nil && SafeHeaders(v.Response.Headers())["refreshtoken"] != ""
	})
	if e != nil {
		return bank.Session{}, e
	}
	headers := SafeHeaders(discovery.Request.Headers())
	rotated := SafeHeaders(tokens.Response.Headers())
	for _, k := range []string{"accesstoken", "refreshtoken"} {
		if rotated[k] != "" {
			headers[k] = rotated[k]
		}
	}
	headers["authorization"] = "Bearer " + headers["accesstoken"]
	headers["channel"] = "201"
	storage, e := b.Page.Evaluate(`()=>Object.fromEntries(["deviceId","rememberedUsername","_secure__ls__metadata"].map(key=>[key,localStorage.getItem(key)]).filter(([,value])=>value!==null))`)
	if e != nil {
		return bank.Session{}, bank.Fail(bank.Invalid, "device-capture")
	}
	raw, e := json.Marshal(storage)
	var deviceStorage map[string]string
	if e != nil || json.Unmarshal(raw, &deviceStorage) != nil {
		return bank.Session{}, bank.Fail(bank.Invalid, "device-capture")
	}
	return BrowserSession(b, "rogers-bank", headers, rogers.Auth{AccountID: parts[1], CustomerID: parts[2], DeviceStorage: deviceStorage})
}

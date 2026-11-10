package signin

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/banks/rogers"
)

func signInRogers(ctx context.Context, d Dependencies, b *rod.Browser) (session bank.Session, err error) {
	stage := "login"
	defer func() {
		if err != nil {
			f := bank.Classify(err)
			f.Operation = stage + "/" + f.Operation
			err = f
		}
	}()
	p, e := NewPage(ctx, b)
	if e != nil {
		return bank.Session{}, e
	}
	if previous := d.Previous; previous != nil {
		var saved rogers.Auth
		if json.Unmarshal(previous.Auth, &saved) != nil {
			return bank.Session{}, bank.Fail(bank.Invalid, "device-state")
		}
		data, _ := json.Marshal(saved.DeviceStorage)
		// Rogers uses SecureLS values plus its key metadata, not plain localStorage strings.
		_, e = p.EvalOnNewDocument(`(()=>{if(location.origin==="https://selfserve.rogersbank.com"){for(const [key,value] of Object.entries(` + string(data) + `||{})){if(["deviceId","rememberedUsername","_secure__ls__metadata"].includes(key))localStorage.setItem(key,value)}}})()`)
		if e != nil {
			return bank.Session{}, e
		}
	}
	o := Observe(p)
	defer o.Close()
	router := p.HijackRequests()
	e = router.Add("https://selfserve.apis.rogersbank.com/*", "", func(h *rod.Hijack) {
		headers := []*proto.FetchHeaderEntry{}
		for k, v := range h.Request.Headers() {
			value := v.Str()
			if strings.EqualFold(k, "channel") && value == "101" {
				value = "201"
			}
			headers = append(headers, &proto.FetchHeaderEntry{Name: k, Value: value})
		}
		body := []byte(h.Request.Body())
		var data map[string]any
		if json.Unmarshal(body, &data) == nil {
			if data["channel"] == "101" {
				data["channel"] = "201"
			}
			delete(data, "recaptchaToken")
			body, _ = json.Marshal(data)
		}
		h.ContinueRequest(&proto.FetchContinueRequest{Headers: headers, PostData: body})
	})
	if e != nil {
		return bank.Session{}, e
	}
	go router.Run()
	defer func() { _ = router.Stop() }()
	if e = p.Navigate("https://selfserve.rogersbank.com/home"); e != nil {
		return bank.Session{}, bank.Fail(bank.Temporary, "navigation")
	}
	if e = Input(p, `input[aria-label="Username"], input[formcontrolname="username"], input[name="username"]`, d.Credentials.Username); e != nil {
		return bank.Session{}, e
	}
	if e = Input(p, `input[type="password"]`, d.Credentials.Password); e != nil {
		return bank.Session{}, e
	}
	remember, e := p.Timeout(10 * time.Second).Element(`input[name="rememberMe"]`)
	if e != nil {
		return bank.Session{}, bank.Fail(bank.Challenge, "remember-device")
	}
	checked, e := remember.Property("checked")
	if e != nil {
		return bank.Session{}, e
	}
	if !checked.Bool() {
		if e = ClickElement(p, remember); e != nil {
			return bank.Session{}, bank.Fail(bank.Challenge, "remember-device")
		}
	}
	if e = Click(p, "button", `^[Ss]ign in$`); e != nil {
		return bank.Session{}, e
	}
	stage = "login-response"
	login, e := o.Wait(ctx, func(v Exchange) bool {
		return v.Request.Method == "POST" && strings.HasPrefix(v.Request.URL, "https://selfserve.apis.rogersbank.com/v1/authenticate/user/") && v.Response != nil
	})
	if e != nil {
		return bank.Session{}, e
	}
	status := login.Response.Status
	if status == 400 || status == 401 {
		return bank.Session{}, bank.Fail(bank.Credentials, "login")
	}
	if status == 429 {
		return bank.Session{}, bank.Throttle("login", SafeHeaders(login.Response.Headers)["retry-after"])
	}
	if status == 403 {
		return bank.Session{}, bank.Fail(bank.Challenge, "login")
	}
	if status != 200 && status != 412 {
		return bank.Session{}, bank.Fail(bank.Invalid, "login")
	}
	if status == 412 {
		stage = "email-verification"
		email, e := p.Timeout(30*time.Second).ElementR("mat-radio-button,label", `[Ee]mail|@`)
		if e != nil {
			return bank.Session{}, bank.Fail(bank.Challenge, "email-option")
		}
		if e = ClickElement(p, email); e != nil {
			return bank.Session{}, e
		}
		after := time.Now()
		if e = Click(p, "button", `^[Ss]end code$`); e != nil {
			return bank.Session{}, e
		}
		code, e := ReadCode(ctx, d.Credentials.MailToken, MailChallenge{After: after, Sender: d.EmailSender, Subject: d.EmailSubject, Length: d.EmailCodeLength})
		if e != nil {
			return bank.Session{}, e
		}
		if e = Input(p, `input[id="verificationCode"], input[name="verificationCode"], input[autocomplete="one-time-code"]`, code); e != nil {
			return bank.Session{}, e
		}
		if e = Click(p, "button", `^Continue$`); e != nil {
			return bank.Session{}, e
		}
	}
	pattern := regexp.MustCompile(`^https://selfserve\.apis\.rogersbank\.com/corebank/v1/account/(\d+)/customer/(\d+)/detail$`)
	stage = "account-capture"
	discovery, e := o.Wait(ctx, func(v Exchange) bool {
		return v.Request.Method == "GET" && pattern.MatchString(v.Request.URL) && v.Response != nil && v.Response.Status == 200
	})
	if e != nil {
		return bank.Session{}, e
	}
	parts := pattern.FindStringSubmatch(discovery.Request.URL)
	tokens, e := o.Wait(ctx, func(v Exchange) bool {
		return v.Request.Method != "OPTIONS" && strings.HasPrefix(v.Request.URL, "https://selfserve.apis.rogersbank.com/v1/authenticate/") && v.Response != nil && SafeHeaders(v.Response.Headers)["refreshtoken"] != ""
	})
	if e != nil {
		return bank.Session{}, e
	}
	headers := SafeHeaders(discovery.Request.Headers)
	rotated := SafeHeaders(tokens.Response.Headers)
	for _, k := range []string{"accesstoken", "refreshtoken"} {
		if rotated[k] != "" {
			headers[k] = rotated[k]
		}
	}
	headers["authorization"] = "Bearer " + headers["accesstoken"]
	headers["channel"] = "201"
	storage, e := p.Eval(`()=>Object.fromEntries(["deviceId","rememberedUsername","_secure__ls__metadata"].map(key=>[key,localStorage.getItem(key)]).filter(([,value])=>value!==null))`)
	if e != nil {
		return bank.Session{}, bank.Fail(bank.Invalid, "device-capture")
	}
	var deviceStorage map[string]string
	if e = storage.Value.Unmarshal(&deviceStorage); e != nil {
		return bank.Session{}, bank.Fail(bank.Invalid, "device-capture")
	}
	return BrowserSession(b, "rogers-bank", headers, rogers.Auth{AccountID: parts[1], CustomerID: parts[2], DeviceStorage: deviceStorage})
}

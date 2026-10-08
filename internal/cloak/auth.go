package cloak

import (
	"context"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"net/url"
	"sync/atomic"
	"time"
)

// Authentication is page-scoped, continuous, and keeps proxy credentials
// separate from origin credentials. A repeated challenge is cancelled.
type pageAuth struct {
	page      *rod.Page
	delegated atomic.Bool
}

func setupAuth(p *rod.Page, o Options) (*pageAuth, error) {
	proxyUser, proxyPassword := "", ""
	if o.Proxy != nil {
		u, err := o.Proxy.URL()
		if err != nil {
			return nil, err
		}
		if u.Scheme == "http" || u.Scheme == "https" {
			proxyUser, proxyPassword = o.Proxy.Username, o.Proxy.Password
		}
	}
	if proxyUser == "" && o.Context.HTTPUsername == "" {
		return nil, nil
	}
	handler := &pageAuth{page: p}
	attempted := map[proto.FetchRequestID]bool{}
	requests := map[proto.NetworkRequestID]proto.FetchRequestID{}
	wait := p.EachEvent(func(e *proto.FetchRequestPaused) {
		if e.NetworkID != "" {
			requests[e.NetworkID] = e.RequestID
		}
		if !handler.delegated.Load() {
			_ = (proto.FetchContinueRequest{RequestID: e.RequestID}).Call(p)
		}
	}, func(e *proto.FetchAuthRequired) {
		response := &proto.FetchAuthChallengeResponse{Response: proto.FetchAuthChallengeResponseResponseDefault}
		user, password := "", ""
		if e.AuthChallenge.Source == proto.FetchAuthChallengeSourceProxy {
			user, password = proxyUser, proxyPassword
		} else {
			allowed := o.Context.HTTPOrigin == ""
			if !allowed {
				a, ae := url.Parse(o.Context.HTTPOrigin)
				b, be := url.Parse(e.AuthChallenge.Origin)
				allowed = ae == nil && be == nil && a.Scheme == b.Scheme && a.Host == b.Host
			}
			if allowed {
				user, password = o.Context.HTTPUsername, o.Context.HTTPPassword
			}
		}
		if user != "" {
			if attempted[e.RequestID] {
				response.Response = proto.FetchAuthChallengeResponseResponseCancelAuth
			} else {
				attempted[e.RequestID] = true
				response.Response = proto.FetchAuthChallengeResponseResponseProvideCredentials
				response.Username, response.Password = user, password
			}
		}
		_ = (proto.FetchContinueWithAuth{RequestID: e.RequestID, AuthChallengeResponse: response}).Call(p)
	}, func(e *proto.NetworkLoadingFinished) {
		delete(attempted, requests[e.RequestID])
		delete(requests, e.RequestID)
	}, func(e *proto.NetworkLoadingFailed) {
		delete(attempted, requests[e.RequestID])
		delete(requests, e.RequestID)
	})
	go wait()
	if err := handler.enable(); err != nil {
		return nil, err
	}
	return handler, nil
}
func (a *pageAuth) enable() error {
	return (proto.FetchEnable{HandleAuthRequests: true, Patterns: []*proto.FetchRequestPattern{{URLPattern: "*"}}}).Call(a.page)
}
func authFor(p *rod.Page) *pageAuth {
	state, ok := p.GetContext().Value(runtimeKey{}).(*runtimeState)
	if !ok {
		return nil
	}
	_, err := SetupPage(p)
	if err != nil {
		return nil
	}
	value, ok := state.pages.Load(p.TargetID)
	if !ok {
		return nil
	}
	return value.(*pageEntry).auth
}

// NewRequestRouter transfers paused requests to Rod's routes while retaining
// the auth-challenge handler. StartRequestRouter must follow route registration.
func NewRequestRouter(p *rod.Page) *rod.HijackRouter {
	if a := authFor(p); a != nil {
		a.delegated.Store(true)
	}
	return p.HijackRequests()
}
func StartRequestRouter(p *rod.Page, router *rod.HijackRouter) error {
	if a := authFor(p); a != nil {
		if err := router.Add("*", "", func(h *rod.Hijack) { h.ContinueRequest(&proto.FetchContinueRequest{}) }); err != nil {
			return err
		}
		if err := a.enable(); err != nil {
			return err
		}
	}
	go router.Run()
	return nil
}
func StopRequestRouter(p *rod.Page, router *rod.HijackRouter) {
	_ = router.Stop()
	if a := authFor(p); a != nil && p.GetContext().Err() == nil {
		a.delegated.Store(false)
		// Cleanup must not inherit an expired action deadline.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		clone := &pageAuth{page: p.Context(ctx)}
		_ = clone.enable()
	}
}

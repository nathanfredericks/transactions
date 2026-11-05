package browserbroker

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

type diskState struct {
	Reservations []Reservation `json:"reservations"`
	Uncertain    bool          `json:"uncertain"`
}
type Server struct {
	mu               sync.Mutex
	state            diskState
	owner            string
	failed           bool
	rateMu           sync.Mutex
	rateAt           time.Time
	rateCount        int
	StateFile, Token string
	Supervisor       *Supervisor
	Seats            func(context.Context) (int, error)
	IdleTimeout      time.Duration
	LeaseDuration    time.Duration
}

func NewServer(path, token string, sup *Supervisor, seats func(context.Context) (int, error)) (*Server, error) {
	if len(token) < 32 {
		return nil, errors.New("broker token must have at least 32 characters")
	}
	s := &Server{StateFile: path, Token: token, Supervisor: sup, Seats: seats, owner: "releasing", IdleTimeout: 30 * time.Second, LeaseDuration: 10 * time.Minute}
	sup.BeforeStart = func(ctx context.Context) error {
		check, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		count, err := seats(check)
		if err != nil || count != 0 {
			return errors.New("license capacity unconfirmed")
		}
		return nil
	}
	data, err := os.ReadFile(path)
	if err == nil {
		if json.Unmarshal(data, &s.state) != nil {
			return nil, errors.New("invalid broker state")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	// Startup never assumes the vendor seat is free; retain any remote reservation.
	s.state.Uncertain = true
	if err = s.persist(); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Server) persist() error {
	data, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.StateFile)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".broker-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, s.StateFile)
	}
	if err == nil {
		d, e := os.Open(dir)
		if e != nil {
			return e
		}
		err = d.Sync()
		d.Close()
	}
	return err
}
func (s *Server) save() {
	if s.persist() != nil {
		s.failed = true
		s.owner = "releasing"
	}
}
func (s *Server) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = s.Supervisor.Stop()
			return
		case <-ticker.C:
			s.step(ctx)
		}
	}
}
func (s *Server) step(ctx context.Context) {
	s.mu.Lock()
	if s.failed {
		s.mu.Unlock()
		_ = s.Supervisor.Stop()
		return
	}
	now := time.Now()
	active := -1
	pending := -1
	for i := range s.state.Reservations {
		r := &s.state.Reservations[i]
		if (r.Status == "granted" || r.Status == "queued") && !r.ExpiresAt.After(now) {
			if r.Status == "granted" {
				s.state.Uncertain = true
			}
			r.Status = "expired"
			s.save()
		}
		if r.Status == "granted" {
			active = i
		}
		if r.Status == "queued" && pending < 0 {
			pending = i
		}
	}
	if active >= 0 {
		s.owner = "bank"
		s.mu.Unlock()
		return
	}
	needsRelease := s.state.Uncertain || pending >= 0
	if !needsRelease && s.owner == "hermes" && s.Supervisor.Idle(s.IdleTimeout) {
		needsRelease = true
		s.state.Uncertain = true
		s.save()
	}
	if !needsRelease {
		s.mu.Unlock()
		return
	}
	s.owner = "releasing"
	s.mu.Unlock()
	// Slow process/network operations never hold the API/state lock.
	if s.Supervisor.Stop() != nil {
		return
	}
	seatCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	count, err := s.Seats(seatCtx)
	cancel()
	if err != nil || count != 0 {
		select {
		case <-ctx.Done():
		case <-time.After(4 * time.Second):
		}
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return
	}
	s.state.Uncertain = false
	s.owner = "idle"
	// Re-evaluate after I/O: the queued request may have been cancelled.
	for i := range s.state.Reservations {
		r := &s.state.Reservations[i]
		if r.Status == "queued" && r.ExpiresAt.After(time.Now()) {
			r.Status = "granted"
			r.ExpiresAt = time.Now().Add(s.LeaseDuration)
			s.owner = "bank"
			break
		}
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	kept := s.state.Reservations[:0]
	for _, r := range s.state.Reservations {
		if r.CreatedAt.After(cutoff) || r.Status == "granted" || r.Status == "queued" {
			kept = append(kept, r)
		}
	}
	s.state.Reservations = kept
	s.save()
}
func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

func (s *Server) Control() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		n := 0
		for _, v := range s.state.Reservations {
			if v.Status == "queued" {
				n++
			}
		}
		jsonResponse(w, 200, Status{Owner: s.owner, Ready: !s.failed && s.owner != "releasing", Pending: n})
	})
	mux.HandleFunc("POST /v1/reservations", func(w http.ResponseWriter, r *http.Request) {
		var q Request
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&q) != nil || (q.Bank != "nbdb" && q.Bank != "rogers-bank") || !identifier.MatchString(q.JobID) || !identifier.MatchString(q.Generation) {
			http.Error(w, "invalid reservation", 400)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.failed {
			http.Error(w, "broker unavailable", 503)
			return
		}
		for _, v := range s.state.Reservations {
			if v.Request == q {
				jsonResponse(w, 200, v)
				return
			}
		}
		live := 0
		for _, v := range s.state.Reservations {
			if v.Status == "queued" || v.Status == "granted" {
				live++
			}
		}
		if live >= 100 {
			http.Error(w, "broker busy", 429)
			return
		}
		var b [24]byte
		if _, e := rand.Read(b[:]); e != nil {
			http.Error(w, "broker unavailable", 503)
			return
		}
		v := Reservation{ID: hex.EncodeToString(b[:]), Request: q, Status: "queued", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(75 * time.Second)}
		s.state.Reservations = append(s.state.Reservations, v)
		s.save()
		if s.failed {
			http.Error(w, "broker unavailable", 503)
			return
		}
		jsonResponse(w, 202, v)
		// New connections are blocked as soon as this request is persisted.
	})
	mux.HandleFunc("GET /v1/reservations/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, v := range s.state.Reservations {
			if v.ID == r.PathValue("id") {
				jsonResponse(w, 200, v)
				return
			}
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("DELETE /v1/reservations/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i := range s.state.Reservations {
			v := &s.state.Reservations[i]
			if v.ID == r.PathValue("id") {
				if v.Status == "granted" {
					s.state.Uncertain = true
					s.owner = "releasing"
				}
				if v.Status == "granted" || v.Status == "queued" {
					v.Status = "released"
					s.save()
				}
				if s.failed {
					http.Error(w, "broker unavailable", 503)
					return
				}
				w.WriteHeader(204)
				return
			}
		}
		w.WriteHeader(204)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.rateMu.Lock()
		if time.Since(s.rateAt) >= time.Second {
			s.rateAt = time.Now()
			s.rateCount = 0
		}
		s.rateCount++
		limited := s.rateCount > 100
		s.rateMu.Unlock()
		if limited {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limited", 429)
			return
		}
		supplied := r.Header.Get("Authorization")
		expected := "Bearer " + s.Token
		if subtle.ConstantTimeCompare([]byte(supplied), []byte(expected)) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// trackedConn removes hijacked WebSocket connections when ReverseProxy closes them.
type trackedConn struct {
	net.Conn
	owner *Supervisor
}

func (c *trackedConn) Close() error { c.owner.connections.Delete(c); return c.Conn.Close() }

type proxyWriter struct {
	http.ResponseWriter
	sup *Supervisor
}

func (w proxyWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, b, e := w.ResponseWriter.(http.Hijacker).Hijack()
	if e != nil {
		return nil, nil, e
	}
	t := &trackedConn{Conn: c, owner: w.sup}
	w.sup.connections.Store(t, true)
	return t, b, nil
}
func (s *Server) CDP() http.Handler {
	upstream, _ := url.Parse(s.Supervisor.Upstream)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) {
		http.Error(w, "browser unavailable; navigate again", 503)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(200)
			return
		}
		s.mu.Lock()
		blocked := s.failed || s.owner == "bank" || s.owner == "releasing"
		for _, v := range s.state.Reservations {
			if v.Status == "queued" {
				blocked = true
			}
		}
		if blocked {
			s.mu.Unlock()
			w.Header().Set("Retry-After", "5")
			http.Error(w, "browser reserved; navigate again after release", 503)
			return
		}
		s.owner = "hermes"
		s.Supervisor.begin()
		s.mu.Unlock()
		defer s.Supervisor.end()
		if s.Supervisor.Ensure(r.Context(), func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.failed || s.owner != "hermes" {
				return false
			}
			for _, v := range s.state.Reservations {
				if v.Status == "queued" {
					return false
				}
			}
			return true
		}) != nil {
			_ = s.Supervisor.Stop()
			http.Error(w, "browser unavailable", 503)
			return
		}
		// A bank request can arrive while the launcher is starting.
		s.mu.Lock()
		blocked = s.failed || s.owner != "hermes"
		for _, v := range s.state.Reservations {
			if v.Status == "queued" {
				blocked = true
			}
		}
		s.mu.Unlock()
		if blocked {
			http.Error(w, "browser reserved", 503)
			return
		}
		proxy.ServeHTTP(proxyWriter{ResponseWriter: w, sup: s.Supervisor}, r)
	})
}

// VendorSeats reads capacity metadata only. Cookies and browser state never pass here.
func VendorSeats(key string, client *http.Client) func(context.Context) (int, error) {
	return func(ctx context.Context) (int, error) {
		data, _ := json.Marshal(map[string]string{"license_key": key})
		req, _ := http.NewRequestWithContext(ctx, "POST", "https://cloakbrowser.dev/api/license/session/count", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			return 0, errors.New("license service unavailable")
		}
		defer res.Body.Close()
		var v struct {
			Valid  bool `json:"valid"`
			Active *int `json:"active"`
		}
		if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&v) != nil || !v.Valid || v.Active == nil || *v.Active < 0 {
			return 0, errors.New("license capacity unconfirmed")
		}
		return *v.Active, nil
	}
}

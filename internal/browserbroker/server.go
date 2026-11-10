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
	"regexp"
	"sync"
	"time"
)

// Server grants the one CloakBrowser licence seat to a bank or to Hermes.
// The vendor's seat count decides when the seat is free: a bank is granted it,
// and Hermes's browser starts, only once the count is zero. Nothing is stored;
// after a restart the count still keeps Hermes off a seat a bank is using.
type Server struct {
	mu            sync.Mutex
	queue         []Reservation // oldest first
	granted       *Reservation
	owner         string // idle, hermes or bank
	Token         string
	Supervisor    *Supervisor
	Seats         func(context.Context) (int, error)
	IdleTimeout   time.Duration
	LeaseDuration time.Duration
}

func NewServer(token string, sup *Supervisor, seats func(context.Context) (int, error)) (*Server, error) {
	if len(token) < 32 {
		return nil, errors.New("broker token must have at least 32 characters")
	}
	s := &Server{Token: token, Supervisor: sup, Seats: seats, owner: "idle", IdleTimeout: 30 * time.Second, LeaseDuration: 10 * time.Minute}
	sup.BeforeStart = func(ctx context.Context) error { return s.seatFree(ctx) }
	return s, nil
}

// seatFree confirms the vendor counts no occupied seat.
func (s *Server) seatFree(ctx context.Context) error {
	check, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	count, err := s.Seats(check)
	if err != nil || count != 0 {
		return errors.New("license capacity unconfirmed")
	}
	return nil
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

// step expires stale reservations, stops an idle Hermes browser, and grants
// the oldest waiting bank the seat once Hermes is stopped and the seat is free.
func (s *Server) step(ctx context.Context) {
	s.mu.Lock()
	now := time.Now()
	if s.granted != nil && !s.granted.ExpiresAt.After(now) {
		s.granted = nil
		s.owner = "idle"
	}
	live := s.queue[:0]
	for _, r := range s.queue {
		if r.ExpiresAt.After(now) {
			live = append(live, r)
		}
	}
	s.queue = live
	if s.granted != nil {
		s.mu.Unlock()
		return
	}
	stopHermes := len(s.queue) > 0 || (s.owner == "hermes" && s.Supervisor.Idle(s.IdleTimeout))
	waiting := len(s.queue) > 0
	s.mu.Unlock()
	if stopHermes {
		// Slow process and network work never holds the lock.
		if s.Supervisor.Stop() != nil {
			return
		}
		s.mu.Lock()
		if s.owner == "hermes" {
			s.owner = "idle"
		}
		s.mu.Unlock()
	}
	if !waiting || s.seatFree(ctx) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// The head request may have been cancelled while the seat was checked.
	if s.granted == nil && len(s.queue) > 0 {
		r := s.queue[0]
		s.queue = s.queue[1:]
		r.Status, r.ExpiresAt = "granted", time.Now().Add(s.LeaseDuration)
		s.granted, s.owner = &r, "bank"
	}
}

// find returns the reservation with the ID, if it is waiting or granted.
// Caller holds s.mu.
func (s *Server) find(match func(Reservation) bool) (Reservation, bool) {
	if s.granted != nil && match(*s.granted) {
		return *s.granted, true
	}
	for _, r := range s.queue {
		if match(r) {
			return r, true
		}
	}
	return Reservation{}, false
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
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		jsonResponse(w, 200, Status{Owner: s.owner, Ready: true, Pending: len(s.queue)})
	})
	mux.HandleFunc("POST /v1/reservations", func(w http.ResponseWriter, r *http.Request) {
		var q Request
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&q) != nil || !identifier.MatchString(q.Bank) || !identifier.MatchString(q.JobID) || !identifier.MatchString(q.Generation) {
			http.Error(w, "invalid reservation", 400)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		// A retried request gets the reservation it already has.
		if v, ok := s.find(func(v Reservation) bool { return v.Request == q }); ok {
			jsonResponse(w, 200, v)
			return
		}
		var b [24]byte
		if _, e := rand.Read(b[:]); e != nil {
			http.Error(w, "broker unavailable", http.StatusServiceUnavailable)
			return
		}
		v := Reservation{ID: hex.EncodeToString(b[:]), Request: q, Status: "queued", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(75 * time.Second)}
		s.queue = append(s.queue, v)
		jsonResponse(w, 202, v)
	})
	mux.HandleFunc("GET /v1/reservations/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if v, ok := s.find(func(v Reservation) bool { return v.ID == r.PathValue("id") }); ok {
			jsonResponse(w, 200, v)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("DELETE /v1/reservations/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		id := r.PathValue("id")
		// A late DELETE only ever releases its own reservation.
		if s.granted != nil && s.granted.ID == id {
			s.granted, s.owner = nil, "idle"
		}
		kept := s.queue[:0]
		for _, v := range s.queue {
			if v.ID != id {
				kept = append(kept, v)
			}
		}
		s.queue = kept
		w.WriteHeader(204)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		supplied := r.Header.Get("Authorization")
		expected := "Bearer " + s.Token
		if subtle.ConstantTimeCompare([]byte(supplied), []byte(expected)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
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
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("connection cannot be hijacked")
	}
	c, b, e := h.Hijack()
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
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "browser unavailable; navigate again", http.StatusServiceUnavailable)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(200)
			return
		}
		s.mu.Lock()
		if s.granted != nil || len(s.queue) > 0 {
			s.mu.Unlock()
			w.Header().Set("Retry-After", "5")
			http.Error(w, "browser reserved; navigate again after release", http.StatusServiceUnavailable)
			return
		}
		s.owner = "hermes"
		s.Supervisor.begin()
		s.mu.Unlock()
		defer s.Supervisor.end()
		if s.Supervisor.Ensure(r.Context(), func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.owner == "hermes" && len(s.queue) == 0
		}) != nil {
			_ = s.Supervisor.Stop()
			http.Error(w, "browser unavailable", http.StatusServiceUnavailable)
			return
		}
		// A bank request can arrive while the launcher is starting.
		s.mu.Lock()
		blocked := s.owner != "hermes" || len(s.queue) > 0
		s.mu.Unlock()
		if blocked {
			http.Error(w, "browser reserved", http.StatusServiceUnavailable)
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

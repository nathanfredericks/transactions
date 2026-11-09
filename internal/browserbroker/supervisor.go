package browserbroker

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Supervisor owns exactly one private launcher process and its descendants.
// The child keeps the vendor launcher/profile behavior; no Docker socket is needed.
type Supervisor struct {
	mu           sync.Mutex
	Command      []string
	Upstream     string
	BeforeStart  func(context.Context) error
	cmd          *exec.Cmd
	done         chan struct{}
	connections  sync.Map
	activityMu   sync.Mutex
	inflight     int
	lastActivity time.Time
}

func (s *Supervisor) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done == nil {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

func (s *Supervisor) Ensure(ctx context.Context, allowed func() bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !allowed() {
		return errors.New("browser reserved")
	}
	if s.done != nil {
		select {
		case <-s.done:
			s.cmd = nil
		default:
			return nil
		}
	}
	if s.BeforeStart != nil {
		if err := s.BeforeStart(ctx); err != nil {
			return err
		}
	}
	// A reservation may arrive while the vendor check is in flight.
	if !allowed() {
		return errors.New("browser reserved")
	}
	if len(s.Command) == 0 {
		return errors.New("launcher not configured")
	}
	cmd := exec.Command(s.Command[0], s.Command[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Launcher diagnostics may contain private URLs or license material.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return errors.New("launcher start failed")
	}
	done := make(chan struct{})
	s.cmd = cmd
	s.done = done
	go func() { _ = cmd.Wait(); close(done) }()
	client := &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	deadline := time.NewTimer(25 * time.Second)
	defer deadline.Stop()
	for {
		req, _ := http.NewRequestWithContext(ctx, "GET", s.Upstream+"/json/version", nil)
		res, err := client.Do(req)
		if err == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				return nil
			}
		}
		select {
		case <-done:
			return errors.New("launcher exited")
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("launcher readiness timeout")
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (s *Supervisor) CloseConnections() {
	s.connections.Range(func(k, _ any) bool { _ = k.(net.Conn).Close(); s.connections.Delete(k); return true })
}

func (s *Supervisor) Stop() error {
	s.CloseConnections()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == nil {
		return nil
	}
	select {
	case <-s.done:
		s.cmd = nil
		return nil
	default:
	}
	// The launcher handles TERM by closing the persistent context cleanly.
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-s.done:
	case <-time.After(20 * time.Second):
		_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			return errors.New("launcher did not stop")
		}
	}
	// Reap any Chromium descendants left after a crashed launcher.
	_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
	s.cmd = nil
	return nil
}

func (s *Supervisor) begin() {
	s.activityMu.Lock()
	s.inflight++
	s.lastActivity = time.Now()
	s.activityMu.Unlock()
}

func (s *Supervisor) end() {
	s.activityMu.Lock()
	s.inflight--
	s.lastActivity = time.Now()
	s.activityMu.Unlock()
}

func (s *Supervisor) Idle(d time.Duration) bool {
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	return s.inflight == 0 && time.Since(s.lastActivity) > d
}

// browser-broker owns the Hermes launcher and serializes external browser capacity.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nathanfredericks/transactions/internal/browserbroker"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	token, key := os.Getenv("BROWSER_BROKER_TOKEN"), os.Getenv("CLOAKBROWSER_LICENSE_KEY")
	if key == "" {
		slog.Error("license configuration missing")
		os.Exit(1)
	}
	command := os.Args[1:]
	if len(command) == 0 {
		command = []string{"python", "/app/persistent.py"}
	}
	supervisor := &browserbroker.Supervisor{Command: command, Upstream: "http://127.0.0.1:9223"}
	vendor := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	state := os.Getenv("BROWSER_BROKER_STATE")
	if state == "" {
		state = "/state/broker.json"
	}
	broker, err := browserbroker.NewServer(state, token, supervisor, browserbroker.VendorSeats(key, vendor))
	if err != nil {
		slog.Error("broker initialization failed")
		os.Exit(1)
	}
	control := &http.Server{Addr: ":8080", Handler: broker.Control(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	cdp := &http.Server{Addr: ":9222", Handler: broker.CDP(), ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 16384}
	go broker.Run(ctx)
	for _, server := range []*http.Server{control, cdp} {
		go func() {
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("broker listener failed")
				stop()
			}
		}()
	}
	slog.Info("browser broker started")
	<-ctx.Done()
	_ = supervisor.Stop()
	clean, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_ = control.Shutdown(clean)
	_ = cdp.Shutdown(clean)
}

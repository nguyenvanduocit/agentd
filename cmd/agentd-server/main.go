// Command agentd-server serves the agentd installer and the signed daemon release from a directory of published
// artifacts (see package server).
//
//	agentd-server -dist ./dist -public-url https://agentd.example.com -addr :7100
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nguyenvanduocit/agentd/server"
)

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// statusRecorder remembers the status a handler wrote, for the request log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// logRequests writes one line per request: method, path and status.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("request: %s %s %d", r.Method, r.URL.Path, rec.status)
	})
}

func run() error {
	addr := flag.String("addr", env("AGENTD_ADDR", "127.0.0.1:7100"), "listen address (AGENTD_ADDR)")
	dist := flag.String("dist", env("AGENTD_DIST", "dist"), "directory of published artifacts (AGENTD_DIST)")
	public := flag.String("public-url", env("AGENTD_PUBLIC_URL", "http://127.0.0.1:7100"), "the server's own origin, baked into install.sh (AGENTD_PUBLIC_URL)")
	flag.Parse()

	h, err := server.Handler(server.Config{Dist: *dist, PublicURL: *public})
	if err != nil {
		return fmt.Errorf("configure: %w", err)
	}
	srv := &http.Server{Addr: *addr, Handler: logRequests(h), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("agentd-server: serving %s as %s on %s", *dist, *public, *addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

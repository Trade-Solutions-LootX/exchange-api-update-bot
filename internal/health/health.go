// Package health exposes a tiny HTTP server for container liveness/readiness
// checks and at-a-glance operational stats.
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"exchangebot/internal/poller"
)

// Server serves /healthz and /stats.
type Server struct {
	addr string
	p    *poller.Poller
	log  *slog.Logger
	srv  *http.Server
}

// New builds a health server. addr may be empty to disable.
func New(addr string, p *poller.Poller, log *slog.Logger) *Server {
	return &Server{addr: addr, p: p, log: log}
}

// Start launches the server in a goroutine (no-op if addr is empty).
func (s *Server) Start() {
	if s.addr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.p.Snapshot())
	})
	s.srv = &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		s.log.Info("health server listening", "addr", s.addr)
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.log.Error("health server error", "err", err)
		}
	}()
}

// Stop gracefully shuts the server down.
func (s *Server) Stop(ctx context.Context) {
	if s.srv == nil {
		return
	}
	_ = s.srv.Shutdown(ctx)
}

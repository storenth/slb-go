package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/storenth/ecmp-load-balancer/internal/proxy"
)

const DefaultSocketPath = "/run/slb/slb.sock"

type StatsProvider interface{ Stats() proxy.Stats }

type Server struct {
	Path  string
	Stats StatsProvider
}

type statusResponse struct {
	Backends          []proxy.BackendStats `json:"backends"`
	ActiveConnections int64                `json:"active_connections"`
	TotalConnections  uint64               `json:"total_connections"`
	UptimeSeconds     float64              `json:"uptime_seconds"`
}

func (s Server) Run(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o750); err != nil {
		return fmt.Errorf("create control socket directory: %w", err)
	}
	if err := removeStaleSocket(s.Path); err != nil {
		return err
	}
	listener, err := net.Listen("unix", s.Path)
	if err != nil {
		return fmt.Errorf("listen on control socket %q: %w", s.Path, err)
	}
	defer func() { _ = listener.Close(); _ = os.Remove(s.Path) }()
	if err := os.Chmod(s.Path, 0o660); err != nil {
		return fmt.Errorf("set control socket permissions: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", s.handleStatus)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)
		case <-done:
		}
	}()
	err = server.Serve(listener)
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("serve control API: %w", err)
}

func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect control socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("control socket path %q exists and is not a socket", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale control socket: %w", err)
	}
	return nil
}

func (s Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	stats := s.Stats.Stats()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(statusResponse{Backends: stats.Backends, ActiveConnections: stats.ActiveConnections, TotalConnections: stats.TotalConnections, UptimeSeconds: stats.Uptime.Seconds()})
}

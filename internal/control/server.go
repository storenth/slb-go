package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/storenth/slb-go/internal/proxy"
)

type StatsProvider interface{ Stats() proxy.Stats }

type Server struct {
	Path  string
	Stats StatsProvider
}

type statusResponse struct {
	Services      []proxy.ServiceStats `json:"services"`
	UptimeSeconds float64              `json:"uptime_seconds"`
}

func (s Server) Run(ctx context.Context) error {
	if err := PrepareSocketDir(s.Path); err != nil {
		return err
	}
	if err := prepareSocketFile(s.Path); err != nil {
		return err
	}
	listener, err := net.Listen("unix", s.Path)
	if err != nil {
		return fmt.Errorf("listen on control socket %q: %w", s.Path, err)
	}
	defer func() { _ = listener.Close(); _ = os.Remove(s.Path) }()
	if err := os.Chmod(s.Path, SocketFileMode(s.Path)); err != nil {
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

// prepareSocketFile handles the stale-socket check: if the path exists and a
// daemon answers, fail with "already running"; otherwise remove the stale file.
func prepareSocketFile(path string) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect control socket %q: %w", path, err)
	}
	inUse, err := CheckSocketInUse(path)
	if err != nil {
		return err
	}
	if inUse {
		return fmt.Errorf("control socket %q is already in use; is the daemon already running?", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale control socket %q: %w; set --socket, SLB_SOCKET or control.socket to a different path", path, err)
	}
	return nil
}

func (s Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	stats := s.Stats.Stats()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(statusResponse{Services: stats.Services, UptimeSeconds: stats.Uptime.Seconds()})
}

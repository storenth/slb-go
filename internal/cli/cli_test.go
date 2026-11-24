package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/storenth/ecmp-load-balancer/internal/control"
	"github.com/storenth/ecmp-load-balancer/internal/proxy"
)

type fakeStats struct{}

func (fakeStats) Stats() proxy.Stats {
	return proxy.Stats{Backends: []proxy.BackendStats{{Host: "127.0.0.1", Port: 9443, ActiveConnections: 2, TotalConnections: 7}}, ActiveConnections: 2, TotalConnections: 7, Uptime: 12 * time.Second}
}

func TestStatusOutput(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "slb.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- (control.Server{Path: socketPath, Stats: fakeStats{}}).Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		probeCtx, probeCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, err := control.GetStatus(probeCtx, socketPath)
		probeCancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("control server did not start: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	for _, asJSON := range []bool{false, true} {
		var output bytes.Buffer
		args := []string{"status", "--socket", socketPath}
		if asJSON {
			args = append(args, "--json")
		}
		command := New(&output, &output)
		command.SetArgs(args)
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		if asJSON {
			var value map[string]any
			if err := json.Unmarshal(output.Bytes(), &value); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			if value["total_connections"] != float64(7) {
				t.Fatalf("JSON output = %s", output.String())
			}
		} else if !strings.Contains(output.String(), "127.0.0.1:9443") || !strings.Contains(output.String(), "7 total") {
			t.Fatalf("human output = %q", output.String())
		}
	}
}

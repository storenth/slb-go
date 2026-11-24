package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/storenth/slb-go/internal/control"
	"github.com/storenth/slb-go/internal/proxy"
	"github.com/storenth/slb-go/internal/state"
)

type fakeStats struct{}

func (fakeStats) Stats() proxy.Stats {
	return proxy.Stats{
		Services: []proxy.ServiceStats{{
			Name:              "web",
			Listeners:         []proxy.ListenerStats{{Address: "127.0.0.1", Protocol: "tcp", Port: 8443, ActiveConnections: 2, TotalConnections: 7}},
			Backends:          []proxy.BackendStats{{Host: "127.0.0.1", Port: 9443, ActiveConnections: 2, TotalConnections: 7}},
			ActiveConnections: 2,
			TotalConnections:  7,
		}},
		Uptime: 12 * time.Second,
	}
}

type fakeStatsPortRange struct{}

func (fakeStatsPortRange) Stats() proxy.Stats {
	return proxy.Stats{
		Services: []proxy.ServiceStats{{
			Name: "range",
			Listeners: []proxy.ListenerStats{{
				Address:           "127.0.0.1",
				Protocol:          "tcp",
				PortRange:         &state.PortRange{Start: 30000, End: 30100},
				ActiveConnections: 0,
				TotalConnections:  3,
			}},
			Backends:          []proxy.BackendStats{{Host: "127.0.0.1", Port: 9443, ActiveConnections: 0, TotalConnections: 3}},
			ActiveConnections: 0,
			TotalConnections:  3,
		}},
		Uptime: 5 * time.Second,
	}
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
			services := value["services"].([]any)
			if len(services) != 1 {
				t.Fatalf("JSON output = %s", output.String())
			}
			service := services[0].(map[string]any)
			if service["total_connections"] != float64(7) {
				t.Fatalf("JSON output = %s", output.String())
			}
			listeners := service["listeners"].([]any)
			if len(listeners) != 1 {
				t.Fatalf("JSON output = %s", output.String())
			}
			listener := listeners[0].(map[string]any)
			if listener["address"] != "127.0.0.1" {
				t.Fatalf("JSON listener address = %v, want 127.0.0.1", listener["address"])
			}
			if listener["protocol"] != "tcp" {
				t.Fatalf("JSON listener protocol = %v, want tcp", listener["protocol"])
			}
			if listener["port"] != float64(8443) {
				t.Fatalf("JSON listener port = %v, want 8443", listener["port"])
			}
			if _, ok := listener["port_range"]; ok {
				t.Fatalf("JSON listener port_range should be absent for single port, got %v", listener["port_range"])
			}
		} else if !strings.Contains(output.String(), "127.0.0.1:8443/tcp") || !strings.Contains(output.String(), "127.0.0.1:9443") || !strings.Contains(output.String(), "7 total") || !strings.Contains(output.String(), "Service: web") {
			t.Fatalf("human output = %q", output.String())
		}
	}
}

func TestStatusOutputPortRange(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "slb.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- (control.Server{Path: socketPath, Stats: fakeStatsPortRange{}}).Run(ctx) }()
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
			services := value["services"].([]any)
			if len(services) != 1 {
				t.Fatalf("JSON output = %s", output.String())
			}
			service := services[0].(map[string]any)
			listeners := service["listeners"].([]any)
			if len(listeners) != 1 {
				t.Fatalf("JSON output = %s", output.String())
			}
			listener := listeners[0].(map[string]any)
			if listener["address"] != "127.0.0.1" {
				t.Fatalf("JSON listener address = %v, want 127.0.0.1", listener["address"])
			}
			if listener["protocol"] != "tcp" {
				t.Fatalf("JSON listener protocol = %v, want tcp", listener["protocol"])
			}
			if _, ok := listener["port"]; ok {
				t.Fatalf("JSON listener port should be absent for port range, got %v", listener["port"])
			}
			portRange, ok := listener["port_range"].(map[string]any)
			if !ok {
				t.Fatalf("JSON listener port_range missing or wrong type, got %T", listener["port_range"])
			}
			if portRange["start"] != float64(30000) || portRange["end"] != float64(30100) {
				t.Fatalf("JSON port_range = %v, want start=30000 end=30100", portRange)
			}
		} else if !strings.Contains(output.String(), "127.0.0.1:30000-30100/tcp") {
			t.Fatalf("human output = %q, want containing '127.0.0.1:30000-30100/tcp'", output.String())
		}
	}
}

func TestRunAndStatusIntegration(t *testing.T) {
	// Find a free TCP port for the proxy listener.
	freeListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	freePort := freeListener.Addr().(*net.TCPAddr).Port
	_ = freeListener.Close()

	// Write a minimal config that points the control socket at a temp path.
	socketPath := filepath.Join(t.TempDir(), "slb-dev.sock")
	configPath := filepath.Join(t.TempDir(), "dev.json")
	configData := `{
  "services": [
    {
      "name": "web",
      "listeners": [{"address": "127.0.0.1", "protocol": "tcp", "port": ` + strconv.Itoa(freePort) + `}],
      "backends": [{"host": "127.0.0.1", "port": 9443}],
      "affinity": "none"
    }
  ],
  "control": {"socket": "` + socketPath + `"}
}`
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}

	// Start the daemon via the CLI run command.
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	runDone := make(chan error, 1)
	var runOut bytes.Buffer
	go func() {
		cmd := New(&runOut, &runOut)
		cmd.SetArgs([]string{"run", "--config", configPath})
		err := cmd.ExecuteContext(runCtx)
		runDone <- err
	}()

	// Check if run exited immediately (before we even poll).
	select {
	case err := <-runDone:
		t.Fatalf("run exited immediately: %v\noutput: %s", err, runOut.String())
	default:
	}

	// Wait until the control socket answers.
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for {
		probeCtx, probeCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		_, err := control.GetStatus(probeCtx, socketPath)
		probeCancel()
		if err == nil {
			break
		}
		lastErr = err
		select {
		case err := <-runDone:
			t.Fatalf("run exited during startup: %v\noutput: %s", err, runOut.String())
		default:
		}
		if time.Now().After(deadline) {
			runCancel()
			t.Fatalf("daemon did not start within deadline: %v\nrun output: %s", lastErr, runOut.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Use the CLI status command with --config so it resolves the socket from config.
	var statusOut bytes.Buffer
	statusCmd := New(&statusOut, &statusOut)
	statusCmd.SetArgs([]string{"status", "--config", configPath})
	if err := statusCmd.Execute(); err != nil {
		t.Fatalf("status command failed: %v", err)
	}
	if !strings.Contains(statusOut.String(), "Service: web") {
		t.Fatalf("status output = %q, want containing 'Service: web'", statusOut.String())
	}

	// Also verify --socket flag works for status.
	var flagOut bytes.Buffer
	flagCmd := New(&flagOut, &flagOut)
	flagCmd.SetArgs([]string{"status", "--socket", socketPath})
	if err := flagCmd.Execute(); err != nil {
		t.Fatalf("status --socket failed: %v", err)
	}
	if !strings.Contains(flagOut.String(), "Service: web") {
		t.Fatalf("status --socket output = %q", flagOut.String())
	}

	// Clean up the daemon.
	runCancel()
	if err := <-runDone; err != nil && !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("run exited with error: %v", err)
	}
}

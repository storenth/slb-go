package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
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

func TestStartupLogContainsSocketPathAndSource(t *testing.T) {
	// Find a free TCP port for the proxy listener.
	freeListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	freePort := freeListener.Addr().(*net.TCPAddr).Port
	_ = freeListener.Close()

	// Use a short socket path to avoid macOS' ~104 byte Unix socket path limit.
	socketDir := filepath.Join(os.TempDir(), "slb-startup-test-"+strconv.Itoa(os.Getpid()))
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(socketDir) }()
	socketPath := filepath.Join(socketDir, "slb.sock")

	configPath := filepath.Join(t.TempDir(), "dev.json")
	configData := `{
  "services": [
    {
      "name": "web",
      "listeners": [{"address": "127.0.0.1", "protocol": "tcp", "port": ` + strconv.Itoa(freePort) + `}],
      "backends": [{"host": "127.0.0.1", "port": 9443}],
      "affinity": "none"
    }
  ]
}`
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	var runOut bytes.Buffer
	runDone := make(chan error, 1)
	go func() {
		cmd := New(&runOut, &runOut)
		cmd.SetArgs([]string{"run", "--config", configPath, "--socket", socketPath, "--log-format", "json"})
		err := cmd.ExecuteContext(runCtx)
		runDone <- err
	}()

	// Wait until the control socket answers or run exits.
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

	runCancel()
	if err := <-runDone; err != nil && !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("run exited with error: %v", err)
	}

	output := runOut.String()
	// The startup log line should contain the socket path and its source.
	if !strings.Contains(output, socketPath) {
		t.Fatalf("startup log does not contain socket path %q:\n%s", socketPath, output)
	}
	if !strings.Contains(output, "control_socket_source") {
		t.Fatalf("startup log does not contain control_socket_source:\n%s", output)
	}
	// Since --socket was used, the source should be "flag".
	if !strings.Contains(output, `"flag"`) {
		t.Fatalf("startup log does not show source as flag:\n%s", output)
	}
}

func TestStatusDaemonDownExitCodeAndMessage(t *testing.T) {
	// Point at a socket path where no daemon is running.
	socketPath := filepath.Join(t.TempDir(), "nonexistent.sock")

	var out, errOut bytes.Buffer
	cmd := New(&out, &errOut)
	cmd.SetArgs([]string{"status", "--socket", socketPath})

	// We can't use Execute() directly because it calls os.Exit.
	// Instead, we test the error returned by the command, then verify
	// the error message format.
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// The error should be a daemonUnreachableError.
	var unreachable *daemonUnreachableError
	if !errors.As(err, &unreachable) {
		t.Fatalf("expected daemonUnreachableError, got %T: %v", err, err)
	}

	msg := unreachable.Error()
	if !strings.Contains(msg, socketPath) {
		t.Fatalf("error message does not name socket path %q: %q", socketPath, msg)
	}
	if !strings.Contains(msg, "Is it running?") {
		t.Fatalf("error message does not give a hint: %q", msg)
	}
	if !strings.Contains(msg, "--socket") || !strings.Contains(msg, "SLB_SOCKET") {
		t.Fatalf("error message does not mention overrides: %q", msg)
	}
}

func TestStatusDaemonDownExitCode(t *testing.T) {
	// Test the exit code logic by running the CLI in a subprocess.
	// The subprocess runs the test binary with a special env var that triggers
	// Execute() with a status command pointing at a nonexistent socket.
	if os.Getenv("SLB_TEST_STATUS_EXIT") == "1" {
		os.Args = []string{"slb", "status"}
		_ = Execute(context.Background(), os.Stdout, os.Stderr)
		return
	}

	socketPath := filepath.Join(t.TempDir(), "nonexistent.sock")
	cmd := exec.Command(os.Args[0], "-test.run=TestStatusDaemonDownExitCode")
	cmd.Env = append(os.Environ(),
		"SLB_TEST_STATUS_EXIT=1",
		"SLB_SOCKET="+socketPath,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit code")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != exitUnreachable {
		t.Fatalf("exit code = %d, want %d (daemon unreachable). stderr:\n%s", exitErr.ExitCode(), exitUnreachable, stderr.String())
	}
	if !strings.Contains(stderr.String(), "slb:") {
		t.Fatalf("stderr does not start with 'slb:':\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "cannot reach the slb daemon") {
		t.Fatalf("stderr does not contain expected message:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), socketPath) {
		t.Fatalf("stderr does not name the socket path:\n%s", stderr.String())
	}
}

func TestStatusShowsFailedCounter(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "slb.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (control.Server{Path: socketPath, Stats: fakeStatsWithFailed{}}).Run(ctx)
	}()
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

	// Text output
	var textOut bytes.Buffer
	textCmd := New(&textOut, &textOut)
	textCmd.SetArgs([]string{"status", "--socket", socketPath})
	if err := textCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(textOut.String(), "3 failed") {
		t.Fatalf("text output does not show failed counter:\n%s", textOut.String())
	}

	// JSON output
	var jsonOut bytes.Buffer
	jsonCmd := New(&jsonOut, &jsonOut)
	jsonCmd.SetArgs([]string{"status", "--socket", socketPath, "--json"})
	if err := jsonCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(jsonOut.Bytes(), &value); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	services := value["services"].([]any)
	service := services[0].(map[string]any)
	backends := service["backends"].([]any)
	backend := backends[0].(map[string]any)
	if backend["failed_connections"] != float64(3) {
		t.Fatalf("JSON failed_connections = %v, want 3", backend["failed_connections"])
	}
}

type fakeStatsWithFailed struct{}

func (fakeStatsWithFailed) Stats() proxy.Stats {
	return proxy.Stats{
		Services: []proxy.ServiceStats{{
			Name:              "web",
			Listeners:         []proxy.ListenerStats{{Address: "127.0.0.1", Protocol: "tcp", Port: 8443, ActiveConnections: 1, TotalConnections: 5}},
			Backends:          []proxy.BackendStats{{Host: "127.0.0.1", Port: 9443, ActiveConnections: 1, TotalConnections: 5, FailedConnections: 3}},
			ActiveConnections: 1,
			TotalConnections:  5,
		}},
		Uptime: 10 * time.Second,
	}
}

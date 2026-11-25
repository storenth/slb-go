package proxy_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/storenth/slb-go/internal/balancer"
	"github.com/storenth/slb-go/internal/proxy"
	"github.com/storenth/slb-go/internal/state"
)

// startPlainTCPBackend starts a plain TCP echo backend that echoes back any
// data it receives. It returns the backend address and a cleanup function.
// If idle is positive, the backend enforces its own read deadline so it
// doesn't hang forever if the proxy never closes the connection.
func startPlainTCPBackend(t *testing.T, idle time.Duration) (state.Backend, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { _ = conn.Close() }()
				if idle > 0 {
					_ = conn.SetReadDeadline(time.Now().Add(idle))
				}
				// Echo loop: read, write back.
				buf := make([]byte, 32*1024)
				for {
					n, err := conn.Read(buf)
					if n > 0 {
						if _, werr := conn.Write(buf[:n]); werr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	addr := listener.Addr().(*net.TCPAddr)
	backend := state.Backend{Host: "127.0.0.1", Port: addr.Port}
	cleanup := func() {
		cancel()
		_ = listener.Close()
		<-ctx.Done()
		wg.Wait()
	}
	return backend, cleanup
}

// startProxyWithIdle starts a proxy with the given idle timeout and returns
// the proxy, a cancel function, and the done channel.
func startProxyWithIdle(t *testing.T, backends []state.Backend, idle time.Duration) (*proxy.Proxy, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	service := state.Service{
		Name:      "idle-test",
		Listeners: []state.Listener{{Address: "127.0.0.1", Protocol: "tcp", Port: 0}},
		Backends:  backends,
		Affinity:  "none",
	}
	p := proxy.New(service, balancer.NewRoundRobin(backends), idle)
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for strings.HasSuffix(p.Address(), ":0") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if strings.HasSuffix(p.Address(), ":0") {
		cancel()
		t.Fatal("proxy did not start")
	}
	return p, cancel, done
}

// waitForGoroutineSettle waits for goroutines to settle after proxy shutdown.
func waitForGoroutineSettle(t *testing.T, before int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for runtime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > before+2 {
		t.Fatalf("goroutine leak: after = %d, before = %d (max allowed %d)", got, before, before+2)
	}
}

// TestIdleEnforcement_ClosedOnIdle verifies that a proxied connection with no
// traffic in either direction is closed by the proxy within about twice the
// idle timeout.
func TestIdleEnforcement_ClosedOnIdle(t *testing.T) {
	const idle = 200 * time.Millisecond
	backend, backendCleanup := startPlainTCPBackend(t, 30*time.Second)
	defer backendCleanup()

	before := runtime.NumGoroutine()
	p, cancel, done := startProxyWithIdle(t, []state.Backend{backend}, idle)

	conn, err := net.Dial("tcp", p.Address())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	// Send one byte so the proxy establishes the backend connection, then
	// wait for the echo to confirm the pipe is up.
	if _, err := conn.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	echo := make([]byte, 1)
	if _, err := io.ReadFull(conn, echo); err != nil {
		t.Fatalf("read echo: %v", err)
	}

	// Now go silent: no traffic in either direction.
	start := time.Now()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	n, err := conn.Read(buf)
	elapsed := time.Since(start)

	if err == nil && n > 0 {
		t.Fatalf("connection not closed: received data after %v (idle=%v)", elapsed, idle)
	}
	if elapsed > 2*idle {
		t.Fatalf("idle close took %v, want < %v (2×idle)", elapsed, 2*idle)
	}
	if elapsed < idle {
		t.Fatalf("idle close too fast: %v, want >= %v (idle)", elapsed, idle)
	}
	t.Logf("idle close after %v (idle=%v)", elapsed, idle)

	_ = conn.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("proxy run: %v", err)
	}
	waitForGoroutineSettle(t, before, 2*time.Second)
}

// TestIdleEnforcement_ActiveConnectionSurvives verifies that a connection
// that keeps exchanging data for longer than the idle timeout is NOT closed.
func TestIdleEnforcement_ActiveConnectionSurvives(t *testing.T) {
	const idle = 200 * time.Millisecond
	backend, backendCleanup := startPlainTCPBackend(t, 30*time.Second)
	defer backendCleanup()

	p, cancel, done := startProxyWithIdle(t, []state.Backend{backend}, idle)

	conn, err := net.Dial("tcp", p.Address())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	// Exchange data every 50ms — well under the 200ms idle — for 600ms total
	// (3× the idle timeout). If the proxy closes the connection prematurely,
	// a read or write will fail.
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.Now().Add(600 * time.Millisecond)
	msg := []byte("ping")
	for time.Now().Before(deadline) {
		if _, err := conn.Write(msg); err != nil {
			t.Fatalf("write failed before idle×3: %v", err)
		}
		buf := make([]byte, len(msg))
		if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(conn, buf); err != nil {
			t.Fatalf("read failed before idle×3: %v (idle=%v)", err, idle)
		}
		if string(buf) != "ping" {
			t.Fatalf("echo mismatch: %q", buf)
		}
		<-ticker.C
	}
	t.Logf("connection survived %v of continuous traffic (idle=%v)", 600*time.Millisecond, idle)

	_ = conn.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("proxy run: %v", err)
	}
}

// TestIdleEnforcement_ActivityResetsTimer verifies that activity in either
// direction resets the idle timer. We send data at intervals just under the
// idle timeout; if the timer were not reset, the connection would close.
func TestIdleEnforcement_ActivityResetsTimer(t *testing.T) {
	const idle = 200 * time.Millisecond
	// Send data every 150ms — less than idle (200ms) but close enough that
	// if the timer were NOT reset, the connection would close before the
	// second send (150+150=300ms > 200ms idle).
	backend, backendCleanup := startPlainTCPBackend(t, 30*time.Second)
	defer backendCleanup()

	p, cancel, done := startProxyWithIdle(t, []state.Backend{backend}, idle)

	conn, err := net.Dial("tcp", p.Address())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	// Initial handshake: send one byte and read echo to confirm pipe is up.
	if _, err := conn.Write([]byte("x")); err != nil {
		t.Fatalf("initial write: %v", err)
	}
	echo := make([]byte, 1)
	if _, err := io.ReadFull(conn, echo); err != nil {
		t.Fatalf("initial read: %v", err)
	}

	// Send data every 150ms for 750ms total. Each send resets the idle
	// timer. Without reset, the first idle deadline (200ms) would fire
	// before the second send at 300ms.
	interval := 150 * time.Millisecond
	totalDuration := 750 * time.Millisecond
	msg := []byte("data")
	start := time.Now()
	for elapsed := time.Duration(0); elapsed < totalDuration; elapsed = time.Since(start) {
		if _, err := conn.Write(msg); err != nil {
			t.Fatalf("write failed at %v: %v (idle=%v, interval=%v)", elapsed, err, idle, interval)
		}
		buf := make([]byte, len(msg))
		if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(conn, buf); err != nil {
			t.Fatalf("read failed at %v: %v (idle=%v, interval=%v)", elapsed, err, idle, interval)
		}
		if string(buf) != "data" {
			t.Fatalf("echo mismatch at %v: %q", elapsed, buf)
		}
		time.Sleep(interval)
	}
	t.Logf("connection survived %v with %v intervals (idle=%v)", time.Since(start), interval, idle)

	_ = conn.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("proxy run: %v", err)
	}
}

// TestIdleEnforcement_BothConnsClosedNoLeak verifies that after an idle close,
// both the client and backend connections are closed and there are no
// goroutine leaks.
func TestIdleEnforcement_BothConnsClosedNoLeak(t *testing.T) {
	const idle = 200 * time.Millisecond

	// Track whether the backend connection was closed.
	backendClosed := make(chan struct{}, 1)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { _ = conn.Close() }()
				// Read one byte to establish the pipe, then wait.
				buf := make([]byte, 1)
				_, _ = conn.Read(buf)
				// Wait for the proxy to close us.
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				_, err := conn.Read(buf)
				if err != nil {
					select {
					case backendClosed <- struct{}{}:
					default:
					}
				}
			}()
		}
	}()
	addr := listener.Addr().(*net.TCPAddr)
	backend := state.Backend{Host: "127.0.0.1", Port: addr.Port}
	backendCleanup := func() {
		cancel()
		_ = listener.Close()
		<-ctx.Done()
		wg.Wait()
	}

	before := runtime.NumGoroutine()
	p, proxyCancel, proxyDone := startProxyWithIdle(t, []state.Backend{backend}, idle)

	conn, err := net.Dial("tcp", p.Address())
	if err != nil {
		t.Fatal(err)
	}

	// Send one byte to establish the backend connection.
	if _, err := conn.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Wait for the idle timeout to fire and close both connections.
	time.Sleep(3 * idle)

	// Verify the client connection is closed.
	if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	n, err := conn.Read(buf)
	if err == nil && n > 0 {
		t.Fatal("client connection still open after idle timeout")
	}
	t.Logf("client connection closed after idle timeout")

	// Verify the backend connection was closed.
	select {
	case <-backendClosed:
		t.Logf("backend connection closed after idle timeout")
	case <-time.After(2 * time.Second):
		t.Fatal("backend connection was not closed after idle timeout")
	}

	_ = conn.Close()
	proxyCancel()
	if err := <-proxyDone; err != nil {
		t.Fatalf("proxy run: %v", err)
	}
	backendCleanup()
	waitForGoroutineSettle(t, before, 2*time.Second)
}

// TestIdleEnforcement_BackendToClientActivityResets verifies that activity
// from the backend side (backend→client) also resets the idle timer.
func TestIdleEnforcement_BackendToClientActivityResets(t *testing.T) {
	const idle = 200 * time.Millisecond

	// Backend that sends data periodically without any client writes.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { _ = conn.Close() }()
				// Read the initial byte from the client to establish the pipe.
				buf := make([]byte, 1)
				_, _ = conn.Read(buf)
				// Send data every 150ms — less than idle (200ms).
				ticker := time.NewTicker(150 * time.Millisecond)
				defer ticker.Stop()
				for i := 0; i < 5; i++ {
					<-ticker.C
					if _, err := fmt.Fprintf(conn, "tick%d\n", i); err != nil {
						return
					}
				}
			}()
		}
	}()
	addr := listener.Addr().(*net.TCPAddr)
	backend := state.Backend{Host: "127.0.0.1", Port: addr.Port}
	backendCleanup := func() {
		cancel()
		_ = listener.Close()
		<-ctx.Done()
		wg.Wait()
	}

	p, proxyCancel, proxyDone := startProxyWithIdle(t, []state.Backend{backend}, idle)

	conn, err := net.Dial("tcp", p.Address())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	// Send one byte to establish the backend connection.
	if _, err := conn.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Read data from the backend (via proxy) for 750ms. The backend sends
	// every 150ms, which is less than the 200ms idle. If backend→client
	// activity does NOT reset the timer, the connection would close at
	// 200ms before the second tick at 300ms.
	r := bufio.NewReader(conn)
	received := 0
	start := time.Now()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for time.Since(start) < 750*time.Millisecond {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read failed at %v: %v (idle=%v) — backend→client activity did not reset timer", time.Since(start), err, idle)
		}
		if strings.HasPrefix(line, "tick") {
			received++
		}
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if received < 3 {
		t.Fatalf("expected at least 3 ticks, got %d", received)
	}
	t.Logf("received %d backend ticks over %v (idle=%v)", received, time.Since(start), idle)

	_ = conn.Close()
	proxyCancel()
	if err := <-proxyDone; err != nil {
		t.Fatalf("proxy run: %v", err)
	}
	backendCleanup()
}

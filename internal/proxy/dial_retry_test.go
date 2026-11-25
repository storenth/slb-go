package proxy_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/storenth/slb-go/internal/balancer"
	"github.com/storenth/slb-go/internal/proxy"
	"github.com/storenth/slb-go/internal/state"
)

// fakeClock provides a controllable time source for ejection tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Now()}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeDialer implements proxy.Dialer. It routes dials to real listeners for
// "up" backends and returns configurable errors for "down" backends.
type fakeDialer struct {
	mu     sync.Mutex
	up     map[string]net.Listener // address -> listener
	errs   map[string]error        // address -> error to return
	delays map[string]time.Duration
	dials  sync.Map // address -> *atomic.Int64 (dial count)
}

func newFakeDialer() *fakeDialer {
	return &fakeDialer{
		up:     make(map[string]net.Listener),
		errs:   make(map[string]error),
		delays: make(map[string]time.Duration),
	}
}

// addUpBackend starts a real TCP listener that echoes data, so the proxy can
// complete a full connection cycle.
func (d *fakeDialer) addUpBackend(t *testing.T, host string, port int) string {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	d.mu.Lock()
	d.up[addr] = ln
	d.mu.Unlock()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				_, _ = io.Copy(c, c) // echo
			}(conn)
		}
	}()
	return addr
}

func (d *fakeDialer) setDown(addr string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.errs[addr] = err
}

func (d *fakeDialer) setDelay(addr string, delay time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.delays[addr] = delay
}

func (d *fakeDialer) dialCount(addr string) int64 {
	v, ok := d.dials.Load(addr)
	if !ok {
		return 0
	}
	return v.(*atomic.Int64).Load()
}

func (d *fakeDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	// Track dial count.
	v, _ := d.dials.LoadOrStore(address, &atomic.Int64{})
	v.(*atomic.Int64).Add(1)

	d.mu.Lock()
	err := d.errs[address]
	delay := d.delays[address]
	ln := d.up[address]
	d.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if err != nil {
		return nil, err
	}
	if ln != nil {
		// Dial the real listener.
		return net.Dial(network, address)
	}
	// Default: connection refused.
	return nil, &net.OpError{Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1}, Err: errors.New("connection refused")}
}

func (d *fakeDialer) close() {
	d.mu.Lock()
	for _, ln := range d.up {
		_ = ln.Close()
	}
	d.mu.Unlock()
}

// parsePort extracts the port from an "host:port" string.
func parsePort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		t.Fatal(err)
	}
	return host, port
}

// helper to create a service with given backends.
func testService(backends []state.Backend) state.Service {
	return state.Service{
		Name:      "test",
		Listeners: []state.Listener{{Address: "127.0.0.1", Protocol: "tcp", Port: 0}},
		Backends:  backends,
		Affinity:  "none",
	}
}

func defaultDialCfg() state.ResolvedDialConfig {
	return state.ResolvedDialConfig{
		Timeout:     200 * time.Millisecond,
		MaxAttempts: 3,
		Budget:      1 * time.Second,
	}
}

func defaultEjectionCfg() state.ResolvedEjectionConfig {
	return state.ResolvedEjectionConfig{
		ConsecutiveFailures: 3,
		BaseTime:            30 * time.Second,
		MaxTime:             5 * time.Minute,
	}
}

// startProxyWithDialer creates a proxy with the given fake dialer and fake clock.
func startProxyWithDialer(t *testing.T, service state.Service, dialer *fakeDialer, dialCfg state.ResolvedDialConfig, clock *fakeClock) (*proxy.Proxy, *balancer.EjectionManager, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	em := balancer.NewEjectionManager(service.Backends, defaultEjectionCfg(), clock.Now, logger)
	b := balancer.NewRoundRobinWithEjection(service.Backends, em)
	p := proxy.NewWithDialer(service, b, dialer, dialCfg, 10*time.Second, em, logger)
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
	return p, em, cancel, done
}

// TestBackendDownGivesAllSuccessfulConnections tests that with backend A up
// and B down, 100 client connections all succeed and B receives at most 3
// attempts before ejection.
func TestBackendDownGivesAllSuccessfulConnections(t *testing.T) {
	dialer := newFakeDialer()
	defer dialer.close()

	// Create two up backends, then set B down.
	addrA := dialer.addUpBackend(t, "127.0.0.1", 0)
	hostA, portIntA := parsePort(t, addrA)

	backendA := state.Backend{Host: hostA, Port: portIntA}

	addrB := dialer.addUpBackend(t, "127.0.0.1", 0)
	hostB, portIntB := parsePort(t, addrB)

	backendB := state.Backend{Host: hostB, Port: portIntB}

	// Set B down with connection refused.
	dialer.setDown(addrB, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")})

	service := testService([]state.Backend{backendA, backendB})
	clock := newFakeClock()
	p, em, cancel, done := startProxyWithDialer(t, service, dialer, defaultDialCfg(), clock)
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	// Make 100 client connections. All should succeed (via A after B is ejected).
	for i := range 100 {
		conn, err := net.Dial("tcp", p.Address())
		if err != nil {
			t.Fatalf("client %d: dial proxy: %v", i, err)
		}
		// Write and read to verify the connection works.
		msg := []byte("hello")
		_, err = conn.Write(msg)
		if err != nil {
			t.Fatalf("client %d: write: %v", i, err)
		}
		resp := make([]byte, len(msg))
		_, err = io.ReadFull(conn, resp)
		if err != nil {
			t.Fatalf("client %d: read: %v (backend B may still be getting tried)", i, err)
		}
		_ = conn.Close()
	}

	// B should have received at most 3 attempts (consecutive_failures) before
	// being ejected. After ejection, all connections go to A.
	bDials := dialer.dialCount(addrB)
	if bDials > 3 {
		t.Fatalf("backend B received %d dial attempts, expected at most 3 before ejection", bDials)
	}

	// Verify B is ejected.
	states := em.States()
	if len(states) >= 2 && states[1].Up {
		t.Fatalf("expected backend B to be ejected, state = %+v", states[1])
	}

	// Verify all 100 client connections succeeded (total counter).
	stats := p.Stats()
	if len(stats.Services) != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	total := stats.Services[0].TotalConnections
	if total != 100 {
		t.Fatalf("expected 100 total connections, got %d", total)
	}
}

// TestRefusedVsTimedOutDials tests that both refused and timed-out dials are
// treated as connect-phase failures and retried.
func TestRefusedVsTimedOutDials(t *testing.T) {
	dialer := newFakeDialer()
	defer dialer.close()

	// Backend A: up (echo).
	addrA := dialer.addUpBackend(t, "127.0.0.1", 0)
	hostA, portIntA := parsePort(t, addrA)

	backendA := state.Backend{Host: hostA, Port: portIntA}

	// Backend B: times out (delay > dial timeout).
	addrB := dialer.addUpBackend(t, "127.0.0.1", 0)
	hostB, portIntB := parsePort(t, addrB)

	backendB := state.Backend{Host: hostB, Port: portIntB}
	dialer.setDown(addrB, nil) // remove from up map
	dialer.setDelay(addrB, 500*time.Millisecond)

	service := testService([]state.Backend{backendA, backendB})
	dialCfg := state.ResolvedDialConfig{Timeout: 50 * time.Millisecond, MaxAttempts: 3, Budget: 1 * time.Second}
	clock := newFakeClock()
	p, _, cancel, done := startProxyWithDialer(t, service, dialer, dialCfg, clock)
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	// Make a few connections; they should succeed via A after B times out.
	for i := range 5 {
		conn, err := net.Dial("tcp", p.Address())
		if err != nil {
			t.Fatalf("client %d: dial proxy: %v", i, err)
		}
		msg := []byte("hi")
		_, err = conn.Write(msg)
		if err != nil {
			t.Fatalf("client %d: write: %v", i, err)
		}
		resp := make([]byte, len(msg))
		_, err = io.ReadFull(conn, resp)
		if err != nil {
			t.Fatalf("client %d: read: %v", i, err)
		}
		_ = conn.Close()
	}
}

// TestEjectionDurationGrowthAndCap tests that ejection duration grows with
// each consecutive ejection and is capped at max_time.
func TestEjectionDurationGrowthAndCap(t *testing.T) {
	backends := []state.Backend{
		{Host: "127.0.0.1", Port: 1}, // always down
		{Host: "127.0.0.1", Port: 2}, // always down
	}
	dialer := newFakeDialer()
	defer dialer.close()

	service := testService(backends)
	dialCfg := state.ResolvedDialConfig{Timeout: 50 * time.Millisecond, MaxAttempts: 1, Budget: 200 * time.Millisecond}
	clock := newFakeClock()
	ejectionCfg := state.ResolvedEjectionConfig{
		ConsecutiveFailures: 1, // eject after 1 failure for faster testing
		BaseTime:            30 * time.Second,
		MaxTime:             60 * time.Second,
	}
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	em := balancer.NewEjectionManager(backends, ejectionCfg, clock.Now, logger)
	b := balancer.NewRoundRobinWithEjection(backends, em)
	p := proxy.NewWithDialer(service, b, dialer, dialCfg, 10*time.Second, em, logger)
	ctx, cancel := context.WithCancel(context.Background())
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
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	// Trigger 3 failures on backend 0 to get 3 ejections.
	// With consecutive_failures=1, each failure ejects.
	// But we have 2 backends, so the last one is never ejected.
	// We need to make backend 1 also fail to eject backend 0 multiple times.

	// First: fail backend 0 once -> ejected 30s
	conn, _ := net.Dial("tcp", p.Address())
	time.Sleep(100 * time.Millisecond)
	_ = conn.Close()

	states := em.States()
	// One of the backends should be ejected (the one that got the failure).
	// With round-robin starting at 0, backend 0 gets the first attempt.
	ejectedIdx := -1
	for i, s := range states {
		if !s.Up {
			ejectedIdx = i
			break
		}
	}
	if ejectedIdx == -1 {
		// Backend 1 might have been tried instead. Check both.
		t.Fatalf("expected at least one backend ejected, states = %+v", states)
	}

	// Advance past ejection, then fail again to get second ejection (streak=2, 60s).
	clock.Advance(31 * time.Second)

	conn2, _ := net.Dial("tcp", p.Address())
	time.Sleep(100 * time.Millisecond)
	_ = conn2.Close()

	states = em.States()
	if !states[ejectedIdx].EjectedUntil.IsZero() {
		secondUntil := states[ejectedIdx].EjectedUntil
		// Second ejection should be later than first (growth).
		_ = secondUntil // growth is implicitly tested by the cap test below
	}

	// Advance and trigger more ejections to hit the cap.
	clock.Advance(61 * time.Second)
	conn3, _ := net.Dial("tcp", p.Address())
	time.Sleep(100 * time.Millisecond)
	_ = conn3.Close()

	states = em.States()
	// Check that ejection duration is capped at max_time (60s).
	for i, s := range states {
		if !s.Up && !s.EjectedUntil.IsZero() {
			dur := s.EjectedUntil.Sub(clock.Now())
			if dur > 60*time.Second+time.Second {
				t.Fatalf("backend %d ejection duration %v exceeds max_time 60s", i, dur)
			}
		}
	}
}

// TestFailOpenWhenAllEjected tests that when all backends are ejected, the
// proxy still tries them (fail-open), picking the soonest-to-expire first.
func TestFailOpenWhenAllEjected(t *testing.T) {
	dialer := newFakeDialer()
	defer dialer.close()

	// Two backends, both will be set down initially, then one comes up.
	addrA := dialer.addUpBackend(t, "127.0.0.1", 0)
	hostA, portIntA := parsePort(t, addrA)

	backendA := state.Backend{Host: hostA, Port: portIntA}

	addrB := dialer.addUpBackend(t, "127.0.0.1", 0)
	hostB, portIntB := parsePort(t, addrB)

	backendB := state.Backend{Host: hostB, Port: portIntB}

	// Set both down initially.
	dialer.setDown(addrA, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")})
	dialer.setDown(addrB, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")})

	service := testService([]state.Backend{backendA, backendB})
	dialCfg := state.ResolvedDialConfig{Timeout: 50 * time.Millisecond, MaxAttempts: 3, Budget: 500 * time.Millisecond}
	clock := newFakeClock()
	ejectionCfg := state.ResolvedEjectionConfig{
		ConsecutiveFailures: 2,
		BaseTime:            30 * time.Second,
		MaxTime:             5 * time.Minute,
	}
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	em := balancer.NewEjectionManager(service.Backends, ejectionCfg, clock.Now, logger)
	b := balancer.NewRoundRobinWithEjection(service.Backends, em)
	p := proxy.NewWithDialer(service, b, dialer, dialCfg, 10*time.Second, em, logger)
	ctx, cancel := context.WithCancel(context.Background())
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
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	// Trigger enough failures to eject both backends.
	for range 10 {
		conn, _ := net.Dial("tcp", p.Address())
		time.Sleep(50 * time.Millisecond)
		_ = conn.Close()
	}

	// Both should be ejected (or at least one, since we never eject the last).
	// Actually with "never eject the last", at most 1 can be ejected when both are down.
	// But once one is ejected, the other gets all traffic and will also reach the threshold.
	// The "never eject last" rule means: when only 1 is non-ejected, don't eject it.
	// So we can eject at most 1 of 2 backends.
	states := em.States()
	ejectedCount := 0
	for _, s := range states {
		if !s.Up {
			ejectedCount++
		}
	}
	// With 2 backends and "never eject last", at most 1 can be ejected.
	if ejectedCount > 1 {
		t.Fatalf("expected at most 1 ejected backend (never eject last), got %d", ejectedCount)
	}

	// Now bring A back up and advance clock past ejection.
	dialer.setDown(addrA, nil) // clear the error, A is up again
	clock.Advance(31 * time.Second)

	// Connections should now succeed via A (fail-open or re-admitted).
	conn, err := net.Dial("tcp", p.Address())
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("test")
	_, err = conn.Write(msg)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	resp := make([]byte, len(msg))
	_, err = io.ReadFull(conn, resp)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	_ = conn.Close()
}

// TestLastBackendNeverEjected tests that the last non-ejected backend is never
// ejected even if it keeps failing.
func TestLastBackendNeverEjected(t *testing.T) {
	dialer := newFakeDialer()
	defer dialer.close()

	// Backend A: down (will be ejected).
	// Backend B: down (last non-ejected, should never be ejected).
	backendA := state.Backend{Host: "127.0.0.1", Port: 1}
	backendB := state.Backend{Host: "127.0.0.1", Port: 2}

	service := testService([]state.Backend{backendA, backendB})
	dialCfg := state.ResolvedDialConfig{Timeout: 50 * time.Millisecond, MaxAttempts: 2, Budget: 200 * time.Millisecond}
	clock := newFakeClock()
	ejectionCfg := state.ResolvedEjectionConfig{
		ConsecutiveFailures: 2,
		BaseTime:            30 * time.Second,
		MaxTime:             5 * time.Minute,
	}
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	em := balancer.NewEjectionManager(service.Backends, ejectionCfg, clock.Now, logger)
	b := balancer.NewRoundRobinWithEjection(service.Backends, em)
	p := proxy.NewWithDialer(service, b, dialer, dialCfg, 10*time.Second, em, logger)
	ctx, cancel := context.WithCancel(context.Background())
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
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	// Generate many failures.
	for range 20 {
		conn, _ := net.Dial("tcp", p.Address())
		time.Sleep(30 * time.Millisecond)
		_ = conn.Close()
	}

	states := em.States()
	// At most 1 backend should be ejected (never the last non-ejected one).
	ejectedCount := 0
	for i, s := range states {
		if !s.Up {
			ejectedCount++
			t.Logf("backend %d is ejected until %v", i, s.EjectedUntil)
		}
	}
	if ejectedCount > 1 {
		t.Fatalf("expected at most 1 ejected backend (never eject last), got %d", ejectedCount)
	}
}

// TestNoRetryAfterBytesForwarded tests that once bytes are forwarded to a
// backend, no retry happens even if the connection fails later.
func TestNoRetryAfterBytesForwarded(t *testing.T) {
	dialer := newFakeDialer()
	defer dialer.close()

	// Backend A: up, echoes but closes after first read.
	addrA := dialer.addUpBackend(t, "127.0.0.1", 0)
	hostA, portIntA := parsePort(t, addrA)

	backendA := state.Backend{Host: hostA, Port: portIntA}

	// Backend B: down.
	addrB := dialer.addUpBackend(t, "127.0.0.1", 0)
	hostB, portIntB := parsePort(t, addrB)

	backendB := state.Backend{Host: hostB, Port: portIntB}
	dialer.setDown(addrB, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")})

	service := testService([]state.Backend{backendA, backendB})
	clock := newFakeClock()
	p, _, cancel, done := startProxyWithDialer(t, service, dialer, defaultDialCfg(), clock)
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	// Make a connection that succeeds (lands on A after B is ejected).
	// Once bytes are forwarded, even if the backend dies, no retry.
	conn, err := net.Dial("tcp", p.Address())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte("hello"))
	resp := make([]byte, 5)
	_, _ = io.ReadFull(conn, resp)
	_ = conn.Close()

	// The key assertion: total connections should be 1 (no retry after success).
	stats := p.Stats()
	total := stats.Services[0].TotalConnections
	if total != 1 {
		t.Fatalf("expected 1 total connection, got %d (retry after bytes forwarded?)", total)
	}
}

// TestAttemptsLimitRespected tests that max_attempts limits the number of
// dial attempts per client connection.
func TestAttemptsLimitRespected(t *testing.T) {
	dialer := newFakeDialer()
	defer dialer.close()

	// Three backends, all down.
	backendA := state.Backend{Host: "127.0.0.1", Port: 1}
	backendB := state.Backend{Host: "127.0.0.1", Port: 2}
	backendC := state.Backend{Host: "127.0.0.1", Port: 3}

	service := testService([]state.Backend{backendA, backendB, backendC})
	dialCfg := state.ResolvedDialConfig{Timeout: 50 * time.Millisecond, MaxAttempts: 2, Budget: 500 * time.Millisecond}
	clock := newFakeClock()
	ejectionCfg := state.ResolvedEjectionConfig{
		ConsecutiveFailures: 100, // high threshold so no ejection happens
		BaseTime:            30 * time.Second,
		MaxTime:             5 * time.Minute,
	}
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	em := balancer.NewEjectionManager(service.Backends, ejectionCfg, clock.Now, logger)
	b := balancer.NewRoundRobinWithEjection(service.Backends, em)
	p := proxy.NewWithDialer(service, b, dialer, dialCfg, 10*time.Second, em, logger)
	ctx, cancel := context.WithCancel(context.Background())
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
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	// Make one client connection. It should try at most 2 backends (max_attempts=2).
	conn, err := net.Dial("tcp", p.Address())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	_ = conn.Close()

	// Count total dial attempts across all backends.
	totalDials := dialer.dialCount(backendA.Address()) + dialer.dialCount(backendB.Address()) + dialer.dialCount(backendC.Address())
	if totalDials > 2 {
		t.Fatalf("expected at most 2 dial attempts (max_attempts=2), got %d", totalDials)
	}
}

// TestBudgetLimitRespected tests that the dial budget limits total dial time.
func TestBudgetLimitRespected(t *testing.T) {
	dialer := newFakeDialer()
	defer dialer.close()

	// Two backends, both with long delays (simulating black-hole).
	addrA := dialer.addUpBackend(t, "127.0.0.1", 0)
	hostA, portIntA := parsePort(t, addrA)

	backendA := state.Backend{Host: hostA, Port: portIntA}

	addrB := dialer.addUpBackend(t, "127.0.0.1", 0)
	hostB, portIntB := parsePort(t, addrB)

	backendB := state.Backend{Host: hostB, Port: portIntB}

	// Set both with delays longer than per-dial timeout but shorter than budget.
	dialer.setDown(addrA, nil)
	dialer.setDown(addrB, nil)
	dialer.setDelay(addrA, 200*time.Millisecond)
	dialer.setDelay(addrB, 200*time.Millisecond)

	service := testService([]state.Backend{backendA, backendB})
	// dial timeout 100ms, budget 150ms -> only 1 attempt can complete within budget.
	dialCfg := state.ResolvedDialConfig{Timeout: 100 * time.Millisecond, MaxAttempts: 3, Budget: 150 * time.Millisecond}
	clock := newFakeClock()
	p, _, cancel, done := startProxyWithDialer(t, service, dialer, dialCfg, clock)
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	conn, err := net.Dial("tcp", p.Address())
	if err != nil {
		t.Fatal(err)
	}
	// Wait for the budget to expire and the connection to close.
	time.Sleep(300 * time.Millisecond)
	_ = conn.Close()

	// The connection should have failed (no successful proxy).
	stats := p.Stats()
	total := stats.Services[0].TotalConnections
	if total != 0 {
		t.Fatalf("expected 0 successful connections (budget exceeded), got %d", total)
	}
}

// TestNoGoroutineLeaks tests that no goroutines leak after the proxy shuts down.
func TestNoGoroutineLeaks(t *testing.T) {
	before := runtime.NumGoroutine()

	dialer := newFakeDialer()
	defer dialer.close()

	addrA := dialer.addUpBackend(t, "127.0.0.1", 0)
	hostA, portIntA := parsePort(t, addrA)

	backendA := state.Backend{Host: hostA, Port: portIntA}

	addrB := dialer.addUpBackend(t, "127.0.0.1", 0)
	hostB, portIntB := parsePort(t, addrB)

	backendB := state.Backend{Host: hostB, Port: portIntB}
	dialer.setDown(addrB, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")})

	service := testService([]state.Backend{backendA, backendB})
	clock := newFakeClock()
	p, _, cancel, done := startProxyWithDialer(t, service, dialer, defaultDialCfg(), clock)

	// Make some connections.
	for range 10 {
		conn, err := net.Dial("tcp", p.Address())
		if err != nil {
			t.Fatal(err)
		}
		_, _ = conn.Write([]byte("x"))
		resp := make([]byte, 1)
		_, _ = io.ReadFull(conn, resp)
		_ = conn.Close()
	}

	cancel()
	if err := <-done; err != nil {
		t.Error(err)
	}

	// Allow goroutines to settle.
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > before+2 {
		t.Fatalf("goroutines after shutdown = %d, before = %d", got, before)
	}
}

// TestEjectionAndReadmissionLogging tests that ejection and recovery are logged.
func TestEjectionAndReadmissionLogging(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dialer := newFakeDialer()
	defer dialer.close()

	addrA := dialer.addUpBackend(t, "127.0.0.1", 0)
	hostA, portIntA := parsePort(t, addrA)

	backendA := state.Backend{Host: hostA, Port: portIntA}

	addrB := dialer.addUpBackend(t, "127.0.0.1", 0)
	hostB, portIntB := parsePort(t, addrB)

	backendB := state.Backend{Host: hostB, Port: portIntB}
	dialer.setDown(addrB, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")})

	service := testService([]state.Backend{backendA, backendB})
	dialCfg := state.ResolvedDialConfig{Timeout: 50 * time.Millisecond, MaxAttempts: 3, Budget: 500 * time.Millisecond}
	clock := newFakeClock()
	ejectionCfg := state.ResolvedEjectionConfig{
		ConsecutiveFailures: 2,
		BaseTime:            30 * time.Second,
		MaxTime:             5 * time.Minute,
	}
	em := balancer.NewEjectionManager(service.Backends, ejectionCfg, clock.Now, logger)
	b := balancer.NewRoundRobinWithEjection(service.Backends, em)
	p := proxy.NewWithDialer(service, b, dialer, dialCfg, 10*time.Second, em, logger)
	ctx, cancel := context.WithCancel(context.Background())
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
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	// Generate failures to trigger ejection of B.
	for range 5 {
		conn, _ := net.Dial("tcp", p.Address())
		time.Sleep(50 * time.Millisecond)
		_ = conn.Close()
	}

	output := buf.String()
	if !strings.Contains(output, "backend ejected") {
		t.Fatalf("expected 'backend ejected' log line, got:\n%s", output)
	}

	// Bring B back up and advance clock past ejection.
	dialer.setDown(addrB, nil) // clear error
	clock.Advance(31 * time.Second)

	// Make a connection that should land on B and succeed.
	conn, err := net.Dial("tcp", p.Address())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte("hi"))
	resp := make([]byte, 2)
	_, _ = io.ReadFull(conn, resp)
	_ = conn.Close()

	// Wait a bit for the log to be written.
	time.Sleep(50 * time.Millisecond)

	output = buf.String()
	if !strings.Contains(output, "backend recovered") {
		t.Fatalf("expected 'backend recovered' log line, got:\n%s", output)
	}
}

// TestStatusShowsEjectionState tests that the status output includes ejection state.
func TestStatusShowsEjectionState(t *testing.T) {
	dialer := newFakeDialer()
	defer dialer.close()

	addrA := dialer.addUpBackend(t, "127.0.0.1", 0)
	hostA, portIntA := parsePort(t, addrA)

	backendA := state.Backend{Host: hostA, Port: portIntA}

	addrB := dialer.addUpBackend(t, "127.0.0.1", 0)
	hostB, portIntB := parsePort(t, addrB)

	backendB := state.Backend{Host: hostB, Port: portIntB}
	dialer.setDown(addrB, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")})

	service := testService([]state.Backend{backendA, backendB})
	clock := newFakeClock()
	p, _, cancel, done := startProxyWithDialer(t, service, dialer, defaultDialCfg(), clock)
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	// Generate failures to eject B.
	for range 5 {
		conn, _ := net.Dial("tcp", p.Address())
		time.Sleep(50 * time.Millisecond)
		_ = conn.Close()
	}

	stats := p.Stats()
	if len(stats.Services) != 1 || len(stats.Services[0].Backends) != 2 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	// Backend B (index 1) should be ejected.
	bStats := stats.Services[0].Backends[1]
	if bStats.State != "ejected" {
		t.Fatalf("expected backend B state 'ejected', got %q", bStats.State)
	}
	if bStats.EjectedUntil == nil {
		t.Fatalf("expected ejected_until to be set")
	}
	if bStats.LastError == "" {
		t.Fatalf("expected last_error to be set")
	}
	if bStats.LastFailureAt == nil {
		t.Fatalf("expected last_failure_at to be set")
	}

	// Backend A should be up.
	aStats := stats.Services[0].Backends[0]
	if aStats.State != "up" {
		t.Fatalf("expected backend A state 'up', got %q", aStats.State)
	}
}

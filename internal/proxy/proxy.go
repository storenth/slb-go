package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/storenth/slb-go/internal/balancer"
	"github.com/storenth/slb-go/internal/state"
)

// Dialer is the interface for dialing backend connections. It is injectable
// for testing.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// tcpDialer wraps net.Dialer to implement Dialer.
type tcpDialer struct {
	d net.Dialer
}

func (d *tcpDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d.d.DialContext(ctx, network, address)
}

// isConnectPhaseFailure reports whether err is a connect-phase failure
// (refused, timeout, unreachable, no route) that warrants a retry on another
// backend. Errors after bytes were forwarded are never retried.
func isConnectPhaseFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	// Connection refused, no route, unreachable — all are *net.OpError with
	// a syscall errno. We treat any dial error as connect-phase since
	// DialContext only fails during connect.
	return true
}

type Stats struct {
	Services []ServiceStats `json:"services"`
	Uptime   time.Duration  `json:"-"`
}

type ServiceStats struct {
	Name              string          `json:"name"`
	Listeners         []ListenerStats `json:"listeners"`
	Backends          []BackendStats  `json:"backends"`
	ActiveConnections int64           `json:"active_connections"`
	TotalConnections  uint64          `json:"total_connections"`
	BytesFromClients  uint64          `json:"bytes_from_clients"`
	BytesFromBackends uint64          `json:"bytes_from_backends"`
}

type ListenerStats struct {
	Address           string           `json:"address"`
	Protocol          string           `json:"protocol"`
	Port              int              `json:"port,omitempty"`
	PortRange         *state.PortRange `json:"port_range,omitempty"`
	ActiveConnections int64            `json:"active_connections"`
	TotalConnections  uint64           `json:"total_connections"`
}

// BackendStats reports per-backend counters and ejection state.
type BackendStats struct {
	Host              string     `json:"host"`
	Port              int        `json:"port"`
	ActiveConnections int64      `json:"active_connections"`
	TotalConnections  uint64     `json:"total_connections"`
	FailedConnections uint64     `json:"failed_connections"`
	BytesFromClients  uint64     `json:"bytes_from_clients"`
	BytesFromBackend  uint64     `json:"bytes_from_backend"`
	State             string     `json:"state"`
	EjectedUntil      *time.Time `json:"ejected_until,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	LastFailureAt     *time.Time `json:"last_failure_at,omitempty"`
}

type backendCounter struct {
	active           atomic.Int64
	total            atomic.Uint64
	failed           atomic.Uint64
	bytesFromClients atomic.Uint64
	bytesFromBackend atomic.Uint64
}

// dialFailureRateLimiter limits dial-failure log lines to at most one per
// backend per window (10 seconds). Suppressed repeats are counted and included
// in the next emitted line.
type dialFailureRateLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	now     func() time.Time
	entries map[string]*rateLimitEntry
}

type rateLimitEntry struct {
	lastLogged time.Time
	suppressed atomic.Uint64
}

func newDialFailureRateLimiter(window time.Duration) *dialFailureRateLimiter {
	return &dialFailureRateLimiter{window: window, now: time.Now, entries: make(map[string]*rateLimitEntry)}
}

// shouldLog reports whether a dial-failure log line should be emitted now for
// the given backend key. If false, the suppressed count is incremented.
func (r *dialFailureRateLimiter) shouldLog(key string) (suppressed uint64, emit bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[key]
	if !ok {
		entry = &rateLimitEntry{}
		r.entries[key] = entry
	}
	now := r.now()
	if entry.lastLogged.IsZero() || now.Sub(entry.lastLogged) >= r.window {
		suppressed := entry.suppressed.Swap(0)
		entry.lastLogged = now
		return suppressed, true
	}
	entry.suppressed.Add(1)
	return 0, false
}

type Proxy struct {
	service      state.Service
	address      string
	balancer     balancer.Balancer
	dialer       Dialer
	dialCfg      state.ResolvedDialConfig
	idleTimeout  time.Duration
	logger       *slog.Logger
	dialFailures *dialFailureRateLimiter
	ejection     *balancer.EjectionManager
	started      time.Time
	total        atomic.Uint64
	active       atomic.Int64
	fromClients  atomic.Uint64
	fromBackend  atomic.Uint64
	counters     []backendCounter
	mu           sync.Mutex
	listener     *net.TCPListener
	connections  map[*net.TCPConn]struct{}
	wg           sync.WaitGroup
}

func New(service state.Service, b balancer.Balancer, idleTimeout time.Duration) *Proxy {
	return NewWithLogger(service, b, idleTimeout, slog.Default())
}

// NewWithLogger creates a Proxy that logs dial failures (warn, rate-limited)
// and per-connection events (debug) through the given logger. Dial settings
// come from service.Dial.Resolve(); idle timeout is global.
func NewWithLogger(service state.Service, b balancer.Balancer, idleTimeout time.Duration, logger *slog.Logger) *Proxy {
	address := ""
	if len(service.Listeners) != 0 {
		address = service.Listeners[0].AddressPort()
	}
	if logger == nil {
		logger = slog.Default()
	}
	dialCfg := service.Dial.Resolve()
	em := balancer.NewEjectionManager(service.Backends, service.Ejection.Resolve(), time.Now, logger)
	return &Proxy{
		service:      service,
		address:      address,
		balancer:     b,
		dialer:       &tcpDialer{d: net.Dialer{Timeout: dialCfg.Timeout}},
		dialCfg:      dialCfg,
		idleTimeout:  idleTimeout,
		logger:       logger,
		dialFailures: newDialFailureRateLimiter(10 * time.Second),
		ejection:     em,
		counters:     make([]backendCounter, len(service.Backends)),
		connections:  make(map[*net.TCPConn]struct{}),
	}
}

// NewWithDialer creates a Proxy with a custom Dialer and EjectionManager,
// intended for tests that need to inject failures and control the clock.
func NewWithDialer(service state.Service, b balancer.Balancer, dialer Dialer, dialCfg state.ResolvedDialConfig, idleTimeout time.Duration, em *balancer.EjectionManager, logger *slog.Logger) *Proxy {
	address := ""
	if len(service.Listeners) != 0 {
		address = service.Listeners[0].AddressPort()
	}
	if logger == nil {
		logger = slog.Default()
	}
	if dialer == nil {
		dialer = &tcpDialer{d: net.Dialer{Timeout: dialCfg.Timeout}}
	}
	return &Proxy{
		service:      service,
		address:      address,
		balancer:     b,
		dialer:       dialer,
		dialCfg:      dialCfg,
		idleTimeout:  idleTimeout,
		logger:       logger,
		dialFailures: newDialFailureRateLimiter(10 * time.Second),
		ejection:     em,
		counters:     make([]backendCounter, len(service.Backends)),
		connections:  make(map[*net.TCPConn]struct{}),
	}
}

// Ejection returns the ejection manager (for status reporting).
func (p *Proxy) Ejection() *balancer.EjectionManager { return p.ejection }

func (p *Proxy) Address() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.listener != nil {
		return p.listener.Addr().String()
	}
	return p.address
}

func (p *Proxy) Run(ctx context.Context) error {
	return p.runWithLogger(ctx, p.logger)
}

func (p *Proxy) runWithLogger(ctx context.Context, logger *slog.Logger) error {
	addr, err := net.ResolveTCPAddr("tcp", p.address)
	if err != nil {
		return fmt.Errorf("resolve listen address for service %q: %w", p.service.Name, err)
	}
	listener, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s for service %q: %w", p.address, p.service.Name, err)
	}
	p.mu.Lock()
	p.listener = listener
	p.started = time.Now()
	p.mu.Unlock()
	logger.Info("listening", "service", p.service.Name, "address", listener.Addr().String())
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			logger.Info("shutdown signal received", "service", p.service.Name)
			_ = listener.Close()
			p.closeConnections()
		case <-stop:
		}
	}()
	defer func() { close(stop); _ = listener.Close(); p.closeConnections(); p.wg.Wait() }()
	for {
		client, err := listener.AcceptTCP()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept connection for service %q: %w", p.service.Name, err)
		}
		p.track(client, true)
		p.wg.Add(1)
		go p.handle(ctx, client)
	}
}

func (p *Proxy) handle(ctx context.Context, client *net.TCPConn) {
	defer p.wg.Done()
	defer p.track(client, false)
	defer func() { _ = client.Close() }()
	startTime := time.Now()

	tried := make(map[int]bool)
	budgetCtx, budgetCancel := context.WithTimeout(ctx, p.dialCfg.Budget)
	defer budgetCancel()

	for attempt := 0; attempt < p.dialCfg.MaxAttempts; attempt++ {
		if budgetCtx.Err() != nil {
			break
		}
		idx, backend, err := p.balancer.Next(balancer.Request{
			Service:       p.service,
			ClientAddress: client.RemoteAddr(),
			TriedBackends: tried,
		})
		if err != nil {
			break
		}
		// Per-attempt dial timeout.
		dialCtx, dialCancel := context.WithTimeout(budgetCtx, p.dialCfg.Timeout)
		conn, dialErr := p.dialer.DialContext(dialCtx, "tcp", backend.Address())
		dialCancel()
		tried[idx] = true

		if dialErr != nil {
			if !isConnectPhaseFailure(dialErr) {
				// Non-connect-phase failure: record but don't retry.
				p.counters[idx].failed.Add(1)
				p.ejection.RecordFailure(idx, dialErr)
				p.logDialFailure(backend, dialErr)
				break
			}
			p.counters[idx].failed.Add(1)
			p.ejection.RecordFailure(idx, dialErr)
			p.logDialFailure(backend, dialErr)
			continue
		}

		server, ok := conn.(*net.TCPConn)
		if !ok {
			_ = conn.Close()
			p.counters[idx].failed.Add(1)
			p.ejection.RecordFailure(idx, fmt.Errorf("dial returned non-TCP conn"))
			continue
		}
		// Success: record and proceed to proxy. No retry after this point.
		p.ejection.RecordSuccess(idx)
		p.proxyConnection(ctx, client, server, idx, startTime)
		return
	}
	// All attempts exhausted: client connection is closed by the deferred
	// client.Close().
}

// closeReason describes why a proxied connection was closed.
type closeReason string

const (
	reasonNone          closeReason = ""
	reasonIdle          closeReason = "idle"
	reasonClientClosed  closeReason = "client closed"
	reasonBackendClosed closeReason = "backend closed"
	reasonError         closeReason = "error"
)

// copyWithIdle copies from src to dst, enforcing an idle timeout. If no bytes
// are read within the idle window (considering activity in the other direction
// via lastActivity), the copy returns with reasonIdle.
//
// When idleTimeout is zero or negative, io.Copy is used instead, preserving
// splice(2) on Linux. A positive idleTimeout requires per-read deadline
// refresh, which gives up splice: reads enter user space so the deadline can
// be reset after each chunk. See docs/02-linux-io.md (Idle timeout vs splice).
func copyWithIdle(dst, src *net.TCPConn, idleTimeout time.Duration, lastActivity *atomic.Int64, srcClosed, dstClosed closeReason) (int64, closeReason) {
	if idleTimeout <= 0 {
		n, _ := io.Copy(dst, src)
		return n, srcClosed
	}
	buf := make([]byte, 32*1024)
	var total int64
	for {
		readStart := time.Now()
		if err := src.SetReadDeadline(readStart.Add(idleTimeout)); err != nil {
			return total, reasonError
		}
		n, err := src.Read(buf)
		if n > 0 {
			total += int64(n)
			lastActivity.Store(time.Now().UnixNano())
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return total, dstClosed
			}
		}
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				last := time.Unix(0, lastActivity.Load())
				if last.After(readStart) {
					continue
				}
				return total, reasonIdle
			}
			return total, srcClosed
		}
	}
}

// proxyConnection does the actual bidirectional copy between client and
// backend. It is called after a successful dial. Bytes are never retried after
// this point.
func (p *Proxy) proxyConnection(ctx context.Context, client, server *net.TCPConn, index int, startTime time.Time) {
	p.track(server, true)
	defer p.track(server, false)
	defer func() { _ = server.Close() }()
	if ctx.Err() != nil {
		return
	}
	p.total.Add(1)
	p.active.Add(1)
	defer p.active.Add(-1)
	p.counters[index].total.Add(1)
	p.counters[index].active.Add(1)
	defer p.counters[index].active.Add(-1)
	p.logger.Debug("connection accepted",
		"service", p.service.Name,
		"client", client.RemoteAddr().String(),
		"backend", server.RemoteAddr().String(),
	)

	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().UnixNano())

	var copies sync.WaitGroup
	var clientBytes, backendBytes int64
	var firstReason closeReason
	var reasonMu sync.Mutex
	setReason := func(r closeReason) {
		reasonMu.Lock()
		if firstReason == reasonNone {
			firstReason = r
		}
		reasonMu.Unlock()
	}
	copies.Add(2)
	go func() {
		defer copies.Done()
		n, reason := copyWithIdle(server, client, p.idleTimeout, &lastActivity, reasonClientClosed, reasonBackendClosed)
		clientBytes = n
		p.recordClientBytes(index, uint64(n))
		if reason == reasonIdle {
			_ = client.Close()
			_ = server.Close()
		} else {
			_ = server.CloseWrite()
		}
		setReason(reason)
	}()
	go func() {
		defer copies.Done()
		n, reason := copyWithIdle(client, server, p.idleTimeout, &lastActivity, reasonBackendClosed, reasonClientClosed)
		backendBytes = n
		p.recordBackendBytes(index, uint64(n))
		if reason == reasonIdle {
			_ = client.Close()
			_ = server.Close()
		} else {
			_ = client.CloseWrite()
		}
		setReason(reason)
	}()
	copies.Wait()
	p.logger.Debug("connection closed",
		"service", p.service.Name,
		"client", client.RemoteAddr().String(),
		"backend", server.RemoteAddr().String(),
		"duration_ms", time.Since(startTime).Milliseconds(),
		"bytes_in", clientBytes,
		"bytes_out", backendBytes,
		"close_reason", firstReason,
	)
}

// logDialFailure emits a rate-limited warn log for a backend dial failure.
func (p *Proxy) logDialFailure(backend state.Backend, err error) {
	key := backend.Address()
	suppressed, emit := p.dialFailures.shouldLog(key)
	if !emit {
		return
	}
	attrs := []any{
		"service", p.service.Name,
		"backend", backend.Address(),
		"error", err.Error(),
	}
	if suppressed > 0 {
		attrs = append(attrs, "suppressed", suppressed)
	}
	p.logger.Warn("backend dial failed", attrs...)
}

func (p *Proxy) recordClientBytes(index int, count uint64) {
	p.fromClients.Add(count)
	if index >= 0 {
		p.counters[index].bytesFromClients.Add(count)
	}
}

func (p *Proxy) recordBackendBytes(index int, count uint64) {
	p.fromBackend.Add(count)
	if index >= 0 {
		p.counters[index].bytesFromBackend.Add(count)
	}
}

func (p *Proxy) track(conn *net.TCPConn, add bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if add {
		p.connections[conn] = struct{}{}
	} else {
		delete(p.connections, conn)
	}
}

func (p *Proxy) closeConnections() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for conn := range p.connections {
		_ = conn.Close()
	}
}

func (p *Proxy) Stats() Stats {
	service := ServiceStats{Name: p.service.Name, ActiveConnections: p.active.Load(), TotalConnections: p.total.Load(), BytesFromClients: p.fromClients.Load(), BytesFromBackends: p.fromBackend.Load()}
	service.Listeners = make([]ListenerStats, len(p.service.Listeners))
	for i, listener := range p.service.Listeners {
		service.Listeners[i] = ListenerStats{Address: listener.Address, Protocol: listener.Protocol, Port: listener.Port, PortRange: listener.PortRange, ActiveConnections: service.ActiveConnections, TotalConnections: service.TotalConnections}
	}
	service.Backends = make([]BackendStats, len(p.service.Backends))
	ejectionStates := p.ejection.States()
	for i, backend := range p.service.Backends {
		bs := BackendStats{
			Host:              backend.Host,
			Port:              backend.Port,
			ActiveConnections: p.counters[i].active.Load(),
			TotalConnections:  p.counters[i].total.Load(),
			FailedConnections: p.counters[i].failed.Load(),
			BytesFromClients:  p.counters[i].bytesFromClients.Load(),
			BytesFromBackend:  p.counters[i].bytesFromBackend.Load(),
		}
		if i < len(ejectionStates) {
			es := ejectionStates[i]
			if es.Up {
				bs.State = "up"
			} else {
				bs.State = "ejected"
				t := es.EjectedUntil
				bs.EjectedUntil = &t
			}
			bs.LastError = es.LastErr
			if !es.LastFailureAt.IsZero() {
				t := es.LastFailureAt
				bs.LastFailureAt = &t
			}
		}
		service.Backends[i] = bs
	}
	result := Stats{Services: []ServiceStats{service}}
	p.mu.Lock()
	if !p.started.IsZero() {
		result.Uptime = time.Since(p.started)
	}
	p.mu.Unlock()
	return result
}

type Collection struct {
	Proxies []*Proxy
	Started time.Time
}

func (c Collection) Run(ctx context.Context, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	errs := make(chan error, len(c.Proxies))
	for _, p := range c.Proxies {
		go func(p *Proxy) { errs <- p.runWithLogger(ctx, logger) }(p)
	}
	return <-errs
}

func (c Collection) Stats() Stats {
	result := Stats{Services: make([]ServiceStats, 0, len(c.Proxies))}
	if !c.Started.IsZero() {
		result.Uptime = time.Since(c.Started)
	}
	for _, p := range c.Proxies {
		result.Services = append(result.Services, p.Stats().Services...)
	}
	return result
}

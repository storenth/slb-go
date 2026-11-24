package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/storenth/slb-go/internal/balancer"
	"github.com/storenth/slb-go/internal/state"
)

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

type BackendStats struct {
	Host              string `json:"host"`
	Port              int    `json:"port"`
	ActiveConnections int64  `json:"active_connections"`
	TotalConnections  uint64 `json:"total_connections"`
	FailedConnections uint64 `json:"failed_connections"`
	BytesFromClients  uint64 `json:"bytes_from_clients"`
	BytesFromBackend  uint64 `json:"bytes_from_backend"`
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
	dialTimeout  time.Duration
	idleTimeout  time.Duration
	logger       *slog.Logger
	dialFailures *dialFailureRateLimiter
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

func New(service state.Service, b balancer.Balancer, dialTimeout, idleTimeout time.Duration) *Proxy {
	return NewWithLogger(service, b, dialTimeout, idleTimeout, slog.Default())
}

// NewWithLogger creates a Proxy that logs dial failures (warn, rate-limited)
// and per-connection events (debug) through the given logger.
func NewWithLogger(service state.Service, b balancer.Balancer, dialTimeout, idleTimeout time.Duration, logger *slog.Logger) *Proxy {
	address := ""
	if len(service.Listeners) != 0 {
		address = service.Listeners[0].AddressPort()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Proxy{
		service:      service,
		address:      address,
		balancer:     b,
		dialTimeout:  dialTimeout,
		idleTimeout:  idleTimeout,
		logger:       logger,
		dialFailures: newDialFailureRateLimiter(10 * time.Second),
		counters:     make([]backendCounter, len(service.Backends)),
		connections:  make(map[*net.TCPConn]struct{}),
	}
}

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
	backend, err := p.balancer.Next(balancer.Request{Service: p.service, ClientAddress: client.RemoteAddr()})
	if err != nil {
		return
	}
	index := p.backendIndex(backend)
	dialer := net.Dialer{Timeout: p.dialTimeout, KeepAlive: p.idleTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", backend.Address())
	if err != nil {
		if index >= 0 {
			p.counters[index].failed.Add(1)
		}
		p.logDialFailure(backend, err)
		return
	}
	server, ok := conn.(*net.TCPConn)
	if !ok {
		_ = conn.Close()
		return
	}
	p.track(server, true)
	defer p.track(server, false)
	defer func() { _ = server.Close() }()
	if ctx.Err() != nil {
		return
	}
	p.configureIdleDetection(client)
	p.configureIdleDetection(server)
	p.total.Add(1)
	p.active.Add(1)
	defer p.active.Add(-1)
	if index >= 0 {
		p.counters[index].total.Add(1)
		p.counters[index].active.Add(1)
		defer p.counters[index].active.Add(-1)
	}
	p.logger.Debug("connection accepted",
		"service", p.service.Name,
		"client", client.RemoteAddr().String(),
		"backend", backend.Address(),
	)

	var copies sync.WaitGroup
	var clientBytes, backendBytes uint64
	copies.Add(2)
	go func() {
		defer copies.Done()
		count, _ := io.Copy(server, client)
		clientBytes = uint64(count)
		p.recordClientBytes(index, clientBytes)
		_ = server.CloseWrite()
	}()
	go func() {
		defer copies.Done()
		count, _ := io.Copy(client, server)
		backendBytes = uint64(count)
		p.recordBackendBytes(index, backendBytes)
		_ = client.CloseWrite()
	}()
	copies.Wait()
	p.logger.Debug("connection closed",
		"service", p.service.Name,
		"client", client.RemoteAddr().String(),
		"backend", backend.Address(),
		"duration_ms", time.Since(startTime).Milliseconds(),
		"bytes_in", clientBytes,
		"bytes_out", backendBytes,
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

func (p *Proxy) configureIdleDetection(conn *net.TCPConn) {
	if p.idleTimeout <= 0 {
		return
	}
	interval := p.idleTimeout / 3
	if interval < time.Second {
		interval = time.Second
	}
	// Per-read idle deadline refresh requires wrapping or chunking io.Copy and
	// disables Linux splice. Keep plain TCPConn copies and use the configured
	// idle period for TCP keepalive dead-peer detection instead.
	_ = conn.SetKeepAliveConfig(net.KeepAliveConfig{Enable: true, Idle: p.idleTimeout, Interval: interval, Count: 3})
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

func (p *Proxy) backendIndex(want state.Backend) int {
	for i, backend := range p.service.Backends {
		if backend == want {
			return i
		}
	}
	return -1
}

func (p *Proxy) Stats() Stats {
	service := ServiceStats{Name: p.service.Name, ActiveConnections: p.active.Load(), TotalConnections: p.total.Load(), BytesFromClients: p.fromClients.Load(), BytesFromBackends: p.fromBackend.Load()}
	service.Listeners = make([]ListenerStats, len(p.service.Listeners))
	for i, listener := range p.service.Listeners {
		service.Listeners[i] = ListenerStats{Address: listener.Address, Protocol: listener.Protocol, Port: listener.Port, PortRange: listener.PortRange, ActiveConnections: service.ActiveConnections, TotalConnections: service.TotalConnections}
	}
	service.Backends = make([]BackendStats, len(p.service.Backends))
	for i, backend := range p.service.Backends {
		service.Backends[i] = BackendStats{Host: backend.Host, Port: backend.Port, ActiveConnections: p.counters[i].active.Load(), TotalConnections: p.counters[i].total.Load(), FailedConnections: p.counters[i].failed.Load(), BytesFromClients: p.counters[i].bytesFromClients.Load(), BytesFromBackend: p.counters[i].bytesFromBackend.Load()}
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

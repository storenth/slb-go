package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/storenth/ecmp-load-balancer/internal/balancer"
	"github.com/storenth/ecmp-load-balancer/internal/state"
)

type Stats struct {
	Backends          []BackendStats `json:"backends"`
	ActiveConnections int64          `json:"active_connections"`
	TotalConnections  uint64         `json:"total_connections"`
	Uptime            time.Duration  `json:"-"`
}

type BackendStats struct {
	Host              string `json:"host"`
	Port              int    `json:"port"`
	ActiveConnections int64  `json:"active_connections"`
	TotalConnections  uint64 `json:"total_connections"`
}

type backendCounter struct {
	active atomic.Int64
	total  atomic.Uint64
}

type Proxy struct {
	address     string
	balancer    balancer.Balancer
	backends    []state.Backend
	dialTimeout time.Duration
	idleTimeout time.Duration
	started     time.Time
	total       atomic.Uint64
	active      atomic.Int64
	counters    []backendCounter
	mu          sync.Mutex
	listener    *net.TCPListener
	connections map[*net.TCPConn]struct{}
	wg          sync.WaitGroup
}

func New(address string, backends []state.Backend, b balancer.Balancer, dialTimeout, idleTimeout time.Duration) *Proxy {
	return &Proxy{address: address, backends: append([]state.Backend(nil), backends...), balancer: b, dialTimeout: dialTimeout, idleTimeout: idleTimeout, counters: make([]backendCounter, len(backends)), connections: make(map[*net.TCPConn]struct{})}
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
	addr, err := net.ResolveTCPAddr("tcp", p.address)
	if err != nil {
		return fmt.Errorf("resolve listen address: %w", err)
	}
	listener, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", p.address, err)
	}
	p.mu.Lock()
	p.listener = listener
	p.started = time.Now()
	p.mu.Unlock()
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
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
			return fmt.Errorf("accept connection: %w", err)
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
	backend, err := p.balancer.Next()
	if err != nil {
		return
	}
	index := p.backendIndex(backend)
	dialer := net.Dialer{Timeout: p.dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", backend.Address())
	if err != nil {
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
	p.total.Add(1)
	p.active.Add(1)
	defer p.active.Add(-1)
	if index >= 0 {
		p.counters[index].total.Add(1)
		p.counters[index].active.Add(1)
		defer p.counters[index].active.Add(-1)
	}

	// Deadlines enforce inactivity but disable the net package's splice fast path.
	// Milestone 1 chooses bounded idle connections over splice; io.Copy still uses
	// bare *net.TCPConn values and can regain splice if idle timeout is later zero.
	if p.idleTimeout > 0 {
		deadline := time.Now().Add(p.idleTimeout)
		_ = client.SetDeadline(deadline)
		_ = server.SetDeadline(deadline)
	}
	var copies sync.WaitGroup
	copies.Add(2)
	go func() { defer copies.Done(); _, _ = io.Copy(server, client); _ = server.CloseWrite() }()
	go func() { defer copies.Done(); _, _ = io.Copy(client, server); _ = client.CloseWrite() }()
	copies.Wait()
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
	for i, backend := range p.backends {
		if backend == want {
			return i
		}
	}
	return -1
}

func (p *Proxy) Stats() Stats {
	result := Stats{ActiveConnections: p.active.Load(), TotalConnections: p.total.Load()}
	p.mu.Lock()
	if !p.started.IsZero() {
		result.Uptime = time.Since(p.started)
	}
	p.mu.Unlock()
	result.Backends = make([]BackendStats, len(p.backends))
	for i, backend := range p.backends {
		result.Backends[i] = BackendStats{Host: backend.Host, Port: backend.Port, ActiveConnections: p.counters[i].active.Load(), TotalConnections: p.counters[i].total.Load()}
	}
	return result
}

package balancer

import (
	"errors"
	"net"
	"sync"

	"github.com/storenth/slb-go/internal/state"
)

var ErrNoBackends = errors.New("no backends available")

type Request struct {
	Service       state.Service
	ClientAddress net.Addr
}

type Balancer interface {
	Next(Request) (state.Backend, error)
}

type RoundRobin struct {
	mu       sync.Mutex
	backends []state.Backend
	next     uint64
}

func NewRoundRobin(backends []state.Backend) *RoundRobin {
	return &RoundRobin{backends: append([]state.Backend(nil), backends...)}
}

func (r *RoundRobin) Next(_ Request) (state.Backend, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.backends) == 0 {
		return state.Backend{}, ErrNoBackends
	}
	backend := r.backends[r.next%uint64(len(r.backends))]
	r.next++
	return backend, nil
}

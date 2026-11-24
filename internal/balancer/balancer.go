package balancer

import (
	"errors"
	"sync"

	"github.com/storenth/ecmp-load-balancer/internal/state"
)

var ErrNoBackends = errors.New("no backends available")

type Balancer interface{ Next() (state.Backend, error) }

type RoundRobin struct {
	mu       sync.Mutex
	backends []state.Backend
	next     uint64
}

func NewRoundRobin(backends []state.Backend) *RoundRobin {
	return &RoundRobin{backends: append([]state.Backend(nil), backends...)}
}

func (r *RoundRobin) Next() (state.Backend, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.backends) == 0 {
		return state.Backend{}, ErrNoBackends
	}
	backend := r.backends[r.next%uint64(len(r.backends))]
	r.next++
	return backend, nil
}

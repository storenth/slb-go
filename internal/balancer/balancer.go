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
	// TriedBackends holds the indices of backends already attempted for this
	// client connection. The balancer skips them when selecting the next
	// backend. (Milestone 1f interface change.)
	TriedBackends map[int]bool
}

// Balancer selects a backend for a given request, excluding already-tried and
// ejected backends.
type Balancer interface {
	Next(Request) (int, state.Backend, error)
}

// RoundRobin distributes connections across backends in round-robin order,
// skipping tried and ejected backends.
type RoundRobin struct {
	mu       sync.Mutex
	backends []state.Backend
	next     uint64
	ejection *EjectionManager
}

// NewRoundRobin creates a RoundRobin balancer without ejection tracking.
// It is retained for backward compatibility; NewRoundRobinWithEjection is
// preferred for milestone 1f.
func NewRoundRobin(backends []state.Backend) *RoundRobin {
	return &RoundRobin{backends: append([]state.Backend(nil), backends...)}
}

// NewRoundRobinWithEjection creates a RoundRobin balancer that consults the
// given EjectionManager to skip ejected backends.
func NewRoundRobinWithEjection(backends []state.Backend, em *EjectionManager) *RoundRobin {
	return &RoundRobin{backends: append([]state.Backend(nil), backends...), ejection: em}
}

func (r *RoundRobin) Next(req Request) (int, state.Backend, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.backends) == 0 {
		return -1, state.Backend{}, ErrNoBackends
	}
	tried := req.TriedBackends
	if tried == nil {
		tried = map[int]bool{}
	}
	// If we have an ejection manager, use it to find eligible backends.
	if r.ejection != nil {
		eligible := r.ejection.EligibleBackends(tried)
		if len(eligible) == 0 {
			return -1, state.Backend{}, ErrNoBackends
		}
		// Pick the first eligible backend that is not tried, using round-robin
		// rotation to spread load.
		start := int(r.next % uint64(len(eligible)))
		r.next++
		idx := eligible[start]
		return idx, r.backends[idx], nil
	}
	// No ejection manager: simple round-robin skipping tried backends.
	for range len(r.backends) {
		idx := int(r.next % uint64(len(r.backends)))
		r.next++
		if !tried[idx] {
			return idx, r.backends[idx], nil
		}
	}
	return -1, state.Backend{}, ErrNoBackends
}

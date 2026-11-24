package balancer

import (
	"errors"
	"sync"
	"testing"

	"github.com/storenth/ecmp-load-balancer/internal/state"
)

func TestRoundRobin(t *testing.T) {
	backends := []state.Backend{{Host: "one", Port: 1}, {Host: "two", Port: 2}}
	roundRobin := NewRoundRobin(backends)
	for i, want := range []state.Backend{backends[0], backends[1], backends[0], backends[1]} {
		got, err := roundRobin.Next()
		if err != nil {
			t.Fatalf("Next %d: %v", i, err)
		}
		if got != want {
			t.Fatalf("Next %d = %+v, want %+v", i, got, want)
		}
	}
}

func TestRoundRobinNoBackends(t *testing.T) {
	_, err := NewRoundRobin(nil).Next()
	if !errors.Is(err, ErrNoBackends) {
		t.Fatalf("error = %v, want ErrNoBackends", err)
	}
}

func TestRoundRobinConcurrent(t *testing.T) {
	roundRobin := NewRoundRobin([]state.Backend{{Host: "one", Port: 1}, {Host: "two", Port: 2}})
	counts := make(map[string]int)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			backend, err := roundRobin.Next()
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			counts[backend.Host]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if counts["one"] != 50 || counts["two"] != 50 {
		t.Fatalf("counts = %v", counts)
	}
}

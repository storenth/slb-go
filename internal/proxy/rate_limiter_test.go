package proxy

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDialFailureRateLimiter(t *testing.T) {
	// Use a fake clock so the test does not sleep.
	var fakeNow atomic.Int64 // unix nanos
	rl := &dialFailureRateLimiter{
		window:  10 * time.Second,
		now:     func() time.Time { return time.Unix(0, fakeNow.Load()) },
		entries: make(map[string]*rateLimitEntry),
	}

	// First call for backend A should emit.
	suppressed, emit := rl.shouldLog("127.0.0.1:9443")
	if !emit || suppressed != 0 {
		t.Fatalf("first call: emit=%v suppressed=%d", emit, suppressed)
	}

	// Immediate second call for the same backend should be suppressed.
	suppressed, emit = rl.shouldLog("127.0.0.1:9443")
	if emit || suppressed != 0 {
		t.Fatalf("second call: emit=%v suppressed=%d", emit, suppressed)
	}

	// Third call: still suppressed, count goes to 2.
	suppressed, emit = rl.shouldLog("127.0.0.1:9443")
	if emit || suppressed != 0 {
		t.Fatalf("third call: emit=%v suppressed=%d", emit, suppressed)
	}

	// Advance past the window: should emit with suppressed=2.
	fakeNow.Add(int64(11 * time.Second))
	suppressed, emit = rl.shouldLog("127.0.0.1:9443")
	if !emit || suppressed != 2 {
		t.Fatalf("after window: emit=%v suppressed=%d", emit, suppressed)
	}

	// A different backend should emit independently.
	suppressed, emit = rl.shouldLog("127.0.0.1:9444")
	if !emit || suppressed != 0 {
		t.Fatalf("different backend: emit=%v suppressed=%d", emit, suppressed)
	}
}

func TestDialFailureRateLimiterConcurrent(t *testing.T) {
	var fakeNow atomic.Int64
	rl := &dialFailureRateLimiter{
		window:  10 * time.Second,
		now:     func() time.Time { return time.Unix(0, fakeNow.Load()) },
		entries: make(map[string]*rateLimitEntry),
	}

	var wg sync.WaitGroup
	var emitCount atomic.Int64
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, emit := rl.shouldLog("127.0.0.1:9999")
			if emit {
				emitCount.Add(1)
			}
		}()
	}
	wg.Wait()

	// Exactly one goroutine should get emit=true (the first one).
	if got := emitCount.Load(); got != 1 {
		t.Fatalf("emitCount = %d, want 1", got)
	}
}

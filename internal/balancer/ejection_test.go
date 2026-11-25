package balancer

import (
	"bytes"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/storenth/slb-go/internal/state"
)

func testEjectionCfg() state.ResolvedEjectionConfig {
	return state.ResolvedEjectionConfig{
		ConsecutiveFailures: 3,
		BaseTime:            30 * time.Second,
		MaxTime:             5 * time.Minute,
	}
}

func TestEjectionManagerRecordFailure(t *testing.T) {
	backends := []state.Backend{{Host: "a", Port: 1}, {Host: "b", Port: 2}}
	cfg := testEjectionCfg()
	var mu sync.Mutex
	now := time.Now()
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
	}
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	em := NewEjectionManager(backends, cfg, clock, logger)

	// 2 failures should not eject.
	em.RecordFailure(0, errors.New("refused"))
	em.RecordFailure(0, errors.New("refused"))
	states := em.States()
	if !states[0].Up {
		t.Fatalf("backend 0 should still be up after 2 failures")
	}

	// 3rd failure should eject (streak=1, 30s).
	em.RecordFailure(0, errors.New("refused"))
	states = em.States()
	if states[0].Up {
		t.Fatalf("backend 0 should be ejected after 3 failures")
	}
	until1 := states[0].EjectedUntil

	// Ejection duration should be ~30s.
	dur1 := until1.Sub(clock())
	if dur1 < 29*time.Second || dur1 > 31*time.Second {
		t.Fatalf("first ejection duration = %v, want ~30s", dur1)
	}

	// Advance past ejection, then fail 3 more times for second ejection (streak=2, 60s).
	advance(31 * time.Second)
	em.RecordFailure(0, errors.New("refused"))
	em.RecordFailure(0, errors.New("refused"))
	em.RecordFailure(0, errors.New("refused"))
	states = em.States()
	if states[0].Up {
		t.Fatalf("backend 0 should be ejected after second round")
	}
	until2 := states[0].EjectedUntil
	dur2 := until2.Sub(clock())
	if dur2 < 59*time.Second || dur2 > 61*time.Second {
		t.Fatalf("second ejection duration = %v, want ~60s", dur2)
	}
}

func TestEjectionManagerCap(t *testing.T) {
	backends := []state.Backend{{Host: "a", Port: 1}, {Host: "b", Port: 2}}
	cfg := state.ResolvedEjectionConfig{
		ConsecutiveFailures: 1,
		BaseTime:            30 * time.Second,
		MaxTime:             60 * time.Second,
	}
	var mu sync.Mutex
	now := time.Now()
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
	}
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	em := NewEjectionManager(backends, cfg, clock, logger)

	// Trigger many ejections to hit the cap.
	for i := range 10 {
		em.RecordFailure(0, errors.New("refused"))
		if i < 9 { // don't advance past the last ejection
			advance(61 * time.Second) // past ejection
		}
	}

	states := em.States()
	if states[0].Up {
		t.Fatalf("backend 0 should be ejected")
	}
	dur := states[0].EjectedUntil.Sub(clock())
	if dur > 60*time.Second+time.Second {
		t.Fatalf("ejection duration = %v, exceeds max_time 60s", dur)
	}
}

func TestEjectionManagerNeverEjectLast(t *testing.T) {
	backends := []state.Backend{{Host: "a", Port: 1}, {Host: "b", Port: 2}}
	cfg := state.ResolvedEjectionConfig{
		ConsecutiveFailures: 1,
		BaseTime:            30 * time.Second,
		MaxTime:             5 * time.Minute,
	}
	now := time.Now()
	clock := func() time.Time { return now }
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	em := NewEjectionManager(backends, cfg, clock, logger)

	// Eject backend 0.
	em.RecordFailure(0, errors.New("refused"))
	states := em.States()
	if states[0].Up {
		t.Fatalf("backend 0 should be ejected")
	}

	// Now try to eject backend 1 (the last non-ejected). It should NOT be ejected.
	em.RecordFailure(1, errors.New("refused"))
	em.RecordFailure(1, errors.New("refused"))
	em.RecordFailure(1, errors.New("refused"))
	states = em.States()
	if !states[1].Up {
		t.Fatalf("backend 1 should NOT be ejected (last non-ejected)")
	}
}

func TestEjectionManagerRecordSuccessResets(t *testing.T) {
	backends := []state.Backend{{Host: "a", Port: 1}, {Host: "b", Port: 2}}
	cfg := testEjectionCfg()
	now := time.Now()
	clock := func() time.Time { return now }
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	em := NewEjectionManager(backends, cfg, clock, logger)

	// 2 failures.
	em.RecordFailure(0, errors.New("refused"))
	em.RecordFailure(0, errors.New("refused"))
	// Success resets.
	em.RecordSuccess(0)
	states := em.States()
	if states[0].ConsecFailures != 0 {
		t.Fatalf("consec failures should be 0 after success, got %d", states[0].ConsecFailures)
	}
	if states[0].EjectionStreak != 0 {
		t.Fatalf("ejection streak should be 0 after success, got %d", states[0].EjectionStreak)
	}

	// Now 3 more failures should eject with streak=1 (not streak=2).
	em.RecordFailure(0, errors.New("refused"))
	em.RecordFailure(0, errors.New("refused"))
	em.RecordFailure(0, errors.New("refused"))
	states = em.States()
	if states[0].Up {
		t.Fatalf("backend 0 should be ejected")
	}
	dur := states[0].EjectedUntil.Sub(clock())
	if dur < 29*time.Second || dur > 31*time.Second {
		t.Fatalf("ejection duration = %v, want ~30s (streak reset by success)", dur)
	}
}

func TestEjectionManagerRecoveryLogging(t *testing.T) {
	backends := []state.Backend{{Host: "a", Port: 1}, {Host: "b", Port: 2}}
	cfg := testEjectionCfg()
	now := time.Now()
	clock := func() time.Time { return now }
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	em := NewEjectionManager(backends, cfg, clock, logger)

	// Record a failure, then success -> should log "backend recovered".
	em.RecordFailure(0, errors.New("refused"))
	buf.Reset()
	em.RecordSuccess(0)
	output := buf.String()
	if !contains(output, "backend recovered") {
		t.Fatalf("expected 'backend recovered' log, got: %s", output)
	}

	// Success without prior failure -> no log.
	buf.Reset()
	em.RecordSuccess(0)
	output = buf.String()
	if output != "" {
		t.Fatalf("expected no log for success without prior failure, got: %s", output)
	}
}

func TestEjectionManagerEjectionLogging(t *testing.T) {
	backends := []state.Backend{{Host: "a", Port: 1}, {Host: "b", Port: 2}}
	cfg := state.ResolvedEjectionConfig{
		ConsecutiveFailures: 1,
		BaseTime:            30 * time.Second,
		MaxTime:             5 * time.Minute,
	}
	now := time.Now()
	clock := func() time.Time { return now }
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	em := NewEjectionManager(backends, cfg, clock, logger)

	em.RecordFailure(0, errors.New("refused"))
	output := buf.String()
	if !contains(output, "backend ejected") {
		t.Fatalf("expected 'backend ejected' log, got: %s", output)
	}
}

func TestEjectionManagerFailOpen(t *testing.T) {
	backends := []state.Backend{{Host: "a", Port: 1}, {Host: "b", Port: 2}}
	cfg := state.ResolvedEjectionConfig{
		ConsecutiveFailures: 1,
		BaseTime:            30 * time.Second,
		MaxTime:             5 * time.Minute,
	}
	now := time.Now()
	clock := func() time.Time { return now }
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	em := NewEjectionManager(backends, cfg, clock, logger)

	// Eject backend 0 (backend 1 stays up as last non-ejected).
	em.RecordFailure(0, errors.New("refused"))

	// EligibleBackends with empty tried should return only backend 1.
	eligible := em.EligibleBackends(map[int]bool{})
	if len(eligible) != 1 || eligible[0] != 1 {
		t.Fatalf("expected only backend 1 eligible, got %v", eligible)
	}

	// If backend 1 is also tried, fail-open should return backend 0 (ejected,
	// soonest to expire).
	eligible = em.EligibleBackends(map[int]bool{1: true})
	if len(eligible) != 1 || eligible[0] != 0 {
		t.Fatalf("expected fail-open to return backend 0, got %v", eligible)
	}
}

func TestRoundRobinWithEjectionSkipsEjected(t *testing.T) {
	backends := []state.Backend{{Host: "a", Port: 1}, {Host: "b", Port: 2}, {Host: "c", Port: 3}}
	cfg := state.ResolvedEjectionConfig{
		ConsecutiveFailures: 1,
		BaseTime:            30 * time.Second,
		MaxTime:             5 * time.Minute,
	}
	now := time.Now()
	clock := func() time.Time { return now }
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	em := NewEjectionManager(backends, cfg, clock, logger)

	// Eject backend 0.
	em.RecordFailure(0, errors.New("refused"))

	rr := NewRoundRobinWithEjection(backends, em)
	// Next should never return backend 0.
	for range 10 {
		idx, _, err := rr.Next(Request{})
		if err != nil {
			t.Fatal(err)
		}
		if idx == 0 {
			t.Fatalf("round-robin returned ejected backend 0")
		}
	}
}

func TestRoundRobinWithEjectionTriedBackends(t *testing.T) {
	backends := []state.Backend{{Host: "a", Port: 1}, {Host: "b", Port: 2}, {Host: "c", Port: 3}}
	now := time.Now()
	clock := func() time.Time { return now }
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	em := NewEjectionManager(backends, testEjectionCfg(), clock, logger)
	rr := NewRoundRobinWithEjection(backends, em)

	// Mark backend 0 as tried.
	tried := map[int]bool{0: true}
	idx, _, err := rr.Next(Request{TriedBackends: tried})
	if err != nil {
		t.Fatal(err)
	}
	if idx == 0 {
		t.Fatalf("round-robin returned tried backend 0")
	}
}

func contains(s, substr string) bool {
	return bytes.Contains([]byte(s), []byte(substr))
}

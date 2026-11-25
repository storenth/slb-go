package balancer

import (
	"log/slog"
	"sync"
	"time"

	"github.com/storenth/slb-go/internal/state"
)

// BackendState describes the runtime state of a backend for status reporting.
type BackendState struct {
	Up             bool
	EjectedUntil   time.Time // zero when Up
	LastErr        string
	LastFailureAt  time.Time
	ConsecFailures int
	EjectionStreak int
}

// EjectionManager tracks passive ejection state per backend. It is safe for
// concurrent use. The clock is injectable for testing.
type EjectionManager struct {
	mu       sync.Mutex
	backends []state.Backend
	cfg      state.ResolvedEjectionConfig
	now      func() time.Time
	logger   *slog.Logger
	states   []backendRuntime
}

type backendRuntime struct {
	consecFailures int
	ejectionStreak int
	ejectedUntil   time.Time
	lastErr        string
	lastFailureAt  time.Time
	hadFailure     bool // tracks whether we've seen failures since last success
}

// NewEjectionManager creates a manager for the given backends and config.
func NewEjectionManager(backends []state.Backend, cfg state.ResolvedEjectionConfig, now func() time.Time, logger *slog.Logger) *EjectionManager {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &EjectionManager{
		backends: append([]state.Backend(nil), backends...),
		cfg:      cfg,
		now:      now,
		logger:   logger,
		states:   make([]backendRuntime, len(backends)),
	}
}

// IsEjected reports whether backend i is currently ejected.
func (m *EjectionManager) IsEjected(i int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.isEjectedLocked(i)
}

func (m *EjectionManager) isEjectedLocked(i int) bool {
	if i < 0 || i >= len(m.states) {
		return false
	}
	until := m.states[i].ejectedUntil
	if until.IsZero() {
		return false
	}
	return m.now().Before(until)
}

// EligibleBackends returns the indices of backends that are not currently
// ejected and not in the tried set. If all backends are ejected (or all are
// tried), it returns the soonest-to-expire ejected backends that are not tried
// (fail-open). If every backend is tried, returns nil.
func (m *EjectionManager) EligibleBackends(tried map[int]bool) []int {
	m.mu.Lock()
	defer m.mu.Unlock()
	var up []int
	var ejected []ejectedCandidate
	for i := range m.backends {
		if tried[i] {
			continue
		}
		if m.isEjectedLocked(i) {
			ejected = append(ejected, ejectedCandidate{index: i, until: m.states[i].ejectedUntil})
		} else {
			up = append(up, i)
		}
	}
	if len(up) > 0 {
		return up
	}
	if len(ejected) == 0 {
		return nil
	}
	// Fail-open: sort ejected by soonest expiry.
	sortEjectedByExpiry(ejected)
	result := make([]int, len(ejected))
	for j, c := range ejected {
		result[j] = c.index
	}
	return result
}

type ejectedCandidate struct {
	index int
	until time.Time
}

func sortEjectedByExpiry(candidates []ejectedCandidate) {
	for i := 1; i < len(candidates); i++ {
		for j := i; j > 0 && candidates[j].until.Before(candidates[j-1].until); j-- {
			candidates[j], candidates[j-1] = candidates[j-1], candidates[j]
		}
	}
}

// nonEjectedCount returns the number of backends that are not currently
// ejected (and not tried).
func (m *EjectionManager) nonEjectedCount(tried map[int]bool) int {
	count := 0
	for i := range m.backends {
		if tried[i] {
			continue
		}
		if !m.isEjectedLocked(i) {
			count++
		}
	}
	return count
}

// RecordFailure records a dial failure for backend i. If the consecutive
// failure count reaches the threshold, the backend is ejected for base_time *
// (ejection streak + 1), capped at max_time. The last non-ejected backend is
// never ejected (fail-open).
func (m *EjectionManager) RecordFailure(i int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i < 0 || i >= len(m.states) {
		return
	}
	m.states[i].consecFailures++
	m.states[i].hadFailure = true
	if err != nil {
		m.states[i].lastErr = err.Error()
	}
	m.states[i].lastFailureAt = m.now()

	threshold := m.cfg.ConsecutiveFailures
	if m.states[i].consecFailures < threshold {
		return
	}
	// Never eject the last non-ejected backend.
	tried := map[int]bool{}
	if m.nonEjectedCount(tried) <= 1 {
		return
	}
	m.states[i].ejectionStreak++
	streak := m.states[i].ejectionStreak
	dur := m.cfg.BaseTime * time.Duration(streak)
	if dur > m.cfg.MaxTime {
		dur = m.cfg.MaxTime
	}
	until := m.now().Add(dur)
	m.states[i].ejectedUntil = until
	// Reset consecutive failures so the next ejection requires another full
	// threshold of failures after re-admission.
	m.states[i].consecFailures = 0

	backend := m.backends[i]
	m.logger.Info("backend ejected",
		"backend", backend.Address(),
		"until", until.Format(time.RFC3339),
		"duration", dur.String(),
		"streak", streak,
	)
}

// RecordSuccess records a successful dial for backend i. It resets the
// consecutive failure count and ejection streak. If the backend had one or
// more failures since the last success, it logs "backend recovered".
func (m *EjectionManager) RecordSuccess(i int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i < 0 || i >= len(m.states) {
		return
	}
	hadFailure := m.states[i].hadFailure
	m.states[i].consecFailures = 0
	m.states[i].ejectionStreak = 0
	m.states[i].ejectedUntil = time.Time{}
	m.states[i].hadFailure = false
	if hadFailure {
		backend := m.backends[i]
		m.logger.Info("backend recovered", "backend", backend.Address())
	}
}

// States returns a snapshot of all backend states for status reporting.
func (m *EjectionManager) States() []BackendState {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]BackendState, len(m.states))
	for i := range m.states {
		ejected := m.isEjectedLocked(i)
		result[i] = BackendState{
			Up:             !ejected,
			EjectedUntil:   m.states[i].ejectedUntil,
			LastErr:        m.states[i].lastErr,
			LastFailureAt:  m.states[i].lastFailureAt,
			ConsecFailures: m.states[i].consecFailures,
			EjectionStreak: m.states[i].ejectionStreak,
		}
	}
	return result
}

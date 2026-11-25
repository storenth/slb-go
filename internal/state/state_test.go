package state

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileStateProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"services":[{"name":"web","listeners":[{"address":"127.0.0.1","protocol":"tcp","port":8443}],"backends":[{"host":"127.0.0.1","port":9443}],"affinity":"none","dial":{"timeout":"2s"}}],"timeouts":{"idle":"3m"}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := (FileStateProvider{Path: path}).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dialCfg := config.Services[0].Dial.Resolve()
	if len(config.Services) != 1 || config.Services[0].Name != "web" || dialCfg.Timeout != 2*time.Second || config.IdleTimeout() != 3*time.Minute {
		t.Fatalf("unexpected config: %+v", config)
	}
}

func TestConfigValidation(t *testing.T) {
	validService := func() Service {
		return Service{
			Name:      "web",
			Listeners: []Listener{{Address: "127.0.0.1", Protocol: "tcp", Port: 8443}},
			Backends:  []Backend{{Host: "127.0.0.1", Port: 9443}},
			Affinity:  "none",
		}
	}
	tests := []struct {
		name     string
		config   Config
		contains string
	}{
		{"no services", Config{}, "at least one service"},
		{"empty listeners", Config{Services: []Service{{Name: "web", Backends: []Backend{{Host: "h", Port: 1}}, Affinity: "none"}}}, "listeners must not be empty"},
		{"empty backends", Config{Services: []Service{{Name: "web", Listeners: []Listener{{Address: "127.0.0.1", Protocol: "tcp", Port: 1}}, Affinity: "none"}}}, "backends must not be empty"},
		{"bad affinity", Config{Services: []Service{{Name: "web", Listeners: []Listener{{Address: "127.0.0.1", Protocol: "tcp", Port: 1}}, Backends: []Backend{{Host: "h", Port: 1}}, Affinity: "sticky"}}}, "affinity must be one of"},
		{"bad protocol", Config{Services: []Service{{Name: "web", Listeners: []Listener{{Address: "127.0.0.1", Protocol: "sctp", Port: 1}}, Backends: []Backend{{Host: "h", Port: 1}}, Affinity: "none"}}}, "protocol must be tcp or udp"},
		{"backend port", Config{Services: []Service{{Name: "web", Listeners: []Listener{{Address: "127.0.0.1", Protocol: "tcp", Port: 1}}, Backends: []Backend{{Host: "h", Port: 0}}, Affinity: "none"}}}, "backends[0].port"},
		{"duration", Config{Services: []Service{validService()}, Timeouts: Timeouts{Idle: "soon"}}, "timeouts.idle"},
		{"duplicate backend", Config{Services: []Service{{Name: "web", Listeners: []Listener{{Address: "127.0.0.1", Protocol: "tcp", Port: 1}}, Backends: []Backend{{Host: "h", Port: 1}, {Host: "h", Port: 1}}, Affinity: "none"}}}, "duplicates"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.config.Validate()
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error = %v, want containing %q", err, test.contains)
			}
		})
	}
}

func TestConfigValidationMultiListenerOK(t *testing.T) {
	config := Config{
		Services: []Service{
			{
				Name: "ftp",
				Listeners: []Listener{
					{Address: "127.0.0.1", Protocol: "tcp", Port: 21},
					{Address: "127.0.0.1", Protocol: "tcp", PortRange: &PortRange{Start: 30000, End: 30100}},
				},
				Backends: []Backend{{Host: "10.0.0.1", Port: 21}},
				Affinity: "client_ip",
			},
		},
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("expected valid, got: %v", err)
	}
}

func TestConfigValidationOverlappingPortsRejected(t *testing.T) {
	config := Config{
		Services: []Service{
			{Name: "a", Listeners: []Listener{{Address: "127.0.0.1", Protocol: "tcp", Port: 8080}}, Backends: []Backend{{Host: "h", Port: 1}}, Affinity: "none"},
			{Name: "b", Listeners: []Listener{{Address: "127.0.0.1", Protocol: "tcp", Port: 8080}}, Backends: []Backend{{Host: "h", Port: 1}}, Affinity: "none"},
		},
	}
	err := config.Validate()
	if err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("error = %v, want containing %q", err, "overlaps")
	}
}

func TestConfigValidationOversizedRangeRejected(t *testing.T) {
	config := Config{
		Services: []Service{
			{Name: "wide", Listeners: []Listener{{Address: "127.0.0.1", Protocol: "tcp", PortRange: &PortRange{Start: 10000, End: 11000}}}, Backends: []Backend{{Host: "h", Port: 1}}, Affinity: "none"},
		},
	}
	err := config.Validate()
	if err == nil || !strings.Contains(err.Error(), "exceeds max_port_range_size") {
		t.Fatalf("error = %v, want containing %q", err, "exceeds max_port_range_size")
	}
}

func TestConfigValidateRuntime(t *testing.T) {
	tests := []struct {
		name     string
		service  Service
		contains string
	}{
		{"udp not implemented", Service{Name: "u", Listeners: []Listener{{Address: "127.0.0.1", Protocol: "udp", Port: 53}}, Backends: []Backend{{Host: "h", Port: 1}}, Affinity: "none"}, "udp"},
		{"port range not implemented", Service{Name: "r", Listeners: []Listener{{Address: "127.0.0.1", Protocol: "tcp", PortRange: &PortRange{Start: 100, End: 200}}}, Backends: []Backend{{Host: "h", Port: 1}}, Affinity: "none"}, "port range"},
		{"multiple listeners not implemented", Service{Name: "m", Listeners: []Listener{{Address: "127.0.0.1", Protocol: "tcp", Port: 1}, {Address: "127.0.0.1", Protocol: "tcp", Port: 2}}, Backends: []Backend{{Host: "h", Port: 1}}, Affinity: "none"}, "multiple listeners"},
		{"affinity not implemented", Service{Name: "a", Listeners: []Listener{{Address: "127.0.0.1", Protocol: "tcp", Port: 1}}, Backends: []Backend{{Host: "h", Port: 1}}, Affinity: "client_ip"}, "affinity"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := Config{Services: []Service{test.service}}
			err := config.ValidateRuntime()
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error = %v, want containing %q", err, test.contains)
			}
		})
	}
}

func TestConfigValidateRuntimeOK(t *testing.T) {
	config := Config{
		Services: []Service{
			{Name: "web", Listeners: []Listener{{Address: "127.0.0.1", Protocol: "tcp", Port: 8443}}, Backends: []Backend{{Host: "127.0.0.1", Port: 9443}}, Affinity: "none"},
		},
	}
	if err := config.ValidateRuntime(); err != nil {
		t.Fatalf("expected runtime valid, got: %v", err)
	}
}

func TestConfigValidationDialEjection(t *testing.T) {
	validService := func() Service {
		return Service{
			Name:      "web",
			Listeners: []Listener{{Address: "127.0.0.1", Protocol: "tcp", Port: 8443}},
			Backends:  []Backend{{Host: "127.0.0.1", Port: 9443}},
			Affinity:  "none",
		}
	}
	tests := []struct {
		name     string
		config   Config
		contains string
	}{
		{
			name:     "bad dial timeout",
			config:   Config{Services: []Service{func() Service { s := validService(); s.Dial.Timeout = "soon"; return s }()}},
			contains: "dial.timeout must be a duration",
		},
		{
			name:     "negative dial timeout",
			config:   Config{Services: []Service{func() Service { s := validService(); s.Dial.Timeout = "-1s"; return s }()}},
			contains: "dial.timeout must be positive",
		},
		{
			name:     "bad dial budget",
			config:   Config{Services: []Service{func() Service { s := validService(); s.Dial.Budget = "soon"; return s }()}},
			contains: "dial.budget must be a duration",
		},
		{
			name:     "negative dial budget",
			config:   Config{Services: []Service{func() Service { s := validService(); s.Dial.Budget = "-1s"; return s }()}},
			contains: "dial.budget must be positive",
		},
		{
			name:     "negative max attempts",
			config:   Config{Services: []Service{func() Service { s := validService(); s.Dial.MaxAttempts = -1; return s }()}},
			contains: "dial.max_attempts must be >= 0",
		},
		{
			name:     "bad ejection base_time",
			config:   Config{Services: []Service{func() Service { s := validService(); s.Ejection.BaseTime = "soon"; return s }()}},
			contains: "ejection.base_time must be a duration",
		},
		{
			name:     "negative ejection base_time",
			config:   Config{Services: []Service{func() Service { s := validService(); s.Ejection.BaseTime = "-1s"; return s }()}},
			contains: "ejection.base_time must be positive",
		},
		{
			name:     "bad ejection max_time",
			config:   Config{Services: []Service{func() Service { s := validService(); s.Ejection.MaxTime = "soon"; return s }()}},
			contains: "ejection.max_time must be a duration",
		},
		{
			name:     "negative ejection max_time",
			config:   Config{Services: []Service{func() Service { s := validService(); s.Ejection.MaxTime = "-1s"; return s }()}},
			contains: "ejection.max_time must be positive",
		},
		{
			name:     "negative ejection consecutive_failures",
			config:   Config{Services: []Service{func() Service { s := validService(); s.Ejection.ConsecutiveFailures = -1; return s }()}},
			contains: "ejection.consecutive_failures must be >= 0",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.config.Validate()
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error = %v, want containing %q", err, test.contains)
			}
		})
	}
}

func TestDialConfigResolveDefaults(t *testing.T) {
	cfg := DialConfig{}
	resolved := cfg.Resolve()
	if resolved.Timeout != DefaultDialTimeout {
		t.Fatalf("default timeout = %v, want %v", resolved.Timeout, DefaultDialTimeout)
	}
	if resolved.MaxAttempts != DefaultDialMaxAttempts {
		t.Fatalf("default max_attempts = %d, want %d", resolved.MaxAttempts, DefaultDialMaxAttempts)
	}
	if resolved.Budget != DefaultDialBudget {
		t.Fatalf("default budget = %v, want %v", resolved.Budget, DefaultDialBudget)
	}
}

func TestEjectionConfigResolveDefaults(t *testing.T) {
	cfg := EjectionConfig{}
	resolved := cfg.Resolve()
	if resolved.ConsecutiveFailures != DefaultEjectionConsecFail {
		t.Fatalf("default consecutive_failures = %d, want %d", resolved.ConsecutiveFailures, DefaultEjectionConsecFail)
	}
	if resolved.BaseTime != DefaultEjectionBaseTime {
		t.Fatalf("default base_time = %v, want %v", resolved.BaseTime, DefaultEjectionBaseTime)
	}
	if resolved.MaxTime != DefaultEjectionMaxTime {
		t.Fatalf("default max_time = %v, want %v", resolved.MaxTime, DefaultEjectionMaxTime)
	}
}

func TestDialConfigResolveCustom(t *testing.T) {
	cfg := DialConfig{Timeout: "2s", MaxAttempts: 5, Budget: "10s"}
	resolved := cfg.Resolve()
	if resolved.Timeout != 2*time.Second {
		t.Fatalf("timeout = %v, want 2s", resolved.Timeout)
	}
	if resolved.MaxAttempts != 5 {
		t.Fatalf("max_attempts = %d, want 5", resolved.MaxAttempts)
	}
	if resolved.Budget != 10*time.Second {
		t.Fatalf("budget = %v, want 10s", resolved.Budget)
	}
}

func TestFileStateProviderRejectsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"services":[{"name":"x","listeners":[{"address":"127.0.0.1","protocol":"tcp","port":1}],"backends":[{"host":"x","port":1}],"affinity":"none"}],"surprise":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := (FileStateProvider{Path: path}).Load(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v", err)
	}
}

func TestTimeoutsDialMovedKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"services":[{"name":"x","listeners":[{"address":"127.0.0.1","protocol":"tcp","port":1}],"backends":[{"host":"x","port":1}],"affinity":"none"}],"timeouts":{"dial":"5s","idle":"5m"}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := (FileStateProvider{Path: path}).Load(context.Background())
	if err == nil {
		t.Fatal("expected error for removed timeouts.dial")
	}
	if !strings.Contains(err.Error(), "timeouts.dial was removed") {
		t.Fatalf("error = %v, want containing %q", err, "timeouts.dial was removed")
	}
	if !strings.Contains(err.Error(), "services[].dial.timeout") {
		t.Fatalf("error = %v, want containing %q", err, "services[].dial.timeout")
	}
}

func TestTimeoutsUnknownKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"services":[{"name":"x","listeners":[{"address":"127.0.0.1","protocol":"tcp","port":1}],"backends":[{"host":"x","port":1}],"affinity":"none"}],"timeouts":{"idle":"5m","bogus":"1s"}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := (FileStateProvider{Path: path}).Load(context.Background())
	if err == nil {
		t.Fatal("expected error for unknown timeouts.bogus")
	}
	if !strings.Contains(err.Error(), "timeouts.bogus") {
		t.Fatalf("error = %v, want containing %q", err, "timeouts.bogus")
	}
}

func TestIdlePrecedence(t *testing.T) {
	tests := []struct {
		name     string
		service  Service
		global   string
		expected time.Duration
	}{
		{
			name:     "service beats global beats built-in",
			service:  Service{Idle: "1h"},
			global:   "5m",
			expected: time.Hour,
		},
		{
			name:     "global beats built-in",
			service:  Service{},
			global:   "10m",
			expected: 10 * time.Minute,
		},
		{
			name:     "built-in default",
			service:  Service{},
			global:   "",
			expected: DefaultIdleTimeout,
		},
		{
			name:     "empty service idle falls through to global",
			service:  Service{Idle: ""},
			global:   "30m",
			expected: 30 * time.Minute,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			globalDefault := durationOr(test.global, DefaultIdleTimeout)
			got := test.service.ResolveIdle(globalDefault)
			if got != test.expected {
				t.Fatalf("ResolveIdle = %v, want %v", got, test.expected)
			}
		})
	}
}

func TestServiceIdleValidation(t *testing.T) {
	validService := func() Service {
		return Service{
			Name:      "web",
			Listeners: []Listener{{Address: "127.0.0.1", Protocol: "tcp", Port: 8443}},
			Backends:  []Backend{{Host: "127.0.0.1", Port: 9443}},
			Affinity:  "none",
		}
	}
	tests := []struct {
		name     string
		service  Service
		contains string
	}{
		{"bad idle duration", func() Service { s := validService(); s.Idle = "soon"; return s }(), "idle must be a duration"},
		{"negative idle", func() Service { s := validService(); s.Idle = "-1s"; return s }(), "idle must be positive"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := Config{Services: []Service{test.service}}.Validate()
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error = %v, want containing %q", err, test.contains)
			}
		})
	}
}

func TestFileStateProviderIdleOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"services":[{"name":"web","listeners":[{"address":"127.0.0.1","protocol":"tcp","port":8443}],"backends":[{"host":"127.0.0.1","port":9443}],"affinity":"none","idle":"1h"}],"timeouts":{"idle":"5m"}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := (FileStateProvider{Path: path}).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	globalIdle := config.IdleTimeout()
	effective := config.Services[0].ResolveIdle(globalIdle)
	if effective != time.Hour {
		t.Fatalf("effective idle = %v, want 1h (per-service override should win over global 5m)", effective)
	}
	if globalIdle != 5*time.Minute {
		t.Fatalf("global idle = %v, want 5m", globalIdle)
	}
}

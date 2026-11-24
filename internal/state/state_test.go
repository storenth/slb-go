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
	data := `{"services":[{"name":"web","listeners":[{"address":"127.0.0.1","protocol":"tcp","port":8443}],"backends":[{"host":"127.0.0.1","port":9443}],"affinity":"none"}],"timeouts":{"dial":"2s","idle":"3m"}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := (FileStateProvider{Path: path}).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Services) != 1 || config.Services[0].Name != "web" || config.DialTimeout() != 2*time.Second || config.IdleTimeout() != 3*time.Minute {
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
		{"duration", Config{Services: []Service{validService()}, Timeouts: Timeouts{Dial: "soon"}}, "timeouts.dial"},
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

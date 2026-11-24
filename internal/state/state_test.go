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
	data := `{"listen":{"address":"127.0.0.1","port":0},"backends":[{"host":"localhost","port":443}],"timeouts":{"dial":"2s","idle":"3m"}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := (FileStateProvider{Path: path}).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if config.Listen.Port != 0 || config.DialTimeout() != 2*time.Second || config.IdleTimeout() != 3*time.Minute {
		t.Fatalf("unexpected config: %+v", config)
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name     string
		config   Config
		contains string
	}{
		{"listen address", Config{Backends: []Backend{{Host: "host", Port: 1}}}, "listen.address is required"},
		{"backend required", Config{Listen: Listen{Address: "localhost", Port: 1}}, "at least one backend"},
		{"backend port", Config{Listen: Listen{Address: "localhost", Port: 1}, Backends: []Backend{{Host: "host", Port: 0}}}, "backends[0].port"},
		{"duration", Config{Listen: Listen{Address: "localhost", Port: 1}, Backends: []Backend{{Host: "host", Port: 1}}, Timeouts: Timeouts{Dial: "soon"}}, "timeouts.dial"},
		{"duplicate", Config{Listen: Listen{Address: "localhost", Port: 1}, Backends: []Backend{{Host: "host", Port: 1}, {Host: "host", Port: 1}}}, "duplicates"},
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

func TestFileStateProviderRejectsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"listen":{"address":"x","port":1},"backends":[{"host":"x","port":1}],"surprise":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := (FileStateProvider{Path: path}).Load(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v", err)
	}
}

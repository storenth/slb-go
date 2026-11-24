package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestResolveLevel(t *testing.T) {
	tests := []struct {
		name    string
		opts    Options
		want    slog.Level
		wantErr bool
	}{
		{"flag wins", Options{FlagLevel: "debug", EnvLevel: "warn"}, slog.LevelDebug, false},
		{"env wins when flag empty", Options{EnvLevel: "warn"}, slog.LevelWarn, false},
		{"default info", Options{}, slog.LevelInfo, false},
		{"flag case insensitive", Options{FlagLevel: "ERROR"}, slog.LevelError, false},
		{"invalid flag", Options{FlagLevel: "trace"}, 0, true},
		{"invalid env errors", Options{EnvLevel: "trace"}, 0, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveLevel(test.opts)
			if test.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != test.want {
				t.Fatalf("ResolveLevel = %v, want %v", got, test.want)
			}
		})
	}
}

func TestResolveLevelFlagBeatsEnvBeatsDefault(t *testing.T) {
	// Flag beats env
	level, err := ResolveLevel(Options{FlagLevel: "error", EnvLevel: "debug"})
	if err != nil || level != slog.LevelError {
		t.Fatalf("flag>env: got %v, %v", level, err)
	}
	// Env beats default
	level, err = ResolveLevel(Options{EnvLevel: "debug"})
	if err != nil || level != slog.LevelDebug {
		t.Fatalf("env>default: got %v, %v", level, err)
	}
	// Default
	level, err = ResolveLevel(Options{})
	if err != nil || level != slog.LevelInfo {
		t.Fatalf("default: got %v, %v", level, err)
	}
}

func TestResolveFormat(t *testing.T) {
	tests := []struct {
		name    string
		opts    Options
		want    Format
		wantErr bool
	}{
		{"flag json wins", Options{FlagFormat: "json", EnvFormat: "text", ErrOut: &bytes.Buffer{}}, FormatJSON, false},
		{"flag text wins", Options{FlagFormat: "text", EnvFormat: "json", ErrOut: &bytes.Buffer{}}, FormatText, false},
		{"env wins when flag empty", Options{EnvFormat: "json", ErrOut: &bytes.Buffer{}}, FormatJSON, false},
		{"default json when not terminal", Options{ErrOut: &bytes.Buffer{}}, FormatJSON, false},
		{"invalid flag", Options{FlagFormat: "xml", ErrOut: &bytes.Buffer{}}, "", true},
		{"invalid env errors", Options{EnvFormat: "xml", ErrOut: &bytes.Buffer{}}, "", true},
		{"flag case insensitive", Options{FlagFormat: "JSON", ErrOut: &bytes.Buffer{}}, FormatJSON, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveFormat(test.opts)
			if test.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != test.want {
				t.Fatalf("ResolveFormat = %v, want %v", got, test.want)
			}
		})
	}
}

func TestResolveFormatDefaultTextOnTerminal(t *testing.T) {
	// When ErrOut is a real terminal (os.Stderr in an interactive test run),
	// the default format is text. We can't easily mock a terminal in unit
	// tests, so we verify the logic by checking that a non-file writer
	// (bytes.Buffer) gives json, and trust isTerminal for the terminal case.
	// This test documents the expected behavior.
	var buf bytes.Buffer
	got, err := ResolveFormat(Options{ErrOut: &buf})
	if err != nil {
		t.Fatal(err)
	}
	if got != FormatJSON {
		t.Fatalf("non-terminal default = %v, want json", got)
	}
}

func TestNewLogger(t *testing.T) {
	t.Run("json format writes json", func(t *testing.T) {
		var buf bytes.Buffer
		logger, _, err := New(Options{FlagFormat: "json", FlagLevel: "info", ErrOut: &buf})
		if err != nil {
			t.Fatal(err)
		}
		logger.Info("test message", "key", "value")
		output := buf.String()
		if !strings.Contains(output, `"msg":"test message"`) {
			t.Fatalf("expected JSON output, got %q", output)
		}
	})
	t.Run("text format writes text", func(t *testing.T) {
		var buf bytes.Buffer
		logger, _, err := New(Options{FlagFormat: "text", FlagLevel: "info", ErrOut: &buf})
		if err != nil {
			t.Fatal(err)
		}
		logger.Info("test message", "key", "value")
		output := buf.String()
		if !strings.Contains(output, `msg="test message"`) {
			t.Fatalf("expected text output, got %q", output)
		}
	})
	t.Run("debug level shows debug messages", func(t *testing.T) {
		var buf bytes.Buffer
		logger, level, err := New(Options{FlagFormat: "json", FlagLevel: "debug", ErrOut: &buf})
		if err != nil {
			t.Fatal(err)
		}
		if level != slog.LevelDebug {
			t.Fatalf("level = %v, want debug", level)
		}
		logger.Debug("debug msg")
		if !strings.Contains(buf.String(), `"msg":"debug msg"`) {
			t.Fatalf("debug message not logged: %q", buf.String())
		}
	})
	t.Run("info level hides debug messages", func(t *testing.T) {
		var buf bytes.Buffer
		logger, _, err := New(Options{FlagFormat: "json", FlagLevel: "info", ErrOut: &buf})
		if err != nil {
			t.Fatal(err)
		}
		logger.Debug("debug msg")
		if buf.String() != "" {
			t.Fatalf("debug message should not be logged at info: %q", buf.String())
		}
	})
}

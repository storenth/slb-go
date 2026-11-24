// Package logging configures the daemon's structured logger.
//
// The daemon logs to stderr via log/slog. Level and format are selected with
// flag > env > default precedence:
//   - --log-level (debug|info|warn|error), default info
//   - --log-format (text|json), default text when stderr is a terminal, json otherwise
//   - SLB_LOG_LEVEL and SLB_LOG_FORMAT environment variables
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Level represents a log severity level.
type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// Format represents a log output format.
type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

// Options carries the inputs for logger configuration.
// Fields are checked in priority order: Flag > Env > Default.
type Options struct {
	// FlagLevel is the value of the --log-level CLI flag (empty if not given).
	FlagLevel string
	// FlagFormat is the value of the --log-format CLI flag (empty if not given).
	FlagFormat string
	// EnvLevel is the value of the SLB_LOG_LEVEL environment variable.
	EnvLevel string
	// EnvFormat is the value of the SLB_LOG_FORMAT environment variable.
	EnvFormat string
	// ErrOut is the writer used to detect whether stderr is a terminal.
	// If nil, os.Stderr is used.
	ErrOut io.Writer
}

// ResolveLevel determines the log level using flag > env > default.
func ResolveLevel(opts Options) (slog.Level, error) {
	raw := firstNonEmpty(opts.FlagLevel, opts.EnvLevel, string(LevelInfo))
	level, err := parseLevel(raw)
	if err != nil {
		return 0, err
	}
	return level, nil
}

// ResolveFormat determines the log format using flag > env > default.
// The default is text when stderr is a terminal, json otherwise.
func ResolveFormat(opts Options) (Format, error) {
	raw := firstNonEmpty(opts.FlagFormat, opts.EnvFormat)
	if raw != "" {
		format, err := parseFormat(raw)
		if err != nil {
			return "", err
		}
		return format, nil
	}
	if isTerminal(opts.ErrOut) {
		return FormatText, nil
	}
	return FormatJSON, nil
}

// New creates a slog.Logger configured with the resolved level and format.
func New(opts Options) (*slog.Logger, slog.Level, error) {
	level, err := ResolveLevel(opts)
	if err != nil {
		return nil, 0, err
	}
	format, err := ResolveFormat(opts)
	if err != nil {
		return nil, 0, err
	}
	out := opts.ErrOut
	if out == nil {
		out = os.Stderr
	}
	handlerOpts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if format == FormatJSON {
		handler = slog.NewJSONHandler(out, handlerOpts)
	} else {
		handler = slog.NewTextHandler(out, handlerOpts)
	}
	return slog.New(handler), level, nil
}

func parseLevel(value string) (slog.Level, error) {
	switch Level(strings.ToLower(strings.TrimSpace(value))) {
	case LevelDebug:
		return slog.LevelDebug, nil
	case LevelInfo:
		return slog.LevelInfo, nil
	case LevelWarn:
		return slog.LevelWarn, nil
	case LevelError:
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q (want debug, info, warn or error)", value)
	}
}

func parseFormat(value string) (Format, error) {
	switch Format(strings.ToLower(strings.TrimSpace(value))) {
	case FormatText:
		return FormatText, nil
	case FormatJSON:
		return FormatJSON, nil
	default:
		return "", fmt.Errorf("invalid log format %q (want text or json)", value)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// isTerminal reports whether the given writer is an os.File pointing at a
// terminal. Non-file writers (e.g. *bytes.Buffer in tests) are not terminals.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

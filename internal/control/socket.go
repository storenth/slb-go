package control

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SocketOptions carries the inputs for socket path resolution.
// Fields are checked in priority order: Flag > Env > Config > Default.
type SocketOptions struct {
	// Flag is the value of the --socket CLI flag (empty if not given).
	Flag string
	// Env is the value of the SLB_SOCKET environment variable (empty if unset).
	Env string
	// Config is the value of the control.socket config field (empty if absent).
	Config string
}

// SocketSource identifies where a resolved socket path came from.
type SocketSource string

const (
	SocketSourceFlag    SocketSource = "flag"
	SocketSourceEnv     SocketSource = "env"
	SocketSourceConfig  SocketSource = "config"
	SocketSourceDefault SocketSource = "default"
)

// DefaultSocketPath is the system-level default used under systemd (root or
// pre-existing /run/slb). It is exported so callers can use it as a flag default.
const DefaultSocketPath = "/run/slb/slb.sock"

// ResolveSocketPath determines the control socket path using the documented
// precedence: --socket flag, SLB_SOCKET env, config control.socket, built-in
// default.
func ResolveSocketPath(opts SocketOptions) string {
	path, _ := ResolveSocketPathWithSource(opts)
	return path
}

// ResolveSocketPathWithSource is like ResolveSocketPath but also returns the
// source of the resolved path (flag, env, config, or default).
func ResolveSocketPathWithSource(opts SocketOptions) (string, SocketSource) {
	if opts.Flag != "" {
		return opts.Flag, SocketSourceFlag
	}
	if opts.Env != "" {
		return opts.Env, SocketSourceEnv
	}
	if opts.Config != "" {
		return opts.Config, SocketSourceConfig
	}
	return defaultSocketPath(), SocketSourceDefault
}

// defaultSocketPath returns the built-in default:
//   - /run/slb/slb.sock when running as root or when /run/slb already exists
//     and is writable (systemd RuntimeDirectory case);
//   - otherwise $XDG_RUNTIME_DIR/slb/slb.sock;
//   - otherwise <os.TempDir()>/slb-<uid>/slb.sock.
func defaultSocketPath() string {
	if useSystemRunDir() {
		return DefaultSocketPath
	}
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return filepath.Join(xdg, "slb", "slb.sock")
	}
	return filepath.Join(os.TempDir(), "slb-"+strconv.Itoa(os.Getuid()), "slb.sock")
}

// useSystemRunDir reports whether /run/slb is the right default: the process
// is root, or /run/slb already exists and is writable (created by systemd).
func useSystemRunDir() bool {
	if os.Getuid() == 0 {
		return true
	}
	info, err := os.Stat("/run/slb")
	if err != nil || !info.IsDir() {
		return false
	}
	return writable("/run/slb")
}

// writable reports whether the given directory is writable by the current
// process. It uses a no-op probe file rather than os.Access so that the check
// reflects real write permission on macOS as well as Linux.
func writable(dir string) bool {
	probe := filepath.Join(dir, ".slb-write-probe")
	f, err := os.Create(probe)
	if err != nil {
		return false
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return true
}

// PrepareSocketDir creates the directory containing the socket path with mode
// 0700 if it does not exist. The error message names the path and suggests
// --socket, SLB_SOCKET or control.socket so users always see how to override.
func PrepareSocketDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf(
			"create control socket directory %q: %w; set --socket, SLB_SOCKET or control.socket to a writable path",
			dir, err,
		)
	}
	return nil
}

// SocketFileMode returns the file mode to apply to the socket file: 0660 under
// /run/slb (group-restricted, systemd-managed), 0600 everywhere else.
func SocketFileMode(path string) os.FileMode {
	if strings.HasPrefix(filepath.Dir(path), "/run/slb") {
		return 0o660
	}
	return 0o600
}

// CheckSocketInUse reports whether a live daemon is listening on the socket at
// path. It returns true if a connection succeeds (something answers), false if
// nothing is listening, and an error only on unexpected failures.
func CheckSocketInUse(path string) (bool, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		// On macOS/Linux a "connection refused" means the socket file exists
		// but nothing is listening — treat as stale. Other errors (permission,
		// timeout) are surfaced to the caller.
		var opErr *net.OpError
		if errors.As(err, &opErr) {
			return false, nil
		}
		return false, fmt.Errorf("probe control socket %q: %w", path, err)
	}
	_ = conn.Close()
	return true, nil
}

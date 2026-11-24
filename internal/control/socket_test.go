package control

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestResolveSocketPath(t *testing.T) {
	// Save and restore env so tests are hermetic.
	t.Setenv("SLB_SOCKET", "")
	xdgBackup := os.Getenv("XDG_RUNTIME_DIR")
	t.Cleanup(func() { _ = os.Setenv("XDG_RUNTIME_DIR", xdgBackup) })
	_ = os.Unsetenv("XDG_RUNTIME_DIR")

	defaultPath := defaultSocketPath()

	tests := []struct {
		name   string
		opts   SocketOptions
		env    string
		xdg    string
		want   string
		wantIn string // when exact match is impractical (default path)
	}{
		{
			name: "flag wins over env and config",
			opts: SocketOptions{Flag: "/tmp/flag.sock", Env: "/tmp/env.sock", Config: "/tmp/cfg.sock"},
			want: "/tmp/flag.sock",
		},
		{
			name: "env wins over config when flag empty",
			opts: SocketOptions{Env: "/tmp/env.sock", Config: "/tmp/cfg.sock"},
			want: "/tmp/env.sock",
		},
		{
			name: "config wins when flag and env empty",
			opts: SocketOptions{Config: "/tmp/cfg.sock"},
			want: "/tmp/cfg.sock",
		},
		{
			name:   "default when nothing set",
			opts:   SocketOptions{},
			wantIn: defaultPath,
		},
		{
			name:   "default uses XDG_RUNTIME_DIR when set",
			opts:   SocketOptions{},
			xdg:    "/tmp/xdg-test",
			wantIn: filepath.Join("/tmp/xdg-test", "slb", "slb.sock"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.xdg != "" {
				_ = os.Setenv("XDG_RUNTIME_DIR", test.xdg)
			} else {
				_ = os.Unsetenv("XDG_RUNTIME_DIR")
			}
			if test.env != "" {
				t.Setenv("SLB_SOCKET", test.env)
			}
			got := ResolveSocketPath(test.opts)
			if test.want != "" {
				if got != test.want {
					t.Fatalf("ResolveSocketPath = %q, want %q", got, test.want)
				}
			} else if test.wantIn != "" {
				if got != test.wantIn {
					t.Fatalf("ResolveSocketPath = %q, want %q", got, test.wantIn)
				}
			}
		})
	}
}

func TestResolveSocketPathEnvFromOSEnv(t *testing.T) {
	// The CLI reads SLB_SOCKET and passes it as SocketOptions.Env.
	t.Setenv("SLB_SOCKET", "/tmp/from-env.sock")
	got := ResolveSocketPath(SocketOptions{Env: os.Getenv("SLB_SOCKET")})
	if got != "/tmp/from-env.sock" {
		t.Fatalf("ResolveSocketPath = %q, want /tmp/from-env.sock", got)
	}
}

func TestSocketFileMode(t *testing.T) {
	tests := []struct {
		path string
		want os.FileMode
	}{
		{"/run/slb/slb.sock", 0o660},
		{"/run/slb/sub/slb.sock", 0o660},
		{"/tmp/slb-dev.sock", 0o600},
		{"/tmp/slb-501/slb.sock", 0o600},
		{filepath.Join(os.TempDir(), "slb.sock"), 0o600},
	}
	for _, test := range tests {
		if got := SocketFileMode(test.path); got != test.want {
			t.Errorf("SocketFileMode(%q) = %o, want %o", test.path, got, test.want)
		}
	}
}

func TestPrepareSocketDir(t *testing.T) {
	t.Run("creates missing dir with 0700", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "nested", "sockdir")
		path := filepath.Join(dir, "slb.sock")
		if err := PrepareSocketDir(path); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode = %o, want 0700", info.Mode().Perm())
		}
	})
	t.Run("error message names path and suggests overrides", func(t *testing.T) {
		// On macOS and Linux, creating a directory under a read-only path
		// produces a permission error. We simulate by pointing at a path
		// whose parent is a file.
		filePath := filepath.Join(t.TempDir(), "iamfile")
		if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := PrepareSocketDir(filepath.Join(filePath, "slb.sock"))
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		msg := err.Error()
		if !strings.Contains(msg, "--socket") || !strings.Contains(msg, "SLB_SOCKET") || !strings.Contains(msg, "control.socket") {
			t.Fatalf("error message does not suggest overrides: %q", msg)
		}
	})
}

// shortSocketPath returns a Unix socket path short enough for macOS (~104 byte
// limit). t.TempDir() paths can exceed this, so we use /tmp with a unique suffix.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(os.TempDir(), "slb-test-"+strconv.Itoa(os.Getpid())+"-"+t.Name())
	// Sanitize: replace path-unsafe characters.
	dir = strings.ReplaceAll(dir, "/", "-")
	dir = strings.ReplaceAll(dir, " ", "-")
	dir = filepath.Join(os.TempDir(), dir)
	if len(dir) > 80 {
		// Fall back to a hash-based short name if still too long.
		dir = filepath.Join(os.TempDir(), "slb-test-"+strconv.Itoa(os.Getpid()))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "slb.sock")
}

func TestPrepareSocketFileStaleRemoval(t *testing.T) {
	t.Run("removes stale socket when nothing answers", func(t *testing.T) {
		path := shortSocketPath(t)
		// Create a stale socket file that nobody is listening on.
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		_ = listener.Close()
		// Now the file exists but nothing answers.
		if err := prepareSocketFile(path); err != nil {
			t.Fatalf("prepareSocketFile: %v", err)
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale socket still exists: %v", err)
		}
	})
	t.Run("fails when daemon already running", func(t *testing.T) {
		path := shortSocketPath(t)
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = listener.Close(); _ = os.Remove(path) }()
		err = prepareSocketFile(path)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "already in use") {
			t.Fatalf("error = %q, want containing 'already in use'", err.Error())
		}
	})
	t.Run("noop when path does not exist", func(t *testing.T) {
		path := shortSocketPath(t)
		_ = os.Remove(path)
		if err := prepareSocketFile(path); err != nil {
			t.Fatalf("prepareSocketFile: %v", err)
		}
	})
}

func TestCheckSocketInUse(t *testing.T) {
	t.Run("true when listening", func(t *testing.T) {
		path := shortSocketPath(t)
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = listener.Close(); _ = os.Remove(path) }()
		inUse, err := CheckSocketInUse(path)
		if err != nil {
			t.Fatal(err)
		}
		if !inUse {
			t.Fatal("expected inUse=true")
		}
	})
	t.Run("false when stale", func(t *testing.T) {
		path := shortSocketPath(t)
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		_ = listener.Close()
		inUse, err := CheckSocketInUse(path)
		if err != nil {
			t.Fatal(err)
		}
		if inUse {
			t.Fatal("expected inUse=false")
		}
	})
}

func TestDefaultSocketPathNonRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("test only meaningful for non-root")
	}
	// On macOS /run/slb does not exist, so default should not be /run/slb/slb.sock.
	t.Setenv("XDG_RUNTIME_DIR", "")
	path := defaultSocketPath()
	if path == DefaultSocketPath {
		t.Fatalf("defaultSocketPath = %q, expected non-/run default on macOS/non-root", path)
	}
	// Should be under temp dir.
	if !strings.HasPrefix(path, os.TempDir()) {
		// Could be under XDG if set by the environment outside our control.
		if runtime.GOOS != "linux" {
			t.Fatalf("defaultSocketPath = %q, expected under %s", path, os.TempDir())
		}
	}
}

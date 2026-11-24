# Milestone 1c prompt: run without root (dev ergonomics)

One-shot prompt. Do not re-run once done.

```text
Read AGENTS.md and docs/02-linux-io.md (section "Control socket") first.

Task: Milestone 1c only. Do not start milestone 2.

Problem: `./bin/slb run --config config/dev.json` fails on a developer machine (macOS, non-root):
  create control socket directory: mkdir /run: read-only file system
The daemon must run without root, and on non-Linux dev machines.

Workaround (macOS): `./bin/slb run --config config/dev.json --socket /tmp/slb-dev.sock` works perfectly.

Do:
1. Control socket path resolution, highest priority first: `--socket` flag (on `run`, `status` and every CLI command that talks to the daemon), env `SLB_SOCKET`, config field `control.socket`, built-in default.
   Built-in default: `/run/slb/slb.sock` when running as root or when `/run/slb` already exists and is writable (systemd RuntimeDirectory case); otherwise `$XDG_RUNTIME_DIR/slb/slb.sock`; otherwise `<os.TempDir()>/slb-<uid>/slb.sock`.
2. Create a missing socket directory with mode 0700. Never fail with a bare OS error: the message must name the path and suggest `--socket`, `SLB_SOCKET` or `control.socket`.
3. Socket file mode 0660 under /run/slb, 0600 in user or temp directories.
4. Stale socket: remove it at startup only if nothing answers on it; if a daemon answers, fail with "already running".
5. `slb status` resolves the path the same way (and may read `control.socket` from `--config` when given).
6. config/dev.json: set `control.socket` to "/tmp/slb-dev.sock" (keep it short: Unix socket paths are limited to about 104 bytes on macOS). `control.socket` stays optional in config validation.
7. README quick start: commands that work without root, plus the note "Linux is required for deployment; macOS works for development (no splice, no keepalived)".
8. Tests: path-resolution precedence (table test), stale-socket handling, and a run+status integration test using a temp socket.

Constraints: no sudo, no new dependencies. Finish by running the checks in AGENTS.md ("Definition of done") and report changes and assumptions.
```

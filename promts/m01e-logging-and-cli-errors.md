# Milestone 1e prompt: daemon logging and readable CLI errors

One-shot prompt. Do not re-run once done.

```text
Read AGENTS.md and docs/02-linux-io.md first.

Task: Milestone 1e only. Do not start milestone 2.

Observed problems:
- `slb run` prints nothing at all: no startup line, no sign it is listening, no error when a backend is unreachable.
- `slb status` without flags prints a raw JSON log line when the daemon is not reachable. The CLI should print a short human-readable error instead.

Do:
1. Daemon logging (`log/slog`, to stderr). Flags `--log-level` (debug|info|warn|error, default info) and `--log-format` (text|json; default text when stderr is a terminal, json otherwise), plus env `SLB_LOG_LEVEL` and `SLB_LOG_FORMAT`. Flag beats env beats default.
2. Log at info: startup (version, config path, control socket path and where it came from: flag, env, config or default), one summary per service (listeners as address:port/protocol, backends, affinity), one "listening" line per listener, config reload results, shutdown signal received, and clean stop.
3. Log at warn: backend dial failures (service, backend address, error), rate-limited to at most one line per backend per 10 seconds with a count of suppressed repeats. Never log secrets.
3b. Count dial failures per backend and show a `failed` counter next to `active` and `total` in `slb status` (text and --json). Do not add retry or ejection yet; that is milestone 3.
4. Log at debug only: each accepted and closed connection (client address, chosen backend, duration, bytes in/out). No per-connection logging at info.
5. CLI commands (`status` and every command that talks to the daemon) must not use slog for user-facing errors. Print `slb: <message>` to stderr and exit non-zero. For an unreachable daemon the message must name the socket path and where it came from (flag, env, config, default) and give a hint, for example: `slb: cannot reach the slb daemon at /tmp/x.sock (default). Is it running? Use --socket, SLB_SOCKET, or --config.` Exit codes: 0 ok, 1 error, 2 usage error, 3 daemon unreachable. Document them in docs/04-cli-and-ui.md.
6. Dev ergonomics: `slb status --config PATH` reads control.socket from that config. Remove `control.socket` from config/dev.json so `slb run --config config/dev.json` and `slb status` agree on the default path without any flag; update the README quick start accordingly.
7. Tests: log level and format selection (flag, env, default precedence); startup log contains the socket path and its source; rate limiting of dial-failure logs; the `failed` counter increments when a backend is down; status error text and exit code 3 when the daemon is down.

Constraints: no sudo, no new dependencies. Finish by running the checks in AGENTS.md ("Definition of done") and report changes and assumptions.
```

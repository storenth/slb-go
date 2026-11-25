# 02 - Linux I/O and Go runtime rules

Short invariants. Verify against `man 7 tcp`, `man 7 socket`, `man 2 splice`, `man 5 systemd.exec`, and the Go `net` package docs before changing related code.

## Platforms
- Production: Linux (Debian 12+): `keepalived`/VRRP, `systemd`, `ip_nonlocal_bind`, `splice`.
- macOS: development and unit/integration tests only (no `splice`, no VRRP). Any Linux-only code lives behind `//go:build linux` with a portable fallback.
- BSD and other Unix-likes: not supported (different HA stack, sysctls and init system).
- Production paths such as `/run/slb/slb.sock` stay as they are (systemd `RuntimeDirectory=slb`); dev overrides them with `--socket`.

## Go networking model
- Go's `net` is blocking-style code on top of the runtime's epoll poller: one goroutine per connection is the normal, cheap design. No manual event loop, no callbacks. Backpressure is natural: a blocked `Write` slows the reader.
- **Zero-copy:** `io.Copy` between two `*net.TCPConn` values uses `splice(2)` on Linux, so bytes need not enter user space. Wrapping a conn in your own `io.Reader`/`io.Writer` (counting, rate-limit, logging) disables this. Count bytes from `io.Copy`'s return value instead. Confirm with the milestone-6 benchmark. (From Go's documented `ReaderFrom` behaviour; verify for the Go version in use.)
- **Half-close:** after one direction's copy ends, call `CloseWrite()` on the destination `*net.TCPConn`; keep the other direction running; fully close both when both are done.
- **Idle timeout vs splice:** per-read deadline refresh needs user-space reads and gives up splice. Options: (a) chunked copy with deadline refresh (simple, slower), (b) splice plus TCP keepalive/`TCP_USER_TIMEOUT` and a coarse absolute limit. Decide, document, and benchmark.
  - **Decision (milestone 1h):** option (a) — chunked copy with `SetReadDeadline` refresh. When a service has a positive idle timeout, `copyWithIdle` reads into a 32 KB buffer and resets the read deadline after each chunk, checking a shared `lastActivity` timestamp (updated by both copy goroutines) so activity in either direction resets the timer. When idle timeout is zero or negative, `io.Copy` is used, preserving splice. This gives up splice on Linux for idle-enforced services: bytes enter user space on every read. The trade-off is simplicity and correctness (idle is enforced at the application level, not via TCP keepalive probes which detect dead peers but do not close idle connections) versus zero-copy throughput. Benchmark the difference before optimising; if it matters, option (b) with `TCP_USER_TIMEOUT` is the fallback.
- **Cancellation:** every connection handler takes a `context.Context`. On cancel, close the conns (this unblocks the blocked `Read`/`Write` calls). Use `errgroup` or `sync.WaitGroup` so no goroutine outlives its connection. Test for goroutine leaks.
- **Timeouts:** `net.Dialer{Timeout: ...}` for dial; `SetReadDeadline` for the ClientHello peek. Never leave a read without a deadline in the handshake phase.
- Do not block in handlers on slow work (DNS in the hot path, disk). Resolve backend addresses at config load/reload.

## Sockets
- `SO_REUSEADDR` is set by Go's listeners on Linux by default. `SO_REUSEPORT` is not; set it via `net.ListenConfig.Control` only if multiple listeners are needed (usually not, since one Go process uses all cores).
- Go enables `TCP_NODELAY` by default; keep it for a proxy.
- Keepalive: `net.Dialer.KeepAlive` / `ListenConfig.KeepAlive`; tune `TCP_KEEPIDLE/INTVL/CNT` via `SyscallConn` if defaults are too slow. Consider `TCP_USER_TIMEOUT` for upstream sockets.
- Listen backlog follows `net.core.somaxconn`; SYN queue follows `net.ipv4.tcp_max_syn_backlog`.

## UDP (milestone 7)
- One `*net.UDPConn` listener; `ReadFromUDPAddrPort` loop. Per-client flow table keyed by client addr:port with a mutex-guarded map; each flow owns a connected upstream UDP socket and a goroutine returning replies to the client.
- Idle-timeout reaper for flows; cap flow count; handle large datagrams (set read buffer size).
- Replies must leave from the VIP address the client sent to, or clients may drop them. Test this explicitly, since unconnected UDP on a multi-address host can pick the wrong source.

## Kernel settings (apply via `/etc/sysctl.d/99-slb.conf`, human-run)
| Setting | Why |
|---|---|
| `net.ipv4.ip_nonlocal_bind=1` | BACKUP can bind the VIP before it owns it |
| `net.core.somaxconn` | accept queue depth |
| `net.ipv4.tcp_max_syn_backlog` | SYN queue depth |
| `net.ipv4.ip_local_port_range` | ephemeral ports for outbound connections to backends |
| `net.ipv4.tcp_tw_reuse` | helps outbound port reuse under heavy churn (outbound only; understand before enabling) |
| `net.core.rmem_max` / `wmem_max` | socket buffer ceilings |

Ephemeral port math: roughly 28k ports by default per (source IP, destination IP, destination port) tuple. One backend under high connection churn can exhaust it. Mitigations: wider range, more backend IP/port pairs, more SLB source IPs.

## File descriptors
- Each proxied client connection uses 2 file descriptors (client leg + backend leg), however many backends exist. Go (1.19+) raises the soft `RLIMIT_NOFILE` to the hard limit at startup, but still set `LimitNOFILE` in the unit well above `2 x max connections` and enforce a configured max-connections limit.
- Report fd usage in `slb status`.

## Control socket
- **Path resolution (first match wins):** `--socket` flag, env `SLB_SOCKET`, config `control.socket`, default. Default: `/run/slb/slb.sock` if root or `/run/slb` exists and is writable (systemd), else `$XDG_RUNTIME_DIR/slb/slb.sock`, else `<tmp>/slb-<uid>/slb.sock`. Dev config uses a short path like `/tmp/slb-dev.sock` (macOS limits Unix socket paths to about 104 bytes).
- Never hardcode `/run` as the only option: macOS has no writable `/run`, and dev runs are non-root.
- `/run/slb/slb.sock`, directory from systemd (`RuntimeDirectory=slb`), mode `0660`, group-restricted. Remove a stale socket on start.
- HTTP+JSON via stdlib `net/http` over the Unix socket. Request size caps, read/write timeouts. The same handlers can later be exposed (authenticated) for the UI.

## systemd and keepalived
- Non-root user, `AmbientCapabilities=CAP_NET_BIND_SERVICE`, `Restart=on-failure`, `LimitNOFILE=`, `RuntimeDirectory=slb`, hardening (`NoNewPrivileges=`, `ProtectSystem=`) where compatible.
- `keepalived` runs `slb health` as its script user; that user must be able to execute the binary and reach the control socket (or use a lightweight local check). Go startup cost keeps this well under 1s.

## Observability
- `log/slog` JSON to stdout/journald; never log secrets.
- Minimum in `slb status`: active connections, total accepted, per-backend active/failed/health state, fd usage, config version, uptime, goroutine count.

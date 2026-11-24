# AGENTS.md

## Project overview
**SLB**: HA load balancer for small and mid-size on-prem businesses without a network team. Value = easy, safe operations (CLI, later web UI). Sized for small-business scale (`docs/05-performance.md`); correctness first.
- **Language:** Go. One static binary `slb` (CLI + daemon + later embedded UI). Debian 12+.
- **Data plane:** our own user-space L4 proxy. TCP passthrough (NEVER terminate TLS); UDP later. Behind a `DataPlane` interface.
- **HA:** Active-Backup pair. `keepalived` owns the VIP and runs `slb health` via `vrrp_script`.
- **State (MVP):** `FileStateProvider` (hot-reloaded JSON) behind a `StateProvider` interface.
- **Control plane:** HTTP+JSON over a Unix socket; CLI and UI are thin clients, no business logic in them.

## Runtime stack
Go binary + `keepalived` + the Linux kernel. Nothing else. HAProxy, Envoy, IPVS and Consul are NOT used in the MVP (benchmark or future options only).

## Docs index (read before related work)
`docs/00-architecture.md` topology, service model, decisions | `docs/01-net-theory.md` TCP/TLS/VRRP/related flows | `docs/02-linux-io.md` Go net, kernel, systemd | `docs/03-product-scope.md` positioning, protocol checklist | `docs/04-cli-and-ui.md` CLI contract, UI rules | `docs/05-performance.md` sizing, terms, speed-up rules | `docs/references.md` sources | `PLANS.md` milestones (prompts are in `prompts/`)

## Layout
`cmd/slb/` | `internal/{proxy,balancer,state,control,health,ha,cli,ui}` | `scripts/` | `config/` | `docs/` | `prompts/`

## Commands
- Build: `go build -o bin/slb ./cmd/slb` | Run: `go run ./cmd/slb run --config config/dev.json` | Certs: `./scripts/gen_certs.sh`
- Human-only (root): `apt-get install keepalived iproute2 tcpdump`, `./scripts/tune_kernel.sh`. Never run `sudo` yourself.

## Definition of done (all must pass)
1. `gofmt -l .` prints nothing; `go vet ./...` clean
2. `golangci-lint run` (if not installed, say so)
3. `go test -race ./...`

New balancer, CLI command, UI action, or health check ships with tests. Integration tests use dummy TLS/UDP backends on localhost (no root, no real VIP). Bug fixes start with a failing test.

## Code rules
- Idiomatic Go; wrap errors with `%w`; no panics in the data path; no global mutable state.
- `internal/proxy`: standard library only. Allowed third-party: `cobra`. Anything else: ask first.
- Every goroutine has an owner and a `context.Context`; no leaks (tests check).
- Logging with `log/slog` (JSON); never log secrets. Bash: `#!/usr/bin/env bash` + `set -euo pipefail`.

## Network rules (why: docs/01, docs/02)
- Close both connections on every exit path; reset, broken pipe and EOF are normal.
- Half-close: on EOF in one direction call `CloseWrite()` on the other side and keep copying the opposite direction.
- Copy with `io.Copy` between plain `*net.TCPConn` (enables `splice`). Never wrap conns on the hot path; count bytes from `io.Copy`'s return value.
- Mandatory configurable timeouts: dial, ClientHello peek, idle (document the idle-vs-splice choice).
- **Service model:** service = listeners (protocol + port or bounded range) + one backend pool + one affinity policy (`none`, `client_ip`, `client_ip+sni`). Affinity scope is the service, never one listener.
- **Selection order:** (1) local pin for this client IP (also for draining backends), (2) deterministic hash (fixed hash function, no per-process seeds, sorted iteration). Pins are per-node and never required for correctness after failover.
- `drain` waits up to a configured maximum, then closes. Never rebalance existing connections.
- Backend probes run on BOTH nodes with hysteresis; `slb health` is separate (local, <1s, exit 0/1).
- PROXY protocol v2: optional, per backend, off by default.
- Out of scope for MVP: protocol helpers/ALGs (e.g. FTP PASV rewriting), active-mode FTP, wide port ranges. Never write protocol-specific (FTP etc.) code in the data path; FTP is only an acceptance test.

## HA, kernel, config
- `net.ipv4.ip_nonlocal_bind=1` via `/etc/sysctl.d/99-slb.conf`. `slb ha init` renders unicast VRRP with a `vrrp_script` running `slb health` (`interval`, `fall`, `rise`); output mode `0600`.
- systemd: non-root user, `AmbientCapabilities=CAP_NET_BIND_SERVICE`, `LimitNOFILE`, `RuntimeDirectory=slb`.
- Never hardcode VIPs, interfaces, secrets or backend IPs: `/etc/slb/config.json` + env overrides. Validate before applying a reload; on failure keep last-known-good.

## Boundaries
- Never: terminate TLS, run `sudo`, touch host `/etc` or live sysctl, commit generated certs, secrets, binaries (`bin/`) or `.bak` files.
- Ask first: new dependencies, changing `StateProvider`/`Balancer`/`DataPlane` interfaces, renaming CLI commands, adding a UI framework, large rewrites.
- Small focused diffs; update docs and tests in the same change. Keep scratch files out of the repo root.

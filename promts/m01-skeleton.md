# Milestone 1 prompt: skeleton, proxy, round-robin, status

One-shot prompt. Do not re-run once the milestone is done.

```text
Read AGENTS.md and docs/00-architecture.md first.

Goal: Milestone 1 only. Do not implement anything from later milestones.

Build (Go, module path: github.com/storenth/slb-go):
1. Skeleton: go.mod, cmd/slb/main.go, internal/{proxy,balancer,state,control,cli}, config/dev.json, scripts/gen_certs.sh. CLI via cobra.
2. StateProvider interface + FileStateProvider: load JSON with `services[]`, each having `listeners[]` (protocol, port; port ranges may be parsed but only a single TCP listener per service must work in this milestone), `backends[]` (host/port) and an `affinity` field (parsed and validated, only `none` implemented now). Validate with clear errors. The config shape must not need to change for milestone 4.
3. Balancer interface + RoundRobin implementation.
4. internal/proxy (stdlib only): accept TCP, pick a backend, copy both directions with io.Copy between *net.TCPConn (no wrapping on the hot path), CloseWrite half-close, dial timeout and idle timeout (document your idle-vs-splice choice in a code comment), context-based shutdown, no goroutine leaks. No TLS termination.
5. Control server: HTTP+JSON over a Unix socket (path configurable, default /run/slb/slb.sock; tests use a temp path) returning status: backends, active connection counts, total connections, uptime.
6. CLI: `slb run --config PATH` and `slb status [--json] [--socket PATH]`.
7. Tests: unit tests for the balancer and config validation; integration tests that start dummy TLS echo backends on localhost, run the proxy, and verify (a) the TLS handshake succeeds end-to-end with the BACKEND's certificate (proving passthrough), (b) round-robin distribution, (c) half-close: client CloseWrite still receives the full response, (d) `slb status` output, (e) no goroutine leaks after shutdown.

Constraints: no sudo, no Consul, no keepalived work yet, no dependencies beyond cobra.

Finish by running the checks in AGENTS.md ("Definition of done") and report results, assumptions, and anything deferred.
```

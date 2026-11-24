# Milestone 1 audit

Audit of the codebase against AGENTS.md and Milestone 1/1b in PLANS.md.
The `state` package was partially migrated to the `services[]` shape; the
rest of the codebase still referenced the old flat `listen`/`backends` shape
and did not compile.

## Matches

- The project builds one Go binary and the proxy is a standard-library TCP passthrough; TLS is not terminated.
- Round-robin selection is concurrency-safe, and integration coverage verifies round-robin and the backend certificate.
- Both copy directions receive plain `*net.TCPConn` values, preserving the Linux `splice` fast path.
- Both copy directions call `CloseWrite` after `io.Copy`, so a half-close does not prematurely stop the opposite direction.
- Backend dialing uses `net.Dialer.Timeout`; shutdown is context-driven, closes tracked sockets to unblock I/O, and waits for connection handlers.
- The control plane is HTTP+JSON over a mode-`0660` Unix socket, and `slb status [--json]` is a thin client.
- There is no global mutable state, protocol-specific data-path code, TLS termination, or dependency beyond Cobra.
- The `state` package already defines `services[]` with `listeners[]` (protocol, port or bounded range, configurable cap), `backends[]`, and `affinity` (none | client_ip | client_ip+sni), with full validation including cross-service port overlap detection.
- The `state` package already has `ValidateRuntime()` that rejects UDP, port ranges, multiple listeners, and non-`none` affinity with clear "not implemented yet (milestone N)" errors.
- The `Balancer` interface already takes a `Request` struct with `Service` and `ClientAddress`, so affinity can be added in milestone 4 without an API change.

## Violations of rules

- The proxy, control, CLI, and all tests still used the old flat `listen`/`backends` config shape, so the code did not compile.
- The control server's `handleStatus` flattened stats to a single `backends` list, losing the per-service and per-listener grouping required by the docs.
- The CLI `run` command constructed a single proxy from `config.Listen.AddressPort()` instead of iterating `config.Services` and constructing one proxy per service.
- The CLI `status` command displayed a flat backend list instead of per-service listeners and backends.
- The proxy `New()` constructor took a raw address string and `[]state.Backend` instead of a `state.Service`, so per-service listener/backend metadata was lost.
- The proxy integration tests called `proxy.New` with the old signature and `balancer.Next()` with no arguments.

## Missing pieces

- `config/dev.json` still uses the old `listen`/`backends` shape instead of `services[]`.
- Tests for the new configuration rules: multi-listener service OK, bad affinity rejected, overlapping ports rejected, oversized range rejected.
- Tests for `ValidateRuntime` startup failures (UDP, port range, multiple listeners, non-`none` affinity).
- Per-service status output tests covering listeners and backends.
- The balancer tests, state tests, proxy integration tests, and CLI tests all needed updating to the new shapes.

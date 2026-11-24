# Milestone 1b prompt: reconcile existing code with updated docs

One-shot prompt for a repo whose skeleton predates the service/affinity model.

```text
Read AGENTS.md, docs/00-architecture.md (section "Service model and affinity"), docs/01-net-theory.md (section "Related flows, persistence and long-lived connections"), docs/02-linux-io.md and PLANS.md first. The docs were updated AFTER the current code was written.

Task: Milestone 1b only. Reconcile the existing code with the updated docs. Do not start milestone 2 or later. Prefer small refactors over rewrites; if you think a large rewrite is needed, stop and explain why instead of doing it.

Step 1 (no code changes yet): audit the repo against AGENTS.md and Milestone 1 in PLANS.md. Write a short gap report to docs/audit-m1.md with three lists: matches, violations of rules (for example: conns wrapped in custom Reader/Writer on the io.Copy hot path, missing CloseWrite half-close, missing dial/idle deadlines, goroutines without context or owner, global mutable state, non-deterministic hashing), and missing pieces.

Step 2: fix the gaps with the smallest diffs. Keep working code and passing tests. Required:
a) Config shape: services[], each with listeners[] (protocol tcp|udp; port or bounded port range, with a configurable cap on range size, default 256), backends[] (host, port) and affinity (none | client_ip | client_ip+sni). Parse and validate everything with clear errors. Reject unknown affinity values, empty listeners/backends, and overlapping listener ports across services. Update config/dev.json. No backward compatibility with the old shape is needed.
b) Only a single TCP listener per service must actually run now. A udp listener, a port range, or an affinity other than none must validate but fail at startup with a clear "not implemented yet (milestone N)" error.
c) Selection plumbing: the Balancer is called with the service and the client address (a small request struct), so affinity and the pin table can be added in milestone 4 without changing the config or API shape. Do NOT implement the pin table or hashing now.
d) Status API and `slb status [--json]`: per service, its listeners and backends (active connections, total connections), plus uptime.
e) Verify and fix if needed: io.Copy between *net.TCPConn without wrapping; byte counts taken from io.Copy's return value; CloseWrite half-close; dial timeout; idle timeout with a code comment explaining the idle-vs-splice choice; context-based shutdown; no goroutine leaks.

Step 3: update and add tests: config validation (multi-listener service OK, bad affinity rejected, overlapping ports rejected, oversized range rejected); the existing integration tests still pass (TLS passthrough shows the BACKEND's certificate, round-robin, half-close, status output, no goroutine leaks).

Constraints: no sudo, no Consul, no keepalived, no new dependencies beyond cobra.

Finish by running the checks in AGENTS.md ("Definition of done"), then report what changed, assumptions, and anything deferred.
```

# PLANS.md

Roadmap only. Each milestone has a one-shot prompt in `prompts/`; do not re-run a finished one. Finish a milestone (all checks in AGENTS.md green) before starting the next, then update the Status column.

| # | Milestone | Status | Prompt | Done when |
|---|-----------|--------|--------|-----------|
| 1 | Go skeleton + proxy + round-robin + `slb status` | done | `prompts/m01-skeleton.md` | TLS passthrough works against dummy backends; `slb status` reads the Unix socket |
| 1b | Reconcile with service/affinity model | done | `prompts/m01b-reconcile.md` | Audit report written; config uses `services[]`/`listeners[]`/`affinity`; checks green |
| 1c | Run without root: control socket path resolution | done | `prompts/m01c-dev-run.md` | `slb run` and `slb status` work without root on a non-Linux dev machine |
| 1d | Status shows listener port; manual smoke test; Linux gate (tests + smoke test on a Debian 12 VM) | done on macOS; Linux gate open | `prompts/m01d-status-port.md` | Smoke test passes on macOS and on Debian; `go test -race ./...` green on Linux |
| 1e | Daemon logging, readable CLI errors, dev socket default | done | `prompts/m01e-logging-and-cli-errors.md` | `slb run` logs startup and errors; `slb status` with no flags works against `slb run` and fails readably when the daemon is down |
| 1f | Dial retry on another backend + passive ejection (pulled forward from M3) | optional, after 1e | `prompts/m01f-dial-retry.md` | With one backend down, 100 client connections all succeed; the dead backend is ejected after 3 failures |
| 2 | Config hot reload + `slb config validate/reload` + `slb backend ...` | todo | | Invalid config never replaces last-known-good; `drain` works (with max wait) |
| 3 | Health: active backend probes (interval 2 s, fall 3, rise 2), re-admission of ejected backends, `slb health` | todo | | Dead backend is detected and re-admitted by probes; `slb health` < 1s with correct exit codes |
| 4 | Balancers (weighted RR, least-conn, P2C, hashes, SNI hash) + service-wide affinity + pin table | todo | | Deterministic across processes; FTP-style test: control and data ports of one service reach the same backend, also during drain |
| 5 | HA: `slb ha init`, keepalived template, kernel script, systemd unit | todo | | Rendered config tested; 0600 perms; `vrrp_script` present |
| 6 | `slb config push`, `slb conns`, PROXY protocol v2 (optional), capacity test + baseline vs HAProxy (see `docs/05-performance.md`) | todo | | Tests green; "tested capacity" table and report in `docs/benchmarks/` |
| 7 | UDP forwarding under the same service/affinity model | todo | | UDP echo/DNS tests green; "mini-protocol" test passes (TCP control announces a UDP port, UDP data reaches the same backend, also during drain) |
| 8 | Web UI on the control API | todo | | UI/CLI parity; auth + CSRF tested |
| 9 | Optional, not planned: IPVS backend for wide-range services | idea | | Only if user demand justifies it |

Before milestone 8: interview 10 target users (see `docs/03-product-scope.md`).

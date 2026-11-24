# PLANS.md

Roadmap only. Each milestone has a one-shot prompt in `prompts/`; do not re-run a finished one. Finish a milestone (all checks in AGENTS.md green) before starting the next, then update the Status column.

| # | Milestone | Status | Prompt | Done when |
|---|-----------|--------|--------|-----------|
| 1 | Go skeleton + proxy + round-robin + `slb status` | built, needs 1b | `prompts/m01-skeleton.md` | TLS passthrough works against dummy backends; `slb status` reads the Unix socket |
| 1b | Reconcile with service/affinity model | next | `prompts/m01b-reconcile.md` | Audit report written; config uses `services[]`/`listeners[]`/`affinity`; checks green |
| 2 | Config hot reload + `slb config validate/reload` + `slb backend ...` | todo | | Invalid config never replaces last-known-good; `drain` works (with max wait) |
| 3 | Health: backend probes + `slb health` | todo | | Dead backend leaves rotation; `slb health` < 1s with correct exit codes |
| 4 | Balancers (weighted RR, least-conn, P2C, hashes, SNI hash) + service-wide affinity + pin table | todo | | Deterministic across processes; FTP-style test: control and data ports of one service reach the same backend, also during drain |
| 5 | HA: `slb ha init`, keepalived template, kernel script, systemd unit | todo | | Rendered config tested; 0600 perms; `vrrp_script` present |
| 6 | `slb config push`, `slb conns`, PROXY protocol v2 (optional), benchmark vs HAProxy (baseline only) | todo | | Tests green; benchmark report in `docs/` |
| 7 | UDP forwarding under the same service/affinity model | todo | | UDP echo/DNS tests green; "mini-protocol" test passes (TCP control announces a UDP port, UDP data reaches the same backend, also during drain) |
| 8 | Web UI on the control API | todo | | UI/CLI parity; auth + CSRF tested |
| 9 | Optional, not planned: IPVS backend for wide-range services | idea | | Only if user demand justifies it |

Before milestone 8: interview 10 target users (see `docs/03-product-scope.md`).

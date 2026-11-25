# Milestone 1g prompt: config cleanup for timeouts

One-shot prompt. Skip it if your agent already made these changes (check config/dev.json and the tests).

```text
Read AGENTS.md and docs/06-config-and-affinity.md (sections "Precedence" and "Timers") first.

Task: Milestone 1g only. Config cleanup, no new features.

Decisions:
- `dial.*` and `ejection.*` exist only per service, with built-in defaults. Remove the global `timeouts.dial`.
- `idle`: global default `timeouts.idle` (built-in 5m) plus an optional per-service `idle` override. Precedence: service, then global, then built-in.
- Unknown config keys are errors (strict decoding). The error names the key and its path. A removed key must say where the setting moved, for example: `timeouts.dial was removed; use services[].dial.timeout`.

Do:
1. Implement the above in config parsing if, validation and the proxy (the effective idle timeout is resolved per service).
2. Update config/dev.json, README and docs/06 examples.
3. Log the effective idle timeout per service in the startup "service loaded" line.
4. Tests: precedence table (service beats global beats built-in), unknown-key error, moved-key message, the per-service idle override is applied (short durations or a fake clock, no long sleeps).

Constraints: standard library only, no sudo. Finish by running the checks in AGENTS.md ("Definition of done") and report changes and assumptions.
```

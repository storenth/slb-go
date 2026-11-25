# Milestone 1f prompt: dial retry on another backend + passive ejection

One-shot prompt. Run after 1e. Do not re-run once done.

```text
Read AGENTS.md and docs/01-net-theory.md (section "Health checking", the failure handling strategy and our defaults) first.

Task: Milestone 1f only. Do not start milestone 2 or 3. No active probes and no `slb health` yet.

Problem: with one backend down, round-robin still sends every Nth client connection to it and those clients get a connection reset.

Do:
1. Config per service (all optional, with these defaults): dial.timeout 3s, dial.max_attempts 3, dial.budget 5s, ejection.consecutive_failures 3, ejection.base_time 30s, ejection.max_time 5m. Validate (positive durations, max_attempts >= 1).
2. Dial path: pick a backend; on a connect-phase failure (refused, timeout, unreachable, no route) pick ANOTHER backend that has not been tried for this client connection and is not ejected, up to max_attempts attempts, within the total dial budget. Never retry after any byte was forwarded to a backend; if ClientHello bytes were peeked, replay them. When attempts are exhausted, close the client connection and count it.
3. Extend the balancer's selection request struct with the set of already-tried backends (this interface change is allowed for this task; mention it in your report).
4. Passive ejection per backend: after ejection.consecutive_failures consecutive dial failures, eject for base_time times the number of consecutive ejections, capped at max_time. A successful dial resets the failure count and the ejection streak.
5. Never eject the last non-ejected backend. If every backend is ejected, try them anyway (fail-open), the one whose ejection expires soonest first.
6. Status (`slb status`, text and --json): per backend state (up, or ejected until a timestamp), the existing active/total/failed counters, plus `last_error` and `last_failure_at`. Document the counter definitions in docs/04-cli-and-ui.md (section "Status counters"), keeping them consistent with that section.
7. Logging: warn on dial failure (keep the 1e rate limiting, and include `suppressed=N` on the next emitted line); info when a backend is ejected and when it is re-admitted; info `backend recovered` on the first successful dial after one or more failures.
7b. Startup log follow-up from 1e: in the `service loaded` line list the backend addresses (not only the count), and add one line with the effective log level, log format, dial settings and ejection settings.
8. Tests with an injectable Dialer and a fake clock (no sleeping): backend A up and B down gives 100 successful client connections and B receives at most 3 attempts before ejection; refused versus timed-out dials; ejection duration growth and cap; fail-open when all are ejected; the last backend is never ejected; no retry after bytes were forwarded; attempts and budget limits respected; no goroutine leaks.

Constraints: standard library only, no sudo. Finish by running the checks in AGENTS.md ("Definition of done") and report changes, assumptions and anything deferred.
```

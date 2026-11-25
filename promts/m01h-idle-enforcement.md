# Milestone 1h prompt: idle timeout is enforced and tested

One-shot prompt. Run it if the manual idle check fails; run it anyway if you want a regression test (the 1g test only checked that the value reaches the proxy).

Manual check first. Set `"idle": "1s"`, start slb and one backend, then measure only the SLB (an `openssl s_client` stdin or a `sleep` in a pipeline distorts the timing):
```bash
python3 - <<'PY'
import socket, time
s = socket.create_connection(("127.0.0.1", 8443))
t = time.time()
s.settimeout(20)
try:
    data = s.recv(1)
    print("closed by the SLB after %.1fs (recv returned %r)" % (time.time() - t, data))
except socket.timeout:
    print("NOT closed after 20s: idle is not enforced")
PY
# expect about 1 s. With --log-level debug the daemon should log the close with its duration.
```

```text
Read AGENTS.md and docs/02-linux-io.md first.

Task: Milestone 1h only. Make the per-service idle timeout actually enforced, and prove it with behavioural tests.

Do:
1. Behavioural tests with idle set to about 200ms: (a) a proxied connection with no traffic in either direction is closed by the proxy within about twice the idle time; (b) a connection that keeps exchanging data for longer than the idle time is NOT closed; (c) activity in either direction resets the timer; (d) after an idle close both the client and backend connections are closed and there are no goroutine leaks.
2. If any test fails, fix the proxy. Keep the io.Copy/splice rule from AGENTS.md where possible; if idle enforcement requires giving up splice for some path, document the trade-off in a code comment and in docs/02-linux-io.md, and tell me in the report.
3. At debug level, log the connection close with its duration and the close reason (idle, client closed, backend closed, error).

Constraints: standard library only, no sudo. Run the checks in AGENTS.md and list each command with its result.
```

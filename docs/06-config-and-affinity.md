# 06 - Config reference (draft) and affinity examples

Status legend: [now] implemented, [M4] planned for milestone 4, [M7] UDP milestone. The authoritative example is `config/dev.json`; if this doc and the validator disagree, fix the doc.

## Shape
```json
{
  "services": [
    {
      "name": "web",
      "listeners": [{"address": "127.0.0.1", "protocol": "tcp", "port": 8443}],
      "backends":  [{"host": "127.0.0.1", "port": 9443}, {"host": "127.0.0.1", "port": 9444}],
      "affinity": "none",
      "dial": {"timeout": "3s", "max_attempts": 3, "budget": "5s"},
      "ejection": {"consecutive_failures": 3, "base_time": "30s", "max_time": "5m"},
      "idle": "5m"
    }
  ],
  "timeouts": {"idle": "5m"}
}
```
- **Precedence:** `idle` = service value, then global `timeouts.idle`, then built-in default (5m). `dial.*` and `ejection.*` exist only per service, with built-in defaults; there is no global `timeouts.dial`. Unknown config keys are errors. A removed key tells you where it moved, for example: `timeouts.dial was removed; use services[].dial.timeout`.
- **`algorithm`** (optional, [M4]): `round_robin` (default) | `least_conn` | `p2c`. Valid ONLY when `affinity` is `none`. Backends may carry a `weight` (default 1) [M4].

## What affinity does
Affinity decides whether a client's connections are balanced independently or kept together.
- `none` [now]: every new connection is balanced on its own (round-robin today). Two curls from the same machine go to different backends.
- `client_ip` [M4]: all connections from the same client IP, to ANY listener of the service, go to the same backend.
- `client_ip+sni` [M4]: the key is client IP plus the TLS server name (read from the ClientHello, never decrypted). The same client asking for different hostnames may land on different backends; the same client and hostname always lands on the same one.

Decision (milestone 4):
1. `affinity != none` means selection is a deterministic hash (rendezvous) of the key over the eligible backends; both HA nodes compute the same answer with no shared state. `algorithm` must then be omitted (validation error otherwise).
2. A local pin table (client key to backend, `pin.ttl` default 5 min, refreshed by any live flow) keeps a client where it is while it has connections, also while its backend drains, and avoids flip-flopping after a failed backend recovers.
3. If the hashed backend is down or ejected, the next backend in the hash ranking is used (same on both nodes).
4. Failover to the other SLB node loses pins; hashing gives the same answer unless the backend set changed.
5. Cost: many users behind one NAT share one backend (skew). Show clients-per-backend in `slb status`.

## Mapping: affinity to selection method [M4]
| `affinity` | Selection method | Notes |
|---|---|---|
| `none` | `algorithm`: `round_robin` (default), `least_conn`, `p2c`; weights from backend `weight` | every connection decided independently |
| `client_ip` | weighted rendezvous hash on the client IP, plus the local pin table | all listeners of the service share the key; ranked fallback list |
| `client_ip+sni` | weighted rendezvous hash on client IP + TLS server name; no SNI (non-TLS, or hidden by ECH) falls back to the client IP | needs the ClientHello peek and its timeout |

Why rendezvous hashing and not plain modulo hashing or Maglev: modulo reshuffles most clients when a backend is added or removed; Maglev targets very large scale and speed; rendezvous is simple, moves only the clients of the changed backend, and yields a ranked fallback order that both HA nodes agree on.
Which fits what: `none` works for any stateless TCP or UDP service. Use `client_ip` whenever a session spans several connections or ports (FTP, DNS over UDP and TCP, SIP), or the app keeps session state in memory.

## Timers (they are different things)
| Setting | Scope | Default | Meaning |
|---|---|---|---|
| `dial.timeout` | service | 3s | max time to connect to ONE backend attempt |
| `dial.budget` | service | 5s | max total time for all attempts of one client connection |
| `idle` (service) / `timeouts.idle` (global) | per-service override / global default | 5m | closes an ESTABLISHED connection that carried no bytes in either direction for this long (frees sockets and ports, drops dead peers) |
| `ejection.*` | service | 3 failures, 30s, 5m | how long a failing backend is skipped |
| `ejection.consecutive_failures` | service | 3 | After 3 consecutive dial failures (no success in between), the backend is ejected |
| `ejection.base_time` | service | 30s | First ejection lasts 30s. Subsequent ejections multiply: 30s × streak (streak 1 = 30s, streak 2 = 60s, streak 3 = 90s, ...) |
| `ejection.max_time` | service | 5m | Ejection duration is capped at 5 minutes no matter how high the streak goes |
| `pin.ttl` [M4] | service | 5m | how long a client IP to backend mapping is remembered after the client's last flow ended |

`idle` is about one connection; `pin.ttl` is about remembering a client. Idle caution: an FTP control connection carries no bytes while a long data transfer runs, so a transfer longer than `idle` would lose its control connection. Give FTP a longer per-service idle (for example `"idle": "1h"`). The same goes for SSH, database pools and sparse WebSockets, or use TCP keepalive.

## Examples
**Stateless website** (any backend can answer anything):
```json
{"name": "web", "listeners": [{"protocol": "tcp", "port": 443}], "backends": [...], "affinity": "none"}
```
**Passive FTP** (control + data ports must reach the same backend) [M4, port ranges]:
```json
{
  "name": "ftp",
  "listeners": [
    {"protocol": "tcp", "port": 21},
    {"protocol": "tcp", "port_range": "30000-30100"}
  ],
  "backends": [{"host": "10.0.0.11", "port": 21}, {"host": "10.0.0.12", "port": 21}],
  "affinity": "client_ip"
}
```
(The exact range field name is whatever the config validator defines; check `docs` and tests when milestone 4 lands. Today a port range or any affinity other than `none` is rejected at startup.)
Backend-side settings are required too, for example vsftpd: `pasv_address=<VIP>`, `pasv_min_port=30000`, `pasv_max_port=30100`; open the same range on the backends' firewalls and use the same range on every backend. Active-mode FTP is not supported.

**Sticky sessions without cookies** (app keeps sessions in memory; one TLS listener): `"affinity": "client_ip"`.
**DNS over UDP and TCP** [M7]: listeners `udp:53` and `tcp:53`, `"affinity": "client_ip"`.
**One VIP, several hostnames** (TLS passthrough): `"affinity": "client_ip+sni"`.

## Testing affinity locally
All local test clients are `127.0.0.1`, so with `client_ip` every curl lands on the same backend. That is the expected result and the first test. To simulate other clients use other source addresses: on Linux `curl --interface 127.0.0.2 ...` works as is; on macOS first add an alias (`sudo ifconfig lo0 alias 127.0.0.2 up`). Expect: same source IP, always the same backend; different source IPs spread across backends.

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
      "ejection": {"consecutive_failures": 3, "base_time": "30s", "max_time": "5m"}
    }
  ],
  "timeouts": {"idle": "5m"}
}
```
- **Precedence for any setting:** service value, then global default, then built-in default. Keep ONE place for each setting; do not set the same timeout both globally and per service (e.g. `dial.timeout` lives in the service; `timeouts.idle` is global). [cleanup: remove `timeouts.dial` or define it as the global default for `dial.timeout`]
- **`algorithm`** (proposed, [M4]): `round_robin` (default) | `least_conn` | `p2c`. Used only when `affinity` is `none`.

## What affinity does
Affinity decides whether a client's connections are balanced independently or kept together.
- `none` [now]: every new connection is balanced on its own (round-robin today). Two curls from the same machine go to different backends.
- `client_ip` [M4]: all connections from the same client IP, to ANY listener of the service, go to the same backend.
- `client_ip+sni` [M4]: the key is client IP plus the TLS server name (read from the ClientHello, never decrypted). The same client asking for different hostnames may land on different backends; the same client and hostname always lands on the same one.

Rules (proposed, decide before M4):
1. `affinity != none` means selection is a deterministic hash (rendezvous) of the key over the eligible backends; both HA nodes compute the same answer with no shared state. `algorithm` must then be omitted (validation error otherwise).
2. A local pin table (client key to backend, `pin.ttl` default 5 min, refreshed by any live flow) keeps a client where it is while it has connections, also while its backend drains, and avoids flip-flopping after a failed backend recovers.
3. If the hashed backend is down or ejected, the next backend in the hash ranking is used (same on both nodes).
4. Failover to the other SLB node loses pins; hashing gives the same answer unless the backend set changed.
5. Cost: many users behind one NAT share one backend (skew). Show clients-per-backend in `slb status`.

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

# 01 - Network theory rules

Short invariants for agents and humans. Each rule has a source. Verify against the source before relying on it for a design change.

## TCP lifecycle
- **Half-close exists.** One side can send FIN (`shutdown(SHUT_WR)`, Go `CloseWrite()`) and still read. A proxy must forward EOF this way and keep the other direction open. Closing both sides on first EOF truncates responses. Source: RFC 9293 (TCP) section on closing; Stevens UNP Vol.1, `shutdown` vs `close`.
- **CLOSE_WAIT = our app has not closed after the peer sent FIN.** A growing CLOSE_WAIT count means a missing `close()`, i.e. a bug in our code, not a network issue. Source: RFC 9293 state diagram.
- **TIME_WAIT sits on the side that closes first.** Many short connections to one backend can exhaust ephemeral ports (see 02-linux-io.md). Source: RFC 9293; Stevens TCP/IP Illustrated Vol.1.
- **RST can arrive at any time.** Treat `ConnectionResetError` and `BrokenPipeError` as normal events, not crashes.
- **A TCP proxy has two independent connections** (client-SLB, SLB-backend). Backends see the SLB's source IP unless PROXY protocol is used.

## TLS passthrough
- The SLB never holds keys and never decrypts. Forward bytes unchanged.
- **ClientHello is plaintext** (TLS 1.2 and 1.3), so SNI (`server_name` extension) can be read without decrypting. Source: RFC 8446 section 4.1.2; RFC 6066 section 3.
- **ClientHello can span several TCP segments or TLS records.** Buffer until the full message is parsed, with a size cap and a short timeout. Never assume it arrives in one `read()`.
- **Encrypted ClientHello (ECH) hides SNI.** SNI routing will stop working for ECH clients. Fall back to IP-based hashing; never fail the connection. Source: IETF ECH draft/RFC (check current status).
- **Cap the ClientHello peek.** Envoy's `tls_inspector` records an oversized ClientHello as an error and caps what it processes at 64 KiB; do the same, and count `sni_found` / `sni_not_found` style metrics. Source: Envoy `tls_inspector` docs.
- Peeked bytes must be replayed to the backend first, byte-for-byte.
- TLS 1.3 session resumption (PSK tickets) is not usable for stickiness at L4. Use deterministic hashing instead.

## PROXY protocol
- Optional header prepended to the backend connection carrying the original client address. Backend must be configured to expect it, otherwise it breaks the connection. Per-backend flag, off by default. Source: HAProxy PROXY protocol spec (v2 is binary, starts with a fixed 12-byte signature).

## Load balancing
- **Round-robin** ignores load. **Least-connections** suits long-lived connections. **Power of two choices** (pick two at random, take the less loaded) is near-optimal and cheap. Source: Mitzenmacher.
- **Source-IP hash** skews badly behind NAT/CGNAT (many users share one IP).
- **Consistent hashing** (rendezvous or Maglev) remaps few clients when a backend is added or removed. **Maglev table size must be prime** and much larger than the backend count. Source: Maglev paper.
- **Determinism rule:** the hash input and function must be identical on both HA nodes, with no per-process random seeds (Go's `hash/maphash` seeds are random per process, and Go map iteration order is randomized: use a fixed hash such as FNV or xxhash, and sort keys before iterating).
- Removing an unhealthy backend must not reshuffle healthy backends' clients under consistent hashing.

## Health checking
- Backend probe (TCP connect, optional TLS handshake without verifying app data) is separate from the local `slb health`.
- Use hysteresis (N failures to mark down, M successes to mark up) to prevent flapping.
- Probes run on BOTH nodes so BACKUP has a current view at failover.

## VRRP / HA
- VRRPv3: master sends advertisements (default interval 1s); backup declares master dead after about 3x interval plus a skew time. Failover is therefore seconds, not milliseconds. Source: RFC 5798.
- New MASTER sends gratuitous ARP so switches and neighbors update the VIP-to-MAC mapping.
- **Existing connections break on failover.** Connection state is not replicated; clients reconnect. Document this, since it is inherent to this design.
- **Split-brain:** if advertisements are lost but both nodes are alive, both claim the VIP. Mitigate with unicast VRRP over a reliable (ideally dedicated) path, and a clear preempt policy.
- `vrrp_script` weight/priority drops make the node lose MASTER when `slb health` fails. `fall`/`rise` add hysteresis. Source: keepalived docs.
- VRRP needs a shared L2 segment. Public clouds typically do not support it.

## Related flows, persistence and long-lived connections
Two different problems, often confused:
1. **Session affinity / related flows:** which backend gets a client's NEXT connection or datagram, when several flows belong to one session.
2. **Long-lived connections:** how one connection behaves over hours (drain, rebalancing, dead-peer detection).

### Related flows (multi-connection protocols)
- **FTP** uses a control connection (port 21) plus separate data connections. In passive mode the server tells the client which port to open; in active mode the server connects back to the client from port 20. If the data connection lands on a different backend than the control connection, the transfer fails. Source: Red Hat LVS administration guide, FTP chapter.
- **Passive FTP behind a balancer needs three things:** (a) the client's data connections must reach the same backend as its control connection, (b) the backend must advertise the VIP (not its own IP) in the passive reply, and (c) a bounded passive port range configured on the backend and forwarded by the balancer (e.g. vsftpd `pasv_address`, `pasv_min_port`, `pasv_max_port`).
- **Active-mode FTP** needs the backend's outbound data connections to be routed or translated correctly; a plain user-space proxy cannot do this. Out of scope.
- **FTPS (TLS) encrypts the control channel,** so a balancer cannot read or rewrite the passive reply. Only affinity plus a fixed port range works.
- **Other examples:** TFTP (UDP 69, then a different port), SIP with RTP media, DNS (UDP then TCP fallback), IKE/IPsec (UDP 500/4500 plus ESP), game and VoIP sessions.
- **Techniques, in order of preference for this product:**
  1. **Service-wide affinity on client IP.** All flows of a service (any listener or port) from the same client IP go to the same backend. Deterministic hashing on client IP only (ignore ports) does this with no shared state, and both HA nodes agree.
  2. **Pin table (per node, local):** remember "client IP -> backend" while a client has live connections or for a persistence TTL. Consult it before the hash. This keeps related flows on the right backend while a backend is draining or the backend set changes. Lost on failover; hashing gives the same answer most of the time.
  3. **Protocol helpers / ALGs** (e.g. IPVS `ip_vs_ftp`, which rewrites passive replies in NAT mode). Fragile, protocol-specific, impossible when encrypted. Not in MVP.
  4. **Kernel port-range services** (IPVS firewall marks over a port range, or persistent port 0). The Red Hat guide marks control plus a wide passive range with one firewall mark so IPVS handles them as one service. This is the right tool for wide or unbounded ranges.
- **Limits of client-IP affinity:** many users behind one NAT share one backend (skew), and a client whose traffic leaves through different NAT addresses can split its flows.
- **Failure:** if the pinned backend dies mid-session, related flows break and clients must reconnect.

### Long-lived connections
- HTTP keep-alive, WebSockets, database pools and SSH sessions can live for hours. `drain` must wait for them, with a configurable maximum, then close.
- Existing connections are never rebalanced. After a backend recovers, least-connections sends it only NEW connections; consider "slow start" later.
- Detect dead peers on idle connections with TCP keepalive / `TCP_USER_TIMEOUT`.
- UDP "connections" are flows with an idle timeout; keep persistence TTL separate from flow idle timeout.

# Architecture

## Product shape
Two layers. **The control plane (CLI, web UI, config validation, HA rendering) is the product.** The data plane (the thing that moves packets) sits behind an interface and can be replaced.

```text
   CLI (slb ...)        Web UI (later)
          \                /
           v              v
        Control API (Unix socket, /run/slb/slb.sock)
                    |
        Config + validation + rollback + keepalived rendering
                    |
        DataPlane interface  --->  (see "Data plane options")
```

## Topology
Active-Backup pair on an L2 segment (on-prem / bare metal / VLAN). The VIP is owned by the MASTER only; the BACKUP receives no client traffic until failover.

```text
                  [ Clients - TLS / UDP ]
                          |
                          v
          VIP 192.168.1.100 (owned by MASTER only)
                          |
             active       |        standby (no traffic)
           +--------------+- - - - - - - - - +
           |                                 :
           v                                 :
 +---------------------+  VRRP   +---------------------+
 | Node 1 (MASTER)     |<------->| Node 2 (BACKUP)     |
 | Debian              | unicast | Debian              |
 |                     |         |                     |
 |  keepalived         |         |  keepalived         |
 |    | vrrp_script    |         |    | vrrp_script    |
 |    v  `slb health`  |         |    v  `slb health`  |
 |  slb-daemon         |         |  slb-daemon         |
 |  control API + data |         |  control API + data |
 |  config.json        |         |  config.json        |
 +----------+----------+         +----------+----------+
            |                               :
            v  (forwarding: active node only)
   [ Backend servers ]
     ^ active health probes from BOTH nodes
```

## Data plane options (decision: B, see ADR-001)
Note: `keepalived` (VRRP/VIP) is the HA layer. HAProxy, Envoy, IPVS and nginx are interchangeable data planes that sit behind it. The common pairings are keepalived+HAProxy and keepalived+IPVS (which keepalived can manage natively).

| Option | Pros | Cons |
|---|---|---|
| A. Own proxy in Python `asyncio` (rejected) | Fast to prototype; full control (SNI routing, PROXY protocol, logs, UDP flows); no backend network changes | Per-byte user-space copy, single event loop per process, venv packaging; lowest ceiling |
| **B. Own proxy in Go (single static binary), CHOSEN** | Much better concurrency/throughput; one binary can embed CLI, daemon and UI; easy `.deb` | Different language from current docs; still user-space copy |
| C. Orchestrate `keepalived` + Linux IPVS (kernel L4, TCP and UDP) | Fastest and most mature; keepalived already provides VRRP, health checks, IPVS management | NAT mode needs backends to route replies via the LB; DR mode needs backend ARP/loopback config; no SNI/PROXY/L7; UDP NAT is conntrack-heavy |
| D. Orchestrate HAProxy (TCP) + keepalived | Mature; SNI, PROXY protocol, stats | UDP only in commercial HAProxy products (ALOHA/Enterprise), to our knowledge; we must maintain config rendering |
| E. Orchestrate Envoy + keepalived | Mature L4 and L7; SNI-based filter-chain selection via `tls_inspector`; TCP and UDP proxying; PROXY protocol; rich stats | Verbose YAML/xDS configuration that we would have to generate and validate; heavier runtime; no VIP/failover of its own, so it still needs keepalived (or BGP/ECMP) |

## Service model and affinity
A **service** is the unit of configuration: one or more **listeners** (protocol plus port or bounded port range), one backend pool, one balancing algorithm, one **affinity policy**. Affinity scope is the service, never a single listener, so related flows on different ports stay together (FTP control + passive data, DNS UDP + TCP, SIP + media on a bounded range).

```text
service "ftp":
  listeners: tcp:21, tcp:30000-30100
  backends:  10.0.0.11, 10.0.0.12
  affinity:  client_ip
```
Backend selection order: (1) local pin for this client IP (honored even for draining backends), (2) deterministic hash. See `docs/01-net-theory.md` (related flows).

Limits of the user-space proxy: one listener socket per port, so port ranges must be bounded and small. Wide or unbounded ranges, port-0 services and protocol helpers belong to a future IPVS data-plane backend (kernel firewall-mark services, the approach Red Hat documents for FTP on LVS). Active-mode FTP and ALGs are out of scope for the MVP.

## Decisions
1. **No Consul in MVP.** Static config is identical on both nodes and synced with `slb config push`. Runtime state is per-node. The `StateProvider` interface keeps Consul possible later.
2. **Both nodes bind the VIP** (proxy mode): needs `net.ipv4.ip_nonlocal_bind=1`.
3. **Deterministic stickiness plus a local pin table** (see Service model). Hash on client IP only for multi-connection services.
3a. **Deterministic stickiness (detail).** Consistent hashing (client IP and/or SNI) gives the same mapping on both nodes with no shared state. Stick tables are lost on failover.
4. **Passthrough only.** No cookie/header stickiness. SNI may be read from the ClientHello; bytes are forwarded unchanged, with a short read timeout.
5. **Client IP visibility.** In proxy mode backends see the SLB IP unless PROXY protocol v2 is enabled (per backend, off by default). OPEN: confirm backends support it.
6. **UDP.** Flow table with idle timeout. Flows break on failover (no replication in MVP).
7. **Split-brain.** Unicast VRRP over a reliable path. Preempt policy: OPEN, decide before milestone 5.
8. **UI** talks only to the control API; the CLI has feature parity. Changes are validate, preview, apply, auto-rollback.
9. **Not for public clouds** (VRRP and gratuitous ARP are blocked).

## ADR-001: data plane and language (DECIDED: Go)
Context: product value is operations UX. The first plan assumed a Python proxy inherited from the initial draft, not chosen against alternatives.
Decision: Go for CLI, daemon and later UI, in one static binary. User-space proxy (option B) behind a `DataPlane` interface. `io.Copy` between TCP conns can use `splice`, goroutines use all cores, and packaging is a single file.
Consequences: IPVS (C), HAProxy (D) and Envoy (E) backends remain possible later via the interface, notably for high-throughput or UDP cases. Re-check this decision against the Milestone 6 benchmark and user interviews.

## Recommended reading
- Stevens, Fenner, Rudoff: *UNIX Network Programming, Vol. 1*; Stevens, Fall: *TCP/IP Illustrated, Vol. 1*; Kerrisk: *The Linux Programming Interface*
- Google SRE book (load balancing); Maglev paper; Mitzenmacher, *The Power of Two Choices*
- RFC 5798 (VRRPv3), RFC 9293 (TCP), RFC 8446 (TLS 1.3), HAProxy PROXY protocol spec
- keepalived and Linux IPVS (`ipvsadm`) documentation

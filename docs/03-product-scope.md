# 03 - Product scope and positioning

## Target user
Small/mid-size on-prem business: 2 servers, a few TCP (later UDP) services, no dedicated network engineer. Needs "HA in an afternoon" that is safe to operate.

## Positioning
Not "a faster load balancer." It is **a guided, safe, HA pair for TCP/UDP services with a UI and CLI.** Wins on: setup wizard, validated changes with rollback, drain-and-upgrade workflow, plain-language status, built-in keepalived pairing.

## Prior art (checked Nov 2026; re-verify before claims)
- HAProxy-WI: web GUI managing HAProxy, Nginx and Keepalived, with config push to peers.
- HAProxy Cluster Manager: self-hosted web UI, Let's Encrypt, active-passive keepalived VIP.
- SKUDONET CE / RELIANOID: full ADC with web GUI (Perl); original Zevenet repos are archived.
- **Envoy**: general-purpose L4/L7 proxy (TCP and UDP proxying, SNI-based filter-chain matching, PROXY protocol). Config-heavy and aimed at service meshes and gateways; it has no VIP failover of its own and no SMB-oriented UI. A data-plane reference and possible backend, not a direct SMB competitor.
- **keepalived + IPVS (LVS)**: the classic kernel L4 HA stack (TCP and UDP). No UI; this is what ALOHA's L4 mode builds on. A possible backend and benchmark baseline.
- OPNsense / pfSense HAProxy plugins: GUI for TCP mode and SSL passthrough; OPNsense nginx plugin has stream (incl. UDP) and PROXY protocol support. Requires adopting a firewall platform.
- **HAProxy ALOHA** (commercial appliance): web UI, configuration wizard, active/standby and active/active HA, config sync, and UDP load balancing either via embedded LVS (kernel) or a custom UDP module. HAProxy Enterprise also has UDP support. This is the closest direct competitor in the "HA + UI + UDP" category; pricing not researched.
- Others (Kemp, Loadbalancer.org, F5): not researched here.

## Honest gaps and risks
- The "UI on top of HAProxy + keepalived" space already has several free options. Differentiation must be UX and safety, and must be validated with real users.
- No market-size or willingness-to-pay evidence has been gathered. Interview 10 target users before investing in the UI.
- Decided: own Go data plane behind a `DataPlane` interface (see ADR-001). Orchestrating HAProxy/IPVS stays possible later; revisit after the Milestone 6 benchmark and user interviews.
- UDP is NOT a gap by itself: ALOHA/Enterprise (commercial), nginx `stream` and IPVS all do it. A possible gap is only an affordable/open UDP+TCP HA product with SMB-friendly UX. Correction: an earlier draft said HAProxy lacked UDP; that holds at most for the community edition (unverified).
- IPVS NAT-mode UDP limits (from ALOHA docs): about 65k source ports per address and heavy connection-tracking load.
- Existing connections/flows drop on failover (no state replication in MVP).
- VRRP needs a shared L2 segment; not for public clouds.

## Multi-connection protocols (FTP, SIP, TFTP, ...)
- Supported by design through the service model: several listeners, one pool, service-wide client-IP affinity.
- Presets planned for the UI/CLI (e.g. "FTP passive") that create the listeners and affinity and print the required backend settings (VIP in passive replies, fixed passive port range).
- Not supported in MVP: active-mode FTP, protocol-rewriting helpers, wide port ranges. Document these clearly in the UI.
- Client-IP affinity skews load behind NAT/CGNAT; show this in `slb status` (clients per backend).

## Will my protocol work? (compatibility checklist)
Ask four questions about the protocol:
1. **Who starts each flow?** If the client starts every flow toward the VIP, it can work. If the server starts a flow toward the client (active FTP, RTSP/SIP media sent by the server), the packets bypass a user-space proxy: not supported in the MVP.
2. **Are the ports fixed or in a small bounded range?** Fixed ranges work. Ports negotiated inside the control channel with an unbounded range need a protocol helper or a kernel (IPVS) service: not supported in the MVP.
3. **Does the payload carry an IP address or port the client will use** (FTP passive reply, SIP/SDP)? The backend must be configured to advertise the VIP and the fixed range. We cannot rewrite payloads (and cannot at all if encrypted).
4. **Do all flows come from the same client IP?** If yes, client-IP affinity keeps them on one backend.

| Protocol | MVP status | Notes |
|---|---|---|
| FTP passive (TCP control + TCP data) | Supported with setup | VIP in PASV reply, bounded passive range, affinity `client_ip`. Reference test case. |
| FTPS passive | Supported with setup | Same, no payload inspection |
| FTP active | Not supported | Server-initiated data connection |
| DNS (UDP + TCP fallback) | Supported | One service, two listeners |
| TCP control + UDP data, client-initiated, bounded ports (typical custom protocols) | Supported with setup | Same mechanism as FTP; proven by the mini-protocol test in milestone 7 |
| RTSP with RTP over TCP (interleaved) | Supported | Single TCP connection |
| RTSP/SIP with server-sent UDP media | Not supported in MVP | Server-initiated UDP; future IPVS/DSR |
| TFTP | Not supported in MVP | Server replies from a new port; needs port-aware handling |
(From protocol knowledge, not verified in this session; confirm with tests before promising support.)

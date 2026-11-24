# 05 - Sizing and performance (small-business scale)

We target small and mid-size on-prem setups. Performance work means: know our limits, document them, avoid cheap mistakes. We do NOT chase line-rate packet forwarding (XDP/eBPF, DPDK, SmartNIC offload): not planned.

## Terms and scale (avoid confusion)
- **Client connection:** a TCP connection from a client to the VIP.
- **Backend connection:** the TCP connection the SLB opens to the chosen backend for that client connection.
- **Proxied connection = 2 TCP legs** (client leg + backend leg). This is per client connection, NOT per backend server.
- **Backends (real servers):** any number per service, 1 to many (PCs, VMs, containers). We never assume a count. Each client connection is sent to exactly one backend.
- **SLB nodes:** exactly two (Active-Backup HA pair). Only the MASTER carries traffic.

Example: 3 backends, 1,000 clients connected = 2,000 TCP sockets on the MASTER (1,000 client legs + 1,000 backend legs), spread over the 3 backends.

## What grows with what
| Resource | Grows with |
|---|---|
| File descriptors | 2 per proxied connection (plus listeners, health probes, control socket) |
| Memory | concurrent connections x socket buffers |
| Ephemeral ports | connections per (SLB source IP, backend IP, backend port); more backends or backend ports means more headroom |
| Health-probe load | number of backends x probe frequency, on BOTH nodes |
| CPU | connection rate and packet rate; bulk throughput is cheap with `splice` |

## Metrics we report
New connections per second, concurrent connections, throughput (Gbit/s), added latency p50/p99, CPU and memory at load, failover time. UDP (milestone 7): packets per second.

## Cheap speed-ups (rules, not projects)
- Plain `io.Copy` between `*net.TCPConn` so the kernel can `splice` (AGENTS.md rule).
- No allocations or global locks in the accept/copy path; per-CPU or atomic counters.
- UDP: batch reads and writes where it helps, and size socket buffers.
- Kernel tuning (somaxconn, port range, buffers) and a high `LimitNOFILE`, via human-run scripts.

## Measurement (milestone 6, kept simple)
- Compare against HAProxy on the same hardware as a sanity baseline; HAProxy is NOT a runtime dependency.
- Correctness tests may use VMs, containers or network namespaces. Throughput numbers should come from real hardware with a separate load-generator machine; virtual NICs distort results.
- Record kernel, NIC, Go version, sysctls and commands; store reports in `docs/benchmarks/`.
- Output: a short "tested capacity" table for the README (e.g. concurrent connections and Gbit/s on a stated machine), so users can size their setup.

## Not planned
XDP/eBPF fast path, DPDK, SmartNIC/hardware offload, IPVS/Envoy/HAProxy data planes. Revisit only if real users report that the Go proxy is their bottleneck.

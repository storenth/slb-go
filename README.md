# slb-go

High-availability Layer 4 load balancer for small on-prem setups, written in Go. TCP passthrough (TLS is never terminated), UDP planned, Active-Backup failover with `keepalived`, and a CLI-first workflow (web UI planned).

> **Status: early development.** Not production-ready. See [PLANS.md](PLANS.md) for what exists and what is planned.

## Why
Running two nodes with a floating IP in front of a few TCP/UDP services should not need a network engineer. `slb-go` aims for a guided, safe setup: validated config changes, drain-before-maintenance, clear status output.

It is sized for small-business loads, with measured limits in [docs/05-performance.md](docs/05-performance.md). For line-rate throughput or L7 features, use HAProxy, Envoy or IPVS.

## How it works
```text
Clients --> VIP (owned by MASTER) --> slb daemon --> backend servers
              ^ keepalived (VRRP) pairs Node 1 (MASTER) and Node 2 (BACKUP)
              keepalived runs `slb health` to decide who is MASTER
```
Details: [docs/00-architecture.md](docs/00-architecture.md).

## Features
| Feature | Status |
|---|---|
| TCP passthrough, round-robin | in progress (milestone 1) |
| Services with several listeners + client-IP affinity (FTP-style related flows) | planned (milestones 1b, 4) |
| Health checks, `drain`, hot reload | planned (milestones 2, 3) |
| keepalived HA rendering (`slb ha init`) | planned (milestone 5) |
| UDP | planned (milestone 7) |
| Web UI | planned (milestone 8) |

## Requirements
- Go (see `go.mod` for the minimum version)
- Debian 12+ for deployment, with `keepalived`
- Linux for running (the proxy uses Linux-specific socket features)

## Quick start (development)
```bash
go build -o bin/slb ./cmd/slb
./scripts/gen_certs.sh                 # test certificates for dummy backends
./bin/slb run --config config/dev.json
./bin/slb status
```
Run the tests: `go test -race ./...`

Commands may change while the project is in early development.

## Configuration
Illustrative shape (the authoritative example is `config/dev.json`):
```json
{
  "services": [
    {
      "listeners": [{ "protocol": "tcp", "port": 8443 }],
      "backends": [{ "host": "127.0.0.1", "port": 9443 }],
      "affinity": "none"
    }
  ]
}
```

## Limitations
- Existing connections and UDP flows are dropped on failover (no state replication).
- No cookie or header stickiness (impossible without terminating TLS).
- Active-mode FTP and protocols that need payload rewriting are not supported.
- VRRP needs a shared L2 segment, so public clouds are generally not supported.
- Details and a protocol checklist: [docs/03-product-scope.md](docs/03-product-scope.md).

## Security
The control socket is `0660` and the daemon runs as a non-root user. Report vulnerabilities privately to the maintainer (add contact here) instead of opening a public issue.

## Contributing
Read [AGENTS.md](AGENTS.md) for the rules (they apply to humans and coding agents) and [docs/](docs/) for design notes. Before opening a pull request: `gofmt -l .`, `go vet ./...`, `golangci-lint run`, `go test -race ./...`.

## License
TODO: choose a license and add a `LICENSE` file.

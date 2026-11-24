# Milestone 1 testing

These steps validate the Milestone 1 TCP/TLS passthrough proxy, round-robin
balancing, configuration validation, control socket, CLI output, half-close
behavior, and clean shutdown.

No root privileges are required.

## Prerequisites

- Go 1.24 or newer
- OpenSSL
- `curl` for the optional manual traffic check
- `golangci-lint` for the optional lint check

## Complete automated validation

Run from the repository root:

```bash
go mod tidy
gofmt -w .
go vet ./...
go test -race ./...
go build -o bin/slb ./cmd/slb
```

Verify that formatting is clean without changing files:

```bash
test -z "$(gofmt -l .)"
```

If `golangci-lint` is installed, also run:

```bash
golangci-lint run
```

The expected result is that every command exits successfully and all tests
pass. If `golangci-lint` is not installed, note that in the test report.

## Focused automated tests

TLS passthrough and round-robin distribution:

```bash
go test -race -v ./internal/proxy \
  -run TestTLSPassthroughAndRoundRobin
```

The test verifies that the client receives the backend's certificate, proving
that SLB does not terminate TLS. It also verifies alternating backend selection.

TCP half-close and goroutine cleanup:

```bash
go test -race -v ./internal/proxy \
  -run TestHalfCloseAndShutdownDoesNotLeak
```

Status CLI in human-readable and JSON modes:

```bash
go test -race -v ./internal/cli \
  -run TestStatusOutput
```

Configuration loading and validation:

```bash
go test -race -v ./internal/state
```

Round-robin unit tests:

```bash
go test -race -v ./internal/balancer
```

## Manual smoke test

### 1. Generate a temporary backend certificate

```bash
./scripts/gen_certs.sh
```

This creates files under `testdata/certs/`. They are ignored by Git.

### 2. Start two TLS backends

In the first terminal:

```bash
openssl s_server \
  -accept 9443 \
  -cert testdata/certs/backend-cert.pem \
  -key testdata/certs/backend-key.pem \
  -www
```

In the second terminal:

```bash
openssl s_server \
  -accept 9444 \
  -cert testdata/certs/backend-cert.pem \
  -key testdata/certs/backend-key.pem \
  -www
```

The default development configuration points to these two ports.

### 3. Build and start SLB

In a third terminal:

```bash
go build -o bin/slb ./cmd/slb

./bin/slb run \
  --config config/dev.json \
  --socket /tmp/slb.sock
```

Using `/tmp/slb.sock` avoids requiring permission to create the default
`/run/slb/slb.sock` path.

### 4. Confirm backend TLS certificate passthrough

```bash
openssl s_client \
  -connect 127.0.0.1:8443 \
  -servername slb-test-backend \
  </dev/null 2>/dev/null | \
openssl x509 -noout -subject -issuer
```

The certificate subject should contain `CN = slb-test-backend`. This is the
certificate generated for the backend; SLB does not have or use a TLS key.

### 5. Query the control API through the CLI

Human-readable output:

```bash
./bin/slb status --socket /tmp/slb.sock
```

JSON output:

```bash
./bin/slb status --socket /tmp/slb.sock --json
```

The output should list both backends, uptime, active connections, and total
connections.

### 6. Generate traffic and inspect distribution

```bash
for n in 1 2 3 4; do
  curl -sk https://127.0.0.1:8443/ >/dev/null
done

./bin/slb status --socket /tmp/slb.sock --json
```

For these four new connections, the total connection count should increase by
two for each backend. Earlier certificate or status checks may already have
increased the displayed totals.

### 7. Stop the processes

Press `Ctrl-C` in the SLB terminal and both OpenSSL backend terminals. SLB
should exit promptly and remove `/tmp/slb.sock`.

## Milestone 1 acceptance checklist

- `go vet ./...` succeeds.
- `go test -race ./...` succeeds.
- `gofmt -l .` prints nothing.
- `golangci-lint run` succeeds when installed.
- The client receives the backend certificate through SLB.
- Connections alternate between the configured backends.
- A client half-close still receives the complete backend response.
- Human-readable and JSON status output are valid.
- Shutdown leaves no proxy goroutines running.
- No TLS termination, Consul, keepalived, UDP, or later-milestone behavior is
  involved.

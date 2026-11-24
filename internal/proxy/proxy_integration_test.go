package proxy_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/storenth/slb-go/internal/balancer"
	"github.com/storenth/slb-go/internal/proxy"
	"github.com/storenth/slb-go/internal/state"
)

type tlsBackend struct {
	backend state.Backend
	close   func()
}

func startTLSBackend(t *testing.T, id string, certificate tls.Certificate, waitForEOF bool) tlsBackend {
	t.Helper()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { _ = conn.Close() }()
				if waitForEOF {
					data, _ := io.ReadAll(conn)
					_, _ = fmt.Fprintf(conn, "%s:%s", id, data)
					return
				}
				buffer := make([]byte, 128)
				n, err := conn.Read(buffer)
				if err == nil {
					_, _ = fmt.Fprintf(conn, "%s:%s", id, buffer[:n])
				}
			}()
		}
	}()
	address := listener.Addr().(*net.TCPAddr)
	return tlsBackend{backend: state.Backend{Host: "127.0.0.1", Port: address.Port}, close: func() { cancel(); _ = listener.Close(); <-ctx.Done(); wg.Wait() }}
}

func certificate(t *testing.T) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(42), Subject: pkix.Name{CommonName: "backend.example"}, DNSNames: []string{"backend.example"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, parsed
}

func startProxy(t *testing.T, backends []state.Backend) (*proxy.Proxy, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	service := state.Service{Name: "test", Listeners: []state.Listener{{Address: "127.0.0.1", Protocol: "tcp", Port: 0}}, Backends: backends, Affinity: "none"}
	p := proxy.New(service, balancer.NewRoundRobin(backends), time.Second, 10*time.Second)
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for strings.HasSuffix(p.Address(), ":0") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if strings.HasSuffix(p.Address(), ":0") {
		cancel()
		t.Fatal("proxy did not start")
	}
	return p, cancel, done
}

func dialTLS(t *testing.T, address string, root *x509.Certificate) *tls.Conn {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(root)
	conn, err := tls.Dial("tcp", address, &tls.Config{RootCAs: roots, ServerName: "backend.example", MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestTLSPassthroughAndRoundRobin(t *testing.T) {
	cert, parsed := certificate(t)
	one := startTLSBackend(t, "one", cert, false)
	defer one.close()
	two := startTLSBackend(t, "two", cert, false)
	defer two.close()
	p, cancel, done := startProxy(t, []state.Backend{one.backend, two.backend})
	for i, want := range []string{"one:hello", "two:hello", "one:hello", "two:hello"} {
		conn := dialTLS(t, p.Address(), parsed)
		if len(conn.ConnectionState().PeerCertificates) == 0 || conn.ConnectionState().PeerCertificates[0].SerialNumber.Cmp(big.NewInt(42)) != 0 {
			t.Fatal("did not receive backend certificate")
		}
		_, _ = conn.Write([]byte("hello"))
		buffer := make([]byte, len(want))
		if _, err := io.ReadFull(conn, buffer); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		_ = conn.Close()
		if string(buffer) != want {
			t.Fatalf("request %d = %q, want %q", i, buffer, want)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestHalfCloseAndShutdownDoesNotLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	cert, parsed := certificate(t)
	backend := startTLSBackend(t, "full", cert, true)
	p, cancel, done := startProxy(t, []state.Backend{backend.backend})
	conn := dialTLS(t, p.Address(), parsed)
	_, _ = conn.Write([]byte("request"))
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if string(response) != "full:request" {
		t.Fatalf("response = %q", response)
	}
	_ = conn.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	backend.close()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > before+2 {
		t.Fatalf("goroutines after shutdown = %d, before = %d", got, before)
	}
}

func TestFailedCounterIncrementsOnDialFailure(t *testing.T) {
	// Backend points at a port where nothing is listening, so every dial fails.
	deadBackend := state.Backend{Host: "127.0.0.1", Port: 1} // port 1: nothing listens
	service := state.Service{
		Name:      "dead",
		Listeners: []state.Listener{{Address: "127.0.0.1", Protocol: "tcp", Port: 0}},
		Backends:  []state.Backend{deadBackend},
		Affinity:  "none",
	}
	p := proxy.New(service, balancer.NewRoundRobin([]state.Backend{deadBackend}), 100*time.Millisecond, 10*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	// Wait for the proxy to start listening.
	deadline := time.Now().Add(2 * time.Second)
	for strings.HasSuffix(p.Address(), ":0") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if strings.HasSuffix(p.Address(), ":0") {
		cancel()
		t.Fatal("proxy did not start")
	}

	// Make several client connections; each should fail to dial the backend.
	for range 5 {
		conn, err := net.Dial("tcp", p.Address())
		if err != nil {
			t.Fatalf("dial proxy: %v", err)
		}
		// Give the proxy time to attempt the backend dial and close.
		time.Sleep(200 * time.Millisecond)
		_ = conn.Close()
	}

	// Allow dials to time out and settle.
	time.Sleep(300 * time.Millisecond)

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	stats := p.Stats()
	if len(stats.Services) != 1 || len(stats.Services[0].Backends) != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	failed := stats.Services[0].Backends[0].FailedConnections
	if failed == 0 {
		t.Fatalf("expected failed counter > 0, got %d", failed)
	}
}

func TestDialFailureLoggingRateLimited(t *testing.T) {
	// Use a logger that writes to a buffer so we can inspect the output.
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	deadBackend := state.Backend{Host: "127.0.0.1", Port: 1}
	service := state.Service{
		Name:      "dead",
		Listeners: []state.Listener{{Address: "127.0.0.1", Protocol: "tcp", Port: 0}},
		Backends:  []state.Backend{deadBackend},
		Affinity:  "none",
	}
	p := proxy.NewWithLogger(service, balancer.NewRoundRobin([]state.Backend{deadBackend}), 100*time.Millisecond, 10*time.Second, logger)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for strings.HasSuffix(p.Address(), ":0") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if strings.HasSuffix(p.Address(), ":0") {
		cancel()
		t.Fatal("proxy did not start")
	}

	// Make several client connections rapidly; all should fail to dial.
	// The rate limiter should allow only one warn line for the backend.
	for range 5 {
		conn, err := net.Dial("tcp", p.Address())
		if err != nil {
			t.Fatalf("dial proxy: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
		_ = conn.Close()
	}

	time.Sleep(200 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	output := buf.String()
	// Count the number of "backend dial failed" lines.
	count := strings.Count(output, "backend dial failed")
	if count != 1 {
		t.Fatalf("expected exactly 1 dial-failure log line (rate limited), got %d:\n%s", count, output)
	}
}

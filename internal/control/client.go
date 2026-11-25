package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/storenth/slb-go/internal/state"
)

type Status struct {
	Services      []Service `json:"services"`
	UptimeSeconds float64   `json:"uptime_seconds"`
}

type Service struct {
	Name              string     `json:"name"`
	Listeners         []Listener `json:"listeners"`
	Backends          []Backend  `json:"backends"`
	ActiveConnections int64      `json:"active_connections"`
	TotalConnections  uint64     `json:"total_connections"`
}

type Listener struct {
	Address           string           `json:"address"`
	Protocol          string           `json:"protocol"`
	Port              int              `json:"port,omitempty"`
	PortRange         *state.PortRange `json:"port_range,omitempty"`
	ActiveConnections int64            `json:"active_connections"`
	TotalConnections  uint64           `json:"total_connections"`
}

// AddressPort returns the listener address with its port or port range,
// e.g. "127.0.0.1:8443" or "127.0.0.1:30000-30100".
func (l Listener) AddressPort() string {
	if l.PortRange != nil {
		return net.JoinHostPort(l.Address, fmt.Sprintf("%d-%d", l.PortRange.Start, l.PortRange.End))
	}
	return net.JoinHostPort(l.Address, strconv.Itoa(l.Port))
}

type Backend struct {
	Host              string     `json:"host"`
	Port              int        `json:"port"`
	ActiveConnections int64      `json:"active_connections"`
	TotalConnections  uint64     `json:"total_connections"`
	FailedConnections uint64     `json:"failed_connections"`
	State             string     `json:"state"`
	EjectedUntil      *time.Time `json:"ejected_until,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	LastFailureAt     *time.Time `json:"last_failure_at,omitempty"`
}

func GetStatus(ctx context.Context, socketPath string) (Status, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/v1/status", nil)
	if err != nil {
		return Status{}, fmt.Errorf("create status request: %w", err)
	}
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		return Status{}, fmt.Errorf("request status through %q: %w", socketPath, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return Status{}, fmt.Errorf("status API returned %s", response.Status)
	}
	var status Status
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		return Status{}, fmt.Errorf("decode status response: %w", err)
	}
	return status, nil
}

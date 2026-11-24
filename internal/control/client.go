package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
)

type Status struct {
	Backends          []proxyBackend `json:"backends"`
	ActiveConnections int64          `json:"active_connections"`
	TotalConnections  uint64         `json:"total_connections"`
	UptimeSeconds     float64        `json:"uptime_seconds"`
}

type proxyBackend struct {
	Host              string `json:"host"`
	Port              int    `json:"port"`
	ActiveConnections int64  `json:"active_connections"`
	TotalConnections  uint64 `json:"total_connections"`
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

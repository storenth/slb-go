package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultDialTimeout = 5 * time.Second
	DefaultIdleTimeout = 5 * time.Minute
)

type Backend struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

func (b Backend) Address() string { return net.JoinHostPort(b.Host, strconv.Itoa(b.Port)) }

type Listen struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

func (l Listen) AddressPort() string { return net.JoinHostPort(l.Address, strconv.Itoa(l.Port)) }

type Timeouts struct {
	Dial string `json:"dial"`
	Idle string `json:"idle"`
}

type Config struct {
	Listen   Listen    `json:"listen"`
	Backends []Backend `json:"backends"`
	Timeouts Timeouts  `json:"timeouts,omitempty"`
}

func (c Config) DialTimeout() time.Duration { return durationOr(c.Timeouts.Dial, DefaultDialTimeout) }
func (c Config) IdleTimeout() time.Duration { return durationOr(c.Timeouts.Idle, DefaultIdleTimeout) }

func durationOr(value string, fallback time.Duration) time.Duration {
	if value == "" {
		return fallback
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return d
}

func (c Config) Validate() error {
	var errs []error
	if strings.TrimSpace(c.Listen.Address) == "" {
		errs = append(errs, errors.New("listen.address is required"))
	}
	if c.Listen.Port < 0 || c.Listen.Port > 65535 {
		errs = append(errs, fmt.Errorf("listen.port must be between 0 and 65535, got %d", c.Listen.Port))
	}
	if len(c.Backends) == 0 {
		errs = append(errs, errors.New("at least one backend is required"))
	}
	seen := make(map[string]struct{}, len(c.Backends))
	for i, backend := range c.Backends {
		if strings.TrimSpace(backend.Host) == "" {
			errs = append(errs, fmt.Errorf("backends[%d].host is required", i))
		}
		if backend.Port < 1 || backend.Port > 65535 {
			errs = append(errs, fmt.Errorf("backends[%d].port must be between 1 and 65535, got %d", i, backend.Port))
		}
		if backend.Host != "" && backend.Port > 0 {
			address := backend.Address()
			if _, ok := seen[address]; ok {
				errs = append(errs, fmt.Errorf("backends[%d] duplicates %q", i, address))
			}
			seen[address] = struct{}{}
		}
	}
	for name, value := range map[string]string{"timeouts.dial": c.Timeouts.Dial, "timeouts.idle": c.Timeouts.Idle} {
		if value == "" {
			continue
		}
		d, err := time.ParseDuration(value)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s must be a duration: %w", name, err))
			continue
		}
		if d <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive", name))
		}
	}
	return errors.Join(errs...)
}

type StateProvider interface {
	Load(context.Context) (Config, error)
}

type FileStateProvider struct{ Path string }

func (p FileStateProvider) Load(ctx context.Context) (Config, error) {
	if err := ctx.Err(); err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(p.Path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", p.Path, err)
	}
	var config Config
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", p.Path, err)
	}
	if err := config.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate config %q: %w", p.Path, err)
	}
	return config, nil
}

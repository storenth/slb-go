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
	DefaultDialTimeout  = 5 * time.Second
	DefaultIdleTimeout  = 5 * time.Minute
	DefaultPortRangeCap = 256
)

type Backend struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

func (b Backend) Address() string { return net.JoinHostPort(b.Host, strconv.Itoa(b.Port)) }

type PortRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type Listener struct {
	Address   string     `json:"address"`
	Protocol  string     `json:"protocol"`
	Port      int        `json:"port,omitempty"`
	PortRange *PortRange `json:"port_range,omitempty"`
}

func (l Listener) AddressPort() string { return net.JoinHostPort(l.Address, strconv.Itoa(l.Port)) }

func (l Listener) ports() (int, int) {
	if l.PortRange != nil {
		return l.PortRange.Start, l.PortRange.End
	}
	return l.Port, l.Port
}

type Service struct {
	Name      string     `json:"name"`
	Listeners []Listener `json:"listeners"`
	Backends  []Backend  `json:"backends"`
	Affinity  string     `json:"affinity"`
}

type Timeouts struct {
	Dial string `json:"dial"`
	Idle string `json:"idle"`
}

// Control holds control-plane settings. Socket is optional: when empty the
// daemon falls back to the built-in default path (see docs/02-linux-io.md).
type Control struct {
	Socket string `json:"socket,omitempty"`
}

type Config struct {
	Services         []Service `json:"services"`
	MaxPortRangeSize int       `json:"max_port_range_size,omitempty"`
	Timeouts         Timeouts  `json:"timeouts,omitempty"`
	Control          Control   `json:"control,omitempty"`
}

func (c Config) DialTimeout() time.Duration { return durationOr(c.Timeouts.Dial, DefaultDialTimeout) }
func (c Config) IdleTimeout() time.Duration { return durationOr(c.Timeouts.Idle, DefaultIdleTimeout) }
func (c Config) ControlSocket() string      { return c.Control.Socket }
func (c Config) PortRangeCap() int {
	if c.MaxPortRangeSize == 0 {
		return DefaultPortRangeCap
	}
	return c.MaxPortRangeSize
}

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
	if len(c.Services) == 0 {
		errs = append(errs, errors.New("at least one service is required"))
	}
	if c.MaxPortRangeSize < 0 {
		errs = append(errs, errors.New("max_port_range_size must be positive"))
	}
	serviceNames := make(map[string]struct{}, len(c.Services))
	type listenerOwner struct {
		service string
		index   int
	}
	ports := make(map[string]listenerOwner)
	for serviceIndex, service := range c.Services {
		prefix := fmt.Sprintf("services[%d]", serviceIndex)
		name := strings.TrimSpace(service.Name)
		if name == "" {
			errs = append(errs, fmt.Errorf("%s.name is required", prefix))
		} else if _, exists := serviceNames[name]; exists {
			errs = append(errs, fmt.Errorf("%s.name duplicates %q", prefix, name))
		}
		serviceNames[name] = struct{}{}
		if len(service.Listeners) == 0 {
			errs = append(errs, fmt.Errorf("%s.listeners must not be empty", prefix))
		}
		if len(service.Backends) == 0 {
			errs = append(errs, fmt.Errorf("%s.backends must not be empty", prefix))
		}
		switch service.Affinity {
		case "none", "client_ip", "client_ip+sni":
		default:
			errs = append(errs, fmt.Errorf("%s.affinity must be one of none, client_ip, client_ip+sni, got %q", prefix, service.Affinity))
		}
		for listenerIndex, listener := range service.Listeners {
			listenerPrefix := fmt.Sprintf("%s.listeners[%d]", prefix, listenerIndex)
			if strings.TrimSpace(listener.Address) == "" {
				errs = append(errs, fmt.Errorf("%s.address is required", listenerPrefix))
			}
			if listener.Protocol != "tcp" && listener.Protocol != "udp" {
				errs = append(errs, fmt.Errorf("%s.protocol must be tcp or udp, got %q", listenerPrefix, listener.Protocol))
			}
			if listener.Port != 0 && listener.PortRange != nil {
				errs = append(errs, fmt.Errorf("%s must set either port or port_range, not both", listenerPrefix))
				continue
			}
			start, end := listener.ports()
			if start < 1 || end > 65535 || end < start {
				errs = append(errs, fmt.Errorf("%s port or port_range must be between 1 and 65535 with end >= start", listenerPrefix))
				continue
			}
			if size := end - start + 1; size > c.PortRangeCap() {
				errs = append(errs, fmt.Errorf("%s port range size %d exceeds max_port_range_size %d", listenerPrefix, size, c.PortRangeCap()))
			}
			for port := start; port <= end; port++ {
				key := fmt.Sprintf("%s|%s|%d", listener.Protocol, listener.Address, port)
				if owner, exists := ports[key]; exists && owner.service != name {
					errs = append(errs, fmt.Errorf("%s overlaps port %d with service %q listener %d", listenerPrefix, port, owner.service, owner.index))
					break
				}
				ports[key] = listenerOwner{service: name, index: listenerIndex}
			}
		}
		seenBackends := make(map[string]struct{}, len(service.Backends))
		for backendIndex, backend := range service.Backends {
			backendPrefix := fmt.Sprintf("%s.backends[%d]", prefix, backendIndex)
			if strings.TrimSpace(backend.Host) == "" {
				errs = append(errs, fmt.Errorf("%s.host is required", backendPrefix))
			}
			if backend.Port < 1 || backend.Port > 65535 {
				errs = append(errs, fmt.Errorf("%s.port must be between 1 and 65535, got %d", backendPrefix, backend.Port))
			}
			address := backend.Address()
			if _, exists := seenBackends[address]; exists {
				errs = append(errs, fmt.Errorf("%s duplicates %q", backendPrefix, address))
			}
			seenBackends[address] = struct{}{}
		}
	}
	for name, value := range map[string]string{"timeouts.dial": c.Timeouts.Dial, "timeouts.idle": c.Timeouts.Idle} {
		if value == "" {
			continue
		}
		d, err := time.ParseDuration(value)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s must be a duration: %w", name, err))
		} else if d <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive", name))
		}
	}
	return errors.Join(errs...)
}

func (c Config) ValidateRuntime() error {
	for i, service := range c.Services {
		if len(service.Listeners) != 1 {
			return fmt.Errorf("service %q has %d listeners: multiple listeners are not implemented yet (milestone 4)", service.Name, len(service.Listeners))
		}
		listener := service.Listeners[0]
		if listener.Protocol == "udp" {
			return fmt.Errorf("service %q uses udp: udp listeners are not implemented yet (milestone 7)", service.Name)
		}
		if listener.PortRange != nil {
			return fmt.Errorf("service %q uses a port range: port ranges are not implemented yet (milestone 4)", service.Name)
		}
		if service.Affinity != "none" {
			return fmt.Errorf("services[%d] %q uses affinity %q: affinity is not implemented yet (milestone 4)", i, service.Name, service.Affinity)
		}
	}
	return nil
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

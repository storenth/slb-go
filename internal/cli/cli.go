package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/storenth/slb-go/internal/balancer"
	"github.com/storenth/slb-go/internal/control"
	"github.com/storenth/slb-go/internal/proxy"
	"github.com/storenth/slb-go/internal/state"
)

func New(out, errOut io.Writer) *cobra.Command {
	root := &cobra.Command{Use: "slb", SilenceUsage: true, SilenceErrors: true}
	root.SetOut(out)
	root.SetErr(errOut)
	root.AddCommand(newRun(), newStatus(out))
	return root
}

func newRun() *cobra.Command {
	var configPath, socketPath string
	command := &cobra.Command{Use: "run", Short: "Run the load balancer", RunE: func(cmd *cobra.Command, _ []string) error {
		config, err := (state.FileStateProvider{Path: configPath}).Load(cmd.Context())
		if err != nil {
			return err
		}
		if err := config.ValidateRuntime(); err != nil {
			return err
		}
		resolved := control.ResolveSocketPath(control.SocketOptions{
			Flag:   socketPath,
			Env:    os.Getenv("SLB_SOCKET"),
			Config: config.ControlSocket(),
		})
		ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		collection := proxy.Collection{Started: time.Now()}
		for _, service := range config.Services {
			p := proxy.New(service, balancer.NewRoundRobin(service.Backends), config.DialTimeout(), config.IdleTimeout())
			collection.Proxies = append(collection.Proxies, p)
		}
		controlServer := control.Server{Path: resolved, Stats: collection}
		errs := make(chan error, 2)
		go func() { errs <- collection.Run(ctx) }()
		go func() { errs <- controlServer.Run(ctx) }()
		err = <-errs
		stop()
		second := <-errs
		if err != nil {
			return err
		}
		return second
	}}
	command.Flags().StringVar(&configPath, "config", "config/dev.json", "configuration file")
	command.Flags().StringVar(&socketPath, "socket", "", "control socket path (overrides SLB_SOCKET and config control.socket)")
	return command
}

func newStatus(out io.Writer) *cobra.Command {
	var socketPath, configPath string
	var asJSON bool
	command := &cobra.Command{Use: "status", Short: "Show load balancer status", RunE: func(cmd *cobra.Command, _ []string) error {
		var configSocket string
		if configPath != "" {
			config, err := (state.FileStateProvider{Path: configPath}).Load(cmd.Context())
			if err != nil {
				return err
			}
			configSocket = config.ControlSocket()
		}
		resolved := control.ResolveSocketPath(control.SocketOptions{
			Flag:   socketPath,
			Env:    os.Getenv("SLB_SOCKET"),
			Config: configSocket,
		})
		status, err := control.GetStatus(cmd.Context(), resolved)
		if err != nil {
			return err
		}
		if asJSON {
			encoder := json.NewEncoder(out)
			encoder.SetIndent("", "  ")
			return encoder.Encode(status)
		}
		_, err = fmt.Fprintf(out, "Uptime: %.0fs\n", status.UptimeSeconds)
		if err != nil {
			return err
		}
		for _, service := range status.Services {
			_, err = fmt.Fprintf(out, "\nService: %s  (%d active, %d total)\n", service.Name, service.ActiveConnections, service.TotalConnections)
			if err != nil {
				return err
			}
			for _, listener := range service.Listeners {
				_, err = fmt.Fprintf(out, "  Listener: %s/%s  %d active, %d total\n", listener.AddressPort(), listener.Protocol, listener.ActiveConnections, listener.TotalConnections)
				if err != nil {
					return err
				}
			}
			for _, backend := range service.Backends {
				_, err = fmt.Fprintf(out, "  Backend:  %s:%d  %d active, %d total\n", backend.Host, backend.Port, backend.ActiveConnections, backend.TotalConnections)
				if err != nil {
					return err
				}
			}
		}
		return nil
	}}
	command.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	command.Flags().StringVar(&socketPath, "socket", "", "control socket path (overrides SLB_SOCKET and config control.socket)")
	command.Flags().StringVar(&configPath, "config", "", "configuration file (read control.socket when --socket is not set)")
	return command
}

func Execute(ctx context.Context, out, errOut io.Writer) error {
	return New(out, errOut).ExecuteContext(ctx)
}

func Logger(out io.Writer) *slog.Logger { return slog.New(slog.NewJSONHandler(out, nil)) }

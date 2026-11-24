package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/signal"
	"syscall"

	"github.com/storenth/ecmp-load-balancer/internal/balancer"
	"github.com/storenth/ecmp-load-balancer/internal/control"
	"github.com/storenth/ecmp-load-balancer/internal/proxy"
	"github.com/storenth/ecmp-load-balancer/internal/state"
	"github.com/spf13/cobra"
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
		ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		p := proxy.New(config.Listen.AddressPort(), config.Backends, balancer.NewRoundRobin(config.Backends), config.DialTimeout(), config.IdleTimeout())
		controlServer := control.Server{Path: socketPath, Stats: p}
		errs := make(chan error, 2)
		go func() { errs <- p.Run(ctx) }()
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
	command.Flags().StringVar(&socketPath, "socket", control.DefaultSocketPath, "control socket path")
	return command
}

func newStatus(out io.Writer) *cobra.Command {
	var socketPath string
	var asJSON bool
	command := &cobra.Command{Use: "status", Short: "Show load balancer status", RunE: func(cmd *cobra.Command, _ []string) error {
		status, err := control.GetStatus(cmd.Context(), socketPath)
		if err != nil {
			return err
		}
		if asJSON {
			encoder := json.NewEncoder(out)
			encoder.SetIndent("", "  ")
			return encoder.Encode(status)
		}
		_, err = fmt.Fprintf(out, "Uptime: %.0fs\nConnections: %d active, %d total\nBackends:\n", status.UptimeSeconds, status.ActiveConnections, status.TotalConnections)
		if err != nil {
			return err
		}
		for _, backend := range status.Backends {
			if _, err := fmt.Fprintf(out, "  %s:%d  %d active, %d total\n", backend.Host, backend.Port, backend.ActiveConnections, backend.TotalConnections); err != nil {
				return err
			}
		}
		return nil
	}}
	command.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	command.Flags().StringVar(&socketPath, "socket", control.DefaultSocketPath, "control socket path")
	return command
}

func Execute(ctx context.Context, out, errOut io.Writer) error {
	return New(out, errOut).ExecuteContext(ctx)
}

func Logger(out io.Writer) *slog.Logger { return slog.New(slog.NewJSONHandler(out, nil)) }

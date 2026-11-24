package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/storenth/slb-go/internal/balancer"
	"github.com/storenth/slb-go/internal/control"
	"github.com/storenth/slb-go/internal/logging"
	"github.com/storenth/slb-go/internal/proxy"
	"github.com/storenth/slb-go/internal/state"
)

// Version is the daemon version, overridable at link time.
var Version = "dev"

// Exit codes documented in docs/04-cli-and-ui.md.
const (
	exitOK          = 0
	exitError       = 1
	exitUsage       = 2
	exitUnreachable = 3
)

func New(out, errOut io.Writer) *cobra.Command {
	root := &cobra.Command{Use: "slb", SilenceUsage: true, SilenceErrors: true}
	root.SetOut(out)
	root.SetErr(errOut)
	root.AddCommand(newRun(errOut), newStatus(out, errOut))
	return root
}

func newRun(errOut io.Writer) *cobra.Command {
	var configPath, socketPath, logLevel, logFormat string
	command := &cobra.Command{Use: "run", Short: "Run the load balancer", RunE: func(cmd *cobra.Command, _ []string) error {
		logger, _, err := logging.New(logging.Options{
			FlagLevel:  logLevel,
			FlagFormat: logFormat,
			EnvLevel:   os.Getenv("SLB_LOG_LEVEL"),
			EnvFormat:  os.Getenv("SLB_LOG_FORMAT"),
			ErrOut:     errOut,
		})
		if err != nil {
			return err
		}
		slog.SetDefault(logger)

		config, err := (state.FileStateProvider{Path: configPath}).Load(cmd.Context())
		if err != nil {
			return err
		}
		if err := config.ValidateRuntime(); err != nil {
			return err
		}
		resolved, source := control.ResolveSocketPathWithSource(control.SocketOptions{
			Flag:   socketPath,
			Env:    os.Getenv("SLB_SOCKET"),
			Config: config.ControlSocket(),
		})
		logger.Info("starting slb",
			"version", Version,
			"config", configPath,
			"control_socket", resolved,
			"control_socket_source", string(source),
		)
		for _, service := range config.Services {
			logger.Info("service loaded",
				"service", service.Name,
				"listeners", formatListeners(service.Listeners),
				"backends", len(service.Backends),
				"affinity", service.Affinity,
			)
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		collection := proxy.Collection{Started: time.Now()}
		for _, service := range config.Services {
			p := proxy.NewWithLogger(service, balancer.NewRoundRobin(service.Backends), config.DialTimeout(), config.IdleTimeout(), logger)
			collection.Proxies = append(collection.Proxies, p)
		}
		controlServer := control.Server{Path: resolved, Stats: collection, Logger: logger}
		errs := make(chan error, 2)
		go func() { errs <- collection.Run(ctx, logger) }()
		go func() { errs <- controlServer.Run(ctx) }()
		err = <-errs
		stop()
		second := <-errs
		logger.Info("slb stopped")
		if err != nil {
			return err
		}
		return second
	}}
	command.Flags().StringVar(&configPath, "config", "config/dev.json", "configuration file")
	command.Flags().StringVar(&socketPath, "socket", "", "control socket path (overrides SLB_SOCKET and config control.socket)")
	command.Flags().StringVar(&logLevel, "log-level", "", "log level: debug, info, warn, error (default info)")
	command.Flags().StringVar(&logFormat, "log-format", "", "log format: text, json (default text on terminal, json otherwise)")
	return command
}

func formatListeners(listeners []state.Listener) string {
	parts := make([]string, 0, len(listeners))
	for _, l := range listeners {
		parts = append(parts, l.AddressPort()+"/"+l.Protocol)
	}
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return "[" + out + "]"
}

func newStatus(out io.Writer, _ io.Writer) *cobra.Command {
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
		resolved, source := control.ResolveSocketPathWithSource(control.SocketOptions{
			Flag:   socketPath,
			Env:    os.Getenv("SLB_SOCKET"),
			Config: configSocket,
		})
		status, err := control.GetStatus(cmd.Context(), resolved)
		if err != nil {
			return &daemonUnreachableError{path: resolved, source: source, cause: err}
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
				_, err = fmt.Fprintf(out, "  Backend:  %s:%d  %d active, %d total, %d failed\n", backend.Host, backend.Port, backend.ActiveConnections, backend.TotalConnections, backend.FailedConnections)
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

// daemonUnreachableError is returned when the CLI cannot reach the daemon.
type daemonUnreachableError struct {
	path   string
	source control.SocketSource
	cause  error
}

func (e *daemonUnreachableError) Error() string {
	return fmt.Sprintf("cannot reach the slb daemon at %s (%s). Is it running? Use --socket, SLB_SOCKET, or --config.", e.path, e.source)
}

func (e *daemonUnreachableError) Unwrap() error { return e.cause }

// Execute runs the root command. CLI errors are printed as "slb: <message>"
// to stderr with a documented exit code; slog is never used for user-facing
// errors.
func Execute(ctx context.Context, out, errOut io.Writer) error {
	cmd := New(out, errOut)
	err := cmd.ExecuteContext(ctx)
	if err == nil {
		return nil
	}
	var unreachable *daemonUnreachableError
	if errors.As(err, &unreachable) {
		printError(errOut, unreachable.Error())
		os.Exit(exitUnreachable)
		return nil
	}
	if isUsageError(err) {
		printError(errOut, err.Error())
		os.Exit(exitUsage)
		return nil
	}
	printError(errOut, err.Error())
	os.Exit(exitError)
	return nil
}

// printError writes "slb: <message>\n" to the given writer. It is a helper so
// that errcheck does not flag the unchecked fmt.Fprintf return value (there is
// nothing meaningful to do with a write error right before os.Exit).
func printError(w io.Writer, message string) {
	_, _ = fmt.Fprintf(w, "slb: %s\n", message)
}

// isUsageError reports whether err is a flag parse or unknown command error.
func isUsageError(err error) bool {
	var notExist *pflag.NotExistError
	if errors.As(err, &notExist) {
		return true
	}
	var valueRequired *pflag.ValueRequiredError
	if errors.As(err, &valueRequired) {
		return true
	}
	var invalidValue *pflag.InvalidValueError
	if errors.As(err, &invalidValue) {
		return true
	}
	var invalidSyntax *pflag.InvalidSyntaxError
	return errors.As(err, &invalidSyntax)
}

// Logger is retained for backward compatibility but should not be used for
// user-facing CLI errors.
func Logger(out io.Writer) *slog.Logger { return slog.New(slog.NewJSONHandler(out, nil)) }

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/buildinfo"
	"github.com/kuroky/claude-code-monitor/internal/config"
	"github.com/kuroky/claude-code-monitor/internal/dashboard"
	"github.com/kuroky/claude-code-monitor/internal/logging"
	"github.com/kuroky/claude-code-monitor/internal/otlp"
	"github.com/kuroky/claude-code-monitor/internal/pricing"
	"github.com/kuroky/claude-code-monitor/internal/stats"
	"github.com/kuroky/claude-code-monitor/internal/store"
	"github.com/kuroky/claude-code-monitor/internal/web"
)

const shutdownTimeout = 30 * time.Second

type commandStreams struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

type serverOptions struct {
	configPath    string
	skipIfRunning bool
	noUpdateCheck bool
}

func main() {
	streams := commandStreams{
		stdin:  os.Stdin,
		stdout: os.Stdout,
		stderr: os.Stderr,
	}
	if err := run(context.Background(), os.Args[1:], streams); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, streams commandStreams) error {
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		if _, err := fmt.Fprintln(streams.stdout, buildinfo.Version()); err != nil {
			return fmt.Errorf("write version: %w", err)
		}
		return nil
	}
	err := runServer(ctx, args, streams)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return err
}

func runServer(ctx context.Context, args []string, streams commandStreams) error {
	options, err := parseServerOptions(args, streams.stderr)
	if err != nil {
		return err
	}

	cfg, err := config.Load(options.configPath)
	if err != nil {
		return err
	}

	logging.Setup(cfg.Logging)

	// Hook integrations that want idempotent spawns must exit before the
	// startup update check performs any network access.
	if options.skipIfRunning && alreadyListening(cfg.Server.GRPCListen) {
		logSkipIfRunning(cfg.Server.GRPCListen)
		return nil
	}

	if startupUpdateEnabled(options, os.Getenv) {
		maybeApplyStartupUpdate(ctx, args, streams)
	}

	// Probe after the prompt because an instance that existed before the
	// update check may have exited while waiting for user confirmation.
	if alreadyListening(cfg.Server.GRPCListen) {
		if options.skipIfRunning {
			logSkipIfRunning(cfg.Server.GRPCListen)
			return nil
		}
		if err := stopExistingInstance(cfg.Server.GRPCListen, slog.Default()); err != nil {
			return fmt.Errorf("stop existing instance: %w", err)
		}
	}

	priceEngine, err := pricing.NewEngine(cfg.Pricing, slog.Default())
	if err != nil {
		return fmt.Errorf("init pricing engine: %w", err)
	}
	priceEngine.Start()
	defer priceEngine.Stop()

	db, err := store.Open(cfg.Storage)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			slog.Error("close store", "err", err)
		}
	}()

	migrations, err := store.LoadMigrations()
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	if err := store.RunMigrations(db.SQL, migrations); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	writer, err := store.NewBufferedWriter(db, cfg.Ingest, slog.Default())
	if err != nil {
		return fmt.Errorf("init buffered writer: %w", err)
	}
	writer.Start()

	statsSrv := stats.NewServer(cfg.Stats, writer, priceEngine, slog.Default())
	if webHandler, err := web.Handler(); err == nil {
		statsSrv.SetRootHandler(webHandler)
	} else {
		slog.Warn("web UI not mounted", "err", err)
	}
	dashHandler, err := dashboard.NewHandler(db.SQL, cfg.Dashboard, cfg.Pricing.Enabled, slog.Default())
	if err != nil {
		_ = writer.Stop()
		return fmt.Errorf("init dashboard handler: %w", err)
	}
	dashHandler.SetPriceLookup(priceEngine)
	statsSrv.SetAPIHandler(dashHandler)
	if err := statsSrv.Start(); err != nil {
		_ = writer.Stop()
		return fmt.Errorf("init stats server: %w", err)
	}

	srv, err := otlp.NewServer(cfg, slog.Default(), writer, priceEngine)
	if err != nil {
		_ = statsSrv.Shutdown(context.Background())
		_ = writer.Stop()
		return fmt.Errorf("init otlp server: %w", err)
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()

	slog.Info("server ready",
		"duckdb_path", cfg.Storage.DuckDBPath,
		"grpc_listen", cfg.Server.GRPCListen,
		"stats_listen", cfg.Stats.Listen,
		"capture_enabled", cfg.Capture.Enabled,
	)

	signalCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case <-signalCtx.Done():
		slog.Info("shutdown signal received")
		srv.Shutdown(shutdownTimeout)
	case err := <-serveErr:
		if err != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			_ = statsSrv.Shutdown(shutdownCtx)
			_ = writer.Stop()
			return fmt.Errorf("grpc server: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := statsSrv.Shutdown(shutdownCtx); err != nil {
		slog.Error("stats server shutdown", "err", err)
	}
	if err := writer.Stop(); err != nil {
		slog.Error("buffered writer stop", "err", err)
	}
	return nil
}

func parseServerOptions(args []string, stderr io.Writer) (serverOptions, error) {
	var options serverOptions
	flags := flag.NewFlagSet("claude-code-monitor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&options.configPath, "config", "./config.yaml", "path to YAML config file")
	flags.BoolVar(&options.skipIfRunning, "skip-if-running", false,
		"if another instance is already listening on grpc_listen, exit 0 instead of restarting it (used by SessionStart hooks)")
	flags.BoolVar(&options.noUpdateCheck, "no-update-check", false, "skip the startup GitHub release check")
	if err := flags.Parse(args); err != nil {
		return serverOptions{}, fmt.Errorf("parse flags: %w", err)
	}
	if flags.NArg() != 0 {
		return serverOptions{}, fmt.Errorf("unexpected command or argument %q", flags.Arg(0))
	}
	return options, nil
}

func startupUpdateEnabled(options serverOptions, getenv func(string) string) bool {
	return !options.noUpdateCheck && getenv("CLAUDE_CODE_MONITOR_NO_UPDATE_CHECK") != "1"
}

func logSkipIfRunning(grpcListen string) {
	slog.Info("another instance is listening; -skip-if-running set, exiting",
		"grpc_listen", grpcListen)
}

// alreadyListening reports whether something is already accepting TCP
// connections on the configured grpc_listen. When grpc_listen binds 0.0.0.0
// or :: we probe 127.0.0.1 since that is what a same-host duplicate would
// share with us. False on unparseable addresses (let downstream surface it).
func alreadyListening(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

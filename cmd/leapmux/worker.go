package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/internal/worker/hub"
)

func runWorker(args []string) error {
	fs := flag.NewFlagSet("worker", flag.ExitOnError)
	hubURL := fs.String("hub", "http://localhost:4327", "Hub server URL or unix:<socket-path>")
	dataDir := fs.String("data-dir", defaultWorkerDataDir(), "data directory")
	showVersion := fs.Bool("version", false, "print version and exit")
	_ = fs.Parse(args)

	if *showVersion {
		fmt.Println(version)
		return nil
	}

	logging.PrintBanner("worker", version, *hubURL)

	cfg := &config.Config{
		HubURL:  *hubURL,
		DataDir: *dataDir,
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Check if we already have credentials from a previous registration.
	state, err := cfg.LoadState()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	if state == nil || state.AuthToken == "" {
		// No saved credentials — need to register.
		hostname, _ := os.Hostname()
		result, err := hub.Register(ctx, cfg.HubURL, hostname, runtime.GOOS, runtime.GOARCH, version)
		if err != nil {
			return fmt.Errorf("registration: %w", err)
		}

		state = &config.State{
			WorkerID:  result.WorkerID,
			AuthToken: result.AuthToken,
			OrgID:     result.OrgID,
		}

		if err := cfg.SaveState(state); err != nil {
			return fmt.Errorf("save state: %w", err)
		}

		slog.Info("credentials saved", "path", cfg.StatePath())
	}

	slog.Info("starting worker",
		"worker_id", state.WorkerID,
		"hub", cfg.HubURL,
	)

	client := hub.New(cfg.HubURL, cfg.DataDir)
	defer client.Stop()

	client.OnDeregister = func() {
		slog.Info("worker deregistered by hub, clearing state and shutting down")
		_ = cfg.ClearState()
		stop()
	}

	client.ConnectWithReconnect(ctx, state.AuthToken)

	return nil
}

func defaultWorkerDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "leapmux", "worker")
	}
	return filepath.Join(home, ".config", "leapmux", "worker")
}

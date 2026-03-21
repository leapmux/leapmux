package main

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/leapmux/leapmux/hub"
	hubconfig "github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/internal/util/version"
)

func runHub(args []string) error {
	cfg, showVersion, err := hubconfig.Load(args)
	if err != nil {
		return err
	}

	if showVersion {
		fmt.Println(version.Value)
		return nil
	}

	level, err := logging.ParseLevel(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("invalid log level: %w", err)
	}
	logging.SetLevel(level)

	logging.PrintBanner("hub", version.Value, cfg.Addr)
	logging.PrintAccessURL("hub", cfg.Addr)

	server, err := hub.NewServer(cfg)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return server.Serve(ctx)
}

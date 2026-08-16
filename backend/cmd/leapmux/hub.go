package main

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/leapmux/leapmux/hub"
	hubconfig "github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/util/version"
)

func runHub(args []string) error {
	cfg, showVersion, err := hubconfig.Load(args)
	if err != nil {
		return err
	}

	if showVersion {
		fmt.Println(version.Format())
		return nil
	}

	level, err := logging.ParseLevel(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("invalid log level: %w", err)
	}
	logging.SetLevel(level)

	logging.PrintBanner("hub")

	server, err := hub.NewServer(cfg)
	if err != nil {
		return err
	}
	// Printed after the server exists so the URL is the one the hub will
	// actually use: public_url is a DB setting now, and the banner is the
	// one startup surface that shows the operator the canonical address.
	logging.PrintBannerURL(
		settings.KeyPublicURL.Of(server.SettingsManager().Snapshot(context.Background())),
		cfg.Listen)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return server.Serve(ctx)
}

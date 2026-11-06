package main

import (
	"context"
	"fmt"
	"os/signal"

	"github.com/leapmux/leapmux/hub"
	hubconfig "github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/util/procutil"
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

	// A long-lived daemon: a lost stderr (a dead tee, a closed terminal) must
	// return EPIPE from its log writes, not kill the process before the lease
	// is released.
	procutil.KeepRunningOnBrokenPipe()

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
	server.PrintBannerURL()

	ctx, stop := signal.NotifyContext(context.Background(), procutil.ShutdownSignals()...)
	defer stop()

	return server.Serve(ctx)
}

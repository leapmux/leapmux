package main

import (
	"context"
	"fmt"
	"os/signal"

	"github.com/leapmux/leapmux/hub"
	hubconfig "github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/lifecycle"
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

	// The hub's data lease: acquired before the server exists, so a
	// second hub over the same directory fails at the lease, before any
	// listener binds or store opens. NewServer takes its ownership.
	canonical, err := lifecycle.CanonicalDir(cfg.DataDir)
	if err != nil {
		return err
	}
	directory, err := lifecycle.AcquireDirectory(lifecycle.NewDirectoryOwner(lifecycle.RoleHub, canonical), lifecycle.RoleHub, canonical)
	if err != nil {
		return fmt.Errorf("acquire the hub data directory: %w", err)
	}
	server, err := hub.NewServer(cfg, directory)
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

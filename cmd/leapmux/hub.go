package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/leapmux/leapmux/hub"
	"github.com/leapmux/leapmux/internal/logging"
)

func runHub(args []string) error {
	fs := flag.NewFlagSet("hub", flag.ExitOnError)
	addr := fs.String("addr", ":4327", "listen address")
	dataDir := fs.String("data-dir", defaultHubDataDir(), "data directory")
	devFrontend := fs.String("dev-frontend", "", "Vite dev server URL for reverse proxy (dev mode only)")
	showVersion := fs.Bool("version", false, "print version and exit")
	_ = fs.Parse(args)

	if *showVersion {
		fmt.Println(version)
		return nil
	}

	logging.PrintBanner("hub", version, *addr)
	logging.PrintAccessURL(*addr)

	server, err := hub.NewServer(hub.ServerConfig{
		DataDir:     *dataDir,
		Addr:        *addr,
		DevFrontend: *devFrontend,
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return server.Serve(ctx)
}

func defaultHubDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "leapmux", "hub")
	}
	return filepath.Join(home, ".config", "leapmux", "hub")
}

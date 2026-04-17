package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"

	hubconfig "github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/solo"
	"github.com/leapmux/leapmux/util/version"
)

func runSolo(args []string, soloMode bool) error {
	for _, a := range args {
		if a == "-version" || a == "--version" {
			fmt.Println(version.Value)
			return nil
		}
	}

	modeName := "solo"
	defaultAddr := "127.0.0.1:4327"
	if !soloMode {
		modeName = "dev"
		defaultAddr = ":4327"
	}
	configDir := "~/.config/leapmux/" + modeName
	configFile := configDir + "/" + modeName + ".yaml"

	cliFlags := []string{
		"addr", "data-dir", "dev-frontend",
		"storage-sqlite-max-conns", "max-message-size", "max-incomplete-chunked",
		"api-timeout-seconds", "agent-startup-timeout-seconds", "worktree-create-timeout-seconds",
		"log-level",
	}
	extraFlags := []hubconfig.ExtraFlagDef{
		{Name: "encryption-mode", KoanfKey: "encryption_mode", Usage: "encryption mode (classic, post-quantum)", StrDefault: "post-quantum"},
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	inst, err := solo.Start(ctx, solo.Config{
		Addr:       defaultAddr,
		ConfigDir:  configDir,
		ConfigFile: configFile,
		Args:       args,
		CLIFlags:   cliFlags,
		ExtraFlags: extraFlags,
		DevMode:    !soloMode,
	})
	if err != nil {
		return err
	}
	defer inst.Stop()

	// Exit when the user signals or the Hub errors out.
	hubExited := make(chan error, 1)
	go func() { hubExited <- inst.Wait() }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-hubExited:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

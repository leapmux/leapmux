//go:build windows

package main

import (
	"context"
	"os"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/internal/servicelaunch"
)

func runNativeServiceEntry(args []string) (bool, error) {
	name, _, command, err := servicelaunch.ParseNativeServiceEntry(args)
	if err != nil {
		return true, err
	}
	if name == "" {
		return false, nil
	}
	return true, servicelaunch.RunWindowsService(name, func(ctx context.Context, ready func()) error {
		logging.Setup()
		// The service runs `leapmux daemon <mode> run` through the command tree, so
		// it parses its saved arguments as a person's command line would.
		runners := defaultCLIRunners()
		runners.modes.foreground = runInvocation{Context: ctx, OnReady: ready, ServiceName: name}
		return cli.Run(ctx, newRootCommand(runners), command, os.Stdin, os.Stdout, os.Stderr)
	})
}

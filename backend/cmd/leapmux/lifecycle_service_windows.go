//go:build windows

package main

import (
	"context"
	"fmt"
	"regexp"

	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/internal/servicelaunch"
	"github.com/leapmux/leapmux/lifecycle"
)

func runNativeServiceEntry(args []string) (bool, error) {
	if len(args) == 0 || args[0] != servicelaunch.WindowsServiceEntryFlag {
		return false, nil
	}
	if len(args) < 4 || !regexp.MustCompile(`^leapmux-(hub|worker|solo|dev)-[0-9a-f]{16}$`).MatchString(args[1]) || args[3] != "run" {
		return true, fmt.Errorf("invalid Windows service entry")
	}
	role, err := lifecycle.ParseRole(args[2])
	if err != nil {
		return true, err
	}
	name := args[1]
	return true, servicelaunch.RunWindowsService(name, func(ctx context.Context, ready func()) error {
		logging.Setup()
		return runModeRunWithInvocation(role.String(), runInvocation{Context: ctx, OnReady: ready, ServiceName: name})(cmdCtx{}, args[4:])
	})
}

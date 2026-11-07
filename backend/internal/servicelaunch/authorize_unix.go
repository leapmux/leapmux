//go:build darwin || linux

package servicelaunch

import (
	"context"
	"os"
	"os/exec"

	"github.com/leapmux/leapmux/lifecycle"
)

func authorizeNativeStart(ctx context.Context, spec LaunchSpec) error {
	return authorizeNativeStartWith(ctx, spec, func(ctx context.Context) error {
		command := exec.CommandContext(ctx, "/usr/bin/sudo", "-v")
		command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
		return command.Run()
	})
}

// authorizeNativeStartWith validates runtime inputs before requesting credentials.
func authorizeNativeStartWith(ctx context.Context, spec LaunchSpec, authorize func(context.Context) error) error {
	if spec.Startup != lifecycle.StartupBoot {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateLaunchSpec(spec); err != nil {
		return err
	}
	if _, err := spec.LoadEnvironment(); err != nil {
		return err
	}
	return authorize(ctx)
}

//go:build darwin || linux

package servicelaunch

import (
	"context"
	"os"
	"os/exec"

	"github.com/leapmux/leapmux/lifecycle"
)

func authorizeNativeStart(ctx context.Context, spec LaunchSpec) error {
	if spec.Startup != lifecycle.StartupBoot {
		return nil
	}
	command := exec.CommandContext(ctx, "/usr/bin/sudo", "-v")
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	return command.Run()
}

//go:build darwin || linux

package servicelaunch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
	"github.com/leapmux/leapmux/lifecycle"
)

type privilegedDefinitionCommand func(context.Context, []string) error

func runPrivilegedDefinitionCommand(ctx context.Context, args []string) error {
	command := exec.CommandContext(ctx, "/usr/bin/sudo", args...)
	command.Stdin = os.Stdin
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil {
		return fmt.Errorf("authorize the native definition change: %w: %s", err, output.String())
	}
	return nil
}

func publishNativeDefinition(ctx context.Context, spec LaunchSpec, path string, data []byte) error {
	return publishNativeDefinitionWith(ctx, spec, path, data, runPrivilegedDefinitionCommand)
}

func publishNativeDefinitionWith(ctx context.Context, spec LaunchSpec, path string, data []byte, run privilegedDefinitionCommand) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if spec.Startup == lifecycle.StartupLogin {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return atomicfile.WriteFileDurable(path, data, 0o644)
	}
	if err := validateSystemDefinitionPath(spec, path); err != nil {
		return err
	}
	// Stage under the instance's private directory. Administrator
	// authorization applies only to publication of this native definition.
	stage := filepath.Join(lifecycle.PathsOf(spec.DataDirectory).Dir, "native-definition")
	if err := atomicfile.WriteFileDurable(stage, data, 0o600); err != nil {
		return err
	}
	defer func() { result = errors.Join(result, os.Remove(stage)) }()
	if err := run(ctx, []string{"/usr/bin/install", "-m", "0644", "-o", "0", "-g", "0", stage, path + ".new"}); err != nil {
		return err
	}
	// The complete root-owned file replaces the definition in one rename.
	// An interrupted publication leaves only this instance's .new file,
	// which the next repair replaces before trying the rename again.
	return run(ctx, []string{"/bin/mv", "-f", "--", path + ".new", path})
}

func removeNativeDefinition(ctx context.Context, spec LaunchSpec, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if spec.Startup == lifecycle.StartupLogin {
		return os.Remove(path)
	}
	if err := validateSystemDefinitionPath(spec, path); err != nil {
		return err
	}
	return runPrivilegedDefinitionCommand(ctx, []string{"/bin/rm", "-f", "--", path})
}

func validateSystemDefinitionPath(spec LaunchSpec, path string) error {
	// Removal must remain possible when the provider environment file is
	// missing. Its contents do not participate in native definition identity.
	identity := spec
	identity.EnvironmentFile = nil
	if err := ValidateLaunchSpec(identity); err != nil {
		return err
	}
	if spec.Startup != lifecycle.StartupBoot {
		return fmt.Errorf("system definition changes require boot startup")
	}
	mac := filepath.Join("/Library/LaunchDaemons", "com.leapmux."+spec.Mode.String()+"."+spec.InstanceID+".plist")
	linux := filepath.Join("/etc/systemd/system", "leapmux-"+spec.Mode.String()+"-"+spec.InstanceID+".service")
	if path != mac && path != linux {
		return fmt.Errorf("the system definition path does not match this instance")
	}
	return nil
}

package zcode

import (
	"context"
	"fmt"
	"os/user"
	"runtime"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

type zcodeAccountEnvironment struct {
	home                      string
	credentials               zcodeCredentialStore
	builtinProviderConfigPath string
}

// resolveZCodeAccountEnvironment reads account files with the home directory and
// credential secret that the finalized environment and shell profile select.
func resolveZCodeAccountEnvironment(ctx context.Context, opts agent.Options, environment []string) (zcodeAccountEnvironment, error) {
	homeVariable := "HOME"
	if runtime.GOOS == "windows" {
		homeVariable = "USERPROFILE"
	}
	ctx, cancel := context.WithTimeout(ctx, opts.EffectiveStartupTimeout())
	defer cancel()
	snapshot, result := launch.ShellEnv(ctx, launch.ShellEnvOptions{
		Shell: opts.Shell, LoginShell: opts.LoginShell, WorkingDir: opts.WorkingDir,
		Env: environment, Variables: []string{homeVariable, zcodeCredentialSecretEnv},
		PathVariables: map[string]string{zcodeBuiltinProviderConfigEnv: zcodeJSWhitespaceChars},
	})
	if result != launch.ProbeYes {
		if err := ctx.Err(); err != nil {
			return zcodeAccountEnvironment{}, fmt.Errorf("read the ZCode account environment: %w", err)
		}
		return zcodeAccountEnvironment{}, fmt.Errorf("the ZCode shell did not report its account environment")
	}
	values := snapshot.Values
	home := values[homeVariable]
	if home == "" {
		home = opts.HomeDir
		if current, err := user.Current(); err == nil && current.HomeDir != "" {
			home = current.HomeDir
		}
	}
	return zcodeAccountEnvironment{
		home:                      home,
		credentials:               newZCodeCredentialStore(home, func(key string) string { return values[key] }),
		builtinProviderConfigPath: snapshot.Paths[zcodeBuiltinProviderConfigEnv],
	}, nil
}

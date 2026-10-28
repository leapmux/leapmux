//go:build unix

package fastagent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// fast-agent checks PyPI for a newer release, and prints a notice, unless the
// root option `--no-update-check` says otherwise. It has no environment variable
// and no configuration key for this. Typer reads the option only before the
// subcommand: `acp` declares `ignore_unknown_options`, so the same option after
// `acp` is swallowed and stops nothing. The `acp` mode never runs the check
// today, so the option is the only guard against a later release that does.
func TestStartPassesTheUpdateCheckOptionBeforeTheSubcommand(t *testing.T) {
	runs := agenttest.RunLaunch(t, agenttest.LaunchProbe{Binary: "fast-agent", Start: Start})
	require.Len(t, runs, 1)
	option := slices.Index(runs[0].Args, "--no-update-check")
	subcommand := slices.Index(runs[0].Args, "acp")
	require.NotEqual(t, -1, option, "the launch must pass --no-update-check")
	require.NotEqual(t, -1, subcommand, "the launch must run the acp subcommand")
	assert.Less(t, option, subcommand, "the option must come before the subcommand")
}

func TestFastAgentBypassLaunchArguments(t *testing.T) {
	for _, tc := range []struct {
		name   string
		value  string
		resume string
		want   bool
	}{
		{name: "default"},
		{name: "off", value: "false"},
		{name: "on", value: "true", want: true},
		{name: "resumed on", value: "true", resume: "native-session", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runs := agenttest.RunLaunch(t, agenttest.LaunchProbe{Binary: "fast-agent", Start: func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
				opts.Options = map[string]string{"model": "gpt-4o", "bypassPermissions": tc.value}
				opts.ResumeSessionID = tc.resume
				return Start(ctx, opts, sink)
			}})
			require.Len(t, runs, 1)
			args := runs[0].Args
			wantArgs := []string{"--no-update-check", "acp", "--model", "gpt-4o", "-x", "--subagents"}
			if tc.want {
				wantArgs = append(wantArgs, "--no-permissions")
			}
			assert.Equal(t, wantArgs, args)
			assert.Less(t, slices.Index(args, "--no-update-check"), slices.Index(args, "acp"))
			assert.Contains(t, args, "--subagents")
			assert.Contains(t, args, "gpt-4o")
			if tc.want {
				assert.Greater(t, slices.Index(args, "--no-permissions"), slices.Index(args, "acp"))
			}
		})
	}
}

func TestFastAgentBypassInvalidStartupDoesNotLaunch(t *testing.T) {
	probe := agenttest.NewEnvProbe(t, "fast-agent", nil)
	t.Setenv("PATH", filepath.Dir(probe.Program)+string(os.PathListSeparator)+os.Getenv("PATH"))
	started, err := Start(t.Context(), agent.Options{
		AgentID: "invalid-policy", WorkingDir: t.TempDir(), HomeDir: t.TempDir(), Shell: "/bin/sh",
		Options: map[string]string{"bypassPermissions": "yes"},
	}, agent.NewProviderServices(&agenttest.Sink{}))
	if started != nil {
		t.Cleanup(func() { started.Stop(); _ = started.Wait() })
	}
	require.ErrorContains(t, err, "bypassPermissions")
	assert.Nil(t, started)
	assert.Empty(t, probe.Runs(t, 0), "invalid input cannot start a native process")
}

func TestFastAgentNativeCommandsInvalidStartupDoesNotLaunch(t *testing.T) {
	probe := agenttest.NewEnvProbe(t, "fast-agent", nil)
	t.Setenv("PATH", filepath.Dir(probe.Program)+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, options := range []map[string]string{
		{"nativeCommands": "true"},
		{"nativeCommands": "true", "bypassPermissions": "false"},
		{"nativeCommands": "yes", "bypassPermissions": "true"},
	} {
		started, err := Start(t.Context(), agent.Options{AgentID: "invalid-native", WorkingDir: t.TempDir(), HomeDir: t.TempDir(), Shell: "/bin/sh", Options: options}, agent.NewProviderServices(&agenttest.Sink{}))
		if started != nil {
			started.Stop()
			_ = started.Wait()
		}
		require.Error(t, err)
		assert.Nil(t, started)
	}
	assert.Empty(t, probe.Runs(t, 0))
}

//go:build unix

package codebuddy

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir/agentdirtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CodeBuddy Code updates its own install unless a truthy DISABLE_AUTOUPDATER
// says otherwise (BooleanUtils.isTruthy). The `-p` mode that the worker starts
// skips the updater today, but a daemon (CODEBUDDY_SESSION_KIND=daemon) runs it,
// so the launch pins the switch off after the user's profile.
func TestStartPinsTheUpdaterOff(t *testing.T) {
	runs := agenttest.RequireLaunchPins(t, agenttest.LaunchProbe{
		Binary: "codebuddy", Start: Start, Pins: []string{"DISABLE_AUTOUPDATER=1"},
	})
	for _, run := range runs {
		assert.Contains(t, run.Args, "--include-partial-messages")
	}
}

// Windows PowerShell 5.1, and pwsh in its Legacy argument passing, pass each
// embedded double quote to a native program unescaped. The startup settings
// still reach CodeBuddy. A profile selects the mode: on Unix, pwsh reads its
// profile from XDG_CONFIG_HOME, which the test controls. The profile also
// reports the mode, so the helper fails when pwsh stops reading this profile.
func TestCodebuddyStartupSettingsSurvivePowerShellLegacyArgumentPassing(t *testing.T) {
	for _, baseName := range []string{"plain", `a path with a "quote"`, "a path with ‘ ’ ‚ ‛"} {
		for _, swarm := range []bool{false, true} {
			t.Run(baseName+"/swarm="+strconv.FormatBool(swarm), func(t *testing.T) {
				shell, home := testutil.RequirePowerShellArgumentPassing(t, "Legacy", codebuddyArgumentPassingEnv)
				scenario := "legacy-style"
				if swarm {
					scenario = "legacy-swarm-style"
				}
				useStartupHelper(t, scenario)

				base := filepath.Join(t.TempDir(), baseName)
				require.NoError(t, os.MkdirAll(base, 0o700))
				dirs := agentdirtest.NewDirs(t, []agentdir.Spec{agentDirSpec()}, base)

				started, err := Start(t.Context(), agent.Options{
					Options: map[string]string{OptionOutputStyle: "Concise", OptionSwarmMode: strconv.FormatBool(swarm)},
					AgentID: "codebuddy-legacy-arguments", WorkingDir: t.TempDir(), HomeDir: home, Shell: shell,
					ExtraEnv:  []string{"HOME=" + home, "XDG_CONFIG_HOME=" + home},
					AgentDirs: dirs,
				}, agent.NewProviderServices(&agenttest.Sink{}))
				if started != nil {
					t.Cleanup(func() {
						started.Stop()
						_ = started.Wait()
					})
				}
				require.NoError(t, err)
				assert.Equal(t, "Concise", started.(*Agent).SettingsSnapshot().SurfacedOptions[OptionOutputStyle])
			})
		}
	}
}

func TestCodebuddySwarmLaunchArguments(t *testing.T) {
	for _, tc := range []struct {
		value, resume string
		want          bool
	}{
		{}, {value: "false"}, {value: "true", want: true}, {value: "true", resume: "existing-session", want: true},
	} {
		runs := agenttest.RunLaunch(t, agenttest.LaunchProbe{Binary: "codebuddy", Start: func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
			opts.Options = map[string]string{"swarmMode": tc.value}
			opts.ResumeSessionID = tc.resume
			return Start(ctx, opts, sink)
		}})
		require.Len(t, runs, 1)
		assert.Equal(t, tc.want, slices.Contains(runs[0].Args, "--swarm"))
	}
}

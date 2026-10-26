//go:build unix

package codebuddy

import (
	"os"
	"path/filepath"
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
		t.Run(baseName, func(t *testing.T) {
			shell, home := testutil.RequirePowerShellArgumentPassing(t, "Legacy", codebuddyArgumentPassingEnv)
			useStartupHelper(t, "legacy-style")

			base := filepath.Join(t.TempDir(), baseName)
			require.NoError(t, os.MkdirAll(base, 0o700))
			dirs := agentdirtest.NewDirs(t, []agentdir.Spec{agentDirSpec()}, base)

			started, err := Start(t.Context(), agent.Options{
				Options: map[string]string{OptionOutputStyle: "Concise"},
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

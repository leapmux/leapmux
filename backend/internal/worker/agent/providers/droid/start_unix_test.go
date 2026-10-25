//go:build unix

package droid

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// Factory Droid downloads a release and replaces its own executable unless
// FACTORY_DROID_AUTO_UPDATE_ENABLED is exactly "0" or "false" (any other value
// takes the default of the build, which is off in the npm build and may be on in
// another). The `droid exec` mode that the worker starts never reaches the
// updater, but a `droid` that the agent's own tool starts inherits the
// environment, so the launch pins the switch off after the user's profile.
func TestStartPinsTheUpdaterOff(t *testing.T) {
	agenttest.RequireLaunchPins(t, agenttest.LaunchProbe{
		Binary: droidBinaryName, Start: Start, Pins: []string{"FACTORY_DROID_AUTO_UPDATE_ENABLED=0"},
	})
}

func TestStartUsesNativeAutonomyWithoutPermissionFlags(t *testing.T) {
	for _, mode := range []string{"", contracts.DroidModeDefault, contracts.DroidModeSpec, contracts.DroidModeAutoLow, contracts.DroidModeAutoMedium, contracts.DroidModeAutoHigh} {
		t.Run(mode, func(t *testing.T) {
			runs := agenttest.RunLaunch(t, agenttest.LaunchProbe{Binary: droidBinaryName, Start: func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
				opts.Options = map[string]string{agent.OptionIDPermissionMode: mode}
				return Start(ctx, opts, sink)
			}})
			require.Len(t, runs, 1)
			args := runs[0].Args
			require.GreaterOrEqual(t, len(args), 5)
			assert.Equal(t, []string{"exec", "--input-format", "stream-jsonrpc", "--output-format", "stream-jsonrpc"}, args[:5])
			assert.NotContains(t, args, "--skip-permissions-unsafe", "the process flag must not override a later native autonomy change")
			assert.NotContains(t, args, "--auto", "stream-jsonrpc takes its autonomy from initialize_session")
		})
	}
}

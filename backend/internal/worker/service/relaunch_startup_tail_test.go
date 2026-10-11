//go:build unix

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// The startup of an open ends its registry entry before its tail. The tail can
// relaunch the process for a setting that changed during the startup. That
// relaunch holds the id of its agent, and the goroutine of the open is behind the
// hold. A close that lands during the relaunch finds the hold. The goroutine must
// still learn about the close from its own entry. A close stamps the row only
// after it stops the process. A tail that reads the row too early misses the
// close, and it leaves the new process running for a closed tab.
func TestRelaunchForStartupSettingsChange_ACloseDuringTheRelaunchReachesTheStartup(t *testing.T) {
	t.Parallel()

	rig := newProcessChangeRig(t)
	row := rig.seedRunning()
	open := rig.svc.AgentStartup.begin(rig.id, func() {})
	require.NotNil(t, open)
	// runAgentStartup ends its entry before the tail, and it calls finishEntry when it returns.
	rig.svc.AgentStartup.succeed(rig.id, open)
	launch := rig.svc.startAgentFn
	rig.svc.startAgentFn = func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		rig.svc.AgentStartup.cancelAndClear(rig.id, removeWorktreeOnClose)
		return launch(ctx, opts, sink)
	}
	opts := rig.svc.baseAgentOptions(rig.id, row.WorkingDir, row.AgentProvider)
	opts.Options = OptionMap{agent.OptionIDModel: "opus"}

	_, running := rig.svc.relaunchForStartupSettingsChange(rig.id, row.AgentProvider, opts, row)

	require.True(t, running)
	disposition, raced := rig.svc.AgentStartup.dispositionOf(open)
	assert.True(t, raced, "the goroutine of the open must learn that a close landed during its relaunch")
	assert.Equal(t, removeWorktreeOnClose, disposition)
	rig.svc.AgentStartup.finishEntry(open)
	requireStartupsReleased(t, &rig.svc.AgentStartup.startupCore)
}

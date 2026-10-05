package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

const holdTestProvider = leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE

// A hold registers the replacement as a startup in flight, with the label of a
// restart, and gives the entry back when the replacement ends.
func TestHoldRelaunch_ClaimsTheStartupUntilItIsReleased(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)

	hold := svc.holdRelaunch("agent-1", holdTestProvider)

	status, _, message, tracked := svc.AgentStartup.status("agent-1")
	require.True(t, tracked, "a replacement must register before it stops the old process")
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, status)
	assert.Equal(t, "Restarting Claude Code…", message)

	hold.release()

	_, _, _, tracked = svc.AgentStartup.status("agent-1")
	assert.False(t, tracked, "the agent must derive its status from the process again")
	requireStartupsReleased(t, &svc.AgentStartup.startupCore)
}

// A hold that the registry refused owns nothing. Its release and its settle must
// leave the entry of the holder alone, because that holder reports the agent as
// STARTING or STARTUP_FAILED and ends its own entry.
func TestHoldRelaunch_RefusedHoldLeavesTheHolderAlone(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		// hold takes the id the way the holder does, and returns what to release.
		hold       func(svc *Service) (end func())
		wantStatus leapmuxv1.AgentStatus
	}{
		{"a startup in flight", func(svc *Service) func() {
			handle := svc.AgentStartup.begin("agent-1", func() {})
			require.NotNil(t, handle)
			return func() { svc.AgentStartup.abandon(handle) }
		}, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING},
		{"a startup that failed", func(svc *Service) func() {
			handle := svc.AgentStartup.begin("agent-1", func() {})
			require.NotNil(t, handle)
			svc.AgentStartup.fail(handle, "claude: command not found")
			svc.AgentStartup.finishEntry(handle)
			return func() { svc.AgentStartup.cancelAndClear("agent-1", keepWorktreeOnClose) }
		}, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, _, _ := setupTestService(t)
			end := tc.hold(svc)

			refused := svc.holdRelaunch("agent-1", holdTestProvider)
			refused.release()
			refused.settle()

			status, _, _, tracked := svc.AgentStartup.status("agent-1")
			require.True(t, tracked, "a refused hold must not remove the entry of the holder")
			assert.Equal(t, tc.wantStatus, status)
			end()
			requireStartupsReleased(t, &svc.AgentStartup.startupCore)
		})
	}
}

// A close holds the id, and a replacement must not start a process for a tab that
// is going away.
func TestHoldRelaunch_RefusesWhileACloseHoldsTheID(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	releaseClose := svc.AgentStartup.holdCloseAdmission("agent-1")
	defer releaseClose()

	hold := svc.holdRelaunch("agent-1", holdTestProvider)
	hold.release()

	_, _, _, tracked := svc.AgentStartup.status("agent-1")
	assert.False(t, tracked, "a replacement must not register while a close holds the id")
	requireStartupsReleased(t, &svc.AgentStartup.startupCore)
}

// A close retires the entry of the startup that it finds, without waiting for the
// holder. The holder's release must then leave the registry consistent and the
// in-flight count at zero, and it must not remove an entry that a later startup
// claimed for the same id.
func TestHoldRelaunch_ReleaseAfterACloseRetiredTheEntry(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	hold := svc.holdRelaunch("agent-1", holdTestProvider)
	svc.AgentStartup.cancelAndClear("agent-1", keepWorktreeOnClose)
	later := svc.AgentStartup.begin("agent-1", func() {})
	require.NotNil(t, later)

	hold.release()

	_, _, _, tracked := svc.AgentStartup.status("agent-1")
	assert.True(t, tracked, "the release of a retired hold must not remove the startup that claimed the id later")
	svc.AgentStartup.abandon(later)
	requireStartupsReleased(t, &svc.AgentStartup.startupCore)
}

// The zero value holds nothing, so a caller that was refused ends its change
// with the same calls as a caller that was admitted.
func TestHoldRelaunch_ZeroValueIsInert(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	var hold relaunchHold

	assert.NotPanics(t, hold.release)
	assert.NotPanics(t, hold.settle)
	requireStartupsReleased(t, &svc.AgentStartup.startupCore)
}

// A hold must not change what a refresh of the new process persists. The startup
// window defers that write to a handoff, and a replacement has none for the paths
// that persist nothing after the launch, so the write must land as it did before a
// replacement held an entry. An open still defers it. The wiring is the real one,
// through the Service.
func TestPersistSettingsRefresh_WritesDuringAReplacementAndDefersDuringAnOpen(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		claim     func(svc *Service) (end func())
		wantModel string
	}{
		{"a process replacement", func(svc *Service) func() {
			hold := svc.holdRelaunch("agent-1", holdTestProvider)
			require.NotNil(t, hold.handle)
			return hold.release
		}, "sonnet"},
		{"an open", func(svc *Service) func() {
			handle := svc.AgentStartup.begin("agent-1", func() {})
			require.NotNil(t, handle)
			return func() { svc.AgentStartup.abandon(handle) }
		}, "opus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newRefreshTestFixture(t, settingsSeed{Model: "opus", Effort: "auto", PermissionMode: "default"})
			end := tc.claim(f.svc)
			defer end()

			f.sink.PersistSettingsRefresh(map[string]string{
				agent.OptionIDModel:          "sonnet",
				agent.OptionIDEffort:         "auto",
				agent.OptionIDPermissionMode: "default",
			})

			row, err := f.svc.Queries.GetAgentByID(context.Background(), "agent-1")
			require.NoError(t, err)
			assert.Equal(t, tc.wantModel, parseOptions(row.Options)[agent.OptionIDModel])
		})
	}
}

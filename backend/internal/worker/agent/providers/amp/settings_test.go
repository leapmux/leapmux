package amp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func groupByID(t *testing.T, groups []*leapmuxv1.AvailableOptionGroup, id string) *leapmuxv1.AvailableOptionGroup {
	t.Helper()
	for _, group := range groups {
		if group.GetId() == id {
			return group
		}
	}
	require.Failf(t, "missing option group", "no group %q", id)
	return nil
}

func optionIDs(group *leapmuxv1.AvailableOptionGroup) []string {
	ids := make([]string, 0, len(group.GetOptions()))
	for _, option := range group.GetOptions() {
		ids = append(ids, option.GetId())
	}
	return ids
}

func TestLaunchModes(t *testing.T) {
	t.Parallel()
	options := func(values map[string]string) agent.Options { return agent.Options{Options: optionmap.Map(values)} }

	assert.Equal(t, agentModeMedium, launchAgentMode(options(nil)), "Amp's default mode")
	assert.Equal(t, agentModeUltra, launchAgentMode(options(map[string]string{contracts.AmpOptionAgentMode: agentModeUltra})))
	assert.Equal(t, agentModeMedium, launchAgentMode(options(map[string]string{contracts.AmpOptionAgentMode: "deep"})),
		"a mode Amp does not have falls back to the default")

	assert.Equal(t, contracts.AmpPermissionModeAsk, launchPermissionMode(options(nil)), "no session opens with the checks off")
	assert.Equal(t, contracts.AmpPermissionModeAllowAll,
		launchPermissionMode(options(map[string]string{agent.OptionIDPermissionMode: contracts.AmpPermissionModeAllowAll})))
	assert.Equal(t, contracts.AmpPermissionModeAsk,
		launchPermissionMode(options(map[string]string{agent.OptionIDPermissionMode: "bypassPermissions"})))
}

func TestFastModeBeforeFirstMessage(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withOptions(map[string]string{"fastMode": "true", contracts.AmpOptionAgentMode: agentModeHigh}))
	group := groupByID(t, h.agent.OptionGroups(), "fastMode")
	assert.True(t, group.Mutable)
	assert.Equal(t, "true", group.CurrentValue)
	assert.Equal(t, []string{"false", "true"}, optionIDs(group))
	result := h.agent.UpdateSettings(map[string]string{"fastMode": "false"})
	assert.Equal(t, "false", result.ConfirmedOptions()["fastMode"])
	assert.Equal(t, agentModeHigh, result.ConfirmedOptions()[contracts.AmpOptionAgentMode])
	assert.Equal(t, "false", h.sink.LastSettingsRefresh().Options["fastMode"])
}

func TestFastModeLocksWithTheNativeThread(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withOptions(map[string]string{"fastMode": "true"}))
	h.send("start the thread")
	group := groupByID(t, h.agent.OptionGroups(), "fastMode")
	assert.False(t, group.Mutable)
	assert.Equal(t, "true", group.CurrentValue)
	assert.NotEmpty(t, group.ReadOnlyReason)
	result := h.agent.UpdateSettings(map[string]string{"fastMode": "false"})
	assert.Equal(t, "true", result.ConfirmedOptions()["fastMode"])
}

func TestOptionGroupsBeforeTheFirstMessage(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withOptions(map[string]string{contracts.AmpOptionAgentMode: agentModeHigh}))
	groups := h.agent.OptionGroups()
	require.Len(t, groups, 3)

	mode := groupByID(t, groups, contracts.AmpOptionAgentMode)
	assert.True(t, mode.GetMutable())
	assert.Equal(t, AgentModeLabel, mode.GetLabel())
	assert.Equal(t, []string{agentModeLow, agentModeMedium, agentModeHigh, agentModeUltra}, optionIDs(mode))
	assert.Equal(t, agentModeHigh, mode.GetCurrentValue())
	assert.Equal(t, agentModeMedium, mode.GetDefaultValue())
	assert.Empty(t, mode.GetReadOnlyReason(), "a group the reader can change states no read-only reason")

	permission := groupByID(t, groups, agent.OptionIDPermissionMode)
	assert.True(t, permission.GetMutable())
	assert.Equal(t, PermissionModeLabel, permission.GetLabel())
	assert.Equal(t, []string{contracts.AmpPermissionModeAsk, contracts.AmpPermissionModeAllowAll}, optionIDs(permission))
	assert.Equal(t, contracts.AmpPermissionModeAsk, permission.GetCurrentValue())
}

// After the first message the thread keeps its mode, so the group shows that
// mode alone, read-only, and says why.
func TestOptionGroupsAfterTheFirstMessageFixTheMode(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withOptions(map[string]string{contracts.AmpOptionAgentMode: agentModeUltra}))
	h.send("go")

	mode := groupByID(t, h.agent.OptionGroups(), contracts.AmpOptionAgentMode)
	assert.False(t, mode.GetMutable())
	require.Len(t, mode.GetOptions(), 1)
	assert.Equal(t, agentModeUltra, mode.GetOptions()[0].GetId())
	assert.Equal(t, "Ultra", mode.GetOptions()[0].GetName())
	assert.Equal(t, "Hard, open-ended work across many files or systems", mode.GetOptions()[0].GetDescription(), "the option keeps its own description")
	assert.Equal(t, lockedModeNote, mode.GetReadOnlyReason(), "the group says why it cannot change")
	assert.Equal(t, agentModeUltra, mode.GetCurrentValue())
	assert.Equal(t, agentModeUltra, mode.GetDefaultValue())
	assert.Equal(t, agentModeGroup.GetOrder(), mode.GetOrder())

	permission := groupByID(t, h.agent.OptionGroups(), agent.OptionIDPermissionMode)
	assert.True(t, permission.GetMutable(), "the permission mode still changes live")
	assert.Empty(t, permission.GetReadOnlyReason())
}

func TestUpdateSettingsBeforeTheFirstMessage(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	result := h.agent.UpdateSettings(map[string]string{
		contracts.AmpOptionAgentMode: agentModeLow,
		agent.OptionIDPermissionMode: contracts.AmpPermissionModeAllowAll,
	})
	assert.Equal(t, agentModeLow, result.ConfirmedOptions().Get(contracts.AmpOptionAgentMode))
	assert.Equal(t, contracts.AmpPermissionModeAllowAll, result.ConfirmedOptions().Get(agent.OptionIDPermissionMode))

	refresh := h.sink.LastSettingsRefresh()
	assert.Equal(t, agentModeLow, refresh.Options[contracts.AmpOptionAgentMode])
	assert.Equal(t, contracts.AmpPermissionModeAllowAll, refresh.PermissionMode)

	h.send("go")
	assert.Equal(t, []string{""}, h.startedThreads(), "a mode change before the first message needs no restart")
	assert.Equal(t, agentModeLow, h.agent.agentMode)
}

// A request for another mode after the first message changes nothing, and the
// snapshot confirms the mode the thread keeps.
func TestUpdateSettingsAfterTheFirstMessageKeepsTheMode(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.send("go")
	result := h.agent.UpdateSettings(map[string]string{contracts.AmpOptionAgentMode: agentModeUltra})
	assert.Equal(t, agentModeMedium, result.ConfirmedOptions().Get(contracts.AmpOptionAgentMode))
	assert.Equal(t, []string{""}, h.startedThreads(), "no new thread starts in silence")
}

func TestUpdateSettingsIgnoresUnknownValues(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	result := h.agent.UpdateSettings(map[string]string{
		contracts.AmpOptionAgentMode: "deep",
		agent.OptionIDPermissionMode: "plan",
		OptionFastMode:               "invalid",
	})
	assert.Equal(t, agentModeMedium, result.ConfirmedOptions().Get(contracts.AmpOptionAgentMode))
	assert.Equal(t, "false", result.ConfirmedOptions().Get(OptionFastMode))
	assert.Equal(t, contracts.AmpPermissionModeAsk, result.ConfirmedOptions().Get(agent.OptionIDPermissionMode))
}

func TestSettingsSnapshotConfirmsEveryAxis(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withOptions(map[string]string{agent.OptionIDPermissionMode: contracts.AmpPermissionModeAllowAll}))
	snapshot := h.agent.SettingsSnapshot()
	assert.Equal(t, "false", snapshot.ConfirmedOptions().Get(OptionFastMode))
	assert.Equal(t, agentModeMedium, snapshot.ConfirmedOptions().Get(contracts.AmpOptionAgentMode))
	assert.Equal(t, contracts.AmpPermissionModeAllowAll, snapshot.ConfirmedOptions().Get(agent.OptionIDPermissionMode))
}

func TestLockedModeGroupOfAModeTheDialDoesNotList(t *testing.T) {
	t.Parallel()
	group := threadOptionGroup(agentModeGroup, "smart", true, lockedModeNote)
	require.Len(t, group.GetOptions(), 1)
	assert.Equal(t, "smart", group.GetOptions()[0].GetId())
	assert.Equal(t, "smart", group.GetOptions()[0].GetName(), "a mode of a plugin shows its own id")
	assert.False(t, group.GetMutable())
	assert.Equal(t, lockedModeNote, group.GetReadOnlyReason())
}

// settingsProbeServices inspects the lock scope at the actual persistence boundary.
type settingsProbeServices struct {
	agent.ProviderServices
	beforeRefresh func()
}

func (s settingsProbeServices) PersistSettingsRefresh(refresh optionmap.Map) {
	s.beforeRefresh()
	s.ProviderServices.PersistSettingsRefresh(refresh)
}

func TestSettingsPublicationSerializesChangesWithoutHoldingStateMutex(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	publications := 0
	h.agent.sink = settingsProbeServices{ProviderServices: h.agent.sink, beforeRefresh: func() {
		publications++
		if h.agent.settingsMu.TryLock() {
			h.agent.settingsMu.Unlock()
			t.Error("a concurrent settings change can overtake this persistence")
		}
		if h.agent.mu.TryLock() {
			h.agent.mu.Unlock()
		} else {
			t.Error("the persistence sink cannot read the current settings while the state mutex is held")
		}
	}}
	h.agent.UpdateSettings(map[string]string{OptionFastMode: "true"})
	h.send("start with fast mode")
	assert.Equal(t, 2, publications)
	assert.Equal(t, "true", h.sink.LastSettingsRefresh().Options[OptionFastMode])
}

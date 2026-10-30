package acp

import (
	"encoding/json"
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type heldACPSettingsSink struct {
	*agenttest.Sink
	first   sync.Once
	entered chan struct{}
	release chan struct{}
}

func (sink *heldACPSettingsSink) PersistSettingsRefresh(refresh optionmap.Map) {
	held := false
	sink.first.Do(func() { held = true; close(sink.entered) })
	if held {
		<-sink.release
	}
	sink.Sink.PersistSettingsRefresh(refresh)
}

func TestACPSettingsPublicationCannotOverwriteALaterSessionRefresh(t *testing.T) {
	t.Parallel()
	waitCtx := testutil.DeadlineContext(t)
	sink := &heldACPSettingsSink{Sink: &agenttest.Sink{}, entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(sink.release) }) }
	t.Cleanup(release)
	a := newTestAgent()
	a.sink = agent.NewProviderServices(sink)
	a.sessionID, a.model, a.permissionMode = "native-session", "old-model", "plan"
	seedThinkingEffort(a, "low")
	a.Mu.Lock()
	a.options.markSessionStart()
	a.Mu.Unlock()
	published := make(chan struct{})
	go func() { defer close(published); a.BroadcastSettingsRefresh() }()
	select {
	case <-sink.entered:
	case <-waitCtx.Done():
		t.Fatal("the first settings publication did not reach the sink")
	}
	refreshed := make(chan struct{})
	go func() {
		defer close(refreshed)
		a.applySessionRefresh(json.RawMessage(`{
          "models":{"currentModelId":"new-model"},
          "modes":{"currentModeId":"agent","availableModes":[{"id":"agent","name":"Agent"},{"id":"plan","name":"Plan"}]},
          "configOptions":[{"id":"thinking_effort","category":"thought_level","currentValue":"high","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]}]
        }`))
	}()
	select {
	case <-refreshed:
	case <-waitCtx.Done():
		t.Fatal("the session refresh waited for the held settings callback")
	}
	a.Mu.Lock()
	assert.Equal(t, "new-model", a.model)
	assert.Equal(t, "agent", a.permissionMode)
	a.Mu.Unlock()
	group := optionids.GroupByID(a.OptionGroups(), testThinkingEffort)
	require.NotNil(t, group)
	assert.Equal(t, "low", group.GetCurrentValue(), "session refresh preserves the restored option value")
	release()
	select {
	case <-published:
	case <-waitCtx.Done():
		t.Fatal("the settings publications did not drain")
	}
	require.Equal(t, 2, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "new-model", refresh.Model)
	assert.Equal(t, "agent", refresh.PermissionMode)
	assert.Equal(t, "low", refresh.Options[testThinkingEffort])
}

func TestACPSettingsPublicationPreservesAnUnresolvedRequestedValue(t *testing.T) {
	t.Parallel()
	a, _ := newTestAgentForRPCWithResponder(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	seedThinkingEffort(a, "medium")
	sink := &heldACPSettingsSink{Sink: &agenttest.Sink{}, entered: make(chan struct{}), release: make(chan struct{})}
	a.sink = agent.NewProviderServices(sink)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(sink.release) }) }
	t.Cleanup(release)
	waitCtx := testutil.DeadlineContext(t)
	published := make(chan struct{})
	go func() { defer close(published); a.BroadcastSettingsRefresh() }()
	select {
	case <-sink.entered:
	case <-waitCtx.Done():
		t.Fatal("the older native settings did not reach the sink")
	}
	sink.Sink.PersistSettingsRefresh(optionmap.Map{testThinkingEffort: "high"})
	result := a.UpdateSettings(optionmap.Map{testThinkingEffort: "high"})
	require.True(t, result.AppliedLive)
	assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[testThinkingEffort].State)
	assert.NotContains(t, result.ConfirmedOptions(), testThinkingEffort)
	release()
	select {
	case <-published:
	case <-waitCtx.Done():
		t.Fatal("the settings publications did not finish")
	}
	assert.Equal(t, "high", sink.LastSettingsRefresh().Options[testThinkingEffort])
}

func TestACPSettingsPublicationDeletesTheOptionRemovedByTheNewModel(t *testing.T) {
	t.Parallel()
	a, _, _ := newSwitchAgent(t, switchServer(), false)
	sink := &heldACPSettingsSink{Sink: &agenttest.Sink{}, entered: make(chan struct{}), release: make(chan struct{})}
	a.sink = agent.NewProviderServices(sink)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(sink.release) }) }
	t.Cleanup(release)
	waitCtx := testutil.DeadlineContext(t)
	published := make(chan struct{})
	go func() { defer close(published); a.BroadcastSettingsRefresh() }()
	select {
	case <-sink.entered:
	case <-waitCtx.Done():
		t.Fatal("the older native settings did not reach the sink")
	}
	sink.Sink.PersistSettingsRefresh(optionmap.Map{agent.OptionIDModel: "model-b", testThinkingEffort: "high"})
	result := a.UpdateSettings(optionmap.Map{agent.OptionIDModel: "model-b", testThinkingEffort: "high"})
	require.True(t, result.AppliedLive)
	assert.NotContains(t, result.SurfacedOptions, testThinkingEffort)
	// The service removes the obsolete axis before the old publication resumes.
	sink.Sink.PersistSettingsRefresh(optionmap.Map{testThinkingEffort: ""})
	release()
	select {
	case <-published:
	case <-waitCtx.Done():
		t.Fatal("the settings publications did not finish")
	}
	last := sink.LastSettingsRefresh()
	assert.Equal(t, "model-b", last.Model)
	value, present := last.Options[testThinkingEffort]
	require.True(t, present, "the final delta must remove the vanished axis")
	assert.Empty(t, value)
}

func TestACPSettingsSnapshotKeepsNativeValuesAndSettlementsTogether(t *testing.T) {
	t.Parallel()
	a := newTestAgent()
	seedThinkingEffort(a, "medium")
	var once sync.Once
	a.hooks.LocalOptionGroups = func() []*leapmuxv1.AvailableOptionGroup {
		once.Do(func() {
			a.Mu.Lock()
			a.options.recordOptimistic(testThinkingEffort, "high", testThinkingEffort)
			a.options.markUnresolved(testThinkingEffort)
			a.Mu.Unlock()
		})
		return nil
	}
	snapshot := a.SettingsSnapshot()
	assert.Equal(t, "medium", snapshot.SurfacedOptions[testThinkingEffort])
	assert.Equal(t, "medium", snapshot.ConfirmedOptions()[testThinkingEffort], "the local hook must not mix a later settlement with the earlier native values")
	latest := a.SettingsSnapshot()
	assert.Equal(t, "high", latest.SurfacedOptions[testThinkingEffort])
	assert.Equal(t, agent.OptionSettlementUnresolved, latest.Settlements[testThinkingEffort].State)
}

func TestACPSettingsPublicationOmitsAnUnresolvedAxisAbsentFromTheRequest(t *testing.T) {
	t.Parallel()
	a, _ := newTestAgentForRPCWithResponder(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.model = "known-model"
	seedThinkingEffort(a, "medium")
	first := a.UpdateSettings(optionmap.Map{testThinkingEffort: "high"})
	require.Equal(t, agent.OptionSettlementUnresolved, first.Settlements[testThinkingEffort].State)
	before := sink.SettingsRefreshCount()
	second := a.UpdateSettings(optionmap.Map{})
	assert.Equal(t, agent.OptionSettlementUnresolved, second.Settlements[testThinkingEffort].State)
	require.Equal(t, before+1, sink.SettingsRefreshCount())
	assert.NotContains(t, sink.LastSettingsRefresh().Options, testThinkingEffort)
}

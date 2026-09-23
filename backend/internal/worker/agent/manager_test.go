package agent_test

import (
	"context"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubProvider struct {
	groups         []*leapmuxv1.AvailableOptionGroup
	clearContextFn func() (string, error)
}

func (s *stubProvider) AgentID() string                                 { return "stub" }
func (s *stubProvider) SendInput(string, []*leapmuxv1.Attachment) error { return nil }
func (s *stubProvider) SendInputForSession(string, string, []*leapmuxv1.Attachment) error {
	return agent.ErrInputSessionChanged
}

// PublishTurnActive is inert here: a stub holds no turn flag and no sink, and
// Manager.SendInput calls it only after a refusal this stub never returns.
func (s *stubProvider) PublishTurnActive() agent.TurnState {
	return agent.TurnState{}
}
func (s *stubProvider) SendRawInput([]byte) error { return nil }
func (s *stubProvider) Stop()                     {}
func (s *stubProvider) IsStopped() bool           { return false }
func (s *stubProvider) DiscardOutput()            {}
func (s *stubProvider) ClearContext() (string, error) {
	if s.clearContextFn != nil {
		return s.clearContextFn()
	}
	return "", agent.ErrContextClearUnsupported
}
func (s *stubProvider) Wait() error                                     { return nil }
func (s *stubProvider) Stderr() string                                  { return "" }
func (s *stubProvider) HandleOutput([]byte)                             {}
func (s *stubProvider) OptionGroups() []*leapmuxv1.AvailableOptionGroup { return s.groups }
func (s *stubProvider) SettingsSnapshot() agent.SettingsApplyResult {
	return agent.ConfirmedSettings(agent.CurrentOptions(s.groups))
}
func (s *stubProvider) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	return agent.ConfirmedSettings(options)
}
func (s *stubProvider) Interrupt() error { return nil }

func TestManager_ClearContextWaitsForLifecycleLock(t *testing.T) {
	t.Parallel()

	m := agent.NewManager(testRegistry, nil)
	entered := make(chan struct{}, 1)
	m.PutAgentForTest("locked", &stubProvider{clearContextFn: func() (string, error) {
		entered <- struct{}{}
		return "thread-new", nil
	}})

	unlock := m.LockAgent("locked")
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := m.ClearContext("locked")
		assert.NoError(t, err)
	}()

	premature := false
	select {
	case <-entered:
		premature = true
	case <-time.After(50 * time.Millisecond):
	}
	unlock()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ClearContext did not continue after the lifecycle lock was released")
	}
	assert.False(t, premature, "ClearContext entered the provider while another lifecycle operation held the lock")
}

func TestManager_SendInputUnknownAgent(t *testing.T) {
	m := agent.NewManager(testRegistry, nil)

	assert.Error(t, m.SendInput("nonexistent", "hello", nil), "expected error for unknown agent")

	// The error path must release the lifecycle lock. It is taken before the
	// lookup, so a return that skipped the unlock would leave the agent id locked
	// forever -- every later send, restart, or auto-start for it would park on a
	// mutex nobody holds a reason for, and the agent would simply stop responding
	// with no error anywhere. A second send proves the lock came back.
	done := make(chan struct{})
	go func() {
		defer close(done)
		assert.Error(t, m.SendInput("nonexistent", "hello again", nil), "expected error for unknown agent")
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second send blocked: the failed send did not release the lifecycle lock")
	}
}

func TestManager_StopAndWaitAgent_NotRunning(t *testing.T) {
	m := agent.NewManager(testRegistry, nil)
	assert.False(t, m.StopAndWaitAgent("nonexistent"), "expected false for non-running agent")
}

// blockingStub is a stubProvider whose Wait blocks until waitCh is closed, so a test can
// control exactly when the exit goroutine runs its cleanup.
type blockingStub struct {
	stubProvider
	waitCh chan struct{}
}

func (b *blockingStub) Wait() error { <-b.waitCh; return nil }

func TestManager_ExitCallbackRunsBeforeSlotRelease(t *testing.T) {
	t.Parallel()

	type exitView struct{ hasAgent, alive bool }
	m := agent.NewManager(testRegistry, nil)
	viewDuringExit := make(chan exitView, 1)
	m.SetOnExit(func(agentID string, _ int, _ error, _ bool) {
		viewDuringExit <- exitView{hasAgent: m.HasAgent(agentID), alive: m.AgentAlive(agentID)}
	})
	provider := &blockingStub{waitCh: make(chan struct{})}
	_, err := m.StartAgentWith(context.Background(), agent.Options{
		AgentID: "exiting", WorkingDir: t.TempDir(),
	}, agenttest.Nop(), func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
		return provider, nil
	})
	require.NoError(t, err)

	close(provider.waitCh)
	select {
	case view := <-viewDuringExit:
		assert.True(t, view.hasAgent, "the exit callback must pause the queue before the slot permits a restart")
		// The slot outliving the process is exactly why HasAgent cannot answer
		// "may I write to this provider". A caller that is about to write asks
		// AgentAlive, or it writes to a closed pipe and fails the user's input.
		assert.False(t, view.alive, "a provider that already exited must not read as alive")
	case <-time.After(2 * time.Second):
		t.Fatal("exit callback did not run")
	}
	require.Eventually(t, func() bool { return !m.HasAgent("exiting") }, time.Second, 10*time.Millisecond)
}

// TestManager_ExitGoroutineHonorsIdentityGuard verifies the stop-restart race fix: the old
// provider's background Wait goroutine, when it unblocks AFTER a new provider has taken the
// agent's slot (the restart case), must NOT delete the new provider's map entry or its
// cache. Without the identity check, the stale goroutine would orphan the just-restarted
// agent (SendInput -> ErrAgentNotFound) and wipe its cache.
func TestManager_ExitGoroutineHonorsIdentityGuard(t *testing.T) {
	m := agent.NewManager(testRegistry, nil)
	exited := make(chan struct{})
	m.SetOnExit(func(string, int, error, bool) { close(exited) })

	// Provider A blocks in Wait until released; it is registered with a cache entry.
	old := &blockingStub{
		stubProvider: stubProvider{groups: []*leapmuxv1.AvailableOptionGroup{{Id: agent.OptionIDModel, CurrentValue: "a"}}},
		waitCh:       make(chan struct{}),
	}
	_, err := m.StartAgentWith(context.Background(), agent.Options{
		AgentID:    "r",
		Options:    map[string]string{agent.OptionIDModel: "a"},
		WorkingDir: t.TempDir(),
	}, agenttest.Nop(), func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) { return old, nil })
	require.NoError(t, err)
	require.True(t, m.HasAgent("r"))

	// Simulate a restart that already replaced A with a new provider B (and its cache)
	// while A's Wait goroutine is still blocked.
	newProvider := &blockingStub{
		stubProvider: stubProvider{groups: []*leapmuxv1.AvailableOptionGroup{{Id: agent.OptionIDModel, CurrentValue: "b"}}},
		waitCh:       make(chan struct{}),
	}
	m.PutAgentForTest("r", newProvider)
	m.SeedCachedCatalogForTest("r", newProvider.groups, "b")

	// Release A; its goroutine fires onExit, then runs the identity-guarded delete.
	close(old.waitCh)
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("exit goroutine did not run")
	}

	got, ok := m.AgentForTest("r")
	_, cachedModel, cacheOk := m.CachedCatalogForTest("r")
	assert.True(t, ok, "restarted provider B must survive the old provider's exit")
	assert.True(t, got == agent.Agent(newProvider), "the slot must still hold B, not be deleted by A's stale goroutine")
	assert.True(t, cacheOk, "B's cache entry must survive A's exit")
	assert.Equal(t, "b", cachedModel, "B's cache must not be clobbered by A's goroutine")

	close(newProvider.waitCh)
}

// stopSignalStub is a blockingStub that signals when Stop() is called. stopAndWait reads the
// manager maps (capturing the exit-done channel) BEFORE it calls Stop(), so a test can wait on
// `stopped` to know stopAndWait has captured the channel before releasing Wait -- making the
// stop/exit ordering deterministic instead of racing the exit goroutine's slot delete.
type stopSignalStub struct {
	blockingStub
	stopped chan struct{}
}

func (s *stopSignalStub) Stop() { close(s.stopped) }

// TestManager_StopAndWaitWaitsForOnExit is the regression guard for the onExit restart race:
// stopAndWait must not return until the exiting process's background goroutine -- including its
// onExit cleanup (ClearPendingControlRequests, which deletes by agent id alone) -- has fully
// finished. Otherwise a new provider registered right after a restart's stopAndWait could have
// its freshly-persisted control requests wiped by the old process's late onExit. Waiting makes
// the old process's teardown happen-before any new provider is registered.
func TestManager_StopAndWaitWaitsForOnExit(t *testing.T) {
	m := agent.NewManager(testRegistry, nil)
	onExitStarted := make(chan struct{})
	releaseOnExit := make(chan struct{})
	m.SetOnExit(func(string, int, error, bool) {
		close(onExitStarted)
		<-releaseOnExit // hold onExit open so the test can observe stopAndWait still blocked
	})

	old := &stopSignalStub{
		blockingStub: blockingStub{waitCh: make(chan struct{})},
		stopped:      make(chan struct{}),
	}
	_, err := m.StartAgentWith(context.Background(), agent.Options{
		AgentID:    "w",
		Options:    map[string]string{agent.OptionIDModel: "a"},
		WorkingDir: t.TempDir(),
	}, agenttest.Nop(), func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) { return old, nil })
	require.NoError(t, err)

	stopReturned := make(chan struct{})
	go func() {
		m.StopAndWaitAgent("w")
		close(stopReturned)
	}()

	// Wait until stopAndWait has captured the exit-done channel (it calls Stop() right after),
	// THEN release Wait so the exit goroutine can't delete the slot before stopAndWait sees it.
	<-old.stopped
	close(old.waitCh)

	// The exit goroutine reaches onExit and blocks there. stopAndWait MUST still be blocked --
	// it waits on the exit goroutine's done channel, which is closed only after onExit returns.
	<-onExitStarted
	select {
	case <-stopReturned:
		t.Fatal("stopAndWait returned while the exit goroutine's onExit was still running")
	case <-time.After(100 * time.Millisecond):
	}

	// Let onExit finish; stopAndWait then unblocks and returns.
	close(releaseOnExit)
	select {
	case <-stopReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("stopAndWait did not return after onExit completed")
	}
}

// TestManager_OptionGroupsForRow_SurfacesModelFromRow guards S5: a NOT-running dynamic-model ACP
// provider with no persisted catalog but a model on the row (a LEAPMUX_*_DEFAULT_MODEL override
// that never ran, so no model list was ever discovered) still surfaces a read-only model group, so
// the remote CLI's by-id model read reports the stored model instead of "".
func TestManager_OptionGroupsForRow_SurfacesModelFromRow(t *testing.T) {
	m := agent.NewManager(catalogRegistry(), nil)

	groups := m.OptionGroupsForRow("not-running", dynamicModelProvider, "anthropic/claude-sonnet-4", nil)

	mg := optionids.GroupByID(groups, agent.OptionIDModel)
	require.NotNil(t, mg, "a model group is surfaced from the row's model even with no discovered catalog")
	assert.Equal(t, "anthropic/claude-sonnet-4", mg.GetCurrentValue())
	assert.False(t, mg.GetMutable(), "the synthesized model group is read-only (there is no selectable list)")

	// A no-op when no model is known: nothing to surface.
	none := m.OptionGroupsForRow("not-running-2", dynamicModelProvider, "", nil)
	assert.Nil(t, optionids.GroupByID(none, agent.OptionIDModel), "no model group is invented when the row has no model")
}

// TestManager_OptionGroupsRefreshesCacheFromLive verifies the running cache is refreshed
// from the live catalog, so a model discovered after start (or a live setting change)
// survives a transiently EMPTY live read instead of falling back to the start-time
// (here: never-seeded) catalog.
func TestManager_OptionGroupsRefreshesCacheFromLive(t *testing.T) {
	m := agent.NewManager(catalogRegistry(), nil)

	// Provider starts with an EMPTY catalog, so StartAgent seeds no cache entry.
	p := &blockingStub{waitCh: make(chan struct{})}
	_, err := m.StartAgentWith(context.Background(), agent.Options{
		AgentID:    "c",
		Options:    map[string]string{agent.OptionIDModel: "x"},
		WorkingDir: t.TempDir(),
	}, agenttest.Nop(), func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) { return p, nil })
	require.NoError(t, err)
	_, _, seeded := m.CachedCatalogForTest("c")
	require.False(t, seeded, "an empty start-time catalog seeds no cache entry")

	// The agent now reports a richer catalog (model + a server-driven config option).
	p.groups = []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, CurrentValue: "x", Options: []*leapmuxv1.AvailableOption{{Id: "x"}}},
		{Id: "reasoning_effort", Mutable: true, Options: []*leapmuxv1.AvailableOption{{Id: "high"}, {Id: "low"}}},
	}
	// A read while running returns the live catalog AND refreshes the cache.
	require.NotNil(t, optionids.GroupByID(m.OptionGroups("c", staticCatalogProvider, "x"), "reasoning_effort"))

	// The live catalog then goes transiently empty; the read must fall back to the
	// refreshed cache (carrying reasoning_effort), not a degenerate static fallback.
	p.groups = nil
	assert.NotNil(t, optionids.GroupByID(m.OptionGroups("c", staticCatalogProvider, "x"), "reasoning_effort"),
		"a transient empty live catalog serves the freshest cached catalog")

	close(p.waitCh)
}

func TestManager_StopUnknownAgent(t *testing.T) {
	m := agent.NewManager(testRegistry, nil)
	// Should not panic.
	m.StopAgent("nonexistent")
}

func TestManager_CurrentSettings(t *testing.T) {
	m := agent.NewManager(testRegistry, nil)

	assert.Nil(t, m.CurrentSettings("missing-agent").SurfacedOptions)

	// Registered agent → the provider's in-memory confirmed option values, letting
	// callers read back the effort the agent actually confirmed (e.g. an
	// ultracode request downgraded to xhigh) without a DB round-trip.
	m.PutAgentForTest("running-agent", &stubProvider{groups: []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDEffort, CurrentValue: "xhigh"},
	}})

	got := m.CurrentSettings("running-agent")
	require.NotNil(t, got.SurfacedOptions)
	assert.Equal(t, "xhigh", got.SurfacedOptions[agent.OptionIDEffort])
	assert.Equal(t, agent.OptionSettlementConfirmed, got.Settlements[agent.OptionIDEffort].State)
}

func TestManager_PreloadCache(t *testing.T) {
	m := agent.NewManager(catalogRegistry(), nil)

	groups := []*leapmuxv1.AvailableOptionGroup{
		{Id: "thinkingBudget", Label: "Thinking Budget", Options: []*leapmuxv1.AvailableOption{
			{Id: "low", Name: "Low"},
			{Id: "high", Name: "High"},
		}},
	}

	// Preload cache for a non-running agent.
	m.PreloadCache("preloaded-agent", groups)

	// OptionGroups should return preloaded groups (not static defaults).
	gotGroups := m.OptionGroups("preloaded-agent", staticCatalogProvider, "")
	require.Len(t, gotGroups, 1)
	assert.Equal(t, "thinkingBudget", gotGroups[0].GetId())
	assert.Len(t, gotGroups[0].GetOptions(), 2)
}

// TestManager_OptionGroupsForRowUsesRowSnapshotNotSharedCache guards S4: for a not-running agent
// the row is authoritative (the cache entry was dropped on exit and is only ever re-seeded from
// per-caller row snapshots), so OptionGroupsForRow builds from the CALLER'S snapshot rather than
// the shared cache. A stale catalog a concurrent reader's older snapshot left in the shared cache
// must NOT be served in place of the caller's fresher one.
func TestManager_OptionGroupsForRowUsesRowSnapshotNotSharedCache(t *testing.T) {
	m := agent.NewManager(catalogRegistry(), nil)

	stale := []*leapmuxv1.AvailableOptionGroup{
		{Id: "thinkingBudget", Label: "Thinking Budget", Options: []*leapmuxv1.AvailableOption{{Id: "low", Name: "Low"}}},
	}
	fresh := []*leapmuxv1.AvailableOptionGroup{
		{Id: "thinkingBudget", Label: "Thinking Budget", Options: []*leapmuxv1.AvailableOption{
			{Id: "low", Name: "Low"}, {Id: "high", Name: "High"},
		}},
	}

	// A concurrent reader's OLDER snapshot lands in the shared cache.
	m.PreloadCache("a", stale)

	// This reader holds the FRESHER row snapshot; OptionGroupsForRow must serve it, not the stale cache.
	got := m.OptionGroupsForRow("a", staticCatalogProvider, "", fresh)
	budget := optionids.GroupByID(got, "thinkingBudget")
	require.NotNil(t, budget)
	assert.Len(t, budget.GetOptions(), 2, "the caller's fresh snapshot is served, not the stale shared cache")
}

func TestManager_PreloadCacheSkipsEmpty(t *testing.T) {
	m := agent.NewManager(catalogRegistry(), nil)

	// Preload with nil slice — should not populate cache.
	m.PreloadCache("empty-agent", nil)

	// Should fall back to the static defaults, whose first model is "auto".
	groups := m.OptionGroups("empty-agent", staticCatalogProvider, "")
	require.NotEmpty(t, groups)
	modelGroup := optionids.GroupByID(groups, agent.OptionIDModel)
	require.NotNil(t, modelGroup)
	assert.Equal(t, "auto", modelGroup.GetOptions()[0].GetId(), "should fall back to the static defaults")
}

// TestManager_PreloadCacheSkipsRunningAgent is the regression guard for [A11]: a running
// (or concurrently-starting) agent owns its cache -- seeded model-correct by StartAgent and
// refreshed in OptionGroups -- so PreloadCache must NOT overwrite it with the persisted-row
// snapshot. optionGroupsView gates on HasAgent first, but that check and the write aren't
// atomic, so a StartAgent landing in between would otherwise be reverted to the stale stamp.
func TestManager_PreloadCacheSkipsRunningAgent(t *testing.T) {
	m := agent.NewManager(testRegistry, nil)
	// Seed a fresh, model-correct cache as StartAgent would, then register the running agent.
	live := []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Label: "Model", CurrentValue: "new-model", Options: []*leapmuxv1.AvailableOption{{Id: "new-model"}}},
	}
	m.SeedCachedCatalogForTest("live", live, "new-model")
	m.PutAgentForTest("live", &stubProvider{groups: live})

	// A racing PreloadCache with the stale persisted snapshot must be a no-op.
	persisted := []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Label: "Model", CurrentValue: "old-model", Options: []*leapmuxv1.AvailableOption{{Id: "old-model"}}},
	}
	m.PreloadCache("live", persisted)

	_, cachedModel, _ := m.CachedCatalogForTest("live")
	assert.Equal(t, "new-model", cachedModel,
		"PreloadCache must not clobber a running agent's cache with the stale persisted stamp")
}

func TestManager_AvailableOptionGroupsCachedFallback(t *testing.T) {
	m := agent.NewManager(catalogRegistry(), nil)

	cachedGroups := []*leapmuxv1.AvailableOptionGroup{{
		Id:    "thinkingBudget",
		Label: "Thinking Budget",
		Options: []*leapmuxv1.AvailableOption{
			{Id: "low", Name: "Low"},
		},
	}}

	m.SeedCachedCatalogForTest("cached-agent", cachedGroups, "")

	// Agent is not running — should return cached groups, not static defaults.
	got := m.OptionGroups("cached-agent", dynamicModelProvider, "")
	require.Len(t, got, 1)
	assert.Equal(t, "thinkingBudget", got[0].GetId())
}

// TestManager_CachedCatalogServedByModelStamp verifies how the cache is served relative to the
// requested model for a model-dependent provider, as Claude is: a matching or unknown model serves
// the cache verbatim, while a since-changed model (an offline edit that rewrote options.model
// but not the persisted catalog) REBUILDS the per-model groups for the new model yet PRESERVES
// any model-INDEPENDENT discovered group (e.g. Output Style) instead of dropping it to the bare
// static fallback.
func TestManager_CachedCatalogServedByModelStamp(t *testing.T) {
	m := agent.NewManager(catalogRegistry(), nil)
	// The model group's current value ("sonnet") stamps the cache; outputStyle is a model-
	// INDEPENDENT cache-only group the static fallback never reproduces, as Claude's Output
	// Style is: it is surfaced only at runtime.
	groups := []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Label: "Model", CurrentValue: "sonnet", Options: []*leapmuxv1.AvailableOption{{Id: "sonnet"}, {Id: "haiku"}}},
		{Id: "outputStyle", Label: "Output Style", Options: []*leapmuxv1.AvailableOption{{Id: "default"}}},
	}
	m.PreloadCache("a1", groups)

	// Requested model matches the stamp -> cache served verbatim (cached model group + outputStyle).
	matched := m.OptionGroups("a1", modelDependentProvider, "sonnet")
	assert.Equal(t, "sonnet", optionids.GroupByID(matched, agent.OptionIDModel).GetCurrentValue(),
		"a matching model serves the cached model group verbatim")
	assert.NotNil(t, optionids.GroupByID(matched, "outputStyle"), "a matching model serves the cached catalog")
	// Unknown requested model -> trust the cache as-is.
	assert.NotNil(t, optionids.GroupByID(m.OptionGroups("a1", modelDependentProvider, ""), "outputStyle"),
		"an unknown model trusts the cache")
	// Requested model differs from the stamp -> the per-model groups are rebuilt for the new
	// model (the model group loses its stale cached current), but the model-independent discovered
	// group survives instead of being dropped.
	changed := m.OptionGroups("a1", modelDependentProvider, "haiku")
	assert.Equal(t, "", optionids.GroupByID(changed, agent.OptionIDModel).GetCurrentValue(),
		"a since-changed model rebuilds the model group (no stale cached current)")
	assert.NotNil(t, optionids.GroupByID(changed, "outputStyle"),
		"a model-independent discovered group survives a model edit instead of being dropped")
}

// TestManager_UnstampedCacheRebuiltForModelDependentProvider verifies the S1 fix: an
// UNSTAMPED cache (model current == "") for a model-dependent provider is treated as stale-by-
// model when the requested model is known -- the per-model groups are rebuilt for that model
// rather than served from the cache (whose dependent groups were built for some other / default
// model) -- while a model-INDEPENDENT discovered group (Output Style) is preserved. An unknown
// requested model still trusts the cache. (A non-model-dependent ACP provider keeps serving its
// cache wholesale; see the test above.)
func TestManager_UnstampedCacheRebuiltForModelDependentProvider(t *testing.T) {
	r := catalogRegistry()
	m := agent.NewManager(r, nil)
	require.True(t, r.ProviderHasModelDependentGroupsForTest(modelDependentProvider), "precondition: the provider is model-dependent")

	// An unstamped cache: the model group carries NO current value (model == "" stamp), plus a
	// model-independent cache-only group the static fallback never reproduces.
	groups := []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Label: "Model", Options: []*leapmuxv1.AvailableOption{{Id: "sonnet"}, {Id: "haiku"}}},
		{Id: "outputStyle", Label: "Output Style", Options: []*leapmuxv1.AvailableOption{{Id: "default"}}},
	}
	m.PreloadCache("a1", groups)
	_, cachedModel, _ := m.CachedCatalogForTest("a1")
	require.Equal(t, "", cachedModel, "precondition: the cache is unstamped")

	// A known requested model rebuilds the per-model groups but PRESERVES the discovered group.
	rebuilt := m.OptionGroups("a1", modelDependentProvider, "haiku")
	assert.NotNil(t, optionids.GroupByID(rebuilt, "outputStyle"),
		"a model-independent discovered group survives even when the per-model groups are rebuilt")
	// The model group is rebuilt from the static catalog, not served from the 2-option
	// cached stub -- proving the stale per-model groups were not served.
	assert.Greater(t, len(optionids.GroupByID(rebuilt, agent.OptionIDModel).GetOptions()), 2,
		"the per-model groups are rebuilt from the static catalog, not served from the stale cache")
	// An unknown requested model still trusts the cache (can't tell it's stale).
	assert.NotNil(t, optionids.GroupByID(m.OptionGroups("a1", modelDependentProvider, ""), "outputStyle"),
		"an unknown requested model trusts the unstamped cache")
}

func TestManager_AvailableOptionGroupsPrefersRuntimeGroups(t *testing.T) {
	// A provider whose static groups hold only a primary-agent axis, as OpenCode's do.
	reg := testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE)
	reg.OptionGroups = []*leapmuxv1.AvailableOptionGroup{{
		Id:      agent.OptionIDPrimaryAgent,
		Options: []*leapmuxv1.AvailableOption{{Id: "build"}, {Id: "plan"}},
	}}
	m := agent.NewManager(agenttest.MustNewRegistry(reg), nil)
	runtimeGroups := []*leapmuxv1.AvailableOptionGroup{{
		Id:    agent.OptionIDPrimaryAgent,
		Label: "Primary Agent",
		Options: []*leapmuxv1.AvailableOption{
			{Id: "build", Name: "build"},
			{Id: "architect", Name: "architect"},
		},
	}}

	m.PutAgentForTest("runtime-agent", &stubProvider{groups: runtimeGroups})

	assert.Equal(t, runtimeGroups, m.OptionGroups("runtime-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, ""))

	staticGroups := m.OptionGroups("missing-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, "")
	require.Len(t, staticGroups, 1)
	assert.Equal(t, agent.OptionIDPrimaryAgent, staticGroups[0].GetId())
	assert.Equal(t, "build", staticGroups[0].Options[0].Id)
}

// The manager serves a provider's option groups by three facts of its
// registration: whether the effort tiers depend on the model, what its static
// catalog holds, and whether it has a catalog at all. These synthetic
// registrations state each fact, so the catalog tests depend on no provider.
const (
	// modelDependentProvider manages its effort tiers per model, as Claude does,
	// so a model change rebuilds its per-model groups.
	modelDependentProvider = leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	// staticCatalogProvider has a static catalog whose first model is "auto", and
	// no model-dependent groups, so its cache is served as-is.
	staticCatalogProvider = leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR
	// dynamicModelProvider has no static catalog: its models are discovered at
	// runtime, as an ACP daemon reports them.
	dynamicModelProvider = leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE
)

func catalogRegistry() *agent.Registry {
	modelDependent := testRegistration(modelDependentProvider)
	modelDependent.ManagesEffort = true
	modelDependent.DefaultModels = []*agent.ModelInfo{
		{Id: "opus", SupportedEfforts: []*agent.EffortInfo{{Id: agent.EffortHigh, Name: "High"}}},
		{Id: "sonnet", SupportedEfforts: []*agent.EffortInfo{{Id: agent.EffortHigh, Name: "High"}}},
		{Id: "haiku"},
	}
	staticCatalog := testRegistration(staticCatalogProvider)
	staticCatalog.DefaultModels = []*agent.ModelInfo{{Id: "auto"}, {Id: "gpt-5"}}
	return agenttest.MustNewRegistry(modelDependent, staticCatalog, testRegistration(dynamicModelProvider))
}

package agent_test

import (
	"context"
	"sync"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManager_LiveOptionGroupsUsesNoOfflineOrStaticFallback(t *testing.T) {
	t.Parallel()

	m := agent.NewManager(catalogRegistry(), nil)
	require.NotEmpty(t, m.OptionGroups("absent", staticCatalogProvider, ""), "the ordinary read keeps its static fallback")
	assert.Empty(t, m.LiveOptionGroups("absent", staticCatalogProvider))

	cached := []*leapmuxv1.AvailableOptionGroup{{Id: "offline-only"}}
	m.PreloadCache("offline", cached)
	require.NotEmpty(t, m.OptionGroups("offline", staticCatalogProvider, ""), "the ordinary read keeps its persisted cache")
	assert.Empty(t, m.LiveOptionGroups("offline", staticCatalogProvider))

	m.PutAgentForTest("empty-live", &stubProvider{})
	assert.Empty(t, m.LiveOptionGroups("empty-live", staticCatalogProvider), "a live empty sample cannot invent a static catalog")
}

func TestManager_LiveOptionGroupsKeepsTheLatestLiveCacheForAnEmptySample(t *testing.T) {
	t.Parallel()

	m := agent.NewManager(catalogRegistry(), nil)
	p := &stubProvider{groups: []*leapmuxv1.AvailableOptionGroup{{Id: "first-live"}}}
	m.PutAgentForTest("live", p)
	require.NotNil(t, optionids.GroupByID(m.LiveOptionGroups("live", staticCatalogProvider), "first-live"))
	p.groups = []*leapmuxv1.AvailableOptionGroup{{Id: "latest-live"}}
	require.NotNil(t, optionids.GroupByID(m.OptionGroups("live", staticCatalogProvider, ""), "latest-live"))
	p.groups = nil

	groups := m.LiveOptionGroups("live", staticCatalogProvider)
	assert.NotNil(t, optionids.GroupByID(groups, "latest-live"))
	assert.Nil(t, optionids.GroupByID(groups, "first-live"))
	assert.Nil(t, optionids.GroupByID(groups, agent.OptionIDModel), "the empty sample keeps no static model group")
}

func TestManager_LiveOptionGroupsKeepsTheLiveStartupCacheForAnEmptySample(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(catalogRegistry(), nil)
	p := &blockingStub{stubProvider: stubProvider{groups: []*leapmuxv1.AvailableOptionGroup{{Id: "startup-live"}}}, waitCh: make(chan struct{})}
	exit := sync.OnceFunc(func() { close(p.waitCh) })
	t.Cleanup(func() { exit(); m.StopAndWaitAgent("live") })
	_, err := m.StartAgentWith(context.Background(), agent.Options{AgentID: "live", WorkingDir: t.TempDir()}, agenttest.Nop(),
		func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) { return p, nil })
	require.NoError(t, err)
	p.groups = nil

	assert.NotNil(t, optionids.GroupByID(m.LiveOptionGroups("live", staticCatalogProvider), "startup-live"),
		"the empty sample can reuse the catalog from this process's startup")
}

func TestManager_LiveOptionGroupsRejectsOtherCacheSources(t *testing.T) {
	t.Parallel()

	t.Run("persisted catalog before empty startup", func(t *testing.T) {
		t.Parallel()
		m := agent.NewManager(catalogRegistry(), nil)
		m.PreloadCache("live", []*leapmuxv1.AvailableOptionGroup{{Id: "persisted-only"}})
		p := &blockingStub{waitCh: make(chan struct{})}
		exit := sync.OnceFunc(func() { close(p.waitCh) })
		t.Cleanup(func() { exit(); m.StopAndWaitAgent("live") })
		_, err := m.StartAgentWith(context.Background(), agent.Options{AgentID: "live", WorkingDir: t.TempDir()}, agenttest.Nop(),
			func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) { return p, nil })
		require.NoError(t, err)
		require.NotNil(t, optionids.GroupByID(m.OptionGroups("live", staticCatalogProvider, ""), "persisted-only"),
			"the ordinary read preserves the prior catalog during empty startup")

		assert.Empty(t, m.LiveOptionGroups("live", staticCatalogProvider), "a persisted cache is not a live sample")
	})

	t.Run("previous instance of the same provider", func(t *testing.T) {
		t.Parallel()
		m := agent.NewManager(catalogRegistry(), nil)
		old := &stubProvider{groups: []*leapmuxv1.AvailableOptionGroup{{Id: "previous-process"}}}
		m.PutAgentForTest("live", old)
		require.NotNil(t, optionids.GroupByID(m.LiveOptionGroups("live", staticCatalogProvider), "previous-process"))
		m.PutAgentForTest("live", &stubProvider{})

		assert.Empty(t, m.LiveOptionGroups("live", staticCatalogProvider), "a new process cannot publish the previous process's cache")
	})
}

// heldCatalogProvider lets another provider take the slot during one sample.
type heldCatalogProvider struct {
	stubProvider
	entered chan struct{}
	release chan struct{}
}

func (p *heldCatalogProvider) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	close(p.entered)
	<-p.release
	return p.stubProvider.OptionGroups()
}

func TestManager_LiveOptionGroupsRejectsAProviderReplacedDuringTheSample(t *testing.T) {
	t.Parallel()

	m := agent.NewManager(catalogRegistry(), nil)
	p := &heldCatalogProvider{
		stubProvider: stubProvider{groups: []*leapmuxv1.AvailableOptionGroup{{Id: "old-live"}}},
		entered:      make(chan struct{}), release: make(chan struct{}),
	}
	resume := sync.OnceFunc(func() { close(p.release) })
	defer resume()
	m.PutAgentForTest("live", p)
	result := make(chan []*leapmuxv1.AvailableOptionGroup, 1)
	go func() { result <- m.LiveOptionGroups("live", staticCatalogProvider) }()
	select {
	case <-p.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("The live catalog sample did not start.")
	}
	replacement := &stubProvider{groups: []*leapmuxv1.AvailableOptionGroup{{Id: "new-live"}}}
	m.PutAgentForTest("live", replacement)
	require.NotNil(t, optionids.GroupByID(m.LiveOptionGroups("live", staticCatalogProvider), "new-live"))
	resume()
	select {
	case groups := <-result:
		assert.Empty(t, groups, "the old provider cannot publish after its replacement")
	case <-time.After(30 * time.Second):
		t.Fatal("The old live catalog sample did not finish.")
	}
	cached, _, _ := m.CachedCatalogForTest("live")
	assert.NotNil(t, optionids.GroupByID(cached, "new-live"), "the old sample cannot replace the new provider's cache")
	assert.Nil(t, optionids.GroupByID(cached, "old-live"))
}

func TestManager_LiveOptionGroupsRejectsTheRegisteredExitCallbackSlot(t *testing.T) {
	t.Parallel()

	m := agent.NewManager(catalogRegistry(), nil)
	result := make(chan []*leapmuxv1.AvailableOptionGroup, 1)
	m.SetOnExit(func(id string, _ int, _ error, _ bool) {
		assert.True(t, m.HasAgent(id), "the exit callback still owns the slot")
		result <- m.LiveOptionGroups(id, staticCatalogProvider)
	})
	p := &blockingStub{
		stubProvider: stubProvider{groups: []*leapmuxv1.AvailableOptionGroup{{Id: "ended-live"}}},
		waitCh:       make(chan struct{}),
	}
	_, err := m.StartAgentWith(context.Background(), agent.Options{AgentID: "ended", WorkingDir: t.TempDir()}, agenttest.Nop(),
		func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) { return p, nil })
	require.NoError(t, err)
	close(p.waitCh)
	select {
	case groups := <-result:
		assert.Empty(t, groups, "a retained exit slot supplies no live publication")
	case <-time.After(30 * time.Second):
		t.Fatal("The exit callback did not inspect the live catalog.")
	}
	m.StopAndWaitAgent("ended")
}

type startupCatalogProvider struct {
	blockingStub
	snapshot func() agent.SettingsApplyResult
}

func (p *startupCatalogProvider) SettingsSnapshot() agent.SettingsApplyResult { return p.snapshot() }

func TestManager_StartupCacheKeepsTheModelOfItsCatalogSample(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(catalogRegistry(), nil)
	groupsFor := func(model string) []*leapmuxv1.AvailableOptionGroup {
		return []*leapmuxv1.AvailableOptionGroup{{Id: agent.OptionIDModel, CurrentValue: model,
			Options: []*leapmuxv1.AvailableOption{{Id: "catalog-a"}, {Id: "catalog-b"}},
		}}
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	defer resume()
	p := &startupCatalogProvider{blockingStub: blockingStub{
		stubProvider: stubProvider{groups: groupsFor("catalog-a")}, waitCh: make(chan struct{}),
	}}
	exit := sync.OnceFunc(func() { close(p.waitCh) })
	t.Cleanup(func() { exit(); m.StopAndWaitAgent("live") })
	p.snapshot = func() agent.SettingsApplyResult {
		close(entered)
		<-release
		return p.stubProvider.SettingsSnapshot()
	}
	type startResult struct {
		options map[string]string
		err     error
	}
	result := make(chan startResult, 1)
	go func() {
		options, err := m.StartAgentWith(t.Context(), agent.Options{
			AgentID: "live", AgentProvider: modelDependentProvider, WorkingDir: t.TempDir(),
		}, agenttest.Nop(), func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) { return p, nil })
		result <- startResult{options: options, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("Startup did not read settings after its catalog sample.")
	}
	// The native model changes after the catalog sample and before the settings sample.
	p.groups = groupsFor("catalog-b")
	resume()
	select {
	case started := <-result:
		require.NoError(t, started.err)
		assert.Equal(t, "catalog-b", started.options[agent.OptionIDModel], "startup still returns the independently confirmed settings")
	case <-time.After(30 * time.Second):
		t.Fatal("Startup did not finish after the settings sample resumed.")
	}
	cached, model, present := m.CachedCatalogForTest("live")
	require.True(t, present)
	assert.Equal(t, "catalog-a", optionids.CurrentValue(cached, agent.OptionIDModel))
	assert.Equal(t, "catalog-a", model, "the cache stamp must describe the same sample as the cached groups")
}

func TestManager_StartupCacheKeepsAnUnknownModelStampWithoutAModelGroup(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(catalogRegistry(), nil)
	p := &startupCatalogProvider{blockingStub: blockingStub{
		stubProvider: stubProvider{groups: []*leapmuxv1.AvailableOptionGroup{{Id: "other-axis"}}}, waitCh: make(chan struct{}),
	}, snapshot: func() agent.SettingsApplyResult {
		return agent.ConfirmedSettings(map[string]string{agent.OptionIDModel: "confirmed-only"})
	}}
	exit := sync.OnceFunc(func() { close(p.waitCh) })
	t.Cleanup(func() { exit(); m.StopAndWaitAgent("live") })
	options, err := m.StartAgentWith(t.Context(), agent.Options{
		AgentID: "live", AgentProvider: modelDependentProvider, WorkingDir: t.TempDir(),
	}, agenttest.Nop(), func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) { return p, nil })
	require.NoError(t, err)
	assert.Equal(t, "confirmed-only", options[agent.OptionIDModel])
	cached, model, present := m.CachedCatalogForTest("live")
	require.True(t, present)
	assert.Nil(t, optionids.GroupByID(cached, agent.OptionIDModel), "a model stamp cannot invent a catalog group")
	assert.Empty(t, model, "a catalog without a model group has no known model stamp")
}

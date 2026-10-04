package kimi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func effortIDs(efforts []*agent.EffortInfo) []string {
	return agenttest.OptionIDs(efforts)
}

func TestKimiModelEfforts(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		item kimiModelItem
		want []string
	}{
		{"an effort ladder, strongest first", kimiModelItem{Capabilities: []string{"thinking"}, SupportEfforts: []string{"low", "high", "medium"}},
			[]string{agent.EffortAuto, "high", "medium", "low", "off"}},
		{"a model that thinks with no ladder", kimiModelItem{Capabilities: []string{"thinking"}},
			[]string{agent.EffortAuto, "on", "off"}},
		{"a model that always thinks", kimiModelItem{Capabilities: []string{"always_thinking"}, SupportEfforts: []string{"high"}},
			[]string{agent.EffortAuto, "high"}},
		{"a model that always thinks with no ladder", kimiModelItem{Capabilities: []string{"always_thinking"}},
			[]string{agent.EffortAuto, "on"}},
		{"on and off in the ladder are not levels", kimiModelItem{Capabilities: []string{"thinking"}, SupportEfforts: []string{"on", " ", "off", "low"}},
			[]string{agent.EffortAuto, "low", "off"}},
		{"a ladder with no capability", kimiModelItem{SupportEfforts: []string{"high"}},
			[]string{agent.EffortAuto, "high", "off"}},
		{"a model that cannot think", kimiModelItem{Capabilities: []string{"image_in"}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			efforts := kimiModelEfforts(tc.item)
			if tc.want == nil {
				assert.Nil(t, efforts, "a model that cannot think takes no level at all")
				return
			}
			assert.Equal(t, tc.want, effortIDs(efforts))
		})
	}
}

func TestBuildKimiCatalog(t *testing.T) {
	t.Parallel()

	catalog := buildKimiCatalog([]kimiModelItem{
		{Model: "kimi-k2", DisplayName: "Kimi K2", MaxContextSize: 262144, Capabilities: []string{"thinking", "image_in"}},
		{Model: " kimi-text ", Capabilities: nil},
		{Model: "  "},
	}, "kimi-text")
	require.Len(t, catalog.models, 2, "an entry with no id is skipped")
	assert.Equal(t, "kimi-k2", catalog.models[0].Id)
	assert.Equal(t, "Kimi K2", catalog.models[0].DisplayName)
	assert.EqualValues(t, 262144, catalog.models[0].ContextWindow)
	assert.Equal(t, agent.EffortAuto, catalog.models[0].DefaultEffort)
	assert.False(t, catalog.models[0].IsDefault)
	assert.Equal(t, "kimi-text", catalog.models[1].Id)
	assert.Equal(t, "kimi-text", catalog.models[1].DisplayName, "a model with no name shows its id")
	assert.True(t, catalog.models[1].IsDefault)
	assert.Equal(t, "kimi-text", catalog.defaultModel)
	assert.True(t, catalog.takesImages("kimi-k2"))
	assert.False(t, catalog.takesImages("kimi-text"))
	assert.False(t, catalog.takesImages("unknown"))
	assert.True(t, catalog.has("kimi-k2"))
	assert.False(t, catalog.has(""))
	assert.Nil(t, catalog.model("unknown"))

	unlisted := buildKimiCatalog([]kimiModelItem{{Model: "kimi-k2"}}, "gone-model")
	assert.Empty(t, unlisted.defaultModel, "a configured default the catalog does not list is no default")
}

func TestKimiLaunchModel(t *testing.T) {
	t.Parallel()

	catalog := buildKimiCatalog([]kimiModelItem{{Model: "a"}, {Model: "b"}}, "b")
	assert.Equal(t, "a", catalog.launchModel("a"), "the launch's own model wins")
	assert.Equal(t, "b", catalog.launchModel("missing"), "an unlisted request falls back to the default")
	assert.Equal(t, "b", catalog.launchModel(""))

	noDefault := buildKimiCatalog([]kimiModelItem{{Model: "a"}, {Model: "b"}}, "")
	assert.Equal(t, "a", noDefault.launchModel(""), "with no default the first model runs")

	assert.Empty(t, kimiCatalog{}.launchModel("a"), "an empty catalog binds nothing")
}

func TestLoadKimiCatalog(t *testing.T) {
	t.Parallel()

	t.Run("reads the thinking defaults", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		catalog, err := rig.agent.loadKimiCatalog(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "kimi-k2", catalog.defaultModel)
		require.NotNil(t, catalog.thinking.Enabled)
		assert.True(t, *catalog.thinking.Enabled)
		assert.Equal(t, "medium", catalog.thinking.Effort)
	})

	t.Run("ignores a thinking table it cannot read", func(t *testing.T) {
		t.Parallel()
		fake, server := newFakeKap(t)
		fake.mu.Lock()
		fake.config = map[string]any{"default_model": "kimi-k2", "thinking": "sometimes"}
		fake.mu.Unlock()
		rig := connectKimiTestRig(t, fake, server.URL, agent.Options{})
		rig.agent.Mu.Lock()
		thinking := rig.agent.catalog.thinking
		rig.agent.Mu.Unlock()
		assert.Equal(t, kimiThinkingDefaults{}, thinking)
	})

	t.Run("a configuration with no thinking table leaves Auto on the model's default", func(t *testing.T) {
		t.Parallel()
		fake, server := newFakeKap(t)
		fake.mu.Lock()
		fake.config = map[string]any{"default_model": " kimi-text "}
		fake.mu.Unlock()
		rig := connectKimiTestRig(t, fake, server.URL, agent.Options{})
		rig.agent.Mu.Lock()
		catalog := rig.agent.catalog
		rig.agent.Mu.Unlock()
		assert.Equal(t, kimiThinkingDefaults{}, catalog.thinking)
		assert.Equal(t, "kimi-text", catalog.defaultModel, "the configured default is read without its blanks")
		assert.Equal(t, "kimi-text", agent.CurrentOptions(rig.agent.OptionGroups())[agent.OptionIDModel])
	})

	t.Run("a configuration with no default runs the first model", func(t *testing.T) {
		t.Parallel()
		fake, server := newFakeKap(t)
		fake.mu.Lock()
		fake.config = map[string]any{}
		fake.mu.Unlock()
		rig := connectKimiTestRig(t, fake, server.URL, agent.Options{})
		assert.Equal(t, "kimi-k2", lastProfile(t, rig)["model"], "the server binds no model by itself, so the first one is bound")
	})

	t.Run("reports a model list the server refuses", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.fake.reply("GET "+kimiRouteModels, fakeKapReply{Code: 50000, Msg: "catalog down"})
		_, err := rig.agent.loadKimiCatalog(context.Background())
		require.ErrorContains(t, err, "catalog down")
	})

	t.Run("reports a configuration the server refuses", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.fake.reply("GET "+kimiRouteConfig, fakeKapReply{Code: 50000, Msg: "config down"})
		_, err := rig.agent.loadKimiCatalog(context.Background())
		require.ErrorContains(t, err, "config down")
	})
}

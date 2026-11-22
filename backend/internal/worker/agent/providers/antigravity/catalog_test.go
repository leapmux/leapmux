package antigravity

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAntigravityCatalogEffortSplit(t *testing.T) {
	for _, tc := range []struct{ id, family, effort string }{
		{id: "gemini-3.8-flash-high", family: "gemini-3.8-flash", effort: "high"},
		{id: "claude-opus-4-6-thinking", family: "claude-opus-4-6-thinking"},
		{id: "claude-sonnet-4-6", family: "claude-sonnet-4-6"},
		{id: "gpt-oss-120b-medium", family: "gpt-oss-120b", effort: "medium"},
		{id: "gemini-max", family: "gemini", effort: "max"},
	} {
		family, effort := splitModelEffort(tc.id)
		assert.Equal(t, tc.family, family, tc.id)
		assert.Equal(t, tc.effort, effort, tc.id)
	}
}

func TestAntigravityCatalogDropsDisabledAndResolvesDefault(t *testing.T) {
	catalog := buildAntigravityCatalog(antigravityCatalogReply{ClientModelConfigs: []antigravityModelConfig{
		{ModelID: "first-low", Label: "First", ModelOrAlias: struct {
			Model string `json:"model"`
		}{Model: "FIRST"}},
		{ModelID: "disabled-high", Disabled: true, ModelOrAlias: struct {
			Model string `json:"model"`
		}{Model: "DISABLED"}},
	}})
	assert.Equal(t, "FIRST", catalog.defaultToken)
	assert.NotContains(t, catalog.variants, "disabled")
	variant, effort, err := catalog.resolveModel(agent.DefaultModelSentinel, "")
	require.NoError(t, err)
	assert.Equal(t, "FIRST", variant.token)
	assert.Empty(t, effort)
	_, _, err = catalog.resolveModel("unknown", "")
	assert.ErrorContains(t, err, "unknown is not in the Antigravity catalog")
}

func TestAntigravityEffortFallbackPrefersLowerOnATie(t *testing.T) {
	family := map[string]catalogVariant{"low": {token: "LOW"}, "high": {token: "HIGH"}}
	assert.Equal(t, "low", settleAntigravityEffort(family, "medium"))
	assert.Equal(t, "high", settleAntigravityEffort(family, "max"))
}

func TestAntigravityStrictConfigIsModeIndependent(t *testing.T) {
	first := cascadeConfigForToken("MODEL", "/workspace")
	second := cascadeConfigForToken("MODEL", "/workspace")
	assert.Equal(t, first, second)
	planner := first["plannerConfig"].(map[string]any)
	assert.Equal(t, "MODEL", planner["planModel"])
	tools := planner["toolConfig"].(map[string]any)
	permissions := tools["permissionConfig"].(map[string]any)
	assert.Equal(t, map[string]any{"allow": []string{"read_file(/workspace)"}}, permissions["effectiveGrants"])
	assert.Equal(t, false, tools["autoAllowAllInteractions"])
}

func TestAntigravityCatalogPreservesServerOrder(t *testing.T) {
	var reply antigravityCatalogReply
	require.NoError(t, json.Unmarshal([]byte(`{"clientModelConfigs":[{"modelId":"z-low","label":"Z","modelOrAlias":{"model":"ZL"}},{"modelId":"a","modelOrAlias":{"model":"A"}},{"modelId":"z-high","modelOrAlias":{"model":"ZH"}},{"modelId":"m","modelOrAlias":{"model":"M"}}]}`), &reply))
	for range 64 {
		catalog := buildAntigravityCatalog(reply)
		var ids []string
		for _, model := range catalog.models {
			ids = append(ids, model.Id)
		}
		assert.Equal(t, []string{agent.DefaultModelSentinel, "z", "a", "m"}, ids)
	}
}

func TestAntigravityCatalogSeparatesModelLabelsAndEffortTokens(t *testing.T) {
	var reply antigravityCatalogReply
	require.NoError(t, json.Unmarshal([]byte(`{"clientModelConfigs":[
		{"modelId":"gemini-4-neon-high","label":"Gemini 4 Neon (High)","modelOrAlias":{"model":"NEON_HIGH"}},
		{"modelId":"gemini-4-neon-xhigh","label":"Gemini 4 Neon (XHigh)","modelOrAlias":{"model":"NEON_XHIGH"}},
		{"modelId":"claude-opus-5-5-low","label":"Claude Opus 5.5 (Low)","modelOrAlias":{"model":"OPUS_LOW"}},
		{"modelId":"other","label":"Other (High)","modelOrAlias":{"model":"OTHER"}}
	]}`), &reply))
	catalog := buildAntigravityCatalog(reply)
	require.Len(t, catalog.models, 4)
	assert.Equal(t, "Gemini 4 Neon", catalog.models[1].DisplayName)
	assert.Equal(t, "Claude Opus 5.5", catalog.models[2].DisplayName)
	assert.Equal(t, "Other (High)", catalog.models[3].DisplayName)
	for _, effort := range []string{"high", "xhigh"} {
		variant, selected, err := catalog.resolveModel("gemini-4-neon", effort)
		require.NoError(t, err)
		assert.Equal(t, effort, selected)
		assert.Equal(t, map[string]string{"high": "NEON_HIGH", "xhigh": "NEON_XHIGH"}[effort], variant.token)
	}
}

func TestAntigravityQuotaRefreshCannotDelayInactiveTurn(t *testing.T) {
	a, sink := testAgent(t)
	entered := make(chan struct{})
	release, unblock := newTestGate(t)
	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == languageServerService+"RetrieveUserQuotaSummary" {
			close(entered)
			<-release
		}
		_, _ = w.Write([]byte(`{}`))
	})
	a.beginTurn(a.root)
	done := make(chan error, 1)
	go func() { done <- a.endTurn(a.root, agent.MessageCompletionComplete) }()
	receiveTestValue(t, entered)
	states := sink.TurnStates()
	require.NotEmpty(t, states)
	assert.Equal(t, agent.TurnState{}, states[len(states)-1], "quota I/O must start after inactive state publication")
	unblock()
	require.NoError(t, receiveTestValue(t, done))
}

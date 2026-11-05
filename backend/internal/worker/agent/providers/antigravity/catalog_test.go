package antigravity

import (
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

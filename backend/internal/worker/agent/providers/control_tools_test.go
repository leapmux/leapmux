package providers

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/claude"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/codex"
	"github.com/stretchr/testify/assert"
)

// TestOnlyClaudeSelfDisplaysAControlTool pins that every provider but Claude
// echoes no control answer, and so defers to the synthesized display row. Claude's
// own set is pinned by TestIsSelfDisplayingControlTool in its package.
func TestOnlyClaudeSelfDisplaysAControlTool(t *testing.T) {
	t.Parallel()

	registry := Registry()
	for _, provider := range registry.Providers() {
		if provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE {
			continue
		}
		plugin := registry.Plugin(provider)
		for _, tool := range []string{claude.ToolNameAskUserQuestion, claude.ToolNameExitPlanMode} {
			assert.Falsef(t, plugin.IsSelfDisplayingControlTool(tool), "%s: %s", provider, tool)
		}
	}
	assert.False(t, agent.ProviderDefaults{}.IsSelfDisplayingControlTool(claude.ToolNameAskUserQuestion))
}

// TestOnlyCodexSynthesizesAnInterruptNotice pins that every provider but Codex
// surfaces its interrupt in its own transcript, so it returns no synthetic row
// text. Codex's text is pinned by TestSyntheticInterruptNotice in its package.
func TestOnlyCodexSynthesizesAnInterruptNotice(t *testing.T) {
	t.Parallel()

	registry := Registry()
	for _, provider := range registry.Providers() {
		if provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX {
			continue
		}
		assert.Emptyf(t, registry.Plugin(provider).SyntheticInterruptNotice(), "%s", provider)
	}
	assert.Empty(t, agent.ProviderDefaults{}.SyntheticInterruptNotice())
}

// TestOnlyThePlanModeProvidersReadAPlanModeTool pins provider-owned tool-name
// interpretation: a provider without a plan mode classifies no plan-mode tool name.
// Shared service code consumes only these provider-neutral classifications, so
// provider wire names do not leak back into service-level plan-mode policy.
//
// Claude, Codex, ZCode, Kimi Code and MiMo Code each pin their own reading in their
// own package. ZCode and Kimi Code are here although their tools are their own: their
// names equal Claude's. MiMo reads its own `plan_exit` approval.
func TestOnlyThePlanModeProvidersReadAPlanModeTool(t *testing.T) {
	t.Parallel()

	planModeProviders := map[leapmuxv1.AgentProvider]bool{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE: true,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX:       true,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE:       true,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KIMI_CODE:   true,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE:   true,
	}
	registry := Registry()
	tools := []string{claude.ToolNameEnterPlanMode, claude.ToolNameExitPlanMode, codex.ToolNamePlanModePrompt}
	for _, provider := range registry.Providers() {
		if planModeProviders[provider] {
			continue
		}
		for _, tool := range tools {
			assert.Equalf(t, agent.PlanModeControlNone, registry.Plugin(provider).PlanModeControl(tool), "%s: %s", provider, tool)
		}
	}
	for _, tool := range tools {
		assert.Equal(t, agent.PlanModeControlNone, agent.ProviderDefaults{}.PlanModeControl(tool), tool)
	}
}

// Every ACP provider that is NOT Cursor must leave a create-plan answer alone. The
// named type is what states that, and the registry is what selects it.
func TestOnlyCursorTransformsACreatePlanAnswer(t *testing.T) {
	t.Parallel()

	registry := Registry()
	response := []byte(`{"response":{"request_id":"7","response":{"behavior":"deny","message":"Needs tests."}}}`)
	request := []byte(`{"jsonrpc":"2.0","id":7,"method":"cursor/create_plan","params":{}}`)
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_QWEN_CODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GROK_BUILD,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KIRO,
	} {
		t.Run(provider.String(), func(t *testing.T) {
			t.Parallel()
			res := registry.Plugin(provider).ResolveControlResponse(agent.ControlResponseContext{
				RequestPayload: request, ResponseContent: response,
			})
			assert.Equal(t, response, res.Content)
		})
	}
	res := registry.Plugin(leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR).ResolveControlResponse(agent.ControlResponseContext{
		RequestPayload: request, ResponseContent: response,
	})
	assert.NotEqual(t, response, res.Content, "Cursor answers create_plan with its own outcome")
}

// TestOnlyCodexHasLocalPlanApprovalSettings pins that no provider but Codex
// states local settings for a plan approval. Codex's own settings are pinned by
// TestPlanApprovalOptions_PerProvider in its package.
func TestOnlyCodexHasLocalPlanApprovalSettings(t *testing.T) {
	t.Parallel()

	registry := Registry()
	for _, provider := range append(agentlabels.AllProviders(), leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED) {
		if provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX {
			continue
		}
		assert.Empty(t, registry.Plugin(provider).PlanApprovalOptions(""), "provider %v has no local plan approval settings", provider)
	}
}

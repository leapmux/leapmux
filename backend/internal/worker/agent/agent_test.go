package agent

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAvailableOptionGroups_DefaultOptionMetadata(t *testing.T) {
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
	} {
		groups := AvailableOptionGroupsForProvider(provider)
		require.NotEmpty(t, groups)
		for _, group := range groups {
			defaults := 0
			for _, option := range group.Options {
				if option.IsDefault {
					defaults++
				}
			}
			assert.Equalf(t, 1, defaults, "provider=%s group=%s should expose exactly one default option", provider.String(), group.Key)
		}
	}
}

// TestDisplayName keeps the backend label mapping in lockstep with the
// frontend's agentProviderLabel (frontend/src/components/common/
// AgentProviderIcon.tsx). When a new provider is added to the
// AgentProvider enum, both sides should be updated together so
// "Starting {provider}…" renders consistently.
func TestDisplayName(t *testing.T) {
	cases := []struct {
		provider leapmuxv1.AgentProvider
		want     string
	}{
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "Claude Code"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, "Codex"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI, "Gemini CLI"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, "OpenCode"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT, "GitHub Copilot"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, "Cursor"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, "Goose"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO, "Kilo"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED, "agent"},
		{leapmuxv1.AgentProvider(9999), "agent"}, // unknown value → generic fallback
	}
	for _, tc := range cases {
		t.Run(tc.provider.String(), func(t *testing.T) {
			assert.Equal(t, tc.want, DisplayName(tc.provider))
		})
	}
}

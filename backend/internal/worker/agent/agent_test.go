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
		leapmuxv1.AgentProvider_AGENT_PROVIDER_COPILOT_CLI,
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

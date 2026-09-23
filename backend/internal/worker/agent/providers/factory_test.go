package providers

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAvailableOptionGroups_DefaultOptionMetadata(t *testing.T) {
	t.Parallel()

	registry := Registry()

	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
	} {
		groups := registry.StaticOptionGroups(provider)
		require.NotEmpty(t, groups)
		for _, group := range groups {
			// The default now lives on the group (DefaultValue) instead of a
			// per-option IsDefault flag. A group may omit it (the ACP
			// primary-agent/permission-mode groups rely on the "first option"
			// convention), but when set it must name exactly one of the options.
			if group.GetDefaultValue() == "" {
				continue
			}
			defaults := 0
			for _, option := range group.Options {
				if option.GetId() == group.GetDefaultValue() {
					defaults++
				}
			}
			assert.Equalf(t, 1, defaults, "provider=%s group=%s default value %q must name exactly one option", provider.String(), group.GetId(), group.GetDefaultValue())
		}
	}
}

// A provider's SAFE new-session mode must never be the mode its own bypass preset
// selects, and neither must its fallback for a session that stored none. Goose shipped
// exactly that: its fallback was `auto`, which is also its declared bypass, so every
// resumed Goose session opened with permission prompts disabled.
//
// The bypass values are the frontend plugins' (providers/*/plugin.tsx and stubs/*.tsx),
// which Go cannot import, so they are restated here. That is the point: this test is the
// thing that fails when the two drift.
func TestSafePermissionDefaultsAreNeverAProviderBypassMode(t *testing.T) {
	t.Parallel()

	registry := Registry()

	bypassModes := map[leapmuxv1.AgentProvider]string{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE: contracts.ClaudeModeBypassPermissions,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE:       contracts.GooseModeAuto,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE:       contracts.ZCodeModeYolo,
	}
	for provider, bypass := range bypassModes {
		t.Run(provider.String(), func(t *testing.T) {
			if safe := registry.NewAgentOptionDefaults(provider)[agent.OptionIDPermissionMode]; safe != "" {
				assert.NotEqual(t, bypass, safe,
					"a new session must not open in the mode the bypass shortcut selects")
			}
			assert.NotEqual(t, bypass, registry.FallbackPermissionMode(provider),
				"a session that stored no mode must not fall back to the bypass mode")
		})
	}
}

package providers

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
	"github.com/leapmux/leapmux/internal/worker/agent"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAvailableOptionGroups_DefaultOptionMetadata(t *testing.T) {
	t.Parallel()

	registry := Registry()

	for _, provider := range agentlabels.AllProviders() {
		t.Run(provider.String(), func(t *testing.T) {
			groups := registry.StaticOptionGroups(provider)
			if provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_PI || provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX {
				assert.Empty(t, groups, "this provider discovers its groups at runtime")
				return
			}
			require.NotEmpty(t, groups, "the provider must declare its static groups")
			for _, group := range groups {
				require.NotNil(t, group)
				// The group owns DefaultValue. An ACP group may use its first
				// option instead. A stated default must select exactly one option.
				if group.GetDefaultValue() == "" {
					continue
				}
				defaults := 0
				for _, option := range group.Options {
					if option.GetId() == group.GetDefaultValue() {
						defaults++
					}
				}
				assert.Equalf(t, 1, defaults, "provider=%s group=%s default value %q must select exactly one option", provider, group.GetId(), group.GetDefaultValue())
			}
		})
	}
}

// TestNormalizeModelIDRoutesEveryProvider checks the registry path for each
// provider. The three providers with aliases use inputs that must change.
func TestNormalizeModelIDRoutesEveryProvider(t *testing.T) {
	t.Parallel()

	registry := Registry()
	cases := map[leapmuxv1.AgentProvider]struct{ input, want string }{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE:    {"opus", "opus[1m]"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX:          {"model/alpha", "model/alpha"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR:         {"default[]", "auto"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT: {"model/alpha", "model/alpha"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO:           {"model/alpha", "model/alpha"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE:       {"model/alpha", "model/alpha"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE:          {"model/alpha", "model/alpha"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_PI:             {"model/alpha", "model/alpha"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX:       {"model/alpha", "model/alpha"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE:          {`p\m`, "p/m"},
	}
	providers := agentlabels.AllProviders()
	require.Len(t, cases, len(providers), "each provider needs a normalization case")
	for _, provider := range providers {
		t.Run(provider.String(), func(t *testing.T) {
			tc, ok := cases[provider]
			require.True(t, ok, "the provider needs a normalization case")
			assert.Equal(t, tc.want, registry.NormalizeModelID(provider, tc.input))
		})
	}
	assert.Equal(t, "model/alpha", registry.NormalizeModelID(leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED, "model/alpha"))
}

// A provider's safe new-session mode and fallback must not equal its bypass
// mode. Goose once used `auto` for both fallback and bypass, so resumed sessions
// skipped permission prompts.
//
// Go cannot import the frontend bypass presets. This table records their
// current values. A change to a frontend preset must update this table.
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

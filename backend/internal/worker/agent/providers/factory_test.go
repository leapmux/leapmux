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
			if provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_PI || provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX || provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_FAST_AGENT {
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
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEWHALE:      {"deepseek-ai/DeepSeek-V4-Pro", "deepseek-ai/DeepSeek-V4-Pro"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KIMI_CODE:      {"model/alpha", "model/alpha"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE:      {"model/alpha", "model/alpha"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_QWEN_CODE:      {"mock-model(openai)", "mock-model(openai)"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OH_MY_PI:       {"model/alpha", "model/alpha"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GROK_BUILD:     {"grok-4.6", "grok-4.6"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KIRO:           {"claude-sonnet-4.5", "claude-sonnet-4.5"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP:            {"model/alpha", "model/alpha"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLINE:          {"anthropic/claude-sonnet-4.6", "anthropic/claude-sonnet-4.6"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEBUDDY:      {"model/alpha", "model/alpha"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_QODER:          {"model/alpha", "model/alpha"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_LETTA:          {"openai-compatible/mock-model", "openai-compatible/mock-model"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_DROID:          {"custom:Mock-0", "custom:Mock-0"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_JUNIE:          {"model/alpha", "model/alpha"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC:          {"model/alpha", "model/alpha"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_FAST_AGENT:     {"model/alpha", "model/alpha"},
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
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEWHALE:   contracts.CodewhalePostureFullAccess,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KIMI_CODE:   contracts.KimiModeAuto,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_QWEN_CODE:   contracts.QwenModeYolo,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OH_MY_PI:    contracts.OhMyPiApprovalModeYolo,
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

// Grok Build's and Kiro's bypass presets set a provider option rather than the
// permission mode -- Grok's approval mode, Kiro's policy preset -- so the same
// rule applies to the value of that option that every new agent takes.
//
// Go cannot import the frontend bypass presets. This table records their
// current values. A change to a frontend preset must update this table.
func TestProviderOptionDefaultsAreNeverABypassValue(t *testing.T) {
	t.Parallel()

	registry := Registry()
	bypassValues := map[leapmuxv1.AgentProvider]struct{ option, bypass string }{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GROK_BUILD: {contracts.GrokOptionApprovalMode, contracts.GrokApprovalModeAlwaysApprove},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KIRO:       {contracts.KiroOptionPolicyPreset, contracts.KiroPolicyPresetAllowAll},
	}
	for provider, tc := range bypassValues {
		t.Run(provider.String(), func(t *testing.T) {
			defaults := registry.ProviderOptionDefaults(provider)
			require.Contains(t, defaults, tc.option)
			assert.NotEqual(t, tc.bypass, defaults[tc.option],
				"a new agent must not open in the value the bypass shortcut selects")
		})
	}
}

package providers

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProviderManagesEffort distinguishes providers whose effort tiers belong to the
// MODEL (effort default stamped by resolveProviderDefaults) from every other provider,
// whose effort, if any, is server-driven and model-independent.
//
// Claude, Codex and Pi state their tiers in a static catalog. Native Copilot has no
// static catalog -- the account decides which models exist -- so it raises managesEffort
// in its init() instead; a model switch must still rebuild its tiers.
//
// The two sets are a PARTITION of the generated provider table, so the "false" side is
// derived rather than retyped: a provider added later lands in it automatically, and a
// provider that starts carrying static effort tiers fails here until it is declared.
// ZCode is on the false side although it has real per-model thought levels, because its
// static catalog is empty by design -- every level comes from the user's own
// configuration, so there is no default for LeapMux to stamp at launch.
func TestProviderManagesEffort(t *testing.T) {
	t.Parallel()

	registry := Registry()

	managed := map[leapmuxv1.AgentProvider]bool{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE:    true,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX:          true,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_PI:             true,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT: true,
	}
	for _, p := range agentlabels.AllProviders() {
		if managed[p] {
			assert.Truef(t, registry.ManagesEffort(p), "%v owns model-dependent effort tiers", p)
			continue
		}
		assert.Falsef(t, registry.ManagesEffort(p), "%v has no leapmux-managed effort default", p)
	}
}

// Permission-mode validation asks its OWN question, not the effort predicate's.
//
// The two agreed only by coincidence, and the moment native Copilot declared a
// model-dependent effort catalog it silently gained the authority to REJECT a
// permission mode. Pi is where the difference shows: it manages effort and
// declares no permission modes at all, so the shared predicate admitted it and
// then accepted anything, because an empty group accepts every value.
func TestValidateLaunchOptionsAsksTheProviderItsOwnPermissionQuestion(t *testing.T) {
	t.Parallel()

	registry := Registry()

	for _, tc := range []struct {
		provider         leapmuxv1.AgentProvider
		fixedModes       bool
		managesEffort    bool
		rejectsAnUnknown bool
	}{
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, true, true, true},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, true, true, true},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT, true, true, true},
		// Manages effort, states no permission enum of its own.
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, false, true, false},
		// An ACP provider discovers its modes from the daemon.
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, false, false, false},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, false, false, false},
	} {
		t.Run(tc.provider.String(), func(t *testing.T) {
			assert.Equal(t, tc.fixedModes, registry.HasFixedPermissionModes(tc.provider),
				"permission-mode authority")
			assert.Equal(t, tc.managesEffort, registry.ManagesEffort(tc.provider),
				"effort-catalog question, which must stay separate")

			err := registry.ValidateLaunchOptions(tc.provider, optionmap.Map{agent.OptionIDPermissionMode: "not-a-mode"})
			if tc.rejectsAnUnknown {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "not valid for this provider")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

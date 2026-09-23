package providers

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
)

// TestReportsDefaultModelSentinel pins the per-provider answer that
// defaultModelIDForList dispatches on. Only Claude Code's own catalog lists the
// sentinel as a selectable entry, so only Claude gives it the default badge. For
// every other provider "default" is an ordinary model id: Codex resolves the
// sentinel to a concrete model at startup, and ZCode states every model
// explicitly. The sweep covers every registered provider, so a new provider
// cannot miss the decision.
func TestReportsDefaultModelSentinel(t *testing.T) {
	t.Parallel()

	assert.False(t, agent.ProviderDefaults{}.ReportsDefaultModelSentinel(), "a provider must opt in, not inherit Claude's answer")
	registry := Registry()
	for _, provider := range registry.Providers() {
		want := provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
		assert.Equal(t, want, registry.Plugin(provider).ReportsDefaultModelSentinel(), "provider %s", provider)
	}
}

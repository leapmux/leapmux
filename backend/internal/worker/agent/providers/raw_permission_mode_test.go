package providers

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
)

// TestOnlyClaudeReadsAPermissionModeFromRawInput pins that only Claude speaks
// set_permission_mode. The exact Claude-shaped payload extracts nothing for
// every other registered provider, so a stray frame to a non-Claude agent falls
// through to the generic forward path instead of an eager database write.
func TestOnlyClaudeReadsAPermissionModeFromRawInput(t *testing.T) {
	t.Parallel()

	claudePayload := `{"type":"control_request","request":{"subtype":"set_permission_mode","mode":"bypassPermissions"}}`
	registry := Registry()
	for _, provider := range registry.Providers() {
		mode, ok := registry.Plugin(provider).PermissionModeFromRawInput(claudePayload)
		if provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE {
			assert.True(t, ok, "Claude reads its own payload")
			assert.Equal(t, "bypassPermissions", mode)
			continue
		}
		assert.Falsef(t, ok, "%s", provider)
		assert.Emptyf(t, mode, "%s", provider)
	}
	mode, ok := agent.ProviderDefaults{}.PermissionModeFromRawInput(claudePayload)
	assert.False(t, ok)
	assert.Empty(t, mode)
}

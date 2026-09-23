package claude

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
)

// claudeTestRegistry is a registry of Claude alone, for a test that runs a
// manager. Nothing in this package needs another provider's registration.
var claudeTestRegistry = agenttest.MustNewRegistry(Registration())

// TestNormalizeModelID_FromRegistry verifies that NormalizeModelID routes
// through Claude's registered normalizer (the same one the live agent uses)
// rather than a hand-maintained switch.
func TestNormalizeModelID_FromRegistry(t *testing.T) {
	t.Parallel()

	registry := agenttest.MustNewRegistry(Registration())
	// Claude collapses its fully-qualified CLI id into the alias space.
	const claudeFull = "claude-opus-4-8[1m]"
	assert.Equal(t, normalizeClaudeCodeModel(claudeFull),
		registry.NormalizeModelID(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, claudeFull))
	assert.NotEqual(t, claudeFull,
		registry.NormalizeModelID(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, claudeFull),
		"Claude's normalizer must actually collapse the id")

	// A legacy bare "opus" canonicalizes to "opus[1m]" through the registry path too
	// (Opus is 1M-only; the standard-context alias no longer resolves on its own).
	assert.Equal(t, "opus[1m]",
		registry.NormalizeModelID(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "opus"))
}

package agent_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestProviderFor_NoopFallback(t *testing.T) {
	t.Parallel()

	// UNSPECIFIED has no registered plugin, so the registry returns the
	// noop plugin: Classify produces empty, Merge keeps the newer entry,
	// IsInterrupt returns false.
	plugin := testRegistry.Plugin(leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED)
	class := plugin.Classify(json.RawMessage(`{"method":"mcpServer/startupStatus/updated","params":{"name":"codex_apps"}}`))
	assert.False(t, class.Consolidatable())

	merged, err := plugin.Merge(agent.NotificationClassification{}, json.RawMessage(`{"a":1}`), json.RawMessage(`{"a":2}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"a":2}`, string(merged))

	assert.False(t, plugin.IsInterrupt(`{"type":"abort"}`))
}

// TestNoopProviderListsNothing pins the default a provider inherits by saying
// nothing: it offers nothing, and does not fail.
func TestNoopProviderListsNothing(t *testing.T) {
	t.Parallel()
	sessions, err := agent.ProviderDefaults{}.ListStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: "/some/dir",
	})
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

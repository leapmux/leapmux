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
	// default plugin. Classify returns an empty classification, and IsInterrupt returns false.
	plugin := testRegistry.Plugin(leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED)
	class := plugin.Classify(json.RawMessage(`{"method":"mcpServer/startupStatus/updated","params":{"name":"codex_apps"}}`))
	assert.False(t, class.Consolidatable())

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

func TestNotificationClassificationConstructors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		class agent.NotificationClassification
		kind  agent.NotificationKind
		key   string
	}{
		{agent.NotificationClassification{}, agent.NotificationKindNone, ""},
		{agent.ProviderScopedNotification(""), agent.NotificationKindNone, ""},
		{agent.StatusNotification(), agent.NotificationKindStatus, ""},
		{agent.APIRetryNotification(), agent.NotificationKindAPIRetry, ""},
		{agent.CompactionBoundaryNotification(), agent.NotificationKindCompactionBoundary, ""},
		{agent.ProviderScopedNotification("provider:notice"), agent.NotificationKindProviderScoped, "provider:notice"},
	} {
		assert.Equal(t, tc.kind, tc.class.Kind())
		assert.Equal(t, tc.key, tc.class.Key())
		assert.Equal(t, tc.kind != agent.NotificationKindNone, tc.class.Consolidatable())
	}
}

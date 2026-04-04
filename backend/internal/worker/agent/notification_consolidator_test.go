package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestNotificationConsolidatorForProvider_CodexClassification(t *testing.T) {
	consolidator := NotificationConsolidatorForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)

	rateLimit := json.RawMessage(`{"method":"account/rateLimits/updated","params":{"foo":"bar"}}`)
	startup := json.RawMessage(`{"method":"mcpServer/startupStatus/updated","params":{"name":"codex_apps","status":"ready"}}`)

	assert.Equal(t, NotificationClassification{
		Kind: NotificationKindProviderScoped,
		Key:  "codex:account/rateLimits/updated",
	}, consolidator.Classify(rateLimit))

	assert.Equal(t, NotificationClassification{
		Kind: NotificationKindProviderScoped,
		Key:  "codex:mcpServer/startupStatus/updated:codex_apps",
	}, consolidator.Classify(startup))
}

func TestNotificationConsolidatorForProvider_ClaudeClassification(t *testing.T) {
	consolidator := NotificationConsolidatorForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	assert.Equal(t,
		NotificationClassification{Kind: NotificationKindStatus, Key: "claude:system:status"},
		consolidator.Classify(json.RawMessage(`{"type":"system","subtype":"status","status":"idle"}`)),
	)
	assert.Equal(t,
		NotificationClassification{Kind: NotificationKindAPIRetry, Key: "claude:system:api_retry"},
		consolidator.Classify(json.RawMessage(`{"type":"system","subtype":"api_retry","attempt":2}`)),
	)
	assert.Equal(t,
		NotificationClassification{Kind: NotificationKindCompactionBoundary, Key: "claude:system:compact_boundary"},
		consolidator.Classify(json.RawMessage(`{"type":"system","subtype":"compact_boundary"}`)),
	)
}

func TestNotificationConsolidatorForProvider_DefaultFallback(t *testing.T) {
	consolidator := NotificationConsolidatorForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE)
	class := consolidator.Classify(json.RawMessage(`{"method":"mcpServer/startupStatus/updated","params":{"name":"codex_apps"}}`))
	assert.False(t, class.Consolidatable())

	merged, err := consolidator.Merge(NotificationClassification{}, json.RawMessage(`{"a":1}`), json.RawMessage(`{"a":2}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"a":2}`, string(merged))
}

func TestIsNotificationThreadable_ClaudeSystemUsesConsolidator(t *testing.T) {
	assert.True(t, isNotificationThreadable([]byte(`{"type":"system","subtype":"status","status":"idle"}`), leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM))
	assert.True(t, isNotificationThreadable([]byte(`{"type":"system","subtype":"api_retry","attempt":1}`), leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM))
	assert.False(t, isNotificationThreadable([]byte(`{"type":"system","subtype":"other"}`), leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM))
}

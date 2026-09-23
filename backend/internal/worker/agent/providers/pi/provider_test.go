package pi

import (
	"encoding/json"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProviderFor_PiClassification(t *testing.T) {
	t.Parallel()

	registry := agenttest.MustNewRegistry(Registration())

	plugin := registry.Plugin(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI)

	assert.Equal(t,
		agent.NotificationClassification{Kind: agent.NotificationKindStatus, Key: "pi:compaction_start"},
		plugin.Classify(json.RawMessage(`{"type":"compaction_start","sessionId":"s1"}`)),
		"compaction_start is the in-progress indicator — Status so the latest collapses to one entry",
	)
	assert.Equal(t,
		agent.NotificationClassification{Kind: agent.NotificationKindCompactionBoundary, Key: "pi:compaction_end"},
		plugin.Classify(json.RawMessage(`{"type":"compaction_end","sessionId":"s1","summary":"compacted"}`)),
		"compaction_end is the boundary signal — each occurrence is preserved as a marker",
	)
	assert.Equal(t,
		agent.NotificationClassification{Kind: agent.NotificationKindAPIRetry, Key: "pi:auto_retry_start"},
		plugin.Classify(json.RawMessage(`{"type":"auto_retry_start","attempt":2}`)),
		"auto_retry_start mirrors Claude's api_retry — collapse to the latest attempt",
	)
	assert.Equal(t,
		agent.NotificationClassification{Kind: agent.NotificationKindAPIRetry, Key: "pi:auto_retry_end"},
		plugin.Classify(json.RawMessage(`{"type":"auto_retry_end","attempt":2}`)),
	)
	assert.False(t,
		plugin.Classify(json.RawMessage(`{"type":"extension_error","message":"plugin failed"}`)).Consolidatable(),
		"extension_error is per-error — must not consolidate so partial failures stay visible",
	)
	assert.False(t,
		plugin.Classify(json.RawMessage(`{"type":"some_future_event"}`)).Consolidatable(),
		"unknown event types must fall through unconsolidated, not panic",
	)
	assert.False(t,
		plugin.Classify(json.RawMessage(`not-json`)).Consolidatable(),
		"malformed JSON must produce an empty classification, not a panic",
	)
	assert.False(t,
		plugin.Classify(json.RawMessage(``)).Consolidatable(),
		"empty input must produce an empty classification",
	)

	// Merge mirrors Codex/Claude — last-write-wins with no field merge.
	merged, err := plugin.Merge(
		agent.NotificationClassification{Kind: agent.NotificationKindStatus, Key: "pi:compaction_start"},
		json.RawMessage(`{"type":"compaction_start","attempt":1}`),
		json.RawMessage(`{"type":"compaction_start","attempt":2}`),
	)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"compaction_start","attempt":2}`, string(merged))
}

package zcode

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
)

// The plugin states the child capabilities that the agent type implements. A
// subagent tab reads them before its root runs.
func TestPluginStatesTheChildCapabilitiesOfTheAgent(t *testing.T) {
	t.Parallel()
	agenttest.AssertChildCapabilities(t, Registration().Plugin, (*Agent)(nil))
}

func TestZCodeClassifiesNativeCompactionState(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reason string
		kind   agent.NotificationKind
	}{
		{contracts.ZCodeStateReasonCompactStarted, agent.NotificationKindStatus},
		{contracts.ZCodeStateReasonSessionCompacted, agent.NotificationKindCompactionBoundary},
		{contracts.ZCodeStateReasonSessionCompactFailed, agent.NotificationKindCompactionBoundary},
		{contracts.ZCodeStateReasonSessionCompactCancelled, agent.NotificationKindCompactionBoundary},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			raw := json.RawMessage(fmt.Sprintf(`{"method":%q,"params":{"reason":%q}}`, contracts.ZCodeMethodStateUpdated, tc.reason))
			assert.Equal(t, agent.NotificationClassification{Kind: tc.kind, Key: "zcode:compaction"}, Registration().Plugin.Classify(raw))
		})
	}
	assert.Equal(t, agent.NotificationClassification{}, Registration().Plugin.Classify(json.RawMessage(`{"method":"state.updated","params":{"reason":"model_changed"}}`)))
}

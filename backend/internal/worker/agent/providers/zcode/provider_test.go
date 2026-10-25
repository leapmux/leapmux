package zcode

import (
	"encoding/json"
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

// The worker folds the notifications that repeat. A compaction start and its end
// share one key so one row carries the whole rewrite; the retry notice and the
// steering queue flaps take the latest state only.
func TestZCodeClassifiesConsolidatableNotifications(t *testing.T) {
	t.Parallel()
	sessionUpdated := func(payload string) json.RawMessage {
		return json.RawMessage(`{"eventId":"e","sessionId":"sess-1","seq":1,"type":"` +
			contracts.ZCodeEventSessionUpdated + `","payload":` + payload + `}`)
	}
	for _, tc := range []struct {
		name    string
		payload string
		want    agent.NotificationClassification
	}{
		{"a started compaction", `{"operationId":"c1","status":"started","trigger":"auto"}`, agent.NotificationClassification{}},
		{"a retrying compaction", `{"operationId":"c1","status":"retrying","trigger":"auto","attempt":2,"maxAttempts":5}`, agent.NotificationClassification{}},
		{"a completed compaction", `{"operationId":"c1","status":"completed","trigger":"auto"}`, agent.CompactionBoundaryNotification()},
		{"a skipped compaction", `{"operationId":"c1","status":"skipped","trigger":"auto"}`, agent.CompactionBoundaryNotification()},
		{"a failed compaction", `{"operationId":"c1","status":"failed","trigger":"auto"}`, agent.CompactionBoundaryNotification()},
		{"an interrupted compaction", `{"operationId":"c1","status":"interrupted","trigger":"auto"}`, agent.CompactionBoundaryNotification()},
		{"a scheduled retry", `{"type":"model_retry_scheduled","requestId":"q1","_meta":{"zcode":{"apiRetry":{"kind":"api_retry","attempt":2}}}}`, agent.APIRetryNotification()},
		{"a discarded steer input", `{"pendingInputIds":["p1"],"reason":"user_removed","targetTurnId":"turn_1"}`, agent.ProviderScopedNotification("zcode:steer")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want,
				Registration().Plugin.Classify(sessionUpdated(tc.payload)))
		})
	}
	for name, raw := range map[string]json.RawMessage{
		"a goal verification":                        sessionUpdated(`{"verificationId":"v1","targetId":"g1","status":"completed"}`),
		"a workspace hook admission":                 sessionUpdated(`{"pendingCount":2,"bundleDigest":"abc"}`),
		"a goal change":                              sessionUpdated(`{"action":"set","source":"command","target":{"targetID":"g1"}}`),
		"a model response":                           sessionUpdated(`{"content":"Hi","querySource":"main_turn","stopReason":"stop"}`),
		"a compaction boundary":                      sessionUpdated(`{"boundaryId":"b1","trigger":"auto","phase":"pre_request"}`),
		"a compaction with an unknown status":        sessionUpdated(`{"operationId":"c1","status":"pending","trigger":"auto"}`),
		"a scheduled retry with no api-retry record": sessionUpdated(`{"type":"model_retry_scheduled","requestId":"q1","attempt":2}`),
		"an old state reason":                        json.RawMessage(`{"method":"` + contracts.ZCodeMethodStateUpdated + `","params":{"reason":"` + contracts.ZCodeStateReasonSessionCompacted + `"}}`),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, agent.NotificationClassification{}, Registration().Plugin.Classify(raw))
		})
	}
}

func TestZCodeClassify_SteerNoticesAreNotACompactionSlot(t *testing.T) {
	t.Parallel()
	for _, event := range []string{contracts.ZCodeEventTurnSteerQueued, contracts.ZCodeEventTurnSteerDrained} {
		raw, err := json.Marshal(map[string]any{"type": event, "payload": map[string]any{}})
		assert.NoError(t, err)
		assert.Equal(t, agent.ProviderScopedNotification("zcode:steer"), Registration().Plugin.Classify(raw))
	}
	assert.Equal(t, agent.NotificationClassification{}, Registration().Plugin.Classify(json.RawMessage(`{"type":"session.updated","payload":{"operationId":"c1","status":"started","trigger":"auto"}}`)))
	assert.Equal(t, agent.CompactionBoundaryNotification(), Registration().Plugin.Classify(json.RawMessage(`{"type":"session.updated","payload":{"operationId":"c1","status":"completed","trigger":"auto"}}`)))
}

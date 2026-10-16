package deepseekharness

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// planReviewRequest builds the native `user-questions/request` event of an `exit_plan_mode` review.
func planReviewRequest(t *testing.T, detail string) (request, raw []byte) {
	t.Helper()
	question := map[string]any{
		"id": "plan-review", "question": "Review the plan", "detail": detail,
		"options": []map[string]string{{"label": "Approve"}, {"label": "Keep planning"}},
		"intent":  map[string]string{"kind": "plan-review", "approve": "Approve", "callId": "native-plan"},
	}
	request, err := json.Marshal(map[string]any{"questions": []any{question}})
	require.NoError(t, err)
	raw, err = json.Marshal(map[string]any{
		"type": "waterfall", "event": contracts.DeepseekHarnessControlEventUserQuestions,
		"eventId": "plan-event", "agentId": "native-root", "request": json.RawMessage(request),
	})
	require.NoError(t, err)
	return request, raw
}

func TestPlanReviewHandsTheWorkerAPlanThatItCanDecodeAndTitle(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newOfflineAgentServing(t, agent.NewProviderServices(sink), nil)
	plan := "# Exact native plan\n\n1. Inspect the scratch file.\n2. Implement only after approval."
	request, raw := planReviewRequest(t, plan)

	require.NoError(t, a.publishNativeControl(contracts.DeepseekHarnessControlEventUserQuestions, "plan-event", "native-root", request, raw))

	require.Equal(t, 1, sink.PlanUpdateCount())
	update := sink.LastPlanUpdate()
	// The Worker reads the plan through msgcodec.Decompress, which refuses an unspecified compression.
	assert.NotEqual(t, leapmuxv1.ContentCompression_CONTENT_COMPRESSION_UNSPECIFIED, update.Compression)
	stored, err := msgcodec.Decompress(update.Content, update.Compression)
	require.NoError(t, err)
	assert.Equal(t, plan, string(stored))
	assert.Equal(t, "Exact native plan", update.Title)
	require.Equal(t, 1, sink.PublishedControlCount())
}

func TestPlanReviewWithoutAPlanStoresNothingAndStillAsksTheReader(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newOfflineAgentServing(t, agent.NewProviderServices(sink), nil)
	request, raw := planReviewRequest(t, "")

	require.NoError(t, a.publishNativeControl(contracts.DeepseekHarnessControlEventUserQuestions, "plan-event", "native-root", request, raw))

	assert.Zero(t, sink.PlanUpdateCount())
	require.Equal(t, 1, sink.PublishedControlCount())
}

func TestBypassApprovesPendingAndLaterNativeTools(t *testing.T) {
	sink := &agenttest.ControlSink{}
	a := newOfflineAgentServing(t, agent.NewProviderServices(sink), nil)
	a.clientID = "client"
	catalog, err := convertModelCatalog(decodeNativeCatalog(t, twoReasoningModelsFixture))
	require.NoError(t, err)
	a.catalog, a.selection = catalog, catalog.defaultSelection
	calls := serveNativeSettings(t, a)
	publish := func(id string) {
		request := json.RawMessage(`{"toolName":"bash","callId":"call"}`)
		raw, err := json.Marshal(map[string]any{"type": "waterfall", "event": contracts.DeepseekHarnessControlEventApproval, "eventId": id, "agentId": "native-root", "request": request})
		require.NoError(t, err)
		require.NoError(t, a.publishNativeControl(contracts.DeepseekHarnessControlEventApproval, id, "native-root", request, raw))
	}
	publish("first")
	request, raw := planReviewRequest(t, "# Plan")
	require.NoError(t, a.publishNativeControl(contracts.DeepseekHarnessControlEventUserQuestions, "plan-event", "native-root", request, raw))
	result := a.UpdateSettings(map[string]string{contracts.DeepseekHarnessOptionPermissions: contracts.DeepseekHarnessPermissionPresetDangerFullAccess})
	require.Equal(t, contracts.DeepseekHarnessPermissionPresetDangerFullAccess, result.ConfirmedOptions()[contracts.DeepseekHarnessOptionPermissions])
	require.Eventually(t, func() bool { return len(sink.CanceledControls()) == 1 }, 30*time.Second, time.Millisecond)
	assert.Equal(t, []string{"first"}, sink.CanceledControls())
	assert.Equal(t, map[string]any{"kind": "result", "value": "allowed-once"}, calls.first("$events/result")["outcome"])
	publish("later")
	require.Eventually(t, func() bool {
		calls.mu.Lock()
		defer calls.mu.Unlock()
		count := 0
		for _, call := range calls.calls {
			if call.method == "$events/result" {
				count++
			}
		}
		return count == 2
	}, 30*time.Second, time.Millisecond)
	assert.Equal(t, 2, sink.PublishedControlCount(), "the later tool needs no manual card")
}

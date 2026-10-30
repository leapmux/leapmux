package acp

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type registryFlushSink struct {
	*agenttest.Sink
	onCloseSpan func()
}

func (sink *registryFlushSink) CloseSpan(spanID string) {
	sink.Sink.CloseSpan(spanID)
	if sink.onCloseSpan != nil {
		sink.onCloseSpan()
	}
}

func TestACPUpdateTestHookFlushesDeferredRegistryWrites(t *testing.T) {
	for status, want := range map[string]bgtask.Status{
		"completed": bgtask.StatusSucceeded,
		"failed":    bgtask.StatusFailed,
		"cancelled": bgtask.StatusStopped,
	} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			sink := &registryFlushSink{Sink: &agenttest.Sink{}}
			services := agent.NewProviderServices(sink)
			queue := agent.OutputQueueFor(services)
			require.NotNil(t, queue)
			b := &Base{sink: agent.NewOrderedProviderServices(queue, services), sessionID: "session-1"}
			b.hooks.SubagentFromToolCall = func(call ToolCallEnvelope) *SubagentObservation {
				return &SubagentObservation{
					RowKey: call.ToolCallID, ChildAgentKey: call.ToolCallID,
					Title: "Inspect sample", Prompt: "Read sample.py", Spawns: true, Status: bgtask.StatusRunning,
				}
			}
			b.HandleUpdateForTest(json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"task","title":"spawn","status":"pending","kind":"other"}`))
			opened, found := sink.BackgroundTask("task")
			require.True(t, found)
			require.Equal(t, bgtask.StatusRunning, opened.Status)

			b.optionWriteMu.Lock()
			release := sync.OnceFunc(b.optionWriteMu.Unlock)
			t.Cleanup(release)
			deferredAtRelease := false
			sink.onCloseSpan = func() {
				deferredAtRelease = queue.HasPending()
				release()
			}
			b.hooks.SubagentFromToolCallUpdate = func(call ToolCallUpdateEnvelope) *SubagentObservation {
				return &SubagentObservation{RowKey: call.ToolCallID, Status: want, CloseRow: true, Mode: ModeCloseOnly}
			}
			final, err := json.Marshal(map[string]any{
				"sessionUpdate": "tool_call_update", "toolCallId": "task", "status": status,
				"content": []map[string]any{{"type": "content", "content": map[string]string{"type": "text", "text": "Native task result."}}},
			})
			require.NoError(t, err)
			b.HandleUpdateForTest(final)

			require.True(t, deferredAtRelease, "the publication lock must defer the close during dispatch")
			finished, found := sink.BackgroundTask("task")
			require.True(t, found)
			assert.Equal(t, want, finished.Status, "the post-dispatch flush must close the row after the lock is released")
			assert.False(t, queue.HasPending(), "the final update must not need another native frame")
			assert.Len(t, sink.Messages(), 2, "the retained native result must follow its registry close exactly once")
		})
	}
}

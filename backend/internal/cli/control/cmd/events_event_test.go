package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The text form is one line: the local time with milliseconds, the kind,
// the ids that the event has as key=value with the short form of each id,
// and the summary.
func TestStreamEvent_TextLine(t *testing.T) {
	at := time.Date(2026, 11, 19, 10, 0, 1, 123_000_000, time.UTC)
	stamp := at.Local().Format(eventTimeLayout)

	ev := streamEvent{
		Kind:        kindAgentActivity,
		Time:        at,
		WorkspaceID: "9f3kQ2aBxxxxxxxxxxxxxxxx",
		TabID:       "87a19d5f-4838-4e34-9f0f-56130f0a62b1",
		TabType:     "agent",
		WorkerID:    "1a2b3c4d5e6f",
		Summary:     "idle (3 tool calls)",
	}
	assert.Equal(t, stamp+" agent.activity workspace=9f3kQ2aB tab=87a19d5f worker=1a2b3c4d idle (3 tool calls)", ev.TextLine())

	t.Run("an event without ids prints none", func(t *testing.T) {
		ready := streamEvent{Kind: kindStreamReady, Time: at, Summary: "watching 1 workspace and 0 tabs on 0 workers"}
		assert.Equal(t, stamp+" stream.ready watching 1 workspace and 0 tabs on 0 workers", ready.TextLine())
	})
	t.Run("the source of a stream event prints as it is", func(t *testing.T) {
		down := streamEvent{Kind: kindStreamUnavailable, Time: at, Source: "hub", Summary: "the hub ended the stream; retrying"}
		assert.Equal(t, stamp+" stream.unavailable source=hub the hub ended the stream; retrying", down.TextLine())
	})
	t.Run("a summary of several lines prints as one", func(t *testing.T) {
		ev := streamEvent{Kind: kindTerminalNotification, Time: at, TabID: "t-1", Summary: "Build:\nall\r\n\tgreen"}
		line := ev.TextLine()
		assert.Equal(t, stamp+" terminal.notification tab=t-1 Build: all green", line)
		assert.NotContains(t, line, "\n")
	})

	t.Run("a replayed request prints control characters as text", func(t *testing.T) {
		ev := streamEvent{Kind: kindAgentRequest, Time: at, Summary: "question: confirm\x1b\x07\u009d now"}
		assert.Equal(t, stamp+` agent.request question: confirm\u001b\u0007\u009d now`, ev.TextLine())
	})
	t.Run("a reconciled event says so", func(t *testing.T) {
		ev := streamEvent{Kind: kindTabClosed, Time: at, TabID: "t-1", Summary: "agent tab", Reconciled: true}
		assert.True(t, strings.HasSuffix(ev.TextLine(), " agent tab (after a gap)"))
	})
	t.Run("the time is local", func(t *testing.T) {
		ev := streamEvent{Kind: kindStreamHeartbeat, Time: at}
		parsed, err := time.Parse(eventTimeLayout, strings.Fields(ev.TextLine())[0])
		assert.NoError(t, err)
		assert.True(t, parsed.Equal(at), "the text time is the same instant: %s", parsed)
	})
}

// JSON carries the full ids and only the fields of the kind, so a script
// reads `.data.state` of an agent.activity without a null beside it.
func TestStreamEvent_JSONHoldsOnlyTheFieldsOfItsKind(t *testing.T) {
	at := time.Date(2026, 11, 19, 10, 0, 1, 0, time.UTC)
	zero := int32(0)
	activity := jsonMap(t, streamEvent{
		Kind: kindAgentActivity, Time: at, WorkspaceID: "ws-1", TabID: "87a19d5f-4838-4e34-9f0f-56130f0a62b1", TabType: "agent",
		WorkerID: "worker-A", Summary: "idle (0 tool calls)", State: "idle", ToolCalls: &zero,
	})
	assert.Equal(t, map[string]any{
		"kind": "agent.activity", "time": "2026-11-19T10:00:01Z", "workspace_id": "ws-1",
		"tab_id": "87a19d5f-4838-4e34-9f0f-56130f0a62b1", "tab_type": "agent", "worker_id": "worker-A",
		"summary": "idle (0 tool calls)", "state": "idle", "tool_calls": float64(0),
	}, activity, "a count of zero tool calls is a fact, so it prints")

	down := jsonMap(t, unavailableEvent(streamEvent{Kind: kindStreamUnavailable, Time: at, Source: "worker", WorkerID: "worker-A"}, errWatchEnded, false))
	assert.Equal(t, false, down["retrying"], "a refusal states that the stream does not retry")
	assert.Equal(t, "unavailable", down["code"])
	assert.Equal(t, "the worker ended the watch", down["message"])
	assert.NotContains(t, down, "reconciled")
}

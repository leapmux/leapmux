package cmd

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
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

func TestStreamEvent_SourceIsNotTruncatedByShortID(t *testing.T) {
	at := time.Date(2026, 11, 19, 10, 0, 1, 0, time.UTC)
	ev := streamEvent{Kind: kindStreamUnavailable, Time: at, Source: "custom-long-source", Summary: "offline"}
	assert.Contains(t, ev.TextLine(), " source=custom-long-source ")
}

func TestStreamEvent_TSVFormatsNestedSummaryStructs(t *testing.T) {
	at := time.Date(2026, 11, 19, 10, 0, 1, 0, time.UTC)
	var out bytes.Buffer
	p := &cli.Printer{Out: &out, Err: io.Discard, Format: cli.FormatTSV}

	pct := uint32(65)
	require.NoError(t, p.Event(streamEvent{
		Kind:      kindAgentTodos,
		Time:      at,
		Summary:   "2 of 5 done",
		Todos:     &todoCounts{Total: 5, Completed: 2, InProgress: 1},
		Tasks:     &taskCounts{Total: 3, Active: 1},
		Progress:  &progressReport{State: "normal", Percent: 42},
		Scope:     &scopeCounts{Workspaces: 2, Tabs: 4, Workers: 2, WorkersLive: 1, HubLive: true},
		TabStatus: &tabStatusReport{Text: "Building", Tone: "warning", ProgressPercent: &pct},
	}))
	row := out.String()
	assert.Contains(t, row, "2/5 (1 in progress)")
	assert.Contains(t, row, "1/3 active")
	assert.Contains(t, row, "42%")
	assert.Contains(t, row, "ws=2 tabs=4 workers=1/2 hub=live")
	assert.Contains(t, row, "Building (65%) [warning]")
	assert.Equal(t, "cleared", tabStatusReport{Cleared: true}.TextValue())
}

func TestEventPrinter_SanitizesStderrNoticesAndGuardsNilErr(t *testing.T) {
	at := time.Date(2026, 11, 19, 10, 0, 1, 0, time.UTC)
	filter, err := parseKindFilter([]string{kindTerminalBell})
	require.NoError(t, err)

	var out, errOut bytes.Buffer
	ep := newEventPrinter(&cli.Printer{Out: &out, Err: &errOut, Format: cli.FormatJSON}, filter, 1)
	reached, err := ep.print(streamEvent{
		Kind: kindStreamUnavailable, Time: at, Source: "hub\x1b[31m", Summary: "down",
	})
	require.NoError(t, err)
	assert.False(t, reached)
	assert.Empty(t, out.String())
	assert.NotContains(t, errOut.String(), "\x1b")
	assert.Contains(t, errOut.String(), "stream.unavailable")

	// Nil Err must not panic.
	nilErrPrinter := newEventPrinter(&cli.Printer{Out: &out, Err: nil, Format: cli.FormatJSON}, filter, 1)
	_, err = nilErrPrinter.print(streamEvent{Kind: kindStreamReady, Time: at, Summary: "ready"})
	require.NoError(t, err)

	// Selected event counts and reaches limit.
	reached, err = ep.print(streamEvent{Kind: kindTerminalBell, Time: at, Summary: "bell"})
	require.NoError(t, err)
	assert.True(t, reached)
	assert.Equal(t, 1, ep.printed())
}

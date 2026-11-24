package letta

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lettaOutputFrame(t *testing.T, scope string, delta map[string]any) ([]byte, []byte) {
	t.Helper()
	payload, err := json.Marshal(delta)
	require.NoError(t, err)
	frame, err := json.Marshal(map[string]any{"type": "stream_delta", "subagent_id": scope, "delta": json.RawMessage(payload)})
	require.NoError(t, err)
	return frame, payload
}

func lettaOutputStart(call, run string) map[string]any {
	return map[string]any{"message_type": "client_tool_start", "tool_call_id": call, "tool_name": "Bash", "run_id": run, "tool_args": `{"command":"native command"}`}
}

func lettaOutputWindow(call, run, text string) map[string]any {
	return map[string]any{"type": "message", "id": contracts.LettaToolOutputStreamIDPrefix + call, "message_type": "tool_return_message", "run_id": run, "tool_call_id": call, "status": "success", "tool_return": text}
}

func TestLettaProgressRecognizesOnlyTheExactNativeCallID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		id, call, kind string
		progress       bool
	}{
		{"synthetic-tool-return-stream-call", "call", "tool_return_message", true},
		{"synthetic-tool-return-stream-읽기", "읽기", "tool_return_message", true},
		{"synthetic-tool-return-final", "call", "tool_return_message", false},
		{"synthetic-tool-return-stream-other", "call", "tool_return_message", false},
		{"synthetic-tool-return-stream-call-extra", "call", "tool_return_message", false},
		{"synthetic-tool-return-stream-", "", "tool_return_message", false},
		{"", "call", "tool_return_message", false},
		{"synthetic-tool-return-stream-call", "call", "client_tool_end", false},
		{"synthetic-tool-return-stream-call", "call", "other", false},
	} {
		assert.Equal(t, tc.progress, isLettaToolProgress(&lettaDelta{ID: tc.id, ToolCallID: tc.call, MessageType: tc.kind}), tc)
	}
}

func TestLettaReturnedDataPreservesPresenceAndValidatesCompositeOwners(t *testing.T) {
	t.Parallel()
	for _, value := range []string{`""`, `0`, `false`, `null`, `-7`, `"한글😀"`, `{"zero":0,"empty":""}`, strconv.Quote(strings.Repeat("complete", 20000))} {
		top := &lettaDelta{ToolCallID: "call", ToolReturn: json.RawMessage(value)}
		actual, present, valid := lettaReturnedData(top)
		require.True(t, present)
		require.True(t, valid)
		assert.Equal(t, json.RawMessage(value), actual)
		top.ToolReturns = json.RawMessage(`[{"tool_call_id":"other","tool_return":"foreign"},{"tool_call_id":"call","tool_return":` + value + `}]`)
		actual, present, valid = lettaReturnedData(top)
		require.True(t, present)
		require.True(t, valid)
		assert.Equal(t, json.RawMessage(value), actual)
		top.ToolReturn = nil
		actual, present, valid = lettaReturnedData(top)
		require.True(t, present)
		require.True(t, valid)
		assert.Equal(t, json.RawMessage(value), actual)
	}
	_, present, valid := lettaReturnedData(&lettaDelta{})
	assert.False(t, present)
	assert.True(t, valid)
	for _, list := range []string{`null`, `false`, `{}`, `[]`, `[{"tool_call_id":"other","tool_return":"x"}]`, `[{"tool_call_id":"call","tool_return":"x"},{"tool_call_id":"call","tool_return":"x"}]`, `[{"tool_call_id":"call","tool_return":"different"}]`, `[{"tool_call_id":"call","status":"error","tool_return":"x"}]`} {
		_, _, valid := lettaReturnedData(&lettaDelta{ToolCallID: "call", Status: "success", ToolReturn: json.RawMessage(`"x"`), ToolReturns: json.RawMessage(list)})
		assert.False(t, valid, list)
	}
	_, present, valid = lettaReturnedData(&lettaDelta{ToolCallID: "call", ToolReturns: json.RawMessage(`[{"tool_call_id":"call"}]`)})
	assert.False(t, present)
	assert.True(t, valid)
	_, present, valid = lettaReturnedData(&lettaDelta{ToolCallID: "call", ToolReturn: json.RawMessage(`{"a":0,"b":false}`), ToolReturns: json.RawMessage(`[{"tool_call_id":"call","tool_return":{"b":false,"a":0}}]`)})
	assert.True(t, present)
	assert.True(t, valid)
}

func TestLettaProgressRequiresItsOpenRunAndNeverCreatesACounter(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	start, _ := lettaOutputFrame(t, "", lettaOutputStart("call", "run"))
	a.HandleOutput(start)
	for _, run := range []string{"foreign", ""} {
		frame, payload := lettaOutputFrame(t, "", lettaOutputWindow("call", run, "foreign window"))
		a.HandleOutput(frame)
		rows := sink.Messages()
		assert.Equal(t, payload, rows[len(rows)-1].Content)
		assert.False(t, rows[len(rows)-1].Closing)
	}
	unknown, _ := lettaOutputFrame(t, "", lettaOutputWindow("unknown", "run", "unknown window"))
	a.HandleOutput(unknown)
	assert.Empty(t, sink.ProgressUpdates())
	assert.Empty(t, sink.ClosedSpans())
	for _, text := range []string{"first window", "first window", "", "second 한글😀 window"} {
		frame, _ := lettaOutputFrame(t, "", lettaOutputWindow("call", "run", text))
		a.HandleOutput(frame)
	}
	require.Len(t, sink.ProgressUpdates(), 4)
	for _, update := range sink.ProgressUpdates() {
		assert.Equal(t, agent.ProgressOutputTail, update.Operation)
		assert.Equal(t, "letta-tool-call", update.ScopeID)
		assert.False(t, update.Truncated)
	}
	assert.Equal(t, "second 한글😀 window", sink.ProgressUpdates()[3].Text)
}

func TestLettaEmptyLifecycleStopsLiveOutputAndRetainsTheNativeFinal(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"success", "error"} {
		sink := &agenttest.Sink{}
		a := &Agent{sink: agent.NewProviderServices(sink)}
		start, _ := lettaOutputFrame(t, "", lettaOutputStart("call", "run"))
		a.HandleOutput(start)
		window, _ := lettaOutputFrame(t, "", lettaOutputWindow("call", "run", "live"))
		a.HandleOutput(window)
		end, nativeEnd := lettaOutputFrame(t, "", map[string]any{"message_type": "client_tool_end", "run_id": "run", "tool_call_id": "call", "status": status})
		a.HandleOutput(end)
		assert.Equal(t, nativeEnd, sink.Messages()[2].Content)
		assert.False(t, sink.Messages()[2].Closing)
		assert.Empty(t, sink.ClosedSpans())
		assert.Contains(t, sink.ProgressUpdates(), agent.CompleteOutputProgress("letta-tool-call"))
		before := len(sink.ProgressUpdates())
		a.HandleOutput(window)
		assert.Len(t, sink.ProgressUpdates(), before, "late progress cannot restart an ended execution")
		for i := range 2 {
			final, native := lettaOutputFrame(t, "", map[string]any{"message_type": "tool_return_message", "id": "synthetic-interrupt-tool-return-" + strconv.Itoa(i), "run_id": "run", "tool_call_id": "call", "status": status, "tool_return": "actual final " + strconv.Itoa(i)})
			a.HandleOutput(final)
			rows := sink.Messages()
			assert.Equal(t, native, rows[len(rows)-1].Content)
			assert.True(t, rows[len(rows)-1].Closing, "each genuine native final message must remain")
		}
	}
}

func TestLettaRealClientEndDataPreservesEveryPresentValue(t *testing.T) {
	t.Parallel()
	for _, value := range []any{"", 0, false, nil, -7, "한글😀", strings.Repeat("full", 20000)} {
		sink := &agenttest.Sink{}
		a := &Agent{sink: agent.NewProviderServices(sink)}
		start, _ := lettaOutputFrame(t, "", lettaOutputStart("call", "run"))
		a.HandleOutput(start)
		end, native := lettaOutputFrame(t, "", map[string]any{"message_type": "client_tool_end", "run_id": "run", "tool_call_id": "call", "status": "success", "tool_return": value})
		a.HandleOutput(end)
		rows := sink.Messages()
		require.Len(t, rows, 2)
		assert.Equal(t, native, rows[1].Content)
		assert.True(t, rows[1].Closing)
		assert.Equal(t, []string{"letta-tool-call"}, sink.ClosedSpans())
	}
}

func TestLettaStaleRunCannotEndAReusedCall(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	for _, run := range []string{"old-run", "new-run"} {
		start, _ := lettaOutputFrame(t, "", lettaOutputStart("reused", run))
		a.HandleOutput(start)
	}
	for _, run := range []string{"old-run", ""} {
		end, native := lettaOutputFrame(t, "", map[string]any{"message_type": "client_tool_end", "run_id": run, "tool_call_id": "reused", "status": "success"})
		a.HandleOutput(end)
		final, returned := lettaOutputFrame(t, "", map[string]any{"message_type": "tool_return_message", "run_id": run, "tool_call_id": "reused", "status": "success", "tool_return": "real old data"})
		a.HandleOutput(final)
		rows := sink.Messages()
		assert.Equal(t, native, rows[len(rows)-2].Content)
		assert.Equal(t, returned, rows[len(rows)-1].Content)
	}
	assert.Empty(t, sink.ClosedSpans())
	assert.False(t, a.tools[lettaToolKey("", "reused")].ended())
	window, _ := lettaOutputFrame(t, "", lettaOutputWindow("reused", "new-run", "current new data"))
	a.HandleOutput(window)
	assert.Equal(t, []agent.ProgressUpdate{agent.ResetOutputProgress("letta-tool-reused"), agent.OutputTailProgress("letta-tool-reused", "current new data", false)}, sink.ProgressUpdates())
}

func TestLettaMalformedProgressAndLifecyclePreserveBytesWithoutChangingTheCall(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	start, _ := lettaOutputFrame(t, "", lettaOutputStart("call", "run"))
	a.HandleOutput(start)
	for _, change := range []map[string]any{
		{"tool_return": false},
		{"status": "error"},
		{"tool_returns": false},
		{"tool_returns": []any{map[string]any{"tool_call_id": "foreign", "tool_return": "live"}}},
		{"tool_returns": []any{map[string]any{"tool_call_id": "call", "tool_return": "other"}}},
	} {
		delta := lettaOutputWindow("call", "run", "live")
		for key, value := range change {
			delta[key] = value
		}
		frame, native := lettaOutputFrame(t, "", delta)
		a.HandleOutput(frame)
		rows := sink.Messages()
		assert.Equal(t, native, rows[len(rows)-1].Content)
		assert.False(t, rows[len(rows)-1].Closing)
	}
	for _, status := range []string{"", "unknown"} {
		frame, _ := lettaOutputFrame(t, "", map[string]any{"message_type": "client_tool_end", "tool_call_id": "call", "run_id": "run", "status": status})
		a.HandleOutput(frame)
	}
	assert.Empty(t, sink.ProgressUpdates())
	assert.Empty(t, sink.ClosedSpans())
	assert.False(t, a.tools[lettaToolKey("", "call")].ended())
}

func TestLettaNativeToolCallCarriesItsRunIntoLiveOutput(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	start, _ := lettaOutputFrame(t, "", map[string]any{"message_type": "tool_call_message", "run_id": "run", "tool_calls": []any{map[string]any{"tool_call_id": "call", "name": "Bash", "arguments": `{}`}}})
	a.HandleOutput(start)
	window, _ := lettaOutputFrame(t, "", lettaOutputWindow("call", "run", "native live window"))
	a.HandleOutput(window)
	assert.Equal(t, []agent.ProgressUpdate{agent.OutputTailProgress("letta-tool-call", "native live window", false)}, sink.ProgressUpdates())
}

func TestLettaConcurrentNativeDispatchKeepsIndependentLiveCalls(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	var scripts [][]byte
	for i := range 4 {
		call := "concurrent-" + strconv.Itoa(i)
		start, _ := lettaOutputFrame(t, "", lettaOutputStart(call, "run"))
		window, _ := lettaOutputFrame(t, "", lettaOutputWindow(call, "run", call+" current window"))
		final, _ := lettaOutputFrame(t, "", map[string]any{"message_type": "tool_return_message", "tool_call_id": call, "run_id": "run", "status": "success", "tool_return": call + " complete"})
		scripts = append(scripts, [][]byte{start, window, final}...)
	}
	var workers sync.WaitGroup
	release := make(chan struct{})
	for i := range 4 {
		workers.Go(func() {
			<-release
			for _, frame := range scripts[i*3 : i*3+3] {
				a.HandleOutput(frame)
			}
		})
	}
	close(release)
	workers.Wait()
	assert.Len(t, sink.Messages(), 12)
	assert.Len(t, sink.ClosedSpans(), 4)
	assert.Empty(t, a.tools)
	for i := range 4 {
		call := "concurrent-" + strconv.Itoa(i)
		assert.Contains(t, sink.ProgressUpdates(), agent.OutputTailProgress("letta-tool-"+call, call+" current window", false))
	}
}

// lettaTurnEnd is a `turn_finished` frame with the given stop reason.
func lettaTurnEnd(t *testing.T, stopReason string) []byte {
	t.Helper()
	frame, err := json.Marshal(map[string]any{"type": "turn_finished", "turn_id": "turn-" + stopReason, "stop_reason": stopReason})
	require.NoError(t, err)
	return frame
}

// lettaClosingRows returns the closing rows that copy an opening row: the rows
// whose completion is set and that close a span.
func lettaClosingRows(rows []agenttest.Message) []agenttest.Message {
	var closing []agenttest.Message
	for _, row := range rows {
		if row.Closing && row.Completion != "" {
			closing = append(closing, row)
		}
	}
	return closing
}

func TestLettaTurnEndStoresTheStartOfEachCallItOutlivesAsItsClosingRow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		endStatus  string
		stopReason string
		want       agent.MessageCompletion
	}{
		// `client_tool_end` states how the call ended, whatever the interrupted turn did.
		{name: "an error end in a cancelled turn", endStatus: "error", stopReason: "cancelled", want: agent.MessageCompletionError},
		{name: "a success end in a cancelled turn", endStatus: "success", stopReason: "cancelled", want: agent.MessageCompletionComplete},
		{name: "no end in a cancelled turn", stopReason: "cancelled", want: agent.MessageCompletionInterrupted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := newLettaChildTestAgent(t)
			start, nativeStart := lettaOutputFrame(t, "", lettaOutputStart("call", "run"))
			window, _ := lettaOutputFrame(t, "", lettaOutputWindow("call", "run", "actual live output"))
			a.HandleOutput(start)
			a.HandleOutput(window)
			if tc.endStatus != "" {
				end, _ := lettaOutputFrame(t, "", map[string]any{"message_type": "client_tool_end", "tool_call_id": "call", "run_id": "run", "status": tc.endStatus})
				a.HandleOutput(end)
			}
			assert.Empty(t, sink.ClosedSpans())
			assert.Empty(t, lettaClosingRows(sink.Messages()))

			a.HandleOutput(lettaTurnEnd(t, tc.stopReason))
			rows := sink.Messages()
			require.GreaterOrEqual(t, len(rows), 2)
			closing, turnEnd := rows[len(rows)-2], rows[len(rows)-1]
			assert.Equal(t, nativeStart, closing.Content, "the closing row holds only the bytes that Letta Code sent")
			assert.Equal(t, tc.want, closing.Completion)
			assert.True(t, closing.Closing)
			assert.Equal(t, "letta-tool-call", closing.SpanID)
			assert.Equal(t, "Bash", closing.SpanType)
			assert.Equal(t, "local-conv-1", closing.AgentSessionID)
			assert.True(t, turnEnd.TurnEnd, "the closing row precedes the turn end")
			assert.Len(t, lettaClosingRows(rows), 1)
			assert.Equal(t, []string{"letta-tool-call"}, sink.ClosedSpans())
			assert.Contains(t, sink.ProgressUpdates(), agent.CompleteOutputProgress("letta-tool-call"))
			assert.Empty(t, a.tools)
		})
	}
}

func TestLettaTurnEndStoresNoClosingRowForACallWithItsNativeResult(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	start, _ := lettaOutputFrame(t, "", lettaOutputStart("call", "run"))
	a.HandleOutput(start)
	// Letta Code sends the result of a call that an interrupt stops before the turn end.
	end, _ := lettaOutputFrame(t, "", map[string]any{"message_type": "client_tool_end", "tool_call_id": "call", "run_id": "run", "status": "error"})
	a.HandleOutput(end)
	final, native := lettaOutputFrame(t, "", map[string]any{"message_type": "tool_return_message", "id": "synthetic-interrupt-tool-return-1", "tool_call_id": "call", "run_id": "run", "status": "error", "tool_return": "User interrupted the stream"})
	a.HandleOutput(final)
	a.HandleOutput([]byte(lettaLiveTurnFinishedCancelled))
	rows := sink.Messages()
	require.Len(t, rows, 4)
	assert.Equal(t, native, rows[2].Content)
	assert.True(t, rows[2].Closing)
	assert.Empty(t, lettaClosingRows(rows))
	assert.True(t, rows[3].TurnEnd)
	assert.Equal(t, []string{"letta-tool-call"}, sink.ClosedSpans())
}

func TestLettaTurnEndClosesTheCallsItOutlivesInTheOrderTheyOpened(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	var calls []string
	var starts [][]byte
	for i := range 8 {
		call := "call-" + strconv.Itoa(7-i)
		calls = append(calls, call)
		start, native := lettaOutputFrame(t, "", lettaOutputStart(call, "run"))
		starts = append(starts, native)
		a.HandleOutput(start)
	}
	// One native message can open several calls. Each call keeps its own row.
	multi, _ := lettaOutputFrame(t, "", map[string]any{"message_type": "tool_call_message", "run_id": "run", "tool_calls": []any{
		map[string]any{"tool_call_id": "batch-a", "name": "Read", "arguments": `{"file_path":"a"}`},
		map[string]any{"tool_call_id": "batch-b", "name": "Grep", "arguments": `{"pattern":"b"}`},
	}})
	a.HandleOutput(multi)
	opened := sink.Messages()
	require.Len(t, opened, 10)
	calls = append(calls, "batch-a", "batch-b")
	starts = append(starts, opened[8].Content, opened[9].Content)

	a.HandleOutput([]byte(lettaLiveTurnFinishedCancelled))
	closing := lettaClosingRows(sink.Messages())
	require.Len(t, closing, len(calls))
	var closedSpans []string
	for i, row := range closing {
		assert.Equal(t, "letta-tool-"+calls[i], row.SpanID)
		assert.Equal(t, starts[i], row.Content)
		assert.Equal(t, agent.MessageCompletionInterrupted, row.Completion)
		closedSpans = append(closedSpans, "letta-tool-"+calls[i])
	}
	assert.Equal(t, "Grep", closing[len(closing)-1].SpanType)
	assert.Equal(t, closedSpans, sink.ClosedSpans())
	assert.Equal(t, []int{len(calls)}, agenttest.TurnToolUseCounts(t, sink.Messages()), "the turn counts each call that it outlived")
}

// A call ID that a new run starts again opens the call again: the newer start replaces
// the older, so the turn end closes the call once, with the newer start.
func TestLettaTurnEndClosesACallThatANewRunStartedAgainOnce(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	older, _ := lettaOutputFrame(t, "", lettaOutputStart("reused", "old-run"))
	newerDelta := lettaOutputStart("reused", "new-run")
	newerDelta["tool_args"] = `{"command":"newer native command"}`
	newer, nativeNewer := lettaOutputFrame(t, "", newerDelta)
	a.HandleOutput(older)
	a.HandleOutput(newer)
	a.HandleOutput([]byte(lettaLiveTurnFinishedCancelled))
	closing := lettaClosingRows(sink.Messages())
	require.Len(t, closing, 1)
	assert.Equal(t, nativeNewer, closing[0].Content)
	assert.Equal(t, agent.MessageCompletionInterrupted, closing[0].Completion)
	assert.Equal(t, []string{"letta-tool-reused"}, sink.ClosedSpans())
}

func TestLettaTurnEndLeavesTheOpenCallsOfAChildToTheirLateNativeResult(t *testing.T) {
	t.Parallel()
	a, root := newLettaChildTestAgent(t)
	a.HandleOutput(lettaOutputChildState(t, "child", "running"))
	row, found := root.BackgroundTask("child")
	require.True(t, found)
	child := root.Child(row.ChildAgentID)
	start, _ := lettaOutputFrame(t, "child", lettaOutputStart("child-call", "child-run"))
	a.HandleOutput(start)
	a.HandleOutput([]byte(lettaLiveTurnFinishedCancelled))
	assert.Empty(t, lettaClosingRows(child.Messages()), "the turn of the parent does not end the call of a child")
	assert.Contains(t, a.tools, lettaToolKey("child", "child-call"))

	a.HandleOutput(lettaOutputChildState(t, "child", "completed"))
	assert.Empty(t, lettaClosingRows(child.Messages()), "a native result of a child can follow the end of its task")
	assert.Equal(t, []string{"letta-tool-child-call"}, child.ClosedSpans())
	final, native := lettaOutputFrame(t, "child", map[string]any{"message_type": "tool_return_message", "id": "synthetic-tool-return-late", "tool_call_id": "child-call", "run_id": "child-run", "status": "success", "tool_return": "late child result"})
	a.HandleOutput(final)
	rows := child.Messages()
	assert.Equal(t, native, rows[len(rows)-1].Content)
	assert.True(t, rows[len(rows)-1].Closing)
	assert.Empty(t, lettaClosingRows(rows), "the call has one result")
}

func TestLettaProcessExitStoresAClosingRowForEachOpenCallOfEachScope(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		completion agent.MessageCompletion
		want       agent.MessageCompletion
	}{
		{name: "a stop", completion: agent.MessageCompletionInterrupted, want: agent.MessageCompletionInterrupted},
		{name: "a crash", completion: agent.MessageCompletionError, want: agent.MessageCompletionError},
		// The process ended cleanly, but the call never finished.
		{name: "a clean exit", completion: agent.MessageCompletionComplete, want: agent.MessageCompletionInterrupted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, root := newLettaChildTestAgent(t)
			a.HandleOutput(lettaOutputChildState(t, "child", "running"))
			row, found := root.BackgroundTask("child")
			require.True(t, found)
			child := root.Child(row.ChildAgentID)
			for _, scope := range []string{"", "child"} {
				start, _ := lettaOutputFrame(t, scope, lettaOutputStart("call", "run"))
				a.HandleOutput(start)
			}
			a.finishOutputAfterExit(tc.completion)
			for _, sink := range []*agenttest.Sink{root, child} {
				closing := lettaClosingRows(sink.Messages())
				require.Len(t, closing, 1)
				assert.Equal(t, tc.want, closing[0].Completion)
				assert.Equal(t, "letta-tool-call", closing[0].SpanID)
				assert.Equal(t, []string{"letta-tool-call"}, sink.ClosedSpans())
			}
			assert.Empty(t, a.tools)
		})
	}
}

// handleFrame refuses a frame once the agent discards output, but DiscardOutput
// can land while a frame is in dispatch, after that check.
func TestLettaCloseToolScopeStoresNoClosingRowOnceOutputIsDiscarded(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	start, _ := lettaOutputFrame(t, "", lettaOutputStart("call", "run"))
	a.HandleOutput(start)
	a.DiscardOutput()
	a.closeToolScope("", a.sink, agent.MessageCompletionInterrupted)
	assert.Empty(t, lettaClosingRows(sink.Messages()))
	assert.Equal(t, []string{"letta-tool-call"}, sink.ClosedSpans(), "the call still ends")
	assert.Contains(t, sink.ProgressUpdates(), agent.CompleteOutputProgress("letta-tool-call"))
	assert.Empty(t, a.tools)
}

func TestLettaNormalAndErrorTurnPreserveOpenToolsAndLateReturnAfterInterruptSuppressed(t *testing.T) {
	t.Parallel()

	t.Run("normal and error turns preserve open tools", func(t *testing.T) {
		t.Parallel()
		for _, stopReason := range []string{"end_turn", "error"} {
			a, sink := newLettaChildTestAgent(t)
			start, _ := lettaOutputFrame(t, "", lettaOutputStart("call-open", "run-1"))
			a.HandleOutput(start)
			a.HandleOutput(lettaTurnEnd(t, stopReason))

			assert.Empty(t, sink.ClosedSpans(), stopReason)
			assert.Empty(t, lettaClosingRows(sink.Messages()), stopReason)
			assert.NotContains(t, sink.TurnLifecycle(), "reset_spans", stopReason)
			assert.Contains(t, a.tools, lettaToolKey("", "call-open"), stopReason)

			// Late native tool return closes it cleanly once.
			ret, _ := lettaOutputFrame(t, "", map[string]any{
				"message_type": "tool_return_message", "run_id": "run-1",
				"tool_call_id": "call-open", "status": "success", "tool_return": "done",
			})
			a.HandleOutput(ret)
			assert.Equal(t, []string{"letta-tool-call-open"}, sink.ClosedSpans(), stopReason)
		}
	})

	t.Run("cancelled turn resets spans and suppresses late tool frames", func(t *testing.T) {
		t.Parallel()
		a, sink := newLettaChildTestAgent(t)
		start, _ := lettaOutputFrame(t, "", lettaOutputStart("call-cut", "run-1"))
		a.HandleOutput(start)
		a.HandleOutput([]byte(lettaLiveTurnFinishedCancelled))

		assert.Equal(t, []string{"letta-tool-call-cut"}, sink.ClosedSpans())
		assert.Contains(t, sink.TurnLifecycle(), "reset_spans")
		assert.Contains(t, sink.ProgressUpdates(), agent.ResetProgress())

		countBeforeLate := sink.MessageCount()
		progressBeforeLate := len(sink.ProgressUpdates())
		window, _ := lettaOutputFrame(t, "", lettaOutputWindow("call-cut", "run-1", "late output window"))
		a.HandleOutput(window)
		assert.Equal(t, countBeforeLate, sink.MessageCount(), "late tool progress window after force-close must be dropped")
		assert.Equal(t, progressBeforeLate, len(sink.ProgressUpdates()), "late tool progress window after force-close must not emit progress")

		statusEnd, _ := lettaOutputFrame(t, "", map[string]any{
			"message_type": "client_tool_end", "run_id": "run-1",
			"tool_call_id": "call-cut", "status": "success",
		})
		a.HandleOutput(statusEnd)
		assert.Equal(t, countBeforeLate, sink.MessageCount(), "late status-only client_tool_end after force-close must be dropped")
		assert.Equal(t, progressBeforeLate, len(sink.ProgressUpdates()), "late status-only client_tool_end after force-close must not emit progress")

		ret, _ := lettaOutputFrame(t, "", map[string]any{
			"message_type": "tool_return_message", "run_id": "run-1",
			"tool_call_id": "call-cut", "status": "success", "tool_return": "late",
		})
		a.HandleOutput(ret)
		assert.Equal(t, countBeforeLate, sink.MessageCount(), "late tool_return_message after force-close must be dropped")
		assert.Equal(t, progressBeforeLate, len(sink.ProgressUpdates()), "late tool_return_message after force-close must not emit progress")
	})
}

func TestLettaRootInterruptDoesNotSuppressChildToolReturnWithSharedCallID(t *testing.T) {
	t.Parallel()
	a, root := newLettaChildTestAgent(t)
	a.HandleOutput(lettaOutputChildState(t, "child-one", "running"))
	row, found := root.BackgroundTask("child-one")
	require.True(t, found)
	child := root.Child(row.ChildAgentID)

	rootStart, _ := lettaOutputFrame(t, "", lettaOutputStart("shared-call", "run-root"))
	childStart, _ := lettaOutputFrame(t, "child-one", lettaOutputStart("shared-call", "run-child"))
	a.HandleOutput(rootStart)
	a.HandleOutput(childStart)

	// Interrupting the root turn force-closes root's shared-call only.
	a.HandleOutput([]byte(lettaLiveTurnFinishedCancelled))
	assert.Equal(t, []string{"letta-tool-shared-call"}, root.ClosedSpans())
	assert.NotContains(t, a.tools, lettaToolKey("", "shared-call"))
	assert.Contains(t, a.tools, lettaToolKey("child-one", "shared-call"))
	assert.Empty(t, child.ClosedSpans())

	// A late root tool_return_message is still suppressed.
	rootCountBefore := root.MessageCount()
	lateRootReturn, _ := lettaOutputFrame(t, "", map[string]any{
		"message_type": "tool_return_message", "run_id": "run-root",
		"tool_call_id": "shared-call", "status": "success", "tool_return": "late root",
	})
	a.HandleOutput(lateRootReturn)
	assert.Equal(t, rootCountBefore, root.MessageCount(), "late root tool_return_message after force-close must be dropped")

	// The running child's native tool_return_message for the same tool_call_id must still persist and close its span.
	childCountBefore := child.MessageCount()
	childReturn, nativeChildReturn := lettaOutputFrame(t, "child-one", map[string]any{
		"message_type": "tool_return_message", "run_id": "run-child",
		"tool_call_id": "shared-call", "status": "success", "tool_return": "child done",
	})
	a.HandleOutput(childReturn)
	childRows := child.Messages()
	require.Len(t, childRows, childCountBefore+1, "child tool_return_message must not be dropped by root closedSpans")
	assert.Equal(t, nativeChildReturn, childRows[len(childRows)-1].Content)
	assert.True(t, childRows[len(childRows)-1].Closing)
	assert.Equal(t, "letta-tool-shared-call", childRows[len(childRows)-1].SpanID)
	assert.Equal(t, []string{"letta-tool-shared-call"}, child.ClosedSpans())
	assert.NotContains(t, a.tools, lettaToolKey("child-one", "shared-call"))
}

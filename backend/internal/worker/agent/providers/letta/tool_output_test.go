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
	assert.False(t, a.tools[lettaToolKey("", "reused")].executionEnded)
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
	assert.False(t, a.tools[lettaToolKey("", "call")].executionEnded)
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

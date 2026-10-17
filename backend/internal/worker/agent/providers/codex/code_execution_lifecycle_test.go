package codex

import (
	"encoding/json"
	"fmt"
	"testing"

	agentapi "github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func feedYieldedCodexCell(t *testing.T, a *Agent, threadID, turnID, callID string) {
	t.Helper()
	request, _ := rawExecPair(t, callID, threadID, turnID)
	for _, frame := range [][]byte{
		request,
		codexCellOutputFrame(t, "custom_tool_call_output", callID, codexTestCellYield, threadID, turnID, "first output"),
		codexWaitCallFrame(t, "wait-retained", "205", threadID, turnID),
		codexCellOutputFrame(t, "function_call_output", "wait-retained", codexTestCellYield, threadID, turnID, "second output"),
	} {
		handleCodexOutput(a, providerkit.ParseLine(frame))
	}
}

func assertRetainedCodexCell(t *testing.T, sink *agenttest.Sink, callID string, completion agentapi.MessageCompletion) {
	t.Helper()
	rows := codexSpanRows(sink.Messages(), callID)
	require.Len(t, rows, 2, "retain one request and all available output")
	assert.Contains(t, string(rows[1].Content), "first output")
	assert.Contains(t, string(rows[1].SupplementalContent), "second output")
	assert.Equal(t, completion, rows[1].Completion)
	assert.True(t, rows[1].Closing)
	assert.Contains(t, sink.ClosedSpans(), callID)
}

func TestCodexYieldedOutputSurvivesEveryMainLifecycleExit(t *testing.T) {
	t.Parallel()
	for _, exit := range []string{"stop", "wait", "clear", "next turn", "eviction"} {
		t.Run(exit, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newCodexAgentForRPC(t, func(method string) agenttest.RPCReply {
				if method == "thread/start" {
					return agenttest.RPCReply{Result: json.RawMessage(`{"thread":{"id":"fresh-thread"},"model":"gpt-5.4"}`)}
				}
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			})
			a.threadID, a.mainTurn.id = "main-thread", "raw-turn"
			feedYieldedCodexCell(t, a, "main-thread", "raw-turn", "retained-cell")
			require.Empty(t, sink.Messages(), "the cell still waits for its final output")
			switch exit {
			case "stop":
				a.SimulateExitForTest()
				a.Stop()
			case "wait":
				a.SimulateExitForTest()
				require.NoError(t, a.Wait())
			case "clear":
				_, err := a.ClearContext()
				require.NoError(t, err)
			case "next turn":
				feedCodexLines(a, `{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"new-turn"}}}`)
			case "eviction":
				for index := range codexRawExecutionLimit {
					request, _ := rawExecPair(t, fmt.Sprintf("later-%d", index), "main-thread", "raw-turn")
					handleCodexOutput(a, providerkit.ParseLine(request))
				}
			}
			completion := agentapi.MessageCompletionInterrupted
			if exit == "wait" {
				completion = agentapi.MessageCompletionError
			}
			assertRetainedCodexCell(t, sink, "retained-cell", completion)
			a.outputMu.Lock()
			a.finishCodexRawExecutions("main-thread", agentapi.MessageCompletionInterrupted)
			a.outputMu.Unlock()
			assertRetainedCodexCell(t, sink, "retained-cell", completion)
		})
	}
}

func TestCodexYieldedChildOutputSurvivesRegistryCompletion(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.mainTurn.id = "root-turn"
	feedCodexLines(a,
		`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn","kind":"started","agentThreadId":"child-thread","agentPath":"/root/child"}}}`,
		`{"method":"turn/started","params":{"threadId":"child-thread","turn":{"id":"child-turn"}}}`,
	)
	feedYieldedCodexCell(t, a, "child-thread", "child-turn", "child-cell")
	a.outputMu.Lock()
	a.completeCodexChildRun("child-thread", codexChildTransition{status: bgtask.StatusSucceeded, completion: agentapi.MessageCompletionComplete})
	a.outputMu.Unlock()
	assertRetainedCodexCell(t, sink.Child("child-of-spawn"), "child-cell", agentapi.MessageCompletionInterrupted)
	assert.Empty(t, codexSpanRows(sink.Messages(), "child-cell"))
}

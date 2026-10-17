package codex

import (
	"encoding/json"
	"fmt"
	"testing"

	agentapi "github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/require"
)

const codexHelloReceipt = `{"chunk_id":"a","wall_time_seconds":0,"exit_code":0,"original_token_count":1,"output":"hello"}`

func TestCodexRawExecKeepsOutputThatTheTypedToolDoesNotStore(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		"An independent result",
		`{"chunk_id":"a","wall_time_seconds":0,"exit_code":0,"original_token_count":1,"output":"different"}`,
		`{"chunk_id":"a","wall_time_seconds":0,"exit_code":1,"original_token_count":1,"output":"hello"}`,
		`{"chunk_id":"a","wall_time_seconds":0,"exit_code":0,"original_token_count":1,"output":"hello","extra":"result"}`,
		`{"chunk_id":"a","wall_time_seconds":0,"exit_code":0,"original_token_count":1,"output":"different","output":"hello"}`,
	} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
			a.mainTurn.id = "raw-turn"
			request, _ := codexRawExecFrames(t)
			output := codexCellOutputFrame(t, "custom_tool_call_output", "raw-exec-call", codexTestCellCompleted, "main-thread", "raw-turn", codexHelloReceipt, body)
			for _, frame := range [][]byte{
				request,
				rawExecToolEvent("item/completed", "commandExecution", "main-thread", "raw-turn"),
				output,
			} {
				handleCodexOutput(a, providerkit.ParseLine(frame))
			}
			rows := codexSpanRows(sink.Messages(), "raw-exec-call")
			require.Len(t, rows, 2)
			require.Equal(t, output, rows[1].Content)
		})
	}
}

func TestCodexRawExecKeepsAmbiguousContentBlocks(t *testing.T) {
	t.Parallel()
	receipt, err := json.Marshal(codexHelloReceipt)
	require.NoError(t, err)
	for _, block := range []string{
		fmt.Sprintf(`{"type":"input_text","text":%s,"extra":"independent"}`, receipt),
		fmt.Sprintf(`{"type":"input_text","text":"independent","text":%s}`, receipt),
		fmt.Sprintf(`{"type":"input_image","type":"input_text","text":%s}`, receipt),
	} {
		t.Run(block, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
			a.mainTurn.id = "raw-turn"
			request, _ := codexRawExecFrames(t)
			output := fmt.Sprintf(`{"method":"rawResponseItem/completed","params":{"threadId":"main-thread","turnId":"raw-turn","item":{"type":"custom_tool_call_output","call_id":"raw-exec-call","output":[{"type":"input_text","text":"Script completed\nWall time 0.0 seconds\nOutput:\n"},%s]}}}`, block)
			for _, frame := range [][]byte{request, rawExecToolEvent("item/completed", "commandExecution", "main-thread", "raw-turn"), []byte(output)} {
				handleCodexOutput(a, providerkit.ParseLine(frame))
			}
			rows := codexSpanRows(sink.Messages(), "raw-exec-call")
			require.Len(t, rows, 2)
			require.Equal(t, []byte(output), rows[1].Content)
		})
	}
}

func TestCodexRawExecKeepsReceiptAfterTypedWriteFails(t *testing.T) {
	t.Parallel()
	sink := &codexRawExecFailingSink{failures: 1}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.mainTurn.id = "raw-turn"
	request, _ := codexRawExecFrames(t)
	output := codexCellOutputFrame(t, "custom_tool_call_output", "raw-exec-call", codexTestCellCompleted, "main-thread", "raw-turn", codexHelloReceipt)
	for _, frame := range [][]byte{request, rawExecToolEvent("item/completed", "commandExecution", "main-thread", "raw-turn"), output} {
		handleCodexOutput(a, providerkit.ParseLine(frame))
	}
	rows := codexSpanRows(sink.Messages(), "raw-exec-call")
	require.Len(t, rows, 2)
	require.Equal(t, output, rows[1].Content)
}

func TestCodexRawExecMatchesACommandOnlyDuringItsWait(t *testing.T) {
	t.Parallel()
	for _, insideWait := range []bool{false, true} {
		t.Run(fmt.Sprint(insideWait), func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
			a.mainTurn.id = "raw-turn"
			request, _ := codexRawExecFrames(t)
			tool := rawExecToolEvent("item/completed", "commandExecution", "main-thread", "raw-turn")
			frames := [][]byte{request, codexCellOutputFrame(t, "custom_tool_call_output", "raw-exec-call", codexTestCellYield, "main-thread", "raw-turn")}
			if !insideWait {
				frames = append(frames, tool)
			}
			frames = append(frames, codexWaitCallFrame(t, "wait-1", "205", "main-thread", "raw-turn"))
			if insideWait {
				frames = append(frames, tool)
			}
			frames = append(frames, codexCellOutputFrame(t, "function_call_output", "wait-1", codexTestCellCompleted, "main-thread", "raw-turn", codexHelloReceipt))
			for _, frame := range frames {
				handleCodexOutput(a, providerkit.ParseLine(frame))
			}
			rows := codexSpanRows(sink.Messages(), "raw-exec-call")
			if insideWait {
				require.Empty(t, rows)
			} else {
				require.Len(t, rows, 2)
			}
		})
	}
}

func TestCodexRawExecKeepsImagesAndFailureAfterTypedOutput(t *testing.T) {
	t.Parallel()
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
			a.mainTurn.id = "raw-turn"
			request, _ := codexRawExecFrames(t)
			header := codexTestCellCompleted
			if failed {
				header = "Script failed"
			}
			output := codexCellOutputFrame(t, "custom_tool_call_output", "raw-exec-call", header, "main-thread", "raw-turn", codexHelloReceipt)
			if !failed {
				var frame map[string]any
				require.NoError(t, json.Unmarshal(output, &frame))
				item := frame["params"].(map[string]any)["item"].(map[string]any)
				item["output"] = append(item["output"].([]any), map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAAA"})
				var err error
				output, err = json.Marshal(frame)
				require.NoError(t, err)
			}
			for _, frame := range [][]byte{request, rawExecToolEvent("item/completed", "commandExecution", "main-thread", "raw-turn"), output} {
				handleCodexOutput(a, providerkit.ParseLine(frame))
			}
			rows := codexSpanRows(sink.Messages(), "raw-exec-call")
			require.Len(t, rows, 2)
			require.Equal(t, output, rows[1].Content)
		})
	}
}

func TestCodexRawExecMatchesStoredCommandFailures(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		text        string
		receiptExit int
		retain      bool
	}{
		{name: "matching failure", text: "failure details", receiptExit: 7},
		{name: "different exit status", text: "failure details", receiptExit: 0, retain: true},
		{name: "empty failure output", receiptExit: 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
			a.mainTurn.id = "raw-turn"
			request, _ := codexRawExecFrames(t)
			command := []byte(fmt.Sprintf(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"raw-turn","item":{"type":"commandExecution","id":"failed-command","status":"failed","command":"exit 7","aggregatedOutput":%q,"exitCode":7}}}`, test.text))
			receipt := fmt.Sprintf(`{"chunk_id":"failure","wall_time_seconds":0,"exit_code":%d,"original_token_count":0,"output":%q}`, test.receiptExit, test.text)
			output := codexCellOutputFrame(t, "custom_tool_call_output", "raw-exec-call", codexTestCellCompleted, "main-thread", "raw-turn", receipt)
			for _, frame := range [][]byte{request, command, output} {
				handleCodexOutput(a, providerkit.ParseLine(frame))
			}
			stored := codexSpanRows(sink.Messages(), "failed-command")
			require.Len(t, stored, 1)
			var completed struct {
				Params json.RawMessage `json:"params"`
			}
			require.NoError(t, json.Unmarshal(command, &completed))
			require.Equal(t, []byte(completed.Params), stored[0].Content)
			rows := codexSpanRows(sink.Messages(), "raw-exec-call")
			if test.retain {
				require.Len(t, rows, 2)
				require.Equal(t, output, rows[1].Content)
			} else {
				require.Empty(t, rows)
			}
		})
	}
}

func TestCodexRawExecMatchesStoredCommandsWithoutOrderOrMultiplicity(t *testing.T) {
	t.Parallel()
	for _, unmatched := range []bool{false, true} {
		t.Run(fmt.Sprint(unmatched), func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
			a.mainTurn.id = "raw-turn"
			request, _ := codexRawExecFrames(t)
			handleCodexOutput(a, providerkit.ParseLine(request))
			var receipts []string
			for i := range 3 {
				frame, receipt := codexStoredCommandFrames(i)
				for range i + 1 {
					handleCodexOutput(a, providerkit.ParseLine(frame))
				}
				for range 3 - i {
					receipts = append([]string{receipt}, receipts...)
				}
			}
			if unmatched {
				_, receipt := codexStoredCommandFrames(3)
				receipts = append(receipts, receipt)
			}
			output := codexCellOutputFrame(t, "custom_tool_call_output", "raw-exec-call", codexTestCellCompleted, "main-thread", "raw-turn", receipts...)
			handleCodexOutput(a, providerkit.ParseLine(output))
			rows := codexSpanRows(sink.Messages(), "raw-exec-call")
			if unmatched {
				require.Len(t, rows, 2)
				require.Equal(t, output, rows[1].Content)
			} else {
				require.Empty(t, rows)
			}
		})
	}
}

// codexStoredCommandFrames pairs a completed tool event with its raw receipt.
func codexStoredCommandFrames(index int) ([]byte, string) {
	output := fmt.Sprintf("command output %08d", index)
	frame := []byte(fmt.Sprintf(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"raw-turn","item":{"type":"commandExecution","id":"command-%d","status":"completed","command":"printf result","aggregatedOutput":%q,"exitCode":0}}}`, index, output))
	receipt := fmt.Sprintf(`{"chunk_id":"%d","wall_time_seconds":0,"exit_code":0,"original_token_count":1,"output":%q}`, index, output)
	return frame, receipt
}

func BenchmarkCodexStoredCommandReceipts(b *testing.B) {
	for _, count := range []int{10, 100, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			sink := &agenttest.Sink{}
			a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
			a.mainTurn.id = "raw-turn"
			request, _ := codexRawExecFrames(b)
			handleCodexOutput(a, providerkit.ParseLine(request))
			receipts := make([]string, count)
			for i := range count {
				frame, receipt := codexStoredCommandFrames(i)
				handleCodexOutput(a, providerkit.ParseLine(frame))
				receipts[count-i-1] = receipt
			}
			output := codexFrameItemOutput(b, codexCellOutputFrame(b, "custom_tool_call_output", "raw-exec-call", codexTestCellCompleted, "main-thread", "raw-turn", receipts...))
			require.Len(b, a.rawExecutionCalls, 1)
			for _, call := range a.rawExecutionCalls {
				require.Equal(b, codexExecContentPolls, parseCodexExecSegment(output, call.commands).content)
				b.ReportAllocs()
				for b.Loop() {
					if parseCodexExecSegment(output, call.commands).content != codexExecContentPolls {
						b.Fatal("The parser did not match every stored command receipt")
					}
				}
			}
		})
	}
}

package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	agentapi "github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexRawExecDefersRequestUntilOutput(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.turnID = "raw-turn"
	request, _ := codexRawExecFrames(t)
	handleCodexOutput(a, providerkit.ParseLine(request))
	require.Empty(t, sink.Messages())
	require.Empty(t, sink.OpenSpans())
	require.Empty(t, sink.ReservedColorSpans())
}

func codexRawExecFrames(t testing.TB) ([]byte, []byte) {
	t.Helper()
	return []byte(`{"method":"rawResponseItem/completed","params":{"threadId":"main-thread","turnId":"raw-turn","item":{"type":"custom_tool_call","id":"private-response-success-0-item","status":"completed","call_id":"raw-exec-call","name":"exec","input":"text(\"NATIVE_CODE_SUCCESS_\" + (40 + 2));","internal_chat_message_metadata_passthrough":{"turn_id":"raw-turn"}}},"emittedAtMs":1790872800691}`), []byte(`{"method":"rawResponseItem/completed","params":{"threadId":"main-thread","turnId":"raw-turn","item":{"type":"custom_tool_call_output","id":"ctco_01a0f856-4dd1-7df3-baad-37767de04096","call_id":"raw-exec-call","output":[{"type":"input_text","text":"Script completed\nWall time 0.0 seconds\nOutput:\n"},{"type":"input_text","text":"NATIVE_CODE_SUCCESS_42"}],"internal_chat_message_metadata_passthrough":{"turn_id":"raw-turn","create_time":1790872800.721981}}},"emittedAtMs":1790872800722}`)
}

func TestCodexRawExecPersistsCapturedNativeLifecycle(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.turnID = "raw-turn"
	request, output := codexRawExecFrames(t)
	handleCodexOutput(a, providerkit.ParseLine(request))
	messages := sink.Messages()
	require.Empty(t, messages)
	handleCodexOutput(a, providerkit.ParseLine(output))
	messages = sink.Messages()
	require.Len(t, messages, 2)
	assert.Equal(t, request, messages[0].Content)
	assert.Equal(t, "exec", messages[0].SpanType)
	assert.False(t, messages[0].Closing)
	assert.Equal(t, output, messages[1].Content)
	assert.Equal(t, messages[0].SpanID, messages[1].SpanID)
	assert.True(t, messages[1].Closing)
	assert.Equal(t, 1, a.TurnToolUses)
}

func TestCodexRawExecSuppressesDuplicatesAndUnpairedOutputs(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.turnID = "raw-turn"
	request, output := codexRawExecFrames(t)
	handleCodexOutput(a, providerkit.ParseLine(output))
	assert.Empty(t, sink.Messages())
	handleCodexOutput(a, providerkit.ParseLine(request))
	handleCodexOutput(a, providerkit.ParseLine(request))
	handleCodexOutput(a, providerkit.ParseLine(output))
	handleCodexOutput(a, providerkit.ParseLine(output))
	assert.Len(t, sink.Messages(), 2)
	assert.Equal(t, 1, a.TurnToolUses)
}

func TestCodexRawExecRejectsWrongIdentityAndTool(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"wrong thread", "wrong turn", "wrong call", "absent call", "absent input", "nonexec tool", "foreign namespace"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
			a.turnID = "raw-turn"
			request, output := codexRawExecFrames(t)
			var frame map[string]any
			require.NoError(t, json.Unmarshal(request, &frame))
			params, ok := frame["params"].(map[string]any)
			require.True(t, ok)
			item, ok := params["item"].(map[string]any)
			require.True(t, ok)
			switch scenario {
			case "wrong thread":
				params["threadId"] = "other-thread"
			case "wrong turn":
				params["turnId"] = "other-turn"
			case "wrong call":
				item["call_id"] = "other-call"
			case "absent call":
				delete(item, "call_id")
			case "absent input":
				delete(item, "input")
			case "nonexec tool":
				item["name"] = "other"
			case "foreign namespace":
				item["namespace"] = "other"
			}
			changed, err := json.Marshal(frame)
			require.NoError(t, err)
			handleCodexOutput(a, providerkit.ParseLine(changed))
			handleCodexOutput(a, providerkit.ParseLine(output))
			assert.Empty(t, sink.Messages())
		})
	}
}

func TestCodexRawExecClearsPendingCallsAtTurnEnd(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.turnID = "raw-turn"
	request, output := codexRawExecFrames(t)
	handleCodexOutput(a, providerkit.ParseLine(request))
	require.Empty(t, sink.Messages())
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"raw-turn","status":"interrupted"}}}`)))
	require.Empty(t, sink.OpenSpans())
	require.Empty(t, a.rawExecutionCalls)
	require.Len(t, sink.Messages(), 1)
	require.True(t, sink.Messages()[0].TurnEnd)
	before := len(sink.Messages())
	handleCodexOutput(a, providerkit.ParseLine(output))
	assert.Len(t, sink.Messages(), before)
}

func TestCodexRawExecPreservesFailureHeaderAndImages(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.turnID = "raw-turn"
	request, _ := codexRawExecFrames(t)
	output := []byte(`{"method":"rawResponseItem/completed","params":{"threadId":"main-thread","turnId":"raw-turn","item":{"type":"custom_tool_call_output","call_id":"raw-exec-call","output":[{"type":"input_text","text":"Script failed\nWall time 0.0 seconds\nOutput:\n"},{"type":"input_text","text":"Script error:\nError: NATIVE_CODE_FAILURE"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}}}`)
	handleCodexOutput(a, providerkit.ParseLine(request))
	handleCodexOutput(a, providerkit.ParseLine(output))
	messages := sink.Messages()
	require.Len(t, messages, 2)
	assert.Equal(t, output, messages[1].Content)
	assert.True(t, messages[1].Closing)
}

type codexRawExecFailingSink struct {
	agenttest.Sink
	failures   int
	failResult bool
}

func (s *codexRawExecFailingSink) PersistMessage(source leapmuxv1.MessageSource, content agentapi.MessageContent, span agentapi.SpanInfo) error {
	if s.failures > 0 && (!s.failResult || span.Closing) {
		s.failures--
		return errors.New("the raw exec row could not be stored")
	}
	return s.Sink.PersistMessage(source, content, span)
}

func TestCodexRawExecRetriesOnlyFailedPersistence(t *testing.T) {
	t.Parallel()
	for _, failResult := range []bool{false, true} {
		t.Run(fmt.Sprint(failResult), func(t *testing.T) {
			sink := &codexRawExecFailingSink{failures: 1, failResult: failResult}
			a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
			a.turnID = "raw-turn"
			request, output := codexRawExecFrames(t)
			handleCodexOutput(a, providerkit.ParseLine(request))
			require.Empty(t, sink.Messages())
			handleCodexOutput(a, providerkit.ParseLine(output))
			if failResult {
				require.Len(t, sink.Messages(), 1)
			} else {
				require.Empty(t, sink.Messages())
			}
			require.Equal(t, []string{"raw-exec-call"}, sink.ClosedSpans())
			handleCodexOutput(a, providerkit.ParseLine(output))
			handleCodexOutput(a, providerkit.ParseLine(output))
			require.Len(t, sink.Messages(), 2)
			assert.Equal(t, 1, a.TurnToolUses)
			assert.Equal(t, sink.Messages()[0].SpanColor, sink.Messages()[1].SpanColor)
			if failResult {
				assert.Len(t, sink.ReservedColorSpans(), 1)
			} else {
				assert.Len(t, sink.ReservedColorSpans(), 2, "retry the released reservation before storing the request")
			}
		})
	}
}

func TestCodexRawExecKeepsChildOutputInItsOwnTranscript(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"collabAgentToolCall","id":"spawn-raw-child","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["raw-child"],"prompt":"Run the script.","agentsStates":{}}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"raw-child","turn":{"id":"raw-turn"}}}`)))
	before := len(sink.Messages())
	request, output := codexRawExecFrames(t)
	request = []byte(strings.ReplaceAll(string(request), `"threadId":"main-thread"`, `"threadId":"raw-child"`))
	output = []byte(strings.ReplaceAll(string(output), `"threadId":"main-thread"`, `"threadId":"raw-child"`))
	handleCodexOutput(a, providerkit.ParseLine(request))
	handleCodexOutput(a, providerkit.ParseLine(output))
	assert.Len(t, sink.Messages(), before)
	child := sink.Child("child-of-spawn-raw-child")
	messages := child.Messages()
	var execRows []agenttest.Message
	for _, message := range messages {
		if message.SpanID == "raw-exec-call" {
			execRows = append(execRows, message)
		}
	}
	require.Len(t, execRows, 2)
	assert.Equal(t, request, execRows[0].Content)
	assert.Equal(t, output, execRows[1].Content)
	assert.True(t, execRows[1].Closing)
}

func TestCodexRawExecDoesNotReusePairAcrossTurns(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.turnID = "raw-turn"
	request, output := codexRawExecFrames(t)
	handleCodexOutput(a, providerkit.ParseLine(request))
	handleCodexOutput(a, providerkit.ParseLine(output))
	require.Len(t, sink.Messages(), 2)
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"raw-turn","status":"completed"}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"next-turn"}}}`)))
	request = []byte(strings.ReplaceAll(string(request), `"turnId":"raw-turn"`, `"turnId":"next-turn"`))
	output = []byte(strings.ReplaceAll(string(output), `"turnId":"raw-turn"`, `"turnId":"next-turn"`))
	before := len(sink.Messages())
	handleCodexOutput(a, providerkit.ParseLine(output))
	assert.Len(t, sink.Messages(), before)
	handleCodexOutput(a, providerkit.ParseLine(request))
	handleCodexOutput(a, providerkit.ParseLine(output))
	assert.Len(t, sink.Messages(), before+2)
}

func TestCodexRawExecPreservesExplicitEmptySource(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.turnID = "raw-turn"
	request := []byte(`{"method":"rawResponseItem/completed","params":{"threadId":"main-thread","turnId":"raw-turn","item":{"type":"custom_tool_call","call_id":"empty-script","name":"exec","input":""}}}`)
	handleCodexOutput(a, providerkit.ParseLine(request))
	require.Empty(t, sink.Messages())
	_, output := codexRawExecFrames(t)
	output = []byte(strings.ReplaceAll(string(output), "raw-exec-call", "empty-script"))
	handleCodexOutput(a, providerkit.ParseLine(output))
	require.Len(t, sink.Messages(), 2)
	assert.Equal(t, request, sink.Messages()[0].Content)
}

func TestCodexRawExecAcceptsNativeDefaultNamespace(t *testing.T) {
	t.Parallel()
	for _, namespace := range []string{"", "functions"} {
		t.Run(namespace, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
			a.turnID = "raw-turn"
			request, output := codexRawExecFrames(t)
			request = []byte(strings.Replace(string(request), `"name":"exec"`, `"name":"exec","namespace":`+strconv.Quote(namespace), 1))
			output = []byte(strings.Replace(string(output), `"call_id":"raw-exec-call"`, `"call_id":"raw-exec-call","name":"exec","namespace":`+strconv.Quote(namespace), 1))
			handleCodexOutput(a, providerkit.ParseLine(request))
			handleCodexOutput(a, providerkit.ParseLine(output))
			require.Len(t, sink.Messages(), 2)
			assert.Equal(t, request, sink.Messages()[0].Content)
			assert.Equal(t, output, sink.Messages()[1].Content)
		})
	}
}

func TestCodexRawExecRejectsForeignResultIdentity(t *testing.T) {
	t.Parallel()
	for _, fields := range []string{`"namespace":"foreign"`, `"name":"other-tool"`} {
		t.Run(fields, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
			a.turnID = "raw-turn"
			request, output := codexRawExecFrames(t)
			output = []byte(strings.Replace(string(output), `"call_id":"raw-exec-call"`, `"call_id":"raw-exec-call",`+fields, 1))
			handleCodexOutput(a, providerkit.ParseLine(request))
			handleCodexOutput(a, providerkit.ParseLine(output))
			require.Empty(t, sink.Messages())
		})
	}
}

func TestCodexRawExecSuppressesEmptyShellPoll(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.turnID = "raw-turn"
	request, _ := codexRawExecFrames(t)
	handleCodexOutput(a, providerkit.ParseLine(request))
	output := `{"method":"rawResponseItem/completed","params":{"threadId":"main-thread","turnId":"raw-turn","item":{"type":"custom_tool_call_output","call_id":"raw-exec-call","output":[{"type":"input_text","text":"Script completed\nWall time 5.0 seconds\nOutput:\n"},{"type":"input_text","text":"{\"chunk_id\":\"ee87f5\",\"wall_time_seconds\":5.00207375,\"session_id\":31766,\"original_token_count\":0,\"output\":\"\"}"}]}}}`
	handleCodexOutput(a, providerkit.ParseLine([]byte(output)))
	require.Empty(t, sink.Messages())
	require.Empty(t, sink.OpenSpans())
}

// A script polls one session that exited and one that still runs, and prints each Promise.allSettled record.
func TestCodexRawExecSuppressesSettledShellPolls(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.threadID = "01a13afd-8be5-7362-b9a6-842b40e5fdaf"
	a.turnID = "01a13cc8-c875-7b10-a087-e97b2e58a1bb"
	request := `{"method":"rawResponseItem/completed","params":{"threadId":"01a13afd-8be5-7362-b9a6-842b40e5fdaf","turnId":"01a13cc8-c875-7b10-a087-e97b2e58a1bb","item":{"type":"custom_tool_call","id":"ctc_060b11e00cd3790d016ad038e31f40819198792a512e1814db","status":"completed","call_id":"call_6bd2e783fc574d7bbffaf4dc7d7d2ce6","name":"exec","input":"const r=await Promise.allSettled([tools.write_stdin({session_id:5687,chars:\"\",yield_time_ms:1000,max_output_tokens:1000}),tools.write_stdin({session_id:59563,chars:\"\",yield_time_ms:1000,max_output_tokens:1000})]);for(let i=0;i<r.length;i++)text({i,...r[i]});\n","internal_chat_message_metadata_passthrough":{"turn_id":"01a13cc8-c875-7b10-a087-e97b2e58a1bb","create_time":1792030947.1308835}}},"emittedAtMs":1792030947454}`
	output := `{"method":"rawResponseItem/completed","params":{"threadId":"01a13afd-8be5-7362-b9a6-842b40e5fdaf","turnId":"01a13cc8-c875-7b10-a087-e97b2e58a1bb","item":{"type":"custom_tool_call_output","id":"ctco_01a13d5e-4c10-70d0-8f8d-209ac38361b0","call_id":"call_6bd2e783fc574d7bbffaf4dc7d7d2ce6","output":[{"type":"input_text","text":"Script completed\nWall time 5.0 seconds\nOutput:\n"},{"type":"input_text","text":"{\"i\":0,\"status\":\"fulfilled\",\"value\":{\"chunk_id\":\"bea1b2\",\"wall_time_seconds\":0.00001425,\"exit_code\":0,\"original_token_count\":0,\"output\":\"\"}}"},{"type":"input_text","text":"{\"i\":1,\"status\":\"fulfilled\",\"value\":{\"chunk_id\":\"47f8a2\",\"wall_time_seconds\":5.002878459,\"session_id\":59563,\"original_token_count\":0,\"output\":\"\"}}"}],"internal_chat_message_metadata_passthrough":{"turn_id":"01a13cc8-c875-7b10-a087-e97b2e58a1bb","create_time":1792030952.464418}}},"emittedAtMs":1792030952467}`
	key := codexRawExecutionKey{threadID: a.threadID, turnID: a.turnID, callID: "call_6bd2e783fc574d7bbffaf4dc7d7d2ce6"}
	handleCodexOutput(a, providerkit.ParseLine([]byte(request)))
	require.Contains(t, a.rawExecutionCalls, key, "the request reaches the pending pair")
	handleCodexOutput(a, providerkit.ParseLine([]byte(output)))
	// The output completes the pending pair. An output that the agent dropped would leave it pending.
	require.True(t, a.rawExecutionCalls[key].completed)
	require.Empty(t, sink.Messages())
	require.Empty(t, sink.OpenSpans())
	require.Empty(t, sink.ReservedColorSpans())
	require.Zero(t, a.TurnToolUses)
}

const (
	codexRunningPollReceipt = `{"chunk_id":"a","wall_time_seconds":5.002878459,"session_id":59563,"original_token_count":0,"output":""}`
	codexExitedPollReceipt  = `{"chunk_id":"b","wall_time_seconds":0.00001425,"exit_code":0,"original_token_count":0,"output":""}`
	codexOutputPollReceipt  = `{"chunk_id":"c","wall_time_seconds":0,"session_id":1,"original_token_count":1,"output":"hello"}`
)

// codexCompletedExecOutput builds a native output whose script printed one text block for each body.
func codexCompletedExecOutput(t *testing.T, bodies ...string) json.RawMessage {
	t.Helper()
	blocks := []map[string]string{{"type": "input_text", "text": "Script completed\nWall time 5.0 seconds\nOutput:\n"}}
	for _, body := range bodies {
		blocks = append(blocks, map[string]string{"type": "input_text", "text": body})
	}
	raw, err := json.Marshal(blocks)
	require.NoError(t, err)
	return raw
}

// A script decides how it prints each receipt. Each container below holds only empty successful receipts.
func TestCodexEmptyShellPollAcceptsScriptContainers(t *testing.T) {
	t.Parallel()
	settled := func(receipt string) string { return `{"status":"fulfilled","value":` + receipt + `}` }
	labelled := func(label, receipt string) string { return `{` + label + `,` + receipt[1:] }
	for name, bodies := range map[string][]string{
		"exited receipt":                       {codexExitedPollReceipt},
		"exited and running receipts":          {codexExitedPollReceipt, codexRunningPollReceipt},
		"running receipt with a zero exit":     {`{"chunk_id":"a","wall_time_seconds":0,"session_id":1,"original_token_count":0,"output":"","exit_code":0}`},
		"exponent wall time":                   {`{"chunk_id":"a","wall_time_seconds":9.58e-7,"exit_code":0,"original_token_count":0,"output":""}`},
		"settled record":                       {settled(codexRunningPollReceipt)},
		"settled records with an index":        {labelled(`"i":0`, settled(codexExitedPollReceipt)), labelled(`"i":1`, settled(codexRunningPollReceipt))},
		"settled record with a negative label": {labelled(`"index":-1`, settled(codexExitedPollReceipt))},
		"settled record under a key":           {`{"i":0,"result":` + settled(codexExitedPollReceipt) + `}`},
		"receipts under keys":                  {`{"build":` + codexRunningPollReceipt + `,"test":` + codexExitedPollReceipt + `}`},
		"array of receipts":                    {`[` + codexExitedPollReceipt + `,` + codexRunningPollReceipt + `]`},
		"array of settled records":             {`[` + settled(codexExitedPollReceipt) + `,` + settled(codexRunningPollReceipt) + `]`},
		"padded value":                         {" \n" + codexRunningPollReceipt + "\n"},
	} {
		t.Run(name, func(t *testing.T) {
			require.True(t, codexSuppressRawExecutionOutput(codexCompletedExecOutput(t, bodies...)))
		})
	}
}

// A walk that examined a settled value twice would take time exponential in this depth.
func TestCodexEmptyShellPollWalksDeepValuesOnce(t *testing.T) {
	t.Parallel()
	nest := func(receipt string) string {
		const depth = 2000
		return strings.Repeat(`{"status":"fulfilled","value":`, depth) + receipt + strings.Repeat(`}`, depth)
	}
	require.True(t, codexSuppressRawExecutionOutput(codexCompletedExecOutput(t, nest(codexRunningPollReceipt))))
	require.False(t, codexSuppressRawExecutionOutput(codexCompletedExecOutput(t, nest(codexOutputPollReceipt))))
}

func TestCodexEmptyShellPollPreservesAmbiguousContainers(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"numeric error":            `{"error":5,"result":` + codexRunningPollReceipt + `}`,
		"zero result":              `{"count":0,"result":` + codexRunningPollReceipt + `}`,
		"negative result":          `{"count":-1,"result":` + codexRunningPollReceipt + `}`,
		"large result":             `{"count":9223372036854775807,"result":` + codexRunningPollReceipt + `}`,
		"settled numeric error":    `{"status":"fulfilled","value":` + codexRunningPollReceipt + `,"error":5}`,
		"duplicate container key":  `{"result":"build failed","result":` + codexRunningPollReceipt + `}`,
		"duplicate receipt key":    strings.TrimSuffix(codexRunningPollReceipt, `}`) + `,"output":"failed","output":""}`,
		"duplicate settled status": `{"status":"rejected","status":"fulfilled","value":` + codexRunningPollReceipt + `}`,
		"escaped duplicate key":    `{"result":"build failed","\u0072esult":` + codexRunningPollReceipt + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			require.False(t, codexSuppressRawExecutionOutput(codexCompletedExecOutput(t, body)))
		})
	}
}

func TestCodexEmptyShellPollAcceptsReceiptKeysOnContainers(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"chunk_id", "wall_time_seconds", "session_id", "original_token_count", "output", "exit_code"} {
		t.Run(key, func(t *testing.T) {
			body := `{"` + key + `":` + codexRunningPollReceipt + `}`
			require.True(t, codexSuppressRawExecutionOutput(codexCompletedExecOutput(t, body)))
			body = `{"i":0,"` + key + `":[` + codexExitedPollReceipt + `]}`
			require.True(t, codexSuppressRawExecutionOutput(codexCompletedExecOutput(t, body)))
		})
	}
}

func TestCodexEmptyShellPollRejectsInvalidArrayWithoutAllocatingEveryItem(t *testing.T) {
	measure := func(count int) float64 {
		body := `[` + strings.Repeat(`{},`, count-1) + `{}]`
		raw := codexCompletedExecOutput(t, body)
		return testing.AllocsPerRun(10, func() {
			if codexSuppressRawExecutionOutput(raw) {
				panic("an empty object is not a poll receipt")
			}
		})
	}
	small := measure(10)
	large := measure(10000)
	require.Less(t, large, small*2, "reject the first empty object without allocating the remaining objects")
}

func TestCodexEmptyShellPollValidatesTokenStructure(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`[`, `{`, `{"result":`, `{"result" ` + codexRunningPollReceipt + `}`,
		`[` + codexRunningPollReceipt, `{"result":` + codexRunningPollReceipt,
		`[` + codexRunningPollReceipt + `,]`, `{"result":` + codexRunningPollReceipt + `,}`,
		`{"status":` + codexRunningPollReceipt + `}`, `{"status":"fulfilled","value":0,"poll":` + codexRunningPollReceipt + `}`,
		`{"output":` + codexRunningPollReceipt + `,"count":1}`,
		`{"output":` + codexRunningPollReceipt + `,"session_id":1}`,
		`{"chunk_id":"a","wall_time_seconds":1e999,"exit_code":0,"original_token_count":0,"output":""}`,
	} {
		t.Run(body, func(t *testing.T) {
			require.False(t, codexSuppressRawExecutionOutput(codexCompletedExecOutput(t, body)))
		})
	}
	for _, body := range []string{
		`{"value":` + codexRunningPollReceipt + `,"status":"fulfilled","index":0}`,
		`{"output":"","original_token_count":0,"exit_code":0,"wall_time_seconds":0,"chunk_id":"a"}`,
	} {
		require.True(t, codexSuppressRawExecutionOutput(codexCompletedExecOutput(t, body)))
	}
	for _, depth := range []int{9999, 10000} {
		body := strings.Repeat(`[`, depth) + codexRunningPollReceipt + strings.Repeat(`]`, depth)
		require.Equal(t, depth == 9999, codexSuppressRawExecutionOutput(codexCompletedExecOutput(t, body)))
	}
}

// Feed real provider envelopes through the same serialized path as process output.
func rawExecPair(t testing.TB, id, thread, turn string) ([]byte, []byte) {
	t.Helper()
	request, output := codexRawExecFrames(t)
	replace := strings.NewReplacer("raw-exec-call", id, "main-thread", thread, "raw-turn", turn)
	return []byte(replace.Replace(string(request))), []byte(replace.Replace(string(output)))
}

func rawExecToolEvent(method, kind, thread, turn string) []byte {
	return []byte(fmt.Sprintf(`{"method":%q,"params":{"threadId":%q,"turnId":%q,"item":{"type":%q,"id":"nested-tool","status":"completed","command":"printf hello","aggregatedOutput":"hello","exitCode":0}}}`, method, thread, turn, kind))
}

func TestCodexRawExecToolIntervals(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "imageGeneration", "imageView", "collabAgentToolCall", "webSearch", "sleep", "enteredReviewMode", "exitedReviewMode", "hookPrompt", "functionCallOutput", "agentMessage", "reasoning", "plan", "userMessage", "contextCompaction", "subAgentActivity"} {
		for _, method := range []string{"item/started", "item/completed"} {
			t.Run(kind+method, func(t *testing.T) {
				sink := &agenttest.Sink{}
				a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
				a.turnID = "raw-turn"
				request, output := codexRawExecFrames(t)
				handleCodexOutput(a, providerkit.ParseLine(request))
				handleCodexOutput(a, providerkit.ParseLine(rawExecToolEvent(method, kind, "main-thread", "raw-turn")))
				handleCodexOutput(a, providerkit.ParseLine(output))
				var rows []agenttest.Message
				for _, row := range sink.Messages() {
					if row.SpanID == "raw-exec-call" {
						rows = append(rows, row)
					}
				}
				switch kind {
				case "agentMessage", "reasoning", "plan", "userMessage", "contextCompaction", "subAgentActivity":
					require.Len(t, rows, 2)
				default:
					require.Empty(t, rows)
					require.NotContains(t, sink.ReservedColorSpans(), "raw-exec-call")
				}
			})
		}
	}
}

func TestCodexRawExecIntervalIsolation(t *testing.T) {
	t.Parallel()
	for _, position := range []string{"before", "during", "after", "other turn", "other thread"} {
		t.Run(position, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
			a.turnID = "raw-turn"
			request, output := codexRawExecFrames(t)
			event, _ := newCodexItemEvent(nil, json.RawMessage(`{"threadId":"main-thread","turnId":"raw-turn","item":{"type":"commandExecution","id":"nested"}}`))
			if position == "before" {
				a.observeCodexRawExecutionTool(event)
			}
			handleCodexOutput(a, providerkit.ParseLine(request))
			if position == "other turn" {
				event.turnID = "other"
			}
			if position == "other thread" {
				event.threadID = "other"
			}
			if position != "before" && position != "after" {
				a.observeCodexRawExecutionTool(event)
			}
			handleCodexOutput(a, providerkit.ParseLine(output))
			if position == "after" {
				a.observeCodexRawExecutionTool(event)
			}
			if position == "during" {
				require.Empty(t, sink.Messages())
			} else {
				require.Len(t, sink.Messages(), 2)
			}
		})
	}
}

func TestCodexRawExecMarksAllOverlappingRequests(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.turnID = "raw-turn"
	var outputs [][]byte
	for i := 0; i < 3; i++ {
		request, output := rawExecPair(t, fmt.Sprint(i), "main-thread", "raw-turn")
		handleCodexOutput(a, providerkit.ParseLine(request))
		outputs = append(outputs, output)
	}
	handleCodexOutput(a, providerkit.ParseLine(rawExecToolEvent("item/completed", "commandExecution", "main-thread", "raw-turn")))
	for _, output := range outputs {
		handleCodexOutput(a, providerkit.ParseLine(output))
	}
	require.Len(t, sink.Messages(), 1)
	require.Equal(t, 1, a.TurnToolUses)
	for _, call := range a.rawExecutionCalls {
		require.Empty(t, call.request.Original)
		require.Empty(t, call.output.Original)
	}
}

func TestCodexRawExecCapsIdentitiesAndDiscardsLateOutput(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.turnID = "raw-turn"
	for i := 0; i < 128; i++ {
		request, _ := rawExecPair(t, fmt.Sprint(i), "main-thread", "raw-turn")
		handleCodexOutput(a, providerkit.ParseLine(request))
	}
	require.Len(t, a.rawExecutionCalls, 128)
	request, last := rawExecPair(t, "128", "main-thread", "raw-turn")
	handleCodexOutput(a, providerkit.ParseLine(request))
	require.Len(t, a.rawExecutionCalls, 128)
	_, first := rawExecPair(t, "0", "main-thread", "raw-turn")
	handleCodexOutput(a, providerkit.ParseLine(first))
	require.Empty(t, sink.Messages())
	handleCodexOutput(a, providerkit.ParseLine(last))
	require.Len(t, sink.Messages(), 2)
	a.finishCodexRawExecutions("main-thread")
	require.Empty(t, a.rawExecutionCalls)
}

func TestCodexRawExecPreservesSpanGeometry(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.turnID = "raw-turn"
	request, output := codexRawExecFrames(t)
	handleCodexOutput(a, providerkit.ParseLine(request))
	require.Empty(t, sink.OpenSpans())
	handleCodexOutput(a, providerkit.ParseLine(output))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	require.Empty(t, rows[0].SpansOpenAtPersist)
	require.Equal(t, []agenttest.SpanOpen{{SpanID: "raw-exec-call"}}, rows[1].SpansOpenAtPersist)
	require.Positive(t, rows[0].SpanColor)
	require.Equal(t, rows[0].SpanColor, rows[1].SpanColor)
	require.True(t, rows[1].Closing)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agentapi.MessageContent{Original: []byte(`{}`)}, agentapi.SpanInfo{}))
	require.Empty(t, sink.Messages()[2].SpansOpenAtPersist)
}

func TestCodexRawExecRetriesAtTurnEnd(t *testing.T) {
	t.Parallel()
	sink := &codexRawExecFailingSink{failures: 1, failResult: true}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.turnID = "raw-turn"
	request, output := codexRawExecFrames(t)
	handleCodexOutput(a, providerkit.ParseLine(request))
	handleCodexOutput(a, providerkit.ParseLine(output))
	require.Len(t, sink.Messages(), 1)
	handleCodexOutput(a, providerkit.ParseLine(rawExecToolEvent("item/completed", "commandExecution", "main-thread", "raw-turn")))
	a.finishCodexRawExecutions("main-thread")
	require.Len(t, sink.Messages(), 3)
	require.Empty(t, a.rawExecutionCalls)
	require.Equal(t, output, sink.Messages()[2].Content)
}

func TestCodexRawExecDropsStaleTurns(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.turnID = "raw-turn"
	request, output := codexRawExecFrames(t)
	handleCodexOutput(a, providerkit.ParseLine(request))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"next-turn"}}}`)))
	require.Empty(t, a.rawExecutionCalls)
	handleCodexOutput(a, providerkit.ParseLine(output))
	require.Empty(t, sink.Messages())
}

func TestCodexRawExecRetainsLargePayloads(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.turnID = "raw-turn"
	request, output := codexRawExecFrames(t)
	source := strings.Repeat("x", 9*1024*1024)
	var frame map[string]any
	require.NoError(t, json.Unmarshal(request, &frame))
	frame["params"].(map[string]any)["item"].(map[string]any)["input"] = source
	request, err := json.Marshal(frame)
	require.NoError(t, err)
	handleCodexOutput(a, providerkit.ParseLine(request))
	handleCodexOutput(a, providerkit.ParseLine(output))
	require.Len(t, sink.Messages(), 2)
	require.Equal(t, request, sink.Messages()[0].Content)
}

func TestCodexEmptyShellPollPreservesOtherOutput(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`{"chunk_id":"a","wall_time_seconds":0,"session_id":1,"original_token_count":0,"output":"hello"}`,
		`{"chunk_id":"a","wall_time_seconds":0,"session_id":1,"original_token_count":0,"output":"","exit_code":1}`,
		`{"chunk_id":"a","wall_time_seconds":0,"session_id":1,"original_token_count":0,"output":"","error":"failed"}`,
		`{"chunk_id":"a","wall_time_seconds":0,"original_token_count":0,"output":""}`,
		`{"chunk_id":"a","wall_time_seconds":-1,"session_id":1,"original_token_count":0,"output":""}`,
		`{"chunk_id":"a","wall_time_seconds":0,"session_id":0,"original_token_count":0,"output":""}`,
		`{"chunk_id":"a","wall_time_seconds":0,"session_id":1,"original_token_count":2,"output":""}`,
		`{"output":""}`, `null`, `[]`, `42`, ``, `not JSON`,
		// An exited session keeps its row unless it exited successfully with no output.
		`{"chunk_id":"a","wall_time_seconds":0,"exit_code":1,"original_token_count":0,"output":""}`,
		`{"chunk_id":"a","wall_time_seconds":0,"exit_code":0,"original_token_count":1,"output":"done"}`,
		`{"chunk_id":"","wall_time_seconds":0,"exit_code":0,"original_token_count":0,"output":""}`,
		`{"chunk_id":"a","wall_time_seconds":0,"session_id":null,"exit_code":0,"original_token_count":0,"output":""}`,
		`{"chunk_id":"a","wall_time_seconds":0,"exit_code":"0","original_token_count":0,"output":""}`,
		`{"chunk_id":"a","wall_time_seconds":0,"exit_code":0.5,"original_token_count":0,"output":""}`,
		`{"chunk_id":"a","wall_time_seconds":0,"session_id":1.5,"original_token_count":0,"output":""}`,
		`{"chunk_id":"a","wall_time_seconds":0,"session_id":1,"original_token_count":0.5,"output":""}`,
		`{"chunk_id":"a","wall_time_seconds":"0","session_id":1,"original_token_count":0,"output":""}`,
		`{"chunk_id":"a","session_id":1,"original_token_count":0,"output":""}`,
		`{"chunk_id":"a","wall_time_seconds":0,"session_id":1,"output":""}`,
		`{"chunk_id":1,"wall_time_seconds":0,"session_id":1,"original_token_count":0,"output":""}`,
		`{"chunk_id":"a","wall_time_seconds":0,"session_id":1,"original_token_count":0,"output":null}`,
		`{"chunk_id":"a","wall_time_seconds":0,"session_id":-1,"original_token_count":0,"output":""}`,
		`{"chunk_id":"a","wall_time_seconds":0,"session_id":9223372036854775808,"original_token_count":0,"output":""}`,
		`{"chunk_id":"a","wall_time_seconds":0,"exit_code":-1,"original_token_count":0,"output":""}`,
		// A receipt keeps its row when it holds a field that a receipt does not have, even an integer label.
		`{"i":0,` + codexRunningPollReceipt[1:],
		strings.TrimSuffix(codexRunningPollReceipt, `}`) + `,"signal":9}`,
		strings.TrimSuffix(codexRunningPollReceipt, `}`) + `,"poll":` + codexExitedPollReceipt + `}`,
		// A container keeps its row when it can carry anything besides empty successful receipts.
		`{"status":"rejected","reason":"write_stdin failed: Unknown process id 1"}`,
		`{"status":"fulfilled"}`,
		`{"status":"fulfilled","value":0}`,
		`{"status":"pending","value":` + codexRunningPollReceipt + `}`,
		`{"status":0,"value":` + codexRunningPollReceipt + `}`,
		`{"status":"fulfilled","value":` + codexOutputPollReceipt + `}`,
		`{"status":"fulfilled","value":` + codexRunningPollReceipt + `,"reason":"failed"}`,
		`{"name":"macos","status":"fulfilled","value":` + codexRunningPollReceipt + `}`,
		`{"i":null,"status":"fulfilled","value":` + codexRunningPollReceipt + `}`,
		`{"i":true,"status":"fulfilled","value":` + codexRunningPollReceipt + `}`,
		`{"i":1.5,"status":"fulfilled","value":` + codexRunningPollReceipt + `}`,
		`{"i":9223372036854775808,"status":"fulfilled","value":` + codexRunningPollReceipt + `}`,
		`{"i":0}`, `{}`, `[{}]`, `[[]]`, `"text"`, `true`,
		`{"i":0,"result":"done"}`,
		`{"i":0,"result":{"status":"fulfilled","value":` + codexOutputPollReceipt + `}}`,
		`[` + codexRunningPollReceipt + `,` + codexOutputPollReceipt + `]`,
		codexRunningPollReceipt + ` trailing`,
		codexRunningPollReceipt + codexRunningPollReceipt,
	} {
		t.Run(body, func(t *testing.T) {
			require.False(t, codexSuppressRawExecutionOutput(codexCompletedExecOutput(t, body)))
		})
	}
	t.Run("empty receipt beside output", func(t *testing.T) {
		require.False(t, codexSuppressRawExecutionOutput(codexCompletedExecOutput(t, codexExitedPollReceipt, codexOutputPollReceipt)))
	})
}

func TestCodexRawExecCleanupOnProcessEnd(t *testing.T) {
	t.Parallel()
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprint(stop), func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
			a.turnID = "raw-turn"
			request, _ := codexRawExecFrames(t)
			handleCodexOutput(a, providerkit.ParseLine(request))
			a.SimulateExitForTest()
			if stop {
				a.Stop()
			} else {
				require.NoError(t, a.Wait())
			}
			require.Empty(t, a.rawExecutionCalls)
			require.Empty(t, sink.Messages())
			require.Empty(t, sink.OpenSpans())
		})
	}
}

func TestCodexRawExecCleanupOnContextReset(t *testing.T) {
	t.Parallel()
	a, _, _ := newCodexAgentForRPC(t, func(method string) agenttest.RPCReply {
		require.Equal(t, "thread/start", method)
		return agenttest.RPCReply{Result: json.RawMessage(`{"thread":{"id":"new-thread"},"model":"gpt-5.6-sol","approvalPolicy":"never","sandbox":{"type":"dangerFullAccess"}}`)}
	})
	a.threadID = "main-thread"
	a.turnID = "raw-turn"
	request, _ := codexRawExecFrames(t)
	handleCodexOutput(a, providerkit.ParseLine(request))
	require.Len(t, a.rawExecutionCalls, 1)
	_, err := a.ClearContext()
	require.NoError(t, err)
	require.Empty(t, a.rawExecutionCalls)
}

func TestCodexRawExecReplaysChildIntervals(t *testing.T) {
	t.Parallel()
	for _, nested := range []bool{false, true} {
		t.Run(fmt.Sprint(nested), func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
			a.turnID = "root-turn"
			request, output := rawExecPair(t, "child-exec", "child-thread", "child-turn")
			handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-thread","turn":{"id":"child-turn"}}}`)))
			handleCodexOutput(a, providerkit.ParseLine(request))
			if nested {
				handleCodexOutput(a, providerkit.ParseLine(rawExecToolEvent("item/completed", "commandExecution", "child-thread", "child-turn")))
			}
			handleCodexOutput(a, providerkit.ParseLine(output))
			require.Empty(t, sink.Messages())
			handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn","kind":"started","agentThreadId":"child-thread","agentPath":"/root/child"}}}`)))
			child := sink.Child("child-of-spawn")
			var rows []agenttest.Message
			for _, row := range child.Messages() {
				if row.SpanID == "child-exec" {
					rows = append(rows, row)
				}
			}
			if nested {
				require.Empty(t, rows)
			} else {
				require.Len(t, rows, 2)
			}
		})
	}
}

func TestCodexRawExecMalformedOutputDoesNotFinishInterval(t *testing.T) {
	t.Parallel()
	for _, output := range []string{`null`, `{}`, `0`, `false`} {
		t.Run(output, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
			a.turnID = "raw-turn"
			request, valid := codexRawExecFrames(t)
			handleCodexOutput(a, providerkit.ParseLine(request))
			raw := fmt.Sprintf(`{"method":"rawResponseItem/completed","params":{"threadId":"main-thread","turnId":"raw-turn","item":{"type":"custom_tool_call_output","call_id":"raw-exec-call","output":%s}}}`, output)
			handleCodexOutput(a, providerkit.ParseLine([]byte(raw)))
			require.Empty(t, sink.Messages())
			handleCodexOutput(a, providerkit.ParseLine(rawExecToolEvent("item/completed", "commandExecution", "main-thread", "raw-turn")))
			handleCodexOutput(a, providerkit.ParseLine(valid))
			require.Len(t, sink.Messages(), 1)
		})
	}
}

func TestCodexEmptyShellPollPreservesFailureAndMedia(t *testing.T) {
	t.Parallel()
	receipt := `{"chunk_id":"a","wall_time_seconds":0,"session_id":1,"original_token_count":0,"output":""}`
	for _, kind := range []string{"success", "failure", "image", "extra text", "empty script", "overhead"} {
		t.Run(kind, func(t *testing.T) {
			header := "Script completed\nWall time 0.0 seconds\nOutput:\n"
			if kind == "failure" {
				header = "Script failed\nWall time 0.0 seconds\nOutput:\n"
			}
			if kind == "overhead" {
				header = "Script completed\nWall time 0.100 seconds (code-mode 0.090 seconds; overhead 0.010 seconds)\nOutput:\n"
			}
			blocks := []map[string]string{{"type": "input_text", "text": header}, {"type": "input_text", "text": receipt}}
			if kind == "image" {
				blocks = append(blocks, map[string]string{"type": "input_image", "image_url": "data:image/png;base64,AAAA"})
			}
			if kind == "extra text" {
				blocks = append(blocks, map[string]string{"type": "input_text", "text": "additional result"})
			}
			if kind == "empty script" {
				blocks = blocks[:1]
			}
			raw, err := json.Marshal(blocks)
			require.NoError(t, err)
			require.Equal(t, kind == "success" || kind == "overhead", codexSuppressRawExecutionOutput(raw))
		})
	}
}

func TestCodexRawExecDropsChangedTranscriptOwner(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.turnID = "raw-turn"
	request, output := codexRawExecFrames(t)
	handleCodexOutput(a, providerkit.ParseLine(request))
	sink.UpdateSessionID("replacement-session")
	handleCodexOutput(a, providerkit.ParseLine(output))
	require.Empty(t, sink.Messages())
	require.Empty(t, sink.OpenSpans())
}

func TestCodexRawExecReleasesFailedPairAtTurnEnd(t *testing.T) {
	t.Parallel()
	sink := &codexRawExecFailingSink{failures: 3, failResult: true}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.turnID = "raw-turn"
	request, output := codexRawExecFrames(t)
	handleCodexOutput(a, providerkit.ParseLine(request))
	handleCodexOutput(a, providerkit.ParseLine(output))
	a.finishCodexRawExecutions("main-thread")
	require.Empty(t, a.rawExecutionCalls)
	require.Len(t, sink.Messages(), 1)
	require.Equal(t, 1, sink.failures)
	require.NoError(t, sink.Sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agentapi.MessageContent{Original: []byte(`{}`)}, agentapi.SpanInfo{}))
	require.Empty(t, sink.Messages()[1].SpansOpenAtPersist)
}

func TestCodexRawExecSerializesConcurrentRequests(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.turnID = "raw-turn"
	var requests, outputs [][]byte
	for i := 0; i < 16; i++ {
		request, output := rawExecPair(t, fmt.Sprint(i), "main-thread", "raw-turn")
		requests = append(requests, request)
		outputs = append(outputs, output)
	}
	var workers sync.WaitGroup
	for _, request := range requests {
		workers.Go(func() { handleCodexOutput(a, providerkit.ParseLine(request)) })
	}
	workers.Wait()
	require.Empty(t, sink.Messages())
	require.Empty(t, sink.OpenSpans())
	for _, output := range outputs {
		workers.Go(func() { handleCodexOutput(a, providerkit.ParseLine(output)) })
	}
	workers.Wait()
	messages := sink.Messages()
	require.Len(t, messages, 32)
	for i := 0; i < len(messages); i += 2 {
		require.Equal(t, messages[i].SpanID, messages[i+1].SpanID)
		require.False(t, messages[i].Closing)
		require.True(t, messages[i+1].Closing)
	}
	require.Equal(t, 16, a.TurnToolUses)
}

func TestCodexRawExecSuppressesEmptyYield(t *testing.T) {
	t.Parallel()
	header := "Script running with cell ID 205\nWall time 1.0 seconds\nOutput:\n"
	for _, wrapped := range []bool{false, true} {
		t.Run(fmt.Sprint(wrapped), func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
			a.turnID = "raw-turn"
			request, output := codexRawExecFrames(t)
			var frame map[string]any
			require.NoError(t, json.Unmarshal(output, &frame))
			var content any = header
			if wrapped {
				content = []map[string]string{{"type": "input_text", "text": header}}
			}
			frame["params"].(map[string]any)["item"].(map[string]any)["output"] = content
			output, err := json.Marshal(frame)
			require.NoError(t, err)
			handleCodexOutput(a, providerkit.ParseLine(request))
			handleCodexOutput(a, providerkit.ParseLine(output))
			require.Empty(t, sink.Messages())
			require.Empty(t, sink.ReservedColorSpans())
			require.Empty(t, sink.OpenSpans())
		})
	}
}

func TestCodexRawExecPreservesYieldedContent(t *testing.T) {
	t.Parallel()
	header := "Script running with cell ID 205\nWall time 1.0 seconds\nOutput:\n"
	for _, content := range []any{
		header + "progress",
		[]map[string]string{{"type": "input_text", "text": header}, {"type": "input_text", "text": "progress"}},
		[]map[string]string{{"type": "input_text", "text": header}, {"type": "input_image", "image_url": "data:image/png;base64,AAAA"}},
		"Script failed\nWall time 1.0 seconds\nOutput:\n",
		"Script completed\nWall time 1.0 seconds\nOutput:\n",
		"Script running with cell ID \nWall time 1.0 seconds\nOutput:\n",
	} {
		raw, err := json.Marshal(content)
		require.NoError(t, err)
		require.False(t, codexSuppressRawExecutionOutput(raw), "content: %s", raw)
	}
}

func TestCodexRawExecRetainsFirstOutputOnRetry(t *testing.T) {
	t.Parallel()
	for _, failResult := range []bool{false, true} {
		t.Run(fmt.Sprint(failResult), func(t *testing.T) {
			sink := &codexRawExecFailingSink{failures: 1, failResult: failResult}
			a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
			a.turnID = "raw-turn"
			request, output := codexRawExecFrames(t)
			handleCodexOutput(a, providerkit.ParseLine(request))
			handleCodexOutput(a, providerkit.ParseLine(output))
			require.Zero(t, sink.failures)
			replacement := []byte(strings.ReplaceAll(string(output), "NATIVE_CODE_SUCCESS_42", "REPLACED_OUTPUT"))
			handleCodexOutput(a, providerkit.ParseLine(replacement))
			messages := sink.Messages()
			require.Len(t, messages, 2)
			assert.Equal(t, request, messages[0].Content)
			assert.Equal(t, output, messages[1].Content)
			assert.Equal(t, 1, a.TurnToolUses)
		})
	}
}

func TestCodexRawExecTurnEndPreservesOtherThread(t *testing.T) {
	t.Parallel()
	for _, endedThread := range []string{"main-thread", "child-thread"} {
		t.Run(endedThread, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
			a.turnID = "raw-turn"
			handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"raw-turn","item":{"type":"subAgentActivity","id":"spawn","kind":"started","agentThreadId":"child-thread","agentPath":"/root/child"}}}`)))
			handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-thread","turn":{"id":"raw-turn"}}}`)))
			rootRequest, rootOutput := rawExecPair(t, "shared-exec", "main-thread", "raw-turn")
			childRequest, childOutput := rawExecPair(t, "shared-exec", "child-thread", "raw-turn")
			handleCodexOutput(a, providerkit.ParseLine(rootRequest))
			handleCodexOutput(a, providerkit.ParseLine(childRequest))
			require.Len(t, a.rawExecutionCalls, 2)
			completion := fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":%q,"turn":{"id":"raw-turn","status":"completed"}}}`, endedThread)
			handleCodexOutput(a, providerkit.ParseLine([]byte(completion)))
			require.Len(t, a.rawExecutionCalls, 1)
			endedSink, retainedSink := sink, sink.Child("child-of-spawn")
			lateOutput, retainedOutput := rootOutput, childOutput
			if endedThread == "child-thread" {
				endedSink, retainedSink = retainedSink, endedSink
				lateOutput, retainedOutput = retainedOutput, lateOutput
			}
			endedRows := len(endedSink.Messages())
			handleCodexOutput(a, providerkit.ParseLine(lateOutput))
			assert.Len(t, endedSink.Messages(), endedRows)
			handleCodexOutput(a, providerkit.ParseLine(retainedOutput))
			messages := retainedSink.Messages()
			require.Len(t, messages, 2)
			assert.Equal(t, retainedOutput, messages[1].Content)
			assert.True(t, messages[1].Closing)
		})
	}
}

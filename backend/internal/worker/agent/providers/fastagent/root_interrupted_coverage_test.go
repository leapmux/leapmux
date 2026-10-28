package fastagent

import (
	"encoding/json"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func appendInterruptedModelFixture(t *testing.T, root *rootTranscript, kind agent.AssembledMessageKind, text string) string {
	t.Helper()
	raw, err := agent.MarshalAssembledMessage(kind, text, agent.MessageCompletionInterrupted)
	require.NoError(t, err)
	appendRootFixture(t, root, string(raw))
	return string(raw)
}

func appendInterruptedToolFixture(t *testing.T, root *rootTranscript, closing bool) string {
	t.Helper()
	position := root.Reserve()
	raw := `{"live":"original tool request"}`
	if closing {
		raw = `{"live":"original tool completion"}`
	}
	require.NoError(t, position.Writer().PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(raw)}, agent.SpanInfo{SpanID: "unrelated-live-uuid", SpanType: "execute", Closing: closing}))
	position.Complete()
	return raw
}

func TestFastagentInterruptedArchiveCannotOmitOrMoveCapturedModelOutput(t *testing.T) {
	for _, scenario := range []string{"missing text partial", "missing reasoning partial", "equal aggregate across a tool", "leading tool barrier", "empty later canceled generation", "empty later generation without tools"} {
		t.Run(scenario, func(t *testing.T) {
			a, sink := newFastagentRootSubject(t)
			root := openRootFixture(t, a)
			messages := rootNativeMessages("complete native tool output")[:3]
			var first map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(messages[1], &first))
			first["content"] = json.RawMessage(`[{"type":"text","text":"abc"}]`)
			kind := agent.AssembledMessageKindText
			if scenario == "missing reasoning partial" {
				kind = agent.AssembledMessageKindReasoning
				first["content"] = json.RawMessage(`[]`)
				first["channels"] = json.RawMessage(`{"reasoning":[{"type":"text","text":"abc"}]}`)
			}
			if scenario == "equal aggregate across a tool" || scenario == "leading tool barrier" || scenario == "empty later canceled generation" {
				first["content"] = json.RawMessage(`[{"type":"text","text":"abcXYZ"}]`)
			}
			if scenario == "leading tool barrier" {
				first["content"] = json.RawMessage(`[{"type":"text","text":"XYZ"}]`)
			}
			var expected []string
			if scenario != "leading tool barrier" {
				expected = append(expected, appendInterruptedModelFixture(t, root, kind, "abc"))
			}
			if scenario != "empty later generation without tools" {
				expected = append(expected, appendInterruptedToolFixture(t, root, false))
				expected = append(expected, appendInterruptedToolFixture(t, root, true))
			}
			expected = append(expected, appendInterruptedModelFixture(t, root, kind, "XYZ"))
			if scenario == "empty later canceled generation" {
				messages = append(messages, json.RawMessage(`{"role":"assistant","content":[],"stop_reason":"cancelled"}`))
			}
			var err error
			messages[1], err = json.Marshal(first)
			require.NoError(t, err)
			if scenario == "empty later generation without tools" {
				messages = []json.RawMessage{
					messages[0],
					json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"abcXYZ"}],"stop_reason":"continue"}`),
					json.RawMessage(`{"role":"assistant","content":[],"stop_reason":"cancelled"}`),
				}
			}
			writeRootNativeHistory(t, a, messages)
			require.NoError(t, root.Finish(acp.RootTranscriptCompletion{Reason: acp.RootTranscriptPromptEnded, Completion: agent.MessageCompletionInterrupted}))
			a.rootArchiveWG.Wait()
			rows := sink.Messages()
			require.Len(t, rows, len(expected), "an unproved archive must leave the complete captured representation selected")
			for index, original := range expected {
				assert.JSONEq(t, original, string(rows[index].Content))
			}
			assert.NotEmpty(t, sink.LeapMuxNotifications(), "the fallback must state why native preservation could not be proved")
		})
	}
}

func TestFastagentInterruptedArchiveKeepsProvedModelSegmentsAndNativeToolResults(t *testing.T) {
	for _, nativeText := range []string{"abc", "abcXYZ"} {
		t.Run(nativeText, func(t *testing.T) {
			a, sink := newFastagentRootSubject(t)
			root := openRootFixture(t, a)
			appendInterruptedModelFixture(t, root, agent.AssembledMessageKindText, "abc")
			appendInterruptedToolFixture(t, root, false)
			appendInterruptedToolFixture(t, root, true)
			messages := rootNativeMessages("complete native tool output")[:3]
			var first map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(messages[1], &first))
			first["content"], _ = json.Marshal([]map[string]string{{"type": "text", "text": nativeText}})
			var err error
			messages[1], err = json.Marshal(first)
			require.NoError(t, err)
			writeRootNativeHistory(t, a, messages)
			require.NoError(t, root.Finish(acp.RootTranscriptCompletion{Reason: acp.RootTranscriptPromptEnded, Completion: agent.MessageCompletionInterrupted}))
			a.rootArchiveWG.Wait()
			rows := sink.Messages()
			require.Len(t, rows, 3)
			assert.JSONEq(t, `{"type":"text","text":"`+nativeText+`"}`, string(rows[0].Content))
			assert.Contains(t, string(rows[2].Content), "complete native tool output")
			assert.NotEqual(t, "unrelated-live-uuid", rows[1].SpanID)
			assert.Empty(t, sink.LeapMuxNotifications())
		})
	}
}

func TestFastagentInterruptedArchiveKeepsProvedModelOutputWithNativeCancellationContext(t *testing.T) {
	for _, kind := range []agent.AssembledMessageKind{agent.AssembledMessageKindText, agent.AssembledMessageKindReasoning} {
		t.Run(string(kind), func(t *testing.T) {
			a, sink := newFastagentRootSubject(t)
			root := openRootFixture(t, a)
			appendInterruptedModelFixture(t, root, kind, "abc")
			appendInterruptedToolFixture(t, root, false)
			appendInterruptedToolFixture(t, root, true)
			messages := rootNativeMessages("")[:3]
			var first map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(messages[1], &first))
			if kind == agent.AssembledMessageKindReasoning {
				first["content"] = json.RawMessage(`[]`)
				first["channels"] = json.RawMessage(`{"reasoning":[{"type":"text","text":"abc"}]}`)
			} else {
				first["content"] = json.RawMessage(`[{"type":"text","text":"abc"}]`)
			}
			var err error
			messages[1], err = json.Marshal(first)
			require.NoError(t, err)
			messages[2] = json.RawMessage(`{"role":"user","content":[{"type":"text","text":"**The user interrupted this tool call**"}],"tool_results":{"model-call":{"content":[{"type":"text","text":"**The user interrupted this tool call**"}],"isError":true}}}`)
			writeRootNativeHistory(t, a, messages)
			require.NoError(t, root.Finish(acp.RootTranscriptCompletion{Reason: acp.RootTranscriptPromptEnded, Completion: agent.MessageCompletionInterrupted}))
			a.rootArchiveWG.Wait()
			rows := sink.Messages()
			require.Len(t, rows, 4)
			assert.JSONEq(t, `{"type":"text","text":"abc"}`, string(rows[0].Content))
			assert.True(t, rows[2].Closing)
			assert.Equal(t, agent.MessageCompletionInterrupted, rows[2].Completion)
			assert.JSONEq(t, `{"type":"text","text":"**The user interrupted this tool call**"}`, string(rows[3].Content))
			assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, rows[3].Source)
			assert.Empty(t, sink.LeapMuxNotifications())
		})
	}
}

func TestFastagentInterruptedArchiveRequiresExactEarlierModelRuns(t *testing.T) {
	a, sink := newFastagentRootSubject(t)
	root := openRootFixture(t, a)
	expected := []string{
		appendInterruptedModelFixture(t, root, agent.AssembledMessageKindText, "abc"),
		appendInterruptedToolFixture(t, root, false),
		appendInterruptedToolFixture(t, root, true),
		appendInterruptedModelFixture(t, root, agent.AssembledMessageKindText, "XYZ"),
	}
	messages := rootNativeMessages("complete native tool output")
	var first map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(messages[1], &first))
	first["content"] = json.RawMessage(`[{"type":"text","text":"abcMORE"}]`)
	var err error
	messages[1], err = json.Marshal(first)
	require.NoError(t, err)
	messages[3] = json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"XYZMORE"}],"stop_reason":"cancelled"}`)
	writeRootNativeHistory(t, a, messages)
	require.NoError(t, root.Finish(acp.RootTranscriptCompletion{Reason: acp.RootTranscriptPromptEnded, Completion: agent.MessageCompletionInterrupted}))
	a.rootArchiveWG.Wait()
	rows := sink.Messages()
	require.Len(t, rows, len(expected))
	for index, original := range expected {
		assert.JSONEq(t, original, string(rows[index].Content))
	}
	assert.NotEmpty(t, sink.LeapMuxNotifications(), "only the last captured model run can use a native prefix proof")
}

func TestFastagentInterruptedArchiveCannotOmitACapturedPlan(t *testing.T) {
	a, sink := newFastagentRootSubject(t)
	root := openRootFixture(t, a)
	original := appendInterruptedModelFixture(t, root, agent.AssembledMessageKindPlan, "Keep this accepted plan after interruption.")
	writeRootNativeHistory(t, a, rootNativeMessages("native output without the captured plan"))
	require.NoError(t, root.Finish(acp.RootTranscriptCompletion{Reason: acp.RootTranscriptPromptEnded, Completion: agent.MessageCompletionInterrupted}))
	a.rootArchiveWG.Wait()
	rows := sink.Messages()
	require.Len(t, rows, 1, "a known unsupported assembled kind must retain the complete captured representation")
	assert.JSONEq(t, original, string(rows[0].Content))
	assert.NotEmpty(t, sink.LeapMuxNotifications(), "the fallback must state why native preservation could not be proved")
}

func TestFastagentInterruptedArchiveJoinsNativeBlocksOnlyWithinTheSameGeneration(t *testing.T) {
	a, sink := newFastagentRootSubject(t)
	root := openRootFixture(t, a)
	firstBlock := strings.Repeat("🙂", 9000)
	appendInterruptedModelFixture(t, root, agent.AssembledMessageKindText, firstBlock+"追")
	appendInterruptedToolFixture(t, root, false)
	appendInterruptedToolFixture(t, root, true)
	messages := rootNativeMessages("complete native tool output")[:3]
	var first map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(messages[1], &first))
	content, err := json.Marshal([]map[string]string{
		{"type": "text", "text": firstBlock},
		{"type": "text", "text": "追加"},
	})
	require.NoError(t, err)
	first["content"] = content
	messages[1], err = json.Marshal(first)
	require.NoError(t, err)
	writeRootNativeHistory(t, a, messages)
	require.NoError(t, root.Finish(acp.RootTranscriptCompletion{Reason: acp.RootTranscriptPromptEnded, Completion: agent.MessageCompletionInterrupted}))
	a.rootArchiveWG.Wait()
	rows := sink.Messages()
	require.Len(t, rows, 4)
	for index, expected := range []string{firstBlock, "追加"} {
		var block struct {
			Text string `json:"text"`
		}
		require.NoError(t, json.Unmarshal(rows[index].Content, &block))
		assert.Equal(t, expected, block.Text)
	}
	assert.Contains(t, string(rows[3].Content), "complete native tool output")
	assert.Empty(t, sink.LeapMuxNotifications())
}

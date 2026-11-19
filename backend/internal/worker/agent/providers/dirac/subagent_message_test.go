package dirac

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func diracTestMessageRecord(t *testing.T, id, text string) contracts.DiracTranscriptRecord {
	t.Helper()
	message, err := json.Marshal(map[string]any{"id": id, "conversationHistoryIndex": 0, "content": map[string]any{"type": "markdown", "content": text, "role": "assistant"}})
	require.NoError(t, err)
	return contracts.DiracTranscriptRecord{Type: contracts.DiracRecordKindMessage, SessionID: "child", ParentSessionID: "parent", TaskletID: "Witten", Message: message}
}

func TestDiracNativeMessageReplayUpdatesOnlyItsCapturedView(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("child")
	services := agent.NewOrderedProviderServices(sink.OutputQueue(), agent.NewProviderServices(sink))
	writer, err := captureDiracTaskletTranscript(services, "child")
	require.NoError(t, err)
	first := diracTestMessageRecord(t, "native-text", "First")
	latest := diracTestMessageRecord(t, "native-text", "First and last")
	require.NoError(t, writer.publish([]contracts.DiracTranscriptRecord{first}))
	require.NoError(t, agent.FlushPendingOutput(writer))
	original := sink.Messages()[0].Content
	require.NoError(t, writer.publish([]contracts.DiracTranscriptRecord{latest}))
	require.NoError(t, agent.FlushPendingOutput(writer))
	require.NoError(t, writer.publish([]contracts.DiracTranscriptRecord{latest}))
	require.NoError(t, agent.FlushPendingOutput(writer))
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Equal(t, original, rows[0].Content)
	assert.EqualValues(t, 1, rows[0].SupplementalRevision, "equal replay creates no revision")
	var fields map[string]contracts.DiracTranscriptRecord
	require.NoError(t, json.Unmarshal(rows[0].SupplementalContent, &fields))
	assert.JSONEq(t, string(latest.Message), string(fields[contracts.DiracSupplementRootMessage].Message))
}

func TestDiracNativeMessageCompletionUsesItsExactRequestContext(t *testing.T) {
	t.Parallel()
	for reason, expected := range map[string]agent.MessageCompletion{"user_cancelled": agent.MessageCompletionInterrupted, "streaming_failed": agent.MessageCompletionError, "retries_exhausted": agent.MessageCompletionError, "": agent.MessageCompletionComplete} {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			sink.UpdateSessionID("child")
			services := agent.NewOrderedProviderServices(sink.OutputQueue(), agent.NewProviderServices(sink))
			writer, err := captureDiracTaskletTranscript(services, "child")
			require.NoError(t, err)
			request := map[string]any{"id": "native-api-request", "cancelReason": reason}
			if reason == "" {
				request["stopReason"] = "end_turn"
			}
			status, err := json.Marshal(map[string]any{"id": "native-api-request", "conversationHistoryIndex": -1, "content": map[string]any{"type": "api_status", "status": request}})
			require.NoError(t, err)
			message := diracTestMessageRecord(t, "partial-text", "The native partial answer")
			records, err := diracTaskletMessageRecords("parent", diracTaskletRecord{ID: "Witten", ConversationID: "child"}, []json.RawMessage{status, message.Message})
			require.NoError(t, err)
			require.Len(t, records, 1)
			require.NoError(t, writer.publish(records))
			require.NoError(t, agent.FlushPendingOutput(writer))
			rows := sink.Messages()
			require.Len(t, rows, 1)
			assert.Equal(t, expected, rows[0].Completion)
		})
	}
}

func TestDiracNativeTranscriptKeepsPresentationAndToolsInNativeOrder(t *testing.T) {
	t.Parallel()
	api := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"Read the file"}]}`),
		json.RawMessage(`{"role":"assistant","content":[{"type":"tool_use","id":"read-1","name":"read_file","input":{"paths":["a.txt"]}}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"read-1","content":"Actual native contents"}]}`),
	}
	before := diracTestMessageRecord(t, "before", "Before Read")
	after := diracTestMessageRecord(t, "after", "After Read")
	var value map[string]any
	require.NoError(t, json.Unmarshal(after.Message, &value))
	value["conversationHistoryIndex"] = 2
	after.Message, _ = json.Marshal(value)
	records, err := diracTaskletTranscriptRecords("parent", diracTaskletRecord{ID: "Witten", ConversationID: "child"}, api, []json.RawMessage{before.Message, after.Message})
	require.NoError(t, err)
	require.Len(t, records, 4)
	assert.JSONEq(t, string(before.Message), string(records[0].Message))
	assert.Equal(t, contracts.DiracRecordRoleRequest, records[1].Role)
	assert.Equal(t, contracts.DiracRecordRoleResult, records[2].Role)
	assert.JSONEq(t, string(after.Message), string(records[3].Message))
}

func TestDiracInterruptedPendingRequestUsesItsCommittedTaskletOutcome(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("child")
	services := agent.NewOrderedProviderServices(sink.OutputQueue(), agent.NewProviderServices(sink))
	writer, err := captureDiracTaskletTranscript(services, "child")
	require.NoError(t, err)
	api := []json.RawMessage{json.RawMessage(`{"role":"user","content":[{"type":"text","text":"The native request"}]}`)}
	presentation := []json.RawMessage{
		json.RawMessage(`{"id":"native-request","conversationHistoryIndex":-1,"content":{"type":"api_status","status":{"id":"native-request"}}}`),
		json.RawMessage(`{"id":"partial-voice","conversationHistoryIndex":0,"content":{"type":"markdown","role":"assistant","content":"Native partial text"}}`),
	}
	rawOutcome := json.RawMessage(`{"kind":"interrupted","reason":"Root Task closed","interruptedAt":1}`)
	var outcome diracTaskletOutcome
	require.NoError(t, json.Unmarshal(rawOutcome, &outcome))
	records, err := diracTaskletTranscriptRecords("parent", diracTaskletRecord{ID: "Witten", ConversationID: "child", NativeOutcome: rawOutcome, Outcome: &outcome}, api, presentation)
	require.NoError(t, err)
	require.NoError(t, writer.publish(records))
	require.NoError(t, agent.FlushPendingOutput(writer))
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[0].Completion)
}

func TestDiracTaskletOutcomeSettlesOnlyItsUnfinishedPresentation(t *testing.T) {
	t.Parallel()
	user := json.RawMessage(`{"role":"user","content":[{"type":"text","text":"Native request"}]}`)
	assistant := json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"Completed native answer"}]}`)
	voice := json.RawMessage(`{"id":"native-voice","conversationHistoryIndex":0,"content":{"type":"markdown","role":"assistant","content":"Native text"}}`)
	rawOutcome := json.RawMessage(`{"kind":"interrupted","reason":"Root Task closed","interruptedAt":1}`)
	var outcome diracTaskletOutcome
	require.NoError(t, json.Unmarshal(rawOutcome, &outcome))
	for _, test := range []struct {
		name         string
		api          []json.RawMessage
		presentation []json.RawMessage
		committed    bool
		want         agent.MessageCompletion
	}{
		{"pending request", []json.RawMessage{user}, []json.RawMessage{voice}, true, agent.MessageCompletionInterrupted},
		{"completed request", []json.RawMessage{user, assistant}, []json.RawMessage{voice}, true, ""},
		{"earlier request", []json.RawMessage{user, assistant, user}, []json.RawMessage{voice}, true, ""},
		{"uncommitted outcome", []json.RawMessage{user}, []json.RawMessage{voice}, false, ""},
		{"another deleted range", []json.RawMessage{user}, []json.RawMessage{voice, json.RawMessage(`{"id":"status","conversationHistoryIndex":0,"conversationHistoryDeletedRange":[0,2],"content":{"type":"api_status","status":{"id":"status"}}}`)}, true, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tasklet := diracTaskletRecord{ID: "Witten", ConversationID: "child", NativeOutcome: rawOutcome}
			if test.committed {
				tasklet.Outcome = &outcome
			}
			records, err := diracTaskletTranscriptRecords("parent", tasklet, test.api, test.presentation)
			require.NoError(t, err)
			require.Len(t, records, 1)
			completion, err := diracMessageCompletion(records[0])
			require.NoError(t, err)
			assert.Equal(t, test.want, completion)
		})
	}
}

func TestDiracTaskletOutcomeDoesNotBypassRequestOwnership(t *testing.T) {
	t.Parallel()
	record := diracTestMessageRecord(t, "native-text", "Partial native text")
	record.TaskletOutcome = json.RawMessage(`{"kind":"interrupted","reason":"Root Task closed","interruptedAt":1}`)
	record.RequestContext = json.RawMessage(`{"message_id":"foreign-request","conversation_history_index":3}`)
	_, err := diracTranscriptRecordKey(record)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "another conversation position")
}

func TestDiracTaskletOutcomePreservesTheExactRequestResult(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		cancel     string
		stop       string
		completion agent.MessageCompletion
		failure    string
	}{
		{"completed generation", "", "end_turn", agent.MessageCompletionComplete, ""},
		{"failed generation", "streaming_failed", "", agent.MessageCompletionError, ""},
		{"cancelled generation", "user_cancelled", "", agent.MessageCompletionInterrupted, ""},
		{"unknown cancellation", "unknown-native-cancellation", "", "", "unknown cancellation reason"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			record := diracTestMessageRecord(t, "native-text", "The native response")
			index := int64(-1)
			context := contracts.DiracRequestContext{MessageID: "native-request", HistoryIndex: &index, CancelReason: test.cancel, StopReason: test.stop}
			var err error
			record.RequestContext, err = json.Marshal(context)
			require.NoError(t, err)
			record.TaskletOutcome = json.RawMessage(`{"kind":"interrupted","reason":"Root Task closed","interruptedAt":1}`)
			completion, err := diracMessageCompletion(record)
			if test.failure != "" {
				require.ErrorContains(t, err, test.failure)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.completion, completion)
		})
	}
}

func TestDiracTaskletOutcomeUpdatePreservesItsOriginalAndCompletion(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("child")
	services := agent.NewOrderedProviderServices(sink.OutputQueue(), agent.NewProviderServices(sink))
	writer, err := captureDiracTaskletTranscript(services, "child")
	require.NoError(t, err)
	record := diracTestMessageRecord(t, "native-partial", "Native partial answer")
	require.NoError(t, writer.publish([]contracts.DiracTranscriptRecord{record}))
	require.NoError(t, agent.FlushPendingOutput(writer))
	original := sink.Messages()[0].Content
	record.TaskletOutcome = json.RawMessage(`{"kind":"interrupted","reason":"Root Task closed","interruptedAt":1}`)
	require.NoError(t, writer.publish([]contracts.DiracTranscriptRecord{record}))
	require.NoError(t, agent.FlushPendingOutput(writer))
	assert.Equal(t, original, sink.Messages()[0].Content)
	assert.Equal(t, agent.MessageCompletionInterrupted, sink.Messages()[0].Completion)
	record.TaskletOutcome = nil
	require.NoError(t, writer.publish([]contracts.DiracTranscriptRecord{record}))
	require.NoError(t, agent.FlushPendingOutput(writer))
	assert.Equal(t, agent.MessageCompletionInterrupted, sink.Messages()[0].Completion)
	assert.EqualValues(t, 1, sink.Messages()[0].SupplementalRevision)
}

func TestDiracNativeMessageContextUpdatePreservesItsOriginalAndCancellation(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("child")
	services := agent.NewOrderedProviderServices(sink.OutputQueue(), agent.NewProviderServices(sink))
	writer, err := captureDiracTaskletTranscript(services, "child")
	require.NoError(t, err)
	record := diracTestMessageRecord(t, "native-text", "Unchanged native partial text")
	index := int64(-1)
	context := contracts.DiracRequestContext{MessageID: "native-request", HistoryIndex: &index}
	record.RequestContext, err = json.Marshal(context)
	require.NoError(t, err)
	require.NoError(t, writer.publish([]contracts.DiracTranscriptRecord{record}))
	require.NoError(t, agent.FlushPendingOutput(writer))
	original := sink.Messages()[0].Content
	context.CancelReason = "user_cancelled"
	record.RequestContext, err = json.Marshal(context)
	require.NoError(t, err)
	require.NoError(t, writer.publish([]contracts.DiracTranscriptRecord{record}))
	require.NoError(t, agent.FlushPendingOutput(writer))
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Equal(t, original, rows[0].Content)
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[0].Completion)
	context.MessageID, context.CancelReason, context.StopReason = "another-request", "", "end_turn"
	record.RequestContext, err = json.Marshal(context)
	require.NoError(t, err)
	require.NoError(t, writer.publish([]contracts.DiracTranscriptRecord{record}))
	require.NoError(t, agent.FlushPendingOutput(writer))
	assert.Equal(t, agent.MessageCompletionInterrupted, sink.Messages()[0].Completion)
}

func TestDiracNativeMessageRejectsAmbiguousOrForeignRequestPositions(t *testing.T) {
	t.Parallel()
	message := diracTestMessageRecord(t, "native-text", "Native text")
	status := json.RawMessage(`{"id":"request","conversationHistoryIndex":-1,"content":{"type":"api_status","status":{"cancelReason":"user_cancelled"}}}`)
	for _, statuses := range [][]json.RawMessage{
		{json.RawMessage(`{"id":"request","conversationHistoryIndex":0,"content":{"type":"api_status","status":{"cancelReason":"user_cancelled"}}}`)},
		{status, json.RawMessage(`{"id":"other-request","conversationHistoryIndex":-1,"content":{"type":"api_status","status":{"cancelReason":"streaming_failed"}}}`)},
		{json.RawMessage(`{"id":"request","conversationHistoryIndex":-1,"conversationHistoryDeletedRange":[0,2],"content":{"type":"api_status","status":{"cancelReason":"user_cancelled"}}}`)},
	} {
		records, err := diracTaskletMessageRecords("parent", diracTaskletRecord{ID: "Witten", ConversationID: "child"}, append(statuses, message.Message))
		require.NoError(t, err)
		require.Len(t, records, 1)
		assert.Empty(t, records[0].RequestContext)
	}
}

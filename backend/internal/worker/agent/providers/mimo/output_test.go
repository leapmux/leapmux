package mimo

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mimoTurnFrameCases lists the events that move the turn flag and a sample of
// every other event family, plus an event that does not exist yet.
func mimoTurnFrameCases() []agenttest.TurnFrameCase {
	return []agenttest.TurnFrameCase{
		{Name: "busy", Line: `{"type":"session.status","properties":{"sessionID":"ses_test","status":{"type":"busy"}}}`, Moves: true},
		{Name: "retry", Line: `{"type":"session.status","properties":{"sessionID":"ses_test","status":{"type":"retry","attempt":1,"message":"overloaded","next":1}}}`, Moves: true},
		{Name: "idle", Line: `{"type":"session.status","properties":{"sessionID":"ses_test","status":{"type":"idle"}}}`, Moves: true},
		{Name: "error outside a turn", Line: `{"type":"session.error","properties":{"sessionID":"ses_test","error":{"name":"ProviderModelNotFoundError","data":{"message":"no such model"}}}}`, Moves: true},

		{Name: "abort error", Line: `{"type":"session.error","properties":{"sessionID":"ses_test","error":{"name":"MessageAbortedError","data":{"message":"aborted"}}}}`},
		{Name: "busy of another session", Line: `{"type":"session.status","properties":{"sessionID":"ses_other","status":{"type":"busy"}}}`},
		{Name: "error of another session", Line: `{"type":"session.error","properties":{"sessionID":"ses_other","error":{"name":"APIError"}}}`},
		{Name: "unknown status", Line: `{"type":"session.status","properties":{"sessionID":"ses_test","status":{"type":"sleeping"}}}`},
		{Name: "connected", Line: `{"type":"server.connected","properties":{}}`},
		{Name: "heartbeat", Line: `{"type":"server.heartbeat","properties":{}}`},
		{Name: "message", Line: `{"type":"message.updated","properties":{"sessionID":"ses_test","info":{"id":"msg_1","sessionID":"ses_test","role":"assistant","agentID":"main"}}}`},
		{Name: "text part", Line: `{"type":"message.part.updated","properties":{"part":{"id":"prt_1","messageID":"msg_1","sessionID":"ses_test","type":"text","text":"hi","time":{"start":1,"end":2}}}}`},
		{Name: "delta", Line: `{"type":"message.part.delta","properties":{"sessionID":"ses_test","messageID":"msg_1","partID":"prt_1","field":"text","delta":"hi"}}`},
		{Name: "tool part", Line: `{"type":"message.part.updated","properties":{"part":{"id":"prt_2","messageID":"msg_1","sessionID":"ses_test","type":"tool","tool":"bash","callID":"call-1","state":{"status":"running","input":{"command":"ls"}}}}}`},
		{Name: "compaction part", Line: `{"type":"message.part.updated","properties":{"part":{"id":"prt_3","messageID":"msg_1","sessionID":"ses_test","type":"compaction"}}}`},
		{Name: "compacted", Line: `{"type":"session.compacted","properties":{"sessionID":"ses_test"}}`},
		{Name: "goal", Line: `{"type":"session.goal","properties":{"sessionID":"ses_test","goal":{"condition":"tests pass"}}}`},
		{Name: "permission", Line: `{"type":"permission.asked","properties":{"id":"per_1","sessionID":"ses_test","permission":"bash","patterns":["ls"],"metadata":{},"always":["ls *"]}}`},
		{Name: "permission replied", Line: `{"type":"permission.replied","properties":{"sessionID":"ses_test","requestID":"per_1","reply":"once"}}`},
		{Name: "question", Line: `{"type":"question.asked","properties":{"id":"que_1","sessionID":"ses_test","questions":[{"question":"Pick","header":"Pick","options":[{"label":"A"}]}]}}`},
		{Name: "question replied", Line: `{"type":"question.replied","properties":{"sessionID":"ses_test","requestID":"que_1","answers":[["A"]]}}`},
		{Name: "question rejected", Line: `{"type":"question.rejected","properties":{"sessionID":"ses_test","requestID":"que_1"}}`},
		{Name: "interactive command", Line: `{"type":"bash.interactive.asked","properties":{"id":"0b5d6a4e","sessionID":"ses_test","command":"vim","description":"edit"}}`},
		{Name: "actor registered", Line: `{"type":"actor.registered","properties":{"sessionID":"ses_test","actorID":"general-1","mode":"subagent","description":"Helper","agent":"general","background":true}}`},
		{Name: "actor status", Line: `{"type":"actor.status","properties":{"sessionID":"ses_test","actorID":"general-1","status":"running","turnCount":0,"lastTurnTime":1}}`},
		{Name: "workflow", Line: `{"type":"workflow.started","properties":{"sessionID":"ses_test","runID":"wf_1","name":"review"}}`},
		{Name: "ignored event", Line: `{"type":"session.updated","properties":{"sessionID":"ses_test"}}`},
		{Name: "future event", Line: `{"type":"session.hibernated","properties":{"sessionID":"ses_test"}}`},
		{Name: "not an event", Line: `{"properties":{}}`},
	}
}

func TestTurnFrames(t *testing.T) {
	t.Parallel()
	agenttest.AssertTurnFrames(t, mimoTurnFrameCases(), func(t *testing.T, tc agenttest.TurnFrameCase) []bool {
		a, sink, _ := newSinkTestAgent(t)
		a.HandleOutput([]byte(tc.Line))
		return sink.TurnActives()
	})
}

func TestRisingTurnTokens(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	agenttest.AssertRisingTurnTokens(t, sink, a)
}

func assembledText(t *testing.T, content []byte) (kind, text, completion string) {
	t.Helper()
	var row map[string]string
	require.NoError(t, json.Unmarshal(content, &row))
	require.Equal(t, contracts.AssembledMessageType, row[contracts.AssembledMessageFieldType], "row %s", content)
	return row[contracts.AssembledMessageFieldKind], row[contracts.AssembledMessageFieldText], row[contracts.AssembledMessageFieldCompletion]
}

func assembledRows(sink *agenttest.Sink) []agenttest.Message {
	var rows []agenttest.Message
	for _, row := range sink.Messages() {
		kind, _ := agent.MessageMetadata(agent.MessageContent{Original: row.Content})
		if kind != leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_UNSPECIFIED {
			rows = append(rows, row)
		}
	}
	return rows
}

func actorAbortMessageEvent(t *testing.T, messageID, actor string) []byte {
	t.Helper()
	return eventJSON(t, eventMessageUpdated, map[string]any{"sessionID": testSessionID, "info": map[string]any{
		"id": messageID, "sessionID": testSessionID, "role": roleAssistant, "agentID": actor,
		"error": map[string]any{"name": contracts.MiMoErrorNameAborted, "data": map[string]any{"message": "Aborted"}},
	}})
}

type controlledPartSink struct {
	agent.ProviderServices
	reject     atomic.Int32
	rejectText string
}

func (s *controlledPartSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	return s.persistTo(s.ProviderServices, source, content, span)
}

func (s *controlledPartSink) persistTo(target agent.ProviderServices, source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	if s.reject.Load() > 0 && (s.rejectText == "" || strings.Contains(string(content.Original), s.rejectText)) {
		s.reject.Add(-1)
		return errors.New("the controlled part write failed")
	}
	return target.PersistMessage(source, content, span)
}

func (s *controlledPartSink) ChildSink(childID string) agent.ProviderServices {
	return &controlledChildPartSink{ProviderServices: s.ProviderServices.ChildSink(childID), control: s}
}

type controlledChildPartSink struct {
	agent.ProviderServices
	control *controlledPartSink
}

func (s *controlledChildPartSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	return s.control.persistTo(s.ProviderServices, source, content, span)
}

func TestChildCancellationKeepsTheParentTurnNormal(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	feed(a, messageEvent(t, "msg_child", roleAssistant, actorID, false),
		textPartEvent(t, partTypeText, "part_child", "msg_child", "", false),
		deltaEvent(t, "part_child", "msg_child", "The child partial answer."),
		statusEvent(t, contracts.MiMoStatusTypeIdle))
	beginPartialAnswer(t, a, "msg_parent", "part_parent", "The normal parent answer.")
	feed(a, nativeAbortMarker(t, "session", "msg_child"),
		textPartEvent(t, partTypeText, "part_child", "msg_child", "The child partial answer.", true),
		actorAbortMessageEvent(t, "msg_child", actorID),
		actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeCancelled, 1, ""),
		textPartEvent(t, partTypeText, "part_parent", "msg_parent", "The normal parent answer.", true),
		messageEvent(t, "msg_parent", roleAssistant, mainActorID, true),
		statusEvent(t, contracts.MiMoStatusTypeIdle))
	parent := assembledRows(sink)
	require.Len(t, parent, 1)
	_, text, completion := assembledText(t, parent[0].Content)
	assert.Equal(t, "The normal parent answer.", text)
	assert.Equal(t, string(agent.MessageCompletionComplete), completion)
	rows := sink.Messages()
	assert.Equal(t, agent.MessageCompletionComplete, rows[len(rows)-1].Completion)
	child := assembledRows(sink.Child("child-of-" + spawnSpanID))
	require.Len(t, child, 1)
	_, text, completion = assembledText(t, child[0].Content)
	assert.Equal(t, "The child partial answer.", text)
	assert.Equal(t, string(agent.MessageCompletionInterrupted), completion)
}

func TestUnattributedAbortKeepsMixedPartsInNativeOrder(t *testing.T) {
	t.Parallel()
	for _, firstSeenFinal := range []bool{false, true} {
		t.Run(map[bool]string{false: "opening and closing", true: "first seen final"}[firstSeenFinal], func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			beginPartialAnswer(t, a, "msg_mixed", "part_text", "The cut text.")
			feed(a, textPartEvent(t, partTypeReasoning, "part_reasoning", "msg_mixed", "", false),
				deltaEvent(t, "part_reasoning", "msg_mixed", "The cut reasoning."),
				nativeAbortMarker(t, "session", "msg_mixed"),
				textPartEvent(t, partTypeReasoning, "part_reasoning", "msg_mixed", "The cut reasoning.", true),
				textPartEvent(t, partTypeText, "part_text", "msg_mixed", "The cut text.", true))
			assert.Empty(t, sink.Messages(), "the session error does not identify the owner yet")
			if !firstSeenFinal {
				feed(a, toolPartEvent(t, "part_tool", "msg_mixed", contracts.MiMoToolBash, "call_completed", toolState{
					Status: contracts.MiMoToolStatusRunning, Input: map[string]any{"command": "printf done"},
				}))
			}
			final := toolPartEvent(t, "part_tool", "msg_mixed", contracts.MiMoToolBash, "call_completed", toolState{
				Status: contracts.MiMoToolStatusCompleted, Input: map[string]any{"command": "printf done"},
				Output: "done", Metadata: map[string]any{"exit": 0},
			})
			feed(a, final)
			assert.Empty(t, sink.Messages(), "the same message's tool must stay behind its earlier parts")
			for _, progress := range sink.ProgressUpdates() {
				assert.NotEqual(t, agent.ProgressModelComplete, progress.Operation, "the live part stays visible while attribution waits")
			}
			feed(a, nativeAbortMarker(t, "message", "msg_mixed"))
			rows := sink.Messages()
			want := 3
			if !firstSeenFinal {
				want++
			}
			require.Len(t, rows, want)
			kind, text, completion := assembledText(t, rows[0].Content)
			assert.Equal(t, string(agent.AssembledMessageKindReasoning), kind)
			assert.Equal(t, "The cut reasoning.", text)
			assert.Equal(t, string(agent.MessageCompletionInterrupted), completion)
			kind, text, completion = assembledText(t, rows[1].Content)
			assert.Equal(t, string(agent.AssembledMessageKindText), kind)
			assert.Equal(t, "The cut text.", text)
			assert.Equal(t, string(agent.MessageCompletionInterrupted), completion)
			assert.JSONEq(t, string(final), string(rows[want-1].Content))
			assert.True(t, rows[want-1].Closing)
			assert.Empty(t, rows[want-1].Completion, "the completed native tool keeps its own outcome")
		})
	}
}

func TestUnresolvedMessageDoesNotHoldAnotherMessagesTool(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	beginPartialAnswer(t, a, "msg_cut", "part_cut", "The cut text.")
	feed(a, nativeAbortMarker(t, "session", "msg_cut"),
		textPartEvent(t, partTypeText, "part_cut", "msg_cut", "The cut text.", true),
		messageEvent(t, "msg_other", roleAssistant, mainActorID, false))
	final := toolPartEvent(t, "part_other", "msg_other", contracts.MiMoToolBash, "call_other", toolState{
		Status: contracts.MiMoToolStatusCompleted, Input: map[string]any{"command": "printf other"},
		Output: "other", Metadata: map[string]any{"exit": 0},
	})
	feed(a, final)
	rows := sink.Messages()
	require.Len(t, rows, 1, "another message remains independent")
	assert.JSONEq(t, string(final), string(rows[0].Content))
	assert.Empty(t, rows[0].Completion)
	feed(a, nativeAbortMarker(t, "message", "msg_cut"))
	rows = sink.Messages()
	require.Len(t, rows, 2)
	_, _, completion := assembledText(t, rows[1].Content)
	assert.Equal(t, string(agent.MessageCompletionInterrupted), completion)
}

func TestRetainedFinalPartSurvivesPersistenceFailure(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &controlledPartSink{ProviderServices: a.sink}
	a.sink = controlled
	beginPartialAnswer(t, a, "msg_retry", "part_retry", "The streamed prefix.")
	controlled.reject.Store(1)
	feed(a, nativeAbortMarker(t, "session", "msg_retry"),
		textPartEvent(t, partTypeText, "part_retry", "msg_retry", "The full native final answer.", true))
	feed(a, nativeAbortMarker(t, "message", "msg_retry"))
	assert.Empty(t, assembledRows(sink), "a failed write publishes no final row")
	feed(a, nativeAbortMarker(t, "message", "msg_retry"), nativeAbortMarker(t, "message", "msg_retry"))
	rows := assembledRows(sink)
	require.Len(t, rows, 1, "the repeated metadata retries the retained part and does not duplicate it")
	_, text, completion := assembledText(t, rows[0].Content)
	assert.Equal(t, "The full native final answer.", text, "the retained final replaces the streamed prefix")
	assert.Equal(t, string(agent.MessageCompletionInterrupted), completion)
}

func TestRetainedFinalPartKeepsItsOutcomeAcrossReplacement(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	controlled := &controlledPartSink{ProviderServices: a.sink, rejectText: "The old completed answer."}
	a.sink = controlled
	beginPartialAnswer(t, a, "msg_old", "part_old", "The old prefix.")
	gates := holdAbortReplies(t, server, 2)
	gate := gates[0]
	result := startInterrupt(a)
	awaitAbortArrival(t, gate)
	controlled.reject.Store(2)
	feed(a, textPartEvent(t, partTypeText, "part_old", "msg_old", "The old completed answer.", true))
	feed(a, messageEvent(t, "msg_old", roleAssistant, mainActorID, true), statusEvent(t, contracts.MiMoStatusTypeIdle))
	gate.reply <- http.StatusInternalServerError
	require.Error(t, awaitInterrupt(t, result))
	beginPartialAnswer(t, a, "msg_new", "part_new", "The new cut answer.")
	second := startInterrupt(a)
	awaitAbortArrival(t, gates[1])
	gates[1].reply <- http.StatusOK
	require.NoError(t, awaitInterrupt(t, second))
	feed(a, messageEvent(t, "msg_old", roleAssistant, mainActorID, true))
	rows := assembledRows(sink)
	require.Len(t, rows, 1)
	_, text, completion := assembledText(t, rows[0].Content)
	assert.Equal(t, "The old completed answer.", text)
	assert.Equal(t, string(agent.MessageCompletionComplete), completion)
	feed(a, nativeAbortMarker(t, "message", "msg_new"),
		textPartEvent(t, partTypeText, "part_new", "msg_new", "The new cut answer.", true),
		statusEvent(t, contracts.MiMoStatusTypeIdle))
	rows = assembledRows(sink)
	require.Len(t, rows, 2)
	_, text, completion = assembledText(t, rows[1].Content)
	assert.Equal(t, "The new cut answer.", text)
	assert.Equal(t, string(agent.MessageCompletionInterrupted), completion)
}

func TestDelayedSpawnPartKeepsItsChildIdentity(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	beginPartialAnswer(t, a, "msg_spawn", "part_thought", "The complete thought.")
	feed(a, nativeAbortMarker(t, "session", "msg_spawn"),
		textPartEvent(t, partTypeText, "part_thought", "msg_spawn", "The complete thought.", true),
		actorRegisteredEvent(t, actorID, true),
		toolPartEvent(t, "part_spawn", "msg_spawn", contracts.MiMoToolActor, "call_delayed_spawn", toolState{
			Status: contracts.MiMoToolStatusRunning, Input: spawnInputOf(contracts.MiMoActorActionSpawn, "Deferred native spawn", "The exact child task."),
			Metadata: map[string]any{"actorId": actorID},
		}), actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""),
		messageEvent(t, "msg_child", roleAssistant, actorID, false),
		textPartEvent(t, partTypeText, "part_child", "msg_child", "The exact child answer.", true),
		messageEvent(t, "msg_child", roleAssistant, actorID, true))
	assert.Empty(t, sink.Messages(), "the parent spawn row stays behind its earlier thought")
	childID := "child-of-part_spawn"
	assert.Equal(t, []string{childID}, sink.ChildAgentIDs())
	childMessages := sink.Child(childID).Messages()
	require.Len(t, childMessages, 2, "the delayed spawn retains the original child prompt")
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, childMessages[0].Source)
	assert.JSONEq(t, `{"content":"The exact child task."}`, string(childMessages[0].Content))
	child := assembledRows(sink.Child(childID))
	require.Len(t, child, 1)
	_, text, completion := assembledText(t, child[0].Content)
	assert.Equal(t, "The exact child answer.", text)
	assert.Equal(t, string(agent.MessageCompletionComplete), completion)
	feed(a, messageEvent(t, "msg_spawn", roleAssistant, mainActorID, true))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	_, text, completion = assembledText(t, rows[0].Content)
	assert.Equal(t, "The complete thought.", text)
	assert.Equal(t, string(agent.MessageCompletionComplete), completion)
	assert.Equal(t, "part_spawn", rows[1].SpanID)
}

func TestRetainedToolOpeningRetriesWithoutDuplicateSpans(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &controlledPartSink{ProviderServices: a.sink, rejectText: "part_retry_tool"}
	a.sink = controlled
	beginPartialAnswer(t, a, "msg_retry_tool", "part_thought", "The cut thought.")
	controlled.reject.Store(1)
	feed(a, nativeAbortMarker(t, "session", "msg_retry_tool"),
		textPartEvent(t, partTypeText, "part_thought", "msg_retry_tool", "The cut thought.", true),
		toolPartEvent(t, "part_retry_tool", "msg_retry_tool", contracts.MiMoToolBash, "call_retry_tool", toolState{
			Status: contracts.MiMoToolStatusRunning, Input: map[string]any{"command": "printf done"},
		}), toolPartEvent(t, "part_retry_tool", "msg_retry_tool", contracts.MiMoToolBash, "call_retry_tool", toolState{
			Status: contracts.MiMoToolStatusCompleted, Input: map[string]any{"command": "printf done"}, Output: "done",
		}), nativeAbortMarker(t, "message", "msg_retry_tool"))
	require.Len(t, sink.Messages(), 1, "the failed opener keeps its later result behind it")
	feed(a, nativeAbortMarker(t, "message", "msg_retry_tool"))
	rows := sink.Messages()
	require.Len(t, rows, 3)
	assert.False(t, rows[1].Closing)
	assert.True(t, rows[2].Closing)
	assert.Equal(t, []agenttest.SpanOpen{{SpanID: "part_retry_tool"}}, rows[2].SpansOpenAtPersist,
		"the opening retry keeps exactly one active span")
}

func TestFailedToolOpeningLeavesNoSpanOnAnotherMessagesRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &controlledPartSink{ProviderServices: a.sink, rejectText: "part_failed_opening"}
	a.sink = controlled
	controlled.reject.Store(1)
	feed(a, messageEvent(t, "msg_failed_opening", roleAssistant, mainActorID, false),
		toolPartEvent(t, "part_failed_opening", "msg_failed_opening", contracts.MiMoToolBash, "call_failed_opening", toolState{
			Status: contracts.MiMoToolStatusRunning, Input: map[string]any{"command": "printf failed"},
		}), messageEvent(t, "msg_independent", roleAssistant, mainActorID, false),
		textPartEvent(t, partTypeText, "part_independent", "msg_independent", "The independent message.", true))
	rows := assembledRows(sink)
	require.Len(t, rows, 1)
	assert.Empty(t, rows[0].SpansOpenAtPersist, "a failed opening must not draw a tool rail on an unrelated message")
}

func TestFailedUnfinishedToolClosingSurvivesItsSessionClear(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &controlledPartSink{ProviderServices: a.sink, rejectText: "part_cut_retry"}
	a.sink = controlled
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), messageEvent(t, "msg_cut_retry", roleAssistant, mainActorID, false))
	opening := toolPartEvent(t, "part_cut_retry", "msg_cut_retry", contracts.MiMoToolBash, "call_cut_retry", toolState{
		Status: contracts.MiMoToolStatusRunning, Input: map[string]any{"command": "printf retained"},
	})
	feed(a, opening)
	controlled.reject.Store(10)
	_, err := a.ClearContext()
	require.NoError(t, err)
	controlled.reject.Store(0)
	a.flushUnfinishedOutput(agent.MessageCompletionComplete)
	rows := sink.Messages()
	require.Len(t, rows, 2, "the failed unfinished closing remains available after the session clear")
	assert.JSONEq(t, string(opening), string(rows[1].Content))
	assert.Equal(t, "part_cut_retry", rows[1].SpanID)
	assert.True(t, rows[1].Closing)
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[1].Completion)
	assert.Equal(t, testSessionID, rows[1].AgentSessionID)
	assert.Empty(t, a.messages["msg_cut_retry"].pendingParts)
}

func TestFailedPendingMessageDoesNotBlockANewerFinalAtIdle(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &controlledPartSink{ProviderServices: a.sink, rejectText: "The old full final."}
	a.sink = controlled
	beginPartialAnswer(t, a, "msg_old", "part_old", "The old prefix.")
	controlled.reject.Store(10)
	feed(a, nativeAbortMarker(t, "session", "msg_old"),
		textPartEvent(t, partTypeText, "part_old", "msg_old", "The old full final.", true),
		messageEvent(t, "msg_old", roleAssistant, mainActorID, true),
		messageEvent(t, "msg_new", roleAssistant, mainActorID, false),
		textPartEvent(t, partTypeText, "part_new", "msg_new", "The new full final.", true),
		statusEvent(t, contracts.MiMoStatusTypeIdle))
	rows := assembledRows(sink)
	require.Len(t, rows, 1, "the older failed message cannot block a different message")
	_, text, completion := assembledText(t, rows[0].Content)
	assert.Equal(t, "The new full final.", text)
	assert.Equal(t, string(agent.MessageCompletionComplete), completion)
	controlled.reject.Store(0)
	feed(a, messageEvent(t, "msg_old", roleAssistant, mainActorID, true))
	rows = assembledRows(sink)
	require.Len(t, rows, 2, "the old failed final remains available for its own retry")
	_, text, completion = assembledText(t, rows[1].Content)
	assert.Equal(t, "The old full final.", text)
	assert.Equal(t, string(agent.MessageCompletionComplete), completion)
}

func TestStreamedMessageFailureDoesNotBlockAnotherStreamAtIdle(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &controlledPartSink{ProviderServices: a.sink, rejectText: "The old streamed answer."}
	a.sink = controlled
	beginPartialAnswer(t, a, "msg_old", "part_old", "The old streamed answer.")
	feed(a, messageEvent(t, "msg_new", roleAssistant, mainActorID, false),
		textPartEvent(t, partTypeText, "part_new", "msg_new", "", false),
		deltaEvent(t, "part_new", "msg_new", "The new streamed answer."))
	controlled.reject.Store(10)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
	rows := assembledRows(sink)
	require.Len(t, rows, 1, "each streamed message flushes independently")
	_, text, completion := assembledText(t, rows[0].Content)
	assert.Equal(t, "The new streamed answer.", text)
	assert.Equal(t, string(agent.MessageCompletionComplete), completion)
	controlled.reject.Store(0)
	a.flushUnfinishedOutput(agent.MessageCompletionInterrupted)
	rows = assembledRows(sink)
	require.Len(t, rows, 2)
	_, text, completion = assembledText(t, rows[1].Content)
	assert.Equal(t, "The old streamed answer.", text)
	assert.Equal(t, string(agent.MessageCompletionComplete), completion, "the retry keeps its original resolved outcome")
}

func TestLateFinalSupersedesTheFailedStreamedScope(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &controlledPartSink{ProviderServices: a.sink, rejectText: "The streamed prefix."}
	a.sink = controlled
	beginPartialAnswer(t, a, "msg_late", "part_late", "The streamed prefix.")
	controlled.reject.Store(1)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
	assert.Empty(t, assembledRows(sink))
	controlled.reject.Store(0)
	feed(a, textPartEvent(t, partTypeText, "part_late", "msg_late", "The exact whole native final.", true))
	rows := assembledRows(sink)
	require.Len(t, rows, 1, "the exact final replaces the failed streamed identity")
	_, text, completion := assembledText(t, rows[0].Content)
	assert.Equal(t, "The exact whole native final.", text)
	assert.Equal(t, string(agent.MessageCompletionComplete), completion)
	feed(a, textPartEvent(t, partTypeText, "part_late", "msg_late", "The exact whole native final.", true),
		messageEvent(t, "msg_late", roleAssistant, mainActorID, true))
	assert.Len(t, assembledRows(sink), 1)
	assert.NotContains(t, a.parts, "part_late", "the successful final releases the retained record")
}

func TestNativeMetadataReplacesTheGuessedMessageIdentity(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"child", "aborted child", "user"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			a, sink, server := newSinkTestAgent(t)
			server.respond("GET /session/ses_test/message/msg_unannounced", http.StatusInternalServerError, `{}`)
			feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
				textPartEvent(t, partTypeText, "part_unannounced", "msg_unannounced", "", false),
				deltaEvent(t, "part_unannounced", "msg_unannounced", "The announced prefix."))
			actor, role := "actor_exact", roleAssistant
			if kind == "user" {
				actor, role = mainActorID, roleUser
			}
			if kind == "aborted child" {
				feed(a, actorAbortMessageEvent(t, "msg_unannounced", actor))
			} else {
				feed(a, messageEvent(t, "msg_unannounced", role, actor, false))
			}
			feed(a, textPartEvent(t, partTypeText, "part_unannounced", "msg_unannounced", "The exact announced final.", true))
			assert.Empty(t, assembledRows(sink), "a guessed assistant owner must not override exact child or user metadata")
			if kind != "user" {
				ids := sink.ChildAgentIDs()
				require.Len(t, ids, 1)
				rows := assembledRows(sink.Child(ids[0]))
				require.Len(t, rows, 1)
				_, text, completion := assembledText(t, rows[0].Content)
				assert.Equal(t, "The exact announced final.", text)
				want := agent.MessageCompletionComplete
				if kind == "aborted child" {
					want = agent.MessageCompletionInterrupted
				}
				assert.Equal(t, string(want), completion)
			}
			beginPartialAnswer(t, a, "msg_main", "part_main", "The main answer remains normal.")
			feed(a, textPartEvent(t, partTypeText, "part_main", "msg_main", "The main answer remains normal.", true),
				statusEvent(t, contracts.MiMoStatusTypeIdle))
			rows := assembledRows(sink)
			require.Len(t, rows, 1)
			_, text, completion := assembledText(t, rows[0].Content)
			assert.Equal(t, "The main answer remains normal.", text)
			assert.Equal(t, string(agent.MessageCompletionComplete), completion)
			messages := sink.Messages()
			assert.Equal(t, agent.MessageCompletionComplete, messages[len(messages)-1].Completion)
		})
	}
}

func TestNativeMetadataDropsRetainedTextWithoutAConversationRole(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"user", "summary"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			a, sink, server := newSinkTestAgent(t)
			server.respond("GET /session/ses_test/message/msg_unannounced_final", http.StatusInternalServerError, `{}`)
			feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), nativeAbortMarker(t, "session", "msg_unannounced_final"),
				textPartEvent(t, partTypeText, "part_unannounced_final", "msg_unannounced_final", "The native non-conversation final.", true))
			assert.Empty(t, assembledRows(sink))
			info := map[string]any{"id": "msg_unannounced_final", "sessionID": testSessionID, "role": roleAssistant, "agentID": mainActorID,
				"time": map[string]any{"created": 1, "completed": 2}}
			if kind == "user" {
				info["role"] = roleUser
			} else {
				info["summary"] = true
			}
			feed(a, eventJSON(t, eventMessageUpdated, map[string]any{"sessionID": testSessionID, "info": info}))
			assert.Empty(t, assembledRows(sink), "exact user or summary metadata must discard its retained guessed assistant final")
			assert.NotContains(t, a.parts, "part_unannounced_final")
		})
	}
}

func TestReviewExactMainAbortAfterFailedMessageRead(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	server.respond("GET /session/ses_test/message/msg_review_main", http.StatusInternalServerError, `{}`)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
		textPartEvent(t, partTypeText, "part_review_main", "msg_review_main", "", false),
		deltaEvent(t, "part_review_main", "msg_review_main", "The native interrupted text."),
		actorAbortMessageEvent(t, "msg_review_main", mainActorID),
		textPartEvent(t, partTypeText, "part_review_main", "msg_review_main", "The native interrupted text.", true),
		statusEvent(t, contracts.MiMoStatusTypeIdle))
	rows := sink.Messages()
	require.NotEmpty(t, rows)
	require.True(t, rows[len(rows)-1].TurnEnd)
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[len(rows)-1].Completion,
		"exact main metadata supplies independent interruption evidence")
}

func TestReviewSuccessfulOpeningKeepsItsTranscriptOwner(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"unknown message", "child creation recovers"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			a, sink, server := newSinkTestAgent(t)
			feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy))
			var recovering *recoveringChildPartSink
			if scenario == "unknown message" {
				server.respond("GET /session/ses_test/message/msg_review_tool", http.StatusInternalServerError, `{}`)
			} else {
				feed(a, messageEvent(t, "msg_review_tool", roleAssistant, actorID, false))
				recovering = &recoveringChildPartSink{ProviderServices: a.sink}
				recovering.creationFails.Store(true)
				a.sink = recovering
			}
			opening := toolPartEvent(t, "part_review_tool", "msg_review_tool", contracts.MiMoToolBash, "call_review_tool", toolState{
				Status: contracts.MiMoToolStatusRunning, Input: map[string]any{"command": "printf owned"},
			})
			feed(a, opening)
			require.Len(t, sink.Messages(), 1, "the root fallback opening succeeded")
			if recovering != nil {
				recovering.creationFails.Store(false)
			}
			feed(a, messageEvent(t, "msg_review_tool", roleAssistant, actorID, false))
			final := toolPartEvent(t, "part_review_tool", "msg_review_tool", contracts.MiMoToolBash, "call_review_tool", toolState{
				Status: contracts.MiMoToolStatusCompleted, Input: map[string]any{"command": "printf owned"}, Output: "owned",
			})
			feed(a, final)
			rows := sink.Messages()
			require.Len(t, rows, 2, "the native final belongs to the successful opening transcript")
			assert.JSONEq(t, string(final), string(rows[1].Content))
			assert.True(t, rows[1].Closing)
			assert.Equal(t, "part_review_tool", rows[1].SpanID)
			assert.Contains(t, sink.ClosedSpans(), "part_review_tool")
			for _, childID := range sink.ChildAgentIDs() {
				assert.Empty(t, sink.Child(childID).Messages(), "the child owns no opening for this native tool")
			}
		})
	}
}

func TestReviewCorrectedTextProgressKeepsOtherRootScopes(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	beginPartialAnswer(t, a, "msg_root_progress", "part_root_progress", "ROOTROOT")
	server.respond("GET /session/ses_test/message/msg_child_progress", http.StatusInternalServerError, `{}`)
	feed(a, textPartEvent(t, partTypeText, "part_child_progress", "msg_child_progress", "", false),
		deltaEvent(t, "part_child_progress", "msg_child_progress", "CHILDCHILDCHILDCHILD"))
	require.Greater(t, sink.ProgressSnapshot().ThinkingTokens, int64(2))
	feed(a, messageEvent(t, "msg_child_progress", roleAssistant, actorID, false))
	assert.EqualValues(t, 2, sink.ProgressSnapshot().ThinkingTokens, "only the corrected root scope retires")
	childIDs := sink.ChildAgentIDs()
	require.Len(t, childIDs, 1)
	child := sink.Child(childIDs[0])
	assert.EqualValues(t, 5, child.ProgressSnapshot().ThinkingTokens, "the child receives the complete existing prefix")
	feed(a, textPartEvent(t, partTypeText, "part_child_progress", "msg_child_progress", "CHILDCHILDCHILDCHILD", true))
	assert.EqualValues(t, 2, sink.ProgressSnapshot().ThinkingTokens)
	assert.Zero(t, child.ProgressSnapshot().ThinkingTokens)
}

func TestReviewFailedOpeningMovesOnlyItsOutputProgress(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	controlled := &controlledPartSink{ProviderServices: a.sink, rejectText: "part_child_output"}
	a.sink = controlled
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), messageEvent(t, "msg_root_output", roleAssistant, mainActorID, false),
		toolPartEvent(t, "part_root_output", "msg_root_output", contracts.MiMoToolBash, "call_root_output", toolState{
			Status: contracts.MiMoToolStatusRunning, Input: map[string]any{"command": "printf ROOT"}, Metadata: map[string]any{"output": "ROOT"},
		}))
	server.respond("GET /session/ses_test/message/msg_child_output", http.StatusInternalServerError, `{}`)
	controlled.reject.Store(1)
	feed(a, toolPartEvent(t, "part_child_output", "msg_child_output", contracts.MiMoToolBash, "call_child_output", toolState{
		Status: contracts.MiMoToolStatusRunning, Input: map[string]any{"command": "printf CHILD"}, Metadata: map[string]any{"output": "CHILD"},
	}))
	feed(a, messageEvent(t, "msg_child_output", roleAssistant, actorID, false))
	assert.EqualValues(t, 4, sink.ProgressSnapshot().OutputBytes)
	ids := sink.ChildAgentIDs()
	require.Len(t, ids, 1)
	assert.EqualValues(t, 5, sink.Child(ids[0]).ProgressSnapshot().OutputBytes)
	feed(a, toolPartEvent(t, "part_child_output", "msg_child_output", contracts.MiMoToolBash, "call_child_output", toolState{
		Status: contracts.MiMoToolStatusCompleted, Input: map[string]any{"command": "printf CHILD"}, Output: "CHILD",
	}))
	assert.EqualValues(t, 4, sink.ProgressSnapshot().OutputBytes)
	assert.Zero(t, sink.Child(ids[0]).ProgressSnapshot().OutputBytes)
}

func TestNativeMetadataReplacesTheGuessedToolActor(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	controlled := &controlledPartSink{ProviderServices: a.sink, rejectText: "part_guessed_child_tool"}
	a.sink = controlled
	server.respond("GET /session/ses_test/message/msg_guessed_child_tool", http.StatusInternalServerError, `{}`)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
		actorRegisteredEvent(t, actorID, true), actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""))
	controlled.reject.Store(1)
	opening := toolPartEvent(t, "part_guessed_child_tool", "msg_guessed_child_tool", contracts.MiMoToolBash, "call_guessed_child_tool", toolState{
		Status: contracts.MiMoToolStatusRunning, Input: map[string]any{"command": "printf child"},
	})
	feed(a, opening, messageEvent(t, "msg_guessed_child_tool", roleAssistant, actorID, false), statusEvent(t, contracts.MiMoStatusTypeIdle))
	child := sink.Child("child-of-" + testSessionID + "/" + actorID)
	rows := child.Messages()
	require.Len(t, rows, 1, "the parent idle must not cut the now identified child tool")
	assert.False(t, rows[0].Closing)
	assert.Empty(t, child.ClosedSpans())
	final := toolPartEvent(t, "part_guessed_child_tool", "msg_guessed_child_tool", contracts.MiMoToolBash, "call_guessed_child_tool", toolState{
		Status: contracts.MiMoToolStatusCompleted, Input: map[string]any{"command": "printf child"}, Output: "child",
	})
	feed(a, final)
	rows = child.Messages()
	require.Len(t, rows, 2)
	assert.JSONEq(t, string(final), string(rows[1].Content))
	assert.True(t, rows[1].Closing)
	assert.Empty(t, rows[1].Completion)
}

func TestNativeChildAbortPreservesAnEarlierHeldMainFailure(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	failure := sessionErrorEvent(t, "APIError", "The main model request failed.")
	feed(a, failure, nativeAbortMarker(t, "session", "msg_child_abort"),
		messageEvent(t, "msg_child_abort", roleAssistant, actorID, false), actorAbortMessageEvent(t, "msg_child_abort", actorID),
		failedMessageEvent(t, "msg_p", mainActorID, "APIError", "The main model request failed."),
		statusEvent(t, contracts.MiMoStatusTypeIdle))
	rows := sink.Messages()
	require.NotEmpty(t, rows)
	divider := rows[len(rows)-1]
	assert.True(t, divider.TurnEnd)
	assert.Equal(t, agent.MessageCompletionError, divider.Completion)
	assert.JSONEq(t, string(failure), string(divider.Content))
}

func TestNativeChildAbortSurvivesParentTurnChanges(t *testing.T) {
	t.Parallel()
	for _, position := range []string{"before parent idle", "after parent idle"} {
		t.Run(position, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
			feed(a, messageEvent(t, "msg_child", roleAssistant, actorID, false),
				textPartEvent(t, partTypeText, "part_child", "msg_child", "", false),
				deltaEvent(t, "part_child", "msg_child", "The cut child answer."))
			marker := nativeAbortMarker(t, "session", "msg_child")
			if position == "before parent idle" {
				feed(a, marker)
			}
			feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
			if position == "after parent idle" {
				feed(a, marker)
			}
			feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
				messageEvent(t, "msg_new_parent", roleAssistant, mainActorID, false),
				textPartEvent(t, partTypeText, "part_child", "msg_child", "The cut child answer.", true),
				actorAbortMessageEvent(t, "msg_child", actorID),
				textPartEvent(t, partTypeText, "part_new_parent", "msg_new_parent", "The new parent answer.", true),
				messageEvent(t, "msg_new_parent", roleAssistant, mainActorID, true),
				statusEvent(t, contracts.MiMoStatusTypeIdle))
			assert.Zero(t, sink.NotificationCount(), "an unowned abort cannot become a root notification")
			childRows := assembledRows(sink.Child("child-of-" + spawnSpanID))
			require.Len(t, childRows, 1)
			_, text, completion := assembledText(t, childRows[0].Content)
			assert.Equal(t, "The cut child answer.", text)
			assert.Equal(t, string(agent.MessageCompletionInterrupted), completion)
			rows := sink.Messages()
			assert.Equal(t, agent.MessageCompletionComplete, rows[len(rows)-1].Completion)
		})
	}
}

type recoveringChildPartSink struct {
	agent.ProviderServices
	creationFails atomic.Bool
}

func (s *recoveringChildPartSink) EnsureChildAgent(spec agent.ChildAgentSpec) (string, error) {
	if s.creationFails.Load() {
		return "", errors.New("the controlled child creation failed")
	}
	return s.ProviderServices.EnsureChildAgent(spec)
}

func TestRootFallbackFailureRetriesTheOriginalChildTarget(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &controlledPartSink{ProviderServices: a.sink, rejectText: "The original child full final."}
	recovering := &recoveringChildPartSink{ProviderServices: controlled}
	recovering.creationFails.Store(true)
	a.sink = recovering
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	feed(a, messageEvent(t, "msg_child", roleAssistant, actorID, false),
		textPartEvent(t, partTypeText, "part_child", "msg_child", "", false),
		deltaEvent(t, "part_child", "msg_child", "The child prefix."))
	controlled.reject.Store(1)
	feed(a, nativeAbortMarker(t, "session", "msg_child"),
		textPartEvent(t, partTypeText, "part_child", "msg_child", "The original child full final.", true),
		messageEvent(t, "msg_child", roleAssistant, actorID, true))
	assert.EqualValues(t, 0, controlled.reject.Load(), "failed child creation must still try the original root fallback")
	assert.Empty(t, assembledRows(sink))
	recovering.creationFails.Store(false)
	controlled.reject.Store(0)
	feed(a, messageEvent(t, "msg_child", roleAssistant, actorID, true))
	assert.Empty(t, assembledRows(sink), "the retry must resolve the original child again")
	rows := assembledRows(sink.Child("child-of-" + spawnSpanID))
	require.Len(t, rows, 1)
	_, text, completion := assembledText(t, rows[0].Content)
	assert.Equal(t, "The original child full final.", text)
	assert.Equal(t, string(agent.MessageCompletionComplete), completion)
}

func TestTextPartBecomesOneRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)

	feed(a,
		messageEvent(t, "msg_1", roleAssistant, mainActorID, false),
		textPartEvent(t, partTypeReasoning, "prt_r", "msg_1", "", false),
		deltaEvent(t, "prt_r", "msg_1", "Let me "),
		deltaEvent(t, "prt_r", "msg_1", "think."),
		textPartEvent(t, partTypeReasoning, "prt_r", "msg_1", "Let me think.", true),
		textPartEvent(t, partTypeText, "prt_t", "msg_1", "", false),
		deltaEvent(t, "prt_t", "msg_1", "Hello"),
		textPartEvent(t, partTypeText, "prt_t", "msg_1", "Hello, world.", true),
	)

	messages := sink.Messages()
	require.Len(t, messages, 2, "the final update of each part is the one row; the deltas feed only the progress")
	kind, text, completion := assembledText(t, messages[0].Content)
	assert.Equal(t, string(agent.AssembledMessageKindReasoning), kind)
	assert.Equal(t, "Let me think.", text)
	assert.Equal(t, string(agent.MessageCompletionComplete), completion)
	kind, text, _ = assembledText(t, messages[1].Content)
	assert.Equal(t, string(agent.AssembledMessageKindText), kind)
	assert.Equal(t, "Hello, world.", text, "the final update holds the whole text, not the streamed prefix")

	var streamed []string
	completed := 0
	for _, update := range sink.ProgressUpdates() {
		switch update.Operation {
		case agent.ProgressModelText:
			streamed = append(streamed, update.Text)
		case agent.ProgressModelComplete:
			completed++
		default:
			// The other operations state nothing about the text parts.
		}
	}
	assert.Equal(t, []string{"Let me ", "think.", "Hello"}, streamed)
	assert.Equal(t, 2, completed, "each finished part completes its progress scope")
}

func TestTextPartsThatNoRowCarries(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		events func(t *testing.T) [][]byte
	}{
		{name: "the user's own text", events: func(t *testing.T) [][]byte {
			return [][]byte{
				messageEvent(t, "msg_u", roleUser, "", false),
				userTextPartEvent(t, "prt_u", "msg_u", "What is 2+2?"),
			}
		}},
		{name: "a synthetic text", events: func(t *testing.T) [][]byte {
			part := eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": map[string]any{
				"id": "prt_s", "messageID": "msg_1", "sessionID": testSessionID, "type": partTypeText,
				"text": "<system-reminder>", "synthetic": true, "time": map[string]any{"start": 1, "end": 2},
			}})
			return [][]byte{messageEvent(t, "msg_1", roleAssistant, mainActorID, false), part}
		}},
		{name: "an ignored text", events: func(t *testing.T) [][]byte {
			part := eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": map[string]any{
				"id": "prt_i", "messageID": "msg_1", "sessionID": testSessionID, "type": partTypeText,
				"text": "ignored", "ignored": true, "time": map[string]any{"start": 1, "end": 2},
			}})
			return [][]byte{messageEvent(t, "msg_1", roleAssistant, mainActorID, false), part}
		}},
		{name: "a compaction summary", events: func(t *testing.T) [][]byte {
			summary := eventJSON(t, eventMessageUpdated, map[string]any{"info": map[string]any{
				"id": "msg_s", "sessionID": testSessionID, "role": roleAssistant, "agentID": mainActorID, "summary": true,
			}})
			return [][]byte{summary, textPartEvent(t, partTypeText, "prt_s", "msg_s", "The summary.", true)}
		}},
		{name: "a text of another session", events: func(t *testing.T) [][]byte {
			part := eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": map[string]any{
				"id": "prt_o", "messageID": "msg_o", "sessionID": "ses_other", "type": partTypeText,
				"text": "elsewhere", "time": map[string]any{"start": 1, "end": 2},
			}})
			return [][]byte{part}
		}},
		{name: "a blank text", events: func(t *testing.T) [][]byte {
			return [][]byte{
				messageEvent(t, "msg_1", roleAssistant, mainActorID, false),
				textPartEvent(t, partTypeText, "prt_b", "msg_1", "  \n", true),
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			feed(a, tc.events(t)...)
			assert.Empty(t, sink.Messages())
		})
	}
}

// A part whose message the stream never announced is read from the server, so
// its row reaches the right transcript.
func TestUnannouncedMessageIsReadFromTheServer(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	server.respond("GET /session/ses_test/message/msg_late", http.StatusOK,
		`{"info":{"id":"msg_late","sessionID":"ses_test","role":"user","agentID":"main"},"parts":[]}`)

	feed(a, textPartEvent(t, partTypeText, "prt_1", "msg_late", "The user's words.", true))
	assert.Empty(t, sink.Messages(), "the server says the message is the user's")
	require.Len(t, server.requestsTo("GET /session/ses_test/message/msg_late"), 1)

	feed(a, textPartEvent(t, partTypeText, "prt_2", "msg_late", "More words.", true))
	assert.Len(t, server.requestsTo("GET /session/ses_test/message/msg_late"), 1, "the record is kept after one read")
}

func TestUnreadableMessageIsTakenAsTheMainAgents(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)

	feed(a, textPartEvent(t, partTypeText, "prt_1", "msg_gone", "Still shown.", true))
	require.Len(t, sink.Messages(), 1, "a lost row is worse than one in the main transcript")
	_, text, _ := assembledText(t, sink.Messages()[0].Content)
	assert.Equal(t, "Still shown.", text)
}

func TestToolCallLifecycle(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	input := map[string]any{"command": "ls", "description": "List"}

	feed(a,
		statusEvent(t, contracts.MiMoStatusTypeBusy),
		messageEvent(t, "msg_1", roleAssistant, mainActorID, false),
		toolPartEvent(t, "prt_1", "msg_1", contracts.MiMoToolBash, "call-1", toolState{Status: contracts.MiMoToolStatusPending}),
	)
	assert.Empty(t, sink.Messages(), "a pending call states no input yet")

	feed(a,
		toolPartEvent(t, "prt_1", "msg_1", contracts.MiMoToolBash, "call-1", toolState{Status: contracts.MiMoToolStatusRunning, Input: input}),
		toolPartEvent(t, "prt_1", "msg_1", contracts.MiMoToolBash, "call-1", toolState{Status: contracts.MiMoToolStatusRunning, Input: input,
			Metadata: map[string]any{"output": "a\n", "description": "List"}}),
		toolPartEvent(t, "prt_1", "msg_1", contracts.MiMoToolBash, "call-1", toolState{Status: contracts.MiMoToolStatusRunning, Input: input,
			Metadata: map[string]any{"output": "a\nb\n", "description": "List"}}),
	)
	messages := sink.Messages()
	require.Len(t, messages, 1, "the first running update opens the call, and a later one adds no row")
	assert.Equal(t, "prt_1", messages[0].SpanID)
	assert.Equal(t, contracts.MiMoToolBash, messages[0].SpanType)
	assert.False(t, messages[0].Closing)
	assert.Empty(t, messages[0].SpansOpenAtPersist, "the opener persists before its span opens")
	assert.Equal(t, []agenttest.SpanOpen{{SpanID: "prt_1"}}, sink.OpenSpans())

	var totals []int64
	var tails []string
	for _, update := range sink.ProgressUpdates() {
		switch update.Operation {
		case agent.ProgressOutputTotal:
			totals = append(totals, update.Value)
		case agent.ProgressOutputTail:
			tails = append(tails, update.Text)
		default:
			// The other operations state nothing about the command's output.
		}
	}
	assert.Equal(t, []int64{2, 4}, totals, "the output is cumulative, so the total counts what each update adds")
	assert.Equal(t, []string{"a\n", "a\nb\n"}, tails)

	final := toolPartEvent(t, "prt_1", "msg_1", contracts.MiMoToolBash, "call-1", toolState{Status: contracts.MiMoToolStatusCompleted, Input: input,
		Output: "a\nb\n", Metadata: map[string]any{"output": "a\nb\n", "exit": 0}})
	feed(a, final, final)
	messages = sink.Messages()
	require.Len(t, messages, 2, "a repeated final update closes nothing twice")
	assert.True(t, messages[1].Closing)
	assert.Equal(t, "prt_1", messages[1].SpanID)
	assert.Equal(t, []agenttest.SpanOpen{{SpanID: "prt_1"}}, messages[1].SpansOpenAtPersist, "the closer persists inside its span")
	assert.Equal(t, []string{"prt_1"}, sink.ClosedSpans())
	assert.JSONEq(t, string(final), string(messages[1].Content), "the row is the event MiMo sent")

	feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
	turnEnd := sink.Messages()[2]
	require.True(t, turnEnd.TurnEnd)
	var metadata map[string]any
	require.NoError(t, json.Unmarshal(turnEnd.Metadata, &metadata))
	assert.EqualValues(t, 1, metadata[contracts.MessageMetadataFieldToolUses], "the divider counts the turn's tool calls")
}

// MiMo re-sends a running command's whole output on each update. Only its end
// reaches a reader, so the live tail keeps the last bytes, up to the limit, and
// states that it lost the rest.
func TestLiveOutputTailIsClipped(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	input := map[string]any{"command": "yes | head -n 9000"}
	output := strings.Repeat("y", mimoLiveOutputLimit) + "the end\n"

	feed(a,
		statusEvent(t, contracts.MiMoStatusTypeBusy),
		messageEvent(t, "msg_1", roleAssistant, mainActorID, false),
		toolPartEvent(t, "prt_1", "msg_1", contracts.MiMoToolBash, "call-1", toolState{Status: contracts.MiMoToolStatusRunning, Input: input,
			Metadata: map[string]any{"output": ""}}),
	)
	for _, update := range sink.ProgressUpdates() {
		assert.NotEqual(t, agent.ProgressOutputTail, update.Operation, "an empty output has no tail")
		assert.NotEqual(t, agent.ProgressOutputTotal, update.Operation, "an empty output adds nothing")
	}

	feed(a, toolPartEvent(t, "prt_1", "msg_1", contracts.MiMoToolBash, "call-1", toolState{Status: contracts.MiMoToolStatusRunning, Input: input,
		Metadata: map[string]any{"output": output}}))
	var tails, totals []agent.ProgressUpdate
	for _, update := range sink.ProgressUpdates() {
		switch update.Operation {
		case agent.ProgressOutputTail:
			tails = append(tails, update)
		case agent.ProgressOutputTotal:
			totals = append(totals, update)
		default:
			// The other operations state nothing about the command's output.
		}
	}
	require.Len(t, tails, 1)
	assert.Len(t, tails[0].Text, mimoLiveOutputLimit)
	assert.True(t, strings.HasSuffix(tails[0].Text, "the end\n"), "the tail is the end of the output")
	assert.True(t, tails[0].Truncated, "the tail states that it lost the start")
	require.Len(t, totals, 1)
	assert.EqualValues(t, len(output), totals[0].Value, "the total counts every byte, not only the tail")
}

// A tool part without a call ID or state cannot identify a row.
// A part without an ID or conversation produces no row.
func TestPartsThatAddNoRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	part := func(fields map[string]any) []byte {
		body := map[string]any{"messageID": "msg_1", "sessionID": testSessionID}
		for key, value := range fields {
			body[key] = value
		}
		return eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"sessionID": testSessionID, "part": body})
	}

	feed(a,
		messageEvent(t, "msg_1", roleAssistant, mainActorID, false),
		toolPartEvent(t, "prt_1", "msg_1", contracts.MiMoToolBash, "", toolState{Status: contracts.MiMoToolStatusRunning,
			Input: map[string]any{"command": "ls"}}),
		part(map[string]any{"id": "prt_2", "type": contracts.MiMoPartTypeTool, "tool": contracts.MiMoToolBash, "callID": "call-2"}),
		part(map[string]any{"type": partTypeText, "text": "no id", "time": map[string]any{"start": 1, "end": 2}}),
		part(map[string]any{"id": "prt_4", "type": partTypeStepFinish, "tokens": map[string]any{"input": 10}}),
		eventJSON(t, contracts.MiMoEventMessagePartUpdated, "not an object"),
		eventJSON(t, eventMessagePartDelta, map[string]any{"sessionID": testSessionID, "partID": "prt_9", "field": "text", "delta": "orphan"}),
	)
	assert.Empty(t, sink.Messages())
	assert.Empty(t, a.tools)
	assert.Empty(t, sink.ProgressUpdates(), "a delta of a part that never announced itself cannot be typed")
}

// The first update of a user message can omit the actor. A later update that
// states it wins, and an update that omits it keeps the one already known.
func TestMessageUpdatesKeepTheKnownActor(t *testing.T) {
	t.Parallel()
	a, _, _ := newSinkTestAgent(t)

	feed(a, messageEvent(t, "msg_u", roleUser, "", false))
	assert.Equal(t, mainActorID, a.messages["msg_u"].actorID, "a message that states no actor is the main agent's")
	feed(a, messageEvent(t, "msg_u", roleUser, actorID, false))
	assert.Equal(t, actorID, a.messages["msg_u"].actorID)
	feed(a, messageEvent(t, "msg_u", roleUser, "", true))
	assert.Equal(t, actorID, a.messages["msg_u"].actorID)
	assert.True(t, a.messages["msg_u"].completed)

	feed(a,
		eventJSON(t, eventMessageUpdated, map[string]any{"sessionID": testSessionID, "info": map[string]any{"role": roleUser}}),
		eventJSON(t, eventMessageUpdated, map[string]any{"sessionID": "ses_left", "info": map[string]any{"id": "msg_o", "role": roleUser}}),
	)
	assert.Len(t, a.messages, 1, "a message with no id, or of a session the agent left, is not recorded")
}

// Settlement removes completed observations but retains native tool ownership.
// A running child keeps its message and call.
// A finished tool keeps its compact identity until native session closure.
// Successful closing writes release native bytes and live sink targets.
func TestTurnEndPrunesFinishedBookkeeping(t *testing.T) {
	t.Parallel()
	a, _, _ := newSinkTestAgent(t)
	running := toolState{Status: contracts.MiMoToolStatusRunning, Input: map[string]any{"command": "sleep 100"}}

	feed(a,
		statusEvent(t, contracts.MiMoStatusTypeBusy),
		messageEvent(t, "msg_main", roleAssistant, mainActorID, false),
		toolPartEvent(t, "prt_done", "msg_main", contracts.MiMoToolBash, "call-done", running),
		toolPartEvent(t, "prt_done", "msg_main", contracts.MiMoToolBash, "call-done", toolState{Status: contracts.MiMoToolStatusCompleted,
			Input: map[string]any{"command": "sleep 100"}, Output: "done"}),
		toolPartEvent(t, "prt_cut", "msg_main", contracts.MiMoToolBash, "call-cut", running),
		messageEvent(t, "msg_sub", roleAssistant, actorID, false),
		toolPartEvent(t, "prt_sub", "msg_sub", contracts.MiMoToolBash, "call-sub", running),
		messageEvent(t, "msg_main", roleAssistant, mainActorID, true),
		statusEvent(t, contracts.MiMoStatusTypeIdle),
	)
	assert.ElementsMatch(t, []string{"msg_main", "msg_sub"}, sortedKeys(a.messages), "tool ownership keeps each captured message")
	assert.ElementsMatch(t, []string{"prt_done", "prt_cut", "prt_sub"}, sortedKeys(a.tools))
	for _, id := range []string{"prt_done", "prt_cut"} {
		assert.Nil(t, a.tools[id].lastFrame)
		assert.Nil(t, a.tools[id].openingTarget)
		assert.Nil(t, a.tools[id].progressTarget)
	}
	assert.True(t, a.tools["prt_cut"].final, "the cut call closes nothing a second time")

	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), statusEvent(t, contracts.MiMoStatusTypeIdle))
	assert.ElementsMatch(t, []string{"prt_done", "prt_cut", "prt_sub"}, sortedKeys(a.tools), "later turns keep the original native tool identities")
	_, err := a.ClearContext()
	require.NoError(t, err)
	assert.Empty(t, a.tools, "native session closure releases finished tool identities")
	assert.Empty(t, a.messages, "native session closure releases completed message owners")
}

func TestNativeMessageAbortConfirmsTheCurrentMainTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	beginPartialAnswer(t, a, "msg_current", "part_current", "The actual cut text.")
	feed(a, nativeAbortMarker(t, "message", "msg_current"),
		textPartEvent(t, partTypeText, "part_current", "msg_current", "The actual cut text.", true),
		statusEvent(t, contracts.MiMoStatusTypeIdle))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	_, _, completion := assembledText(t, rows[0].Content)
	assert.Equal(t, string(agent.MessageCompletionInterrupted), completion)
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[1].Completion)
}

func TestNativeMessageAbortRejectsOtherMessageStates(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"finished main", "main becomes child", "child", "child becomes main", "user", "user becomes assistant", "unknown message", "unknown name", "empty name", "malformed error"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			beginPartialAnswer(t, a, "msg_current", "part_current", "The normal answer.")
			messageID, role, actorID := "msg_current", roleAssistant, mainActorID
			var errorBody any = map[string]any{"name": contracts.MiMoErrorNameAborted, "data": map[string]any{"message": "Aborted"}}
			switch name {
			case "finished main":
				feed(a, messageEvent(t, messageID, roleAssistant, mainActorID, true))
			case "main becomes child":
				actorID = "actor_conflicting"
			case "child":
				messageID, actorID = "msg_child", "actor_child"
				feed(a, messageEvent(t, messageID, roleAssistant, actorID, false))
			case "child becomes main":
				messageID = "msg_child"
				feed(a, messageEvent(t, messageID, roleAssistant, "actor_child", false))
			case "user":
				messageID, role = "msg_user", roleUser
				feed(a, messageEvent(t, messageID, role, actorID, false))
			case "user becomes assistant":
				messageID = "msg_user"
				feed(a, messageEvent(t, messageID, roleUser, actorID, false))
			case "unknown message":
				messageID = "msg_unknown"
			case "unknown name":
				errorBody = map[string]any{"name": "MessageAbortedErrorUnknown", "data": map[string]any{"message": "Other failure"}}
			case "empty name":
				errorBody = map[string]any{"name": "", "data": map[string]any{"message": "Other failure"}}
			case "malformed error":
				errorBody = "not an error object"
			}
			feed(a, eventJSON(t, eventMessageUpdated, map[string]any{"sessionID": testSessionID, "info": map[string]any{
				"id": messageID, "sessionID": testSessionID, "role": role, "agentID": actorID, "error": errorBody,
			}}), textPartEvent(t, partTypeText, "part_current", "msg_current", "The normal answer.", true),
				statusEvent(t, contracts.MiMoStatusTypeIdle))
			rows := sink.Messages()
			require.Len(t, rows, 2)
			_, _, completion := assembledText(t, rows[0].Content)
			assert.Equal(t, string(agent.MessageCompletionComplete), completion)
			assert.Equal(t, agent.MessageCompletionComplete, rows[1].Completion)
		})
	}
}

func TestOldAbortedMessageDoesNotConfirmAReplacementTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	beginPartialAnswer(t, a, "msg_old", "part_old", "The old natural answer.")
	feed(a, textPartEvent(t, partTypeText, "part_old", "msg_old", "The old natural answer.", true),
		statusEvent(t, contracts.MiMoStatusTypeIdle))
	beginPartialAnswer(t, a, "msg_new", "part_new", "The new natural answer.")
	feed(a, nativeAbortMarker(t, "message", "msg_old"),
		textPartEvent(t, partTypeText, "part_new", "msg_new", "The new natural answer.", true),
		statusEvent(t, contracts.MiMoStatusTypeIdle))
	rows := sink.Messages()
	require.Len(t, rows, 4)
	for _, index := range []int{0, 2} {
		_, _, completion := assembledText(t, rows[index].Content)
		assert.Equal(t, string(agent.MessageCompletionComplete), completion)
		assert.Equal(t, agent.MessageCompletionComplete, rows[index+1].Completion)
	}
}

func TestNativeSessionAbortRejectsOtherNamesAndMalformedData(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		errorBody  any
		completion agent.MessageCompletion
	}{
		{name: "unknown name", errorBody: map[string]any{"name": "MessageAbortedErrorUnknown", "data": map[string]any{"message": "A different failure"}}, completion: agent.MessageCompletionError},
		{name: "empty name", errorBody: map[string]any{"name": "", "data": map[string]any{"message": "A different failure"}}, completion: agent.MessageCompletionError},
		{name: "malformed error", errorBody: "not an error object", completion: agent.MessageCompletionComplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			beginPartialAnswer(t, a, "msg_current", "part_current", "The completed text.")
			feed(a, eventJSON(t, contracts.MiMoEventSessionError, map[string]any{"sessionID": testSessionID, "error": tc.errorBody}),
				textPartEvent(t, partTypeText, "part_current", "msg_current", "The completed text.", true),
				statusEvent(t, contracts.MiMoStatusTypeIdle))
			rows := sink.Messages()
			require.Len(t, rows, 2)
			_, _, completion := assembledText(t, rows[0].Content)
			assert.Equal(t, string(agent.MessageCompletionComplete), completion)
			assert.Equal(t, tc.completion, rows[1].Completion)
		})
	}
}

func TestErroredMessagesWithoutCompletedTimeArePruned(t *testing.T) {
	t.Parallel()
	a, _, _ := newSinkTestAgent(t)
	beginPartialAnswer(t, a, "msg_errored", "part_errored", "The cut answer.")
	feed(a, nativeAbortMarker(t, "message", "msg_errored"), statusEvent(t, contracts.MiMoStatusTypeIdle))
	require.Contains(t, a.messages, "msg_errored")
	assert.True(t, a.messages["msg_errored"].completed, "the native error ends this message even without time.completed")
	assert.Empty(t, a.messages["msg_errored"].pendingParts)
	assert.NotContains(t, a.parts, "part_errored", "completed output releases its native text")
	_, err := a.ClearContext()
	require.NoError(t, err)
	assert.NotContains(t, a.messages, "msg_errored", "native session closure releases the completed identity")
}

func TestCompletedToolKeepsItsOwnOutcomeUnderAnInterruptedTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	final := toolPartEvent(t, "part_tool", "msg_tool", contracts.MiMoToolBash, "call_completed", toolState{
		Status: contracts.MiMoToolStatusCompleted, Input: map[string]any{"command": "printf done"},
		Output: "done", Metadata: map[string]any{"exit": 0},
	})
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), messageEvent(t, "msg_tool", roleAssistant, mainActorID, false),
		toolPartEvent(t, "part_tool", "msg_tool", contracts.MiMoToolBash, "call_completed", toolState{Status: contracts.MiMoToolStatusRunning,
			Input: map[string]any{"command": "printf done"}}),
		nativeAbortMarker(t, "session", "msg_tool"),
		final)
	require.Len(t, sink.Messages(), 2, "a completed tool with no earlier unresolved part persists immediately")
	feed(a, nativeAbortMarker(t, "message", "msg_tool"), statusEvent(t, contracts.MiMoStatusTypeIdle))
	rows := sink.Messages()
	require.Len(t, rows, 3)
	assert.True(t, rows[1].Closing)
	assert.Empty(t, rows[1].Completion, "the native result supplies its own outcome")
	assert.JSONEq(t, string(final), string(rows[1].Content), "the native completed result stays intact")
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[2].Completion)
}

func TestToolCallFirstSeenFinished(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)

	feed(a,
		messageEvent(t, "msg_1", roleAssistant, mainActorID, false),
		toolPartEvent(t, "prt_1", "msg_1", contracts.MiMoToolRead, "call-1", toolState{Status: contracts.MiMoToolStatusError,
			Input: map[string]any{"filePath": "/nope"}, Error: "File not found"}),
	)
	messages := sink.Messages()
	require.Len(t, messages, 1, "a call that finished before its first update closes with its one row")
	assert.True(t, messages[0].Closing)
	assert.False(t, messages[0].NoSpan, "only a spawn call's lone row owns no span")
	assert.Empty(t, sink.OpenSpans())
}

func TestTurnEndClosesWhatTheTurnLeftOpen(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		end        func(t *testing.T, a *Agent)
		completion agent.MessageCompletion
		divider    string
	}{
		{name: "an idle ends it complete", completion: agent.MessageCompletionComplete, divider: contracts.MiMoEventSessionStatus,
			end: func(t *testing.T, a *Agent) { feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle)) }},
		{name: "an abort ends it interrupted", completion: agent.MessageCompletionInterrupted, divider: contracts.MiMoEventSessionStatus,
			end: func(t *testing.T, a *Agent) {
				feed(a,
					eventJSON(t, contracts.MiMoEventSessionError, map[string]any{"sessionID": testSessionID,
						"error": map[string]any{"name": contracts.MiMoErrorNameAborted, "data": map[string]any{"message": "The operation was aborted."}}}),
					nativeAbortMarker(t, "message", "msg_1"),
					statusEvent(t, contracts.MiMoStatusTypeIdle))
			}},
		{name: "a failure ends it with the failure as the divider", completion: agent.MessageCompletionError, divider: contracts.MiMoEventSessionError,
			end: func(t *testing.T, a *Agent) {
				feed(a,
					eventJSON(t, contracts.MiMoEventSessionError, map[string]any{"sessionID": testSessionID,
						"error": map[string]any{"name": "APIError", "data": map[string]any{"message": "rate limited", "isRetryable": false}}}),
					statusEvent(t, contracts.MiMoStatusTypeIdle))
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			feed(a,
				statusEvent(t, contracts.MiMoStatusTypeBusy),
				messageEvent(t, "msg_1", roleAssistant, mainActorID, false),
				textPartEvent(t, partTypeText, "prt_t", "msg_1", "", false),
				deltaEvent(t, "prt_t", "msg_1", "Half a sen"),
				toolPartEvent(t, "prt_1", "msg_1", contracts.MiMoToolBash, "call-1", toolState{Status: contracts.MiMoToolStatusRunning,
					Input: map[string]any{"command": "sleep 100"}}),
			)
			tc.end(t, a)

			messages := sink.Messages()
			require.Len(t, messages, 4, "opener, unfinished text, unfinished call, divider")
			_, text, completion := assembledText(t, messages[1].Content)
			assert.Equal(t, "Half a sen", text)
			assert.Equal(t, string(tc.completion), completion)
			assert.True(t, messages[2].Closing)
			assert.Equal(t, "prt_1", messages[2].SpanID)
			assert.Equal(t, tc.completion, messages[2].Completion)
			assert.True(t, messages[3].TurnEnd)
			assert.Equal(t, tc.completion, messages[3].Completion)
			assert.Equal(t, tc.divider, rowTypes(t, messages[3:])[0])

			assert.Equal(t, []string{"turn_active:true", "turn_end", "turn_active:false"}, turnLifecycleWithoutResets(sink.TurnLifecycle()),
				"the divider persists before the clear")

			// A later update of the call that the turn end closed changes nothing.
			feed(a, toolPartEvent(t, "prt_1", "msg_1", contracts.MiMoToolBash, "call-1", toolState{Status: contracts.MiMoToolStatusError,
				Input: map[string]any{"command": "sleep 100"}, Error: "aborted"}))
			assert.Len(t, sink.Messages(), 4)
		})
	}
}

// turnLifecycleWithoutResets drops the span resets, which interleave with the
// turn events and are not what these tests assert.
func turnLifecycleWithoutResets(lifecycle []string) []string {
	out := make([]string, 0, len(lifecycle))
	for _, entry := range lifecycle {
		if strings.HasPrefix(entry, "turn_") {
			out = append(out, entry)
		}
	}
	return out
}

func TestSessionErrorOutsideATurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	failure := eventJSON(t, contracts.MiMoEventSessionError, map[string]any{"sessionID": testSessionID,
		"error": map[string]any{"name": "ProviderModelNotFoundError", "data": map[string]any{"message": "no such model"}}})

	feed(a, failure)
	require.Equal(t, 1, sink.NotificationCount(), "a prompt the server refused before its turn reports itself only this way")
	assert.JSONEq(t, string(failure), string(sink.LastNotification().Content))
	assert.Equal(t, []bool{false}, sink.TurnActives(), "the idle turn is republished, since the queue counted the refused prompt")
}

// An error that states no session is no other session's, so it is taken as the
// agent's own.
func TestSessionErrorWithoutASessionIsTheAgents(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	failure := eventJSON(t, contracts.MiMoEventSessionError, map[string]any{
		"error": map[string]any{"name": "ProviderAuthError", "data": map[string]any{"message": "no credential"}}})

	feed(a, failure)
	require.Equal(t, 1, sink.NotificationCount())
	assert.JSONEq(t, string(failure), string(sink.LastNotification().Content))

	a.sessionID = ""
	feed(a, failure)
	assert.Equal(t, 1, sink.NotificationCount(), "an agent that holds no session owns no error")
}

// Exact native message replay cannot claim another failure or repeat its write.
// A bare session.error has no identity and remains a distinct observation.
func TestRepeatedTurnFailureIsDropped(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	failure := eventJSON(t, contracts.MiMoEventSessionError, map[string]any{"sessionID": testSessionID,
		"error": map[string]any{"name": "APIError", "data": map[string]any{"message": "rate limited"}}})

	message := failedMessageEvent(t, "msg_exact_repeated_failure", mainActorID, "APIError", "rate limited")
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), failure, message,
		statusEvent(t, contracts.MiMoStatusTypeIdle), message, message)
	assert.Zero(t, sink.NotificationCount(), "the exact message replay creates no notification")
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.True(t, rows[0].TurnEnd)
	assert.Equal(t, string(failure), string(rows[0].Content))

	require.NoError(t, a.SendInput("again", nil))
	feed(a, failure, message, message)
	assert.Equal(t, 1, sink.NotificationCount(), "a failure after new input is a new failure")
	assert.Equal(t, string(failure), string(sink.LastNotification().Content))
	assert.Len(t, sink.Messages(), 1, "the old message replay creates no second divider")

	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), failure, message)
	assert.Nil(t, a.turnFailure, "the old exact message cannot claim a new observation")
	require.Len(t, a.pendingFailures, 1)
	assert.Equal(t, string(failure), string(a.pendingFailures[0].raw))
	current := failedMessageEvent(t, "msg_current_exact_failure", mainActorID, "APIError", "rate limited")
	feed(a, current, statusEvent(t, contracts.MiMoStatusTypeIdle), current, message)
	rows = sink.Messages()
	require.Len(t, rows, 2)
	assert.True(t, rows[1].TurnEnd)
	assert.Equal(t, string(failure), string(rows[1].Content))
	assert.Equal(t, agent.MessageCompletionError, rows[1].Completion)
	assert.Equal(t, 1, sink.NotificationCount(), "exact message replay does not repeat either successful write")
}

func TestRetryIsANotificationOfTheSameTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	retry := eventJSON(t, contracts.MiMoEventSessionStatus, map[string]any{"sessionID": testSessionID,
		"status": map[string]any{"type": contracts.MiMoStatusTypeRetry, "attempt": 1, "message": "overloaded", "next": 1}})

	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), retry, retry)
	assert.Equal(t, 2, sink.NotificationCount(), "each attempt is a notification, which the thread folds into the latest")
	assert.Equal(t, []bool{true}, sink.TurnActives(), "a retry keeps the turn")
}

func TestStatusOfAnotherSessionMovesNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
		eventJSON(t, contracts.MiMoEventSessionStatus, map[string]any{"sessionID": "ses_left", "status": map[string]any{"type": "idle"}}))
	assert.Equal(t, []bool{true}, sink.TurnActives(), "a session the agent left can still report its idle")
}

func TestCompactionNotifications(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	started := eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": map[string]any{
		"id": "prt_c", "messageID": "msg_u", "sessionID": testSessionID, "type": contracts.MiMoPartTypeCompaction, "auto": false,
	}})
	ended := eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": map[string]any{
		"id": "prt_c", "messageID": "msg_u", "sessionID": testSessionID, "type": contracts.MiMoPartTypeCompaction, "auto": false,
		"projection": map[string]any{"summary": "short"},
	}})

	feed(a, messageEvent(t, "msg_u", roleUser, mainActorID, false), started, started, ended, ended, started)
	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 2, "each phase is persisted once, and a start after the end is a replay")
	assert.JSONEq(t, string(started), string(notifications[0].Content))
	assert.JSONEq(t, string(ended), string(notifications[1].Content))
}

func TestAutoCompactionKeepsTheTurnUntilNativeIdle(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	started := eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": map[string]any{
		"id": "prt_auto", "messageID": "msg_auto", "sessionID": testSessionID,
		"type": contracts.MiMoPartTypeCompaction, "auto": true,
	}})
	ended := eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": map[string]any{
		"id": "prt_auto", "messageID": "msg_auto", "sessionID": testSessionID,
		"type": contracts.MiMoPartTypeCompaction, "auto": true,
		"projection": map[string]any{"summary": "The context is ready."},
	}})

	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), messageEvent(t, "msg_auto", roleUser, mainActorID, false), started, ended)
	assert.Equal(t, []bool{true}, sink.TurnActives())
	assert.ErrorIs(t, a.SendInput("next", nil), agent.ErrAgentBusy)
	assert.Empty(t, server.requestsTo("POST /session/ses_test/prompt_async"))
	feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
}

func TestUsageReadout(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	assistant := func(id, actorID string, cost float64, input int64) []byte {
		return eventJSON(t, eventMessageUpdated, map[string]any{"info": map[string]any{
			"id": id, "sessionID": testSessionID, "role": roleAssistant, "agentID": actorID, "cost": cost,
			"providerID": "mock", "modelID": "alpha",
			"tokens": map[string]any{"input": input, "output": 20, "reasoning": 0, "cache": map[string]any{"read": 100, "write": 10}},
		}})
	}

	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), assistant("msg_1", mainActorID, 0.5, 1000))
	info := sink.LastSessionInfo()
	assert.InDelta(t, 0.5, info[contracts.SessionInfoKeyTotalCostUsd], 1e-9)
	usage, ok := info[contracts.SessionInfoKeyContextUsage].(map[string]any)
	require.True(t, ok, "info %v", info)
	assert.EqualValues(t, 1110, usage[contracts.ContextUsageFieldContextTokens], "input and both cache counts fill the context")
	assert.EqualValues(t, 128000, usage[contracts.ContextUsageFieldContextWindow])

	before := sink.SessionInfoCount()
	feed(a, assistant("msg_1", mainActorID, 0.5, 1000))
	assert.Equal(t, before, sink.SessionInfoCount(), "an update that restates the usage broadcasts nothing")

	feed(a, assistant("msg_2", "general-1", 0.25, 99999))
	info = sink.LastSessionInfo()
	assert.InDelta(t, 0.75, info[contracts.SessionInfoKeyTotalCostUsd], 1e-9, "every actor's calls are billed to the session")
	assert.NotContains(t, info, contracts.SessionInfoKeyContextUsage, "a subagent's context is its own")

	feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
	messages := sink.Messages()
	turnEnd := messages[len(messages)-1]
	require.True(t, turnEnd.TurnEnd)
	var metadata map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(turnEnd.Metadata, &metadata))
	assert.JSONEq(t, `0.75`, string(metadata[contracts.SessionInfoKeyTotalCostUsd]))
	assert.Contains(t, string(metadata[contracts.SessionInfoKeyContextUsage]), `"context_tokens":1110`)
}

// A message that states no cost and no context adds nothing to the readout,
// and a model that the catalog lacks states no context window. A turn with no
// usage at all carries only its tool count.
func TestUsageReadoutEdges(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	assistant := func(id, providerID string, cost float64, input int64) []byte {
		return eventJSON(t, eventMessageUpdated, map[string]any{"info": map[string]any{
			"id": id, "sessionID": testSessionID, "role": roleAssistant, "agentID": mainActorID, "cost": cost,
			"providerID": providerID, "modelID": "alpha",
			"tokens": map[string]any{"input": input, "output": 5, "reasoning": 0, "cache": map[string]any{"read": 0, "write": 0}},
		}})
	}

	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), assistant("msg_1", "mock", 0, 0))
	assert.Zero(t, sink.SessionInfoCount(), "no cost and no context tokens")
	feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
	turnEnd := sink.Messages()[0]
	require.True(t, turnEnd.TurnEnd)
	var metadata map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(turnEnd.Metadata, &metadata))
	assert.NotContains(t, metadata, contracts.SessionInfoKeyTotalCostUsd)
	assert.NotContains(t, metadata, contracts.SessionInfoKeyContextUsage)
	assert.Contains(t, metadata, contracts.MessageMetadataFieldToolUses)

	feed(a, assistant("msg_2", "gone", 0, 500))
	usage, ok := sink.LastSessionInfo()[contracts.SessionInfoKeyContextUsage].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 500, usage[contracts.ContextUsageFieldContextTokens])
	assert.NotContains(t, usage, contracts.ContextUsageFieldContextWindow)
	assert.NotContains(t, sink.LastSessionInfo(), contracts.SessionInfoKeyTotalCostUsd, "a message with no cost adds no cost")
}

// Settlement persists its divider before it clears the active flag.
// PersistTurnEnd reads the tool count before that flag clears.
func TestTurnEndPrecedesTheClear(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)

	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), statusEvent(t, contracts.MiMoStatusTypeIdle))
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy))
	server.respond("GET /session/status", http.StatusOK, `{}`)
	a.reconcileTurn()

	assert.Equal(t, []string{
		"turn_active:true", "turn_end", "turn_active:false",
		"turn_active:true", "turn_end", "turn_active:false",
	}, turnLifecycleWithoutResets(sink.TurnLifecycle()))
}

func TestTurnEndRetiresTheMainAgentsRequests(t *testing.T) {
	t.Parallel()
	a, sink, server := newControlTestAgent(t)

	feed(a,
		statusEvent(t, contracts.MiMoStatusTypeBusy),
		eventJSON(t, contracts.OpenCodeEventQuestionAsked, map[string]any{"id": "que_1", "sessionID": testSessionID,
			"questions": []any{map[string]any{"question": "Pick", "header": "Pick", "options": []any{map[string]any{"label": "A"}}}}}),
		eventJSON(t, contracts.MiMoEventPermissionAsked, map[string]any{"id": "per_1", "sessionID": testSessionID, "permission": "bash"}),
		statusEvent(t, contracts.MiMoStatusTypeIdle),
	)
	assert.ElementsMatch(t, []string{"mimo-question:que_1", "mimo-permission:per_1"}, sink.CanceledControls())
	waitFor(t, func() bool { return len(server.requestsTo("POST /question/que_1/reject")) == 1 },
		"MiMo 0.1.14 keeps a question of an aborted turn, so the turn end rejects it")
	assert.Empty(t, server.requestsTo("POST /permission/per_1/reply"), "MiMo drops a permission of an aborted turn by itself")
}

func sessionErrorEvent(t *testing.T, name, message string) []byte {
	t.Helper()
	return eventJSON(t, contracts.MiMoEventSessionError, map[string]any{"sessionID": testSessionID,
		"error": map[string]any{"name": name, "data": map[string]any{"message": message}}})
}

// failedMessageEvent is the update that MiMo writes after a failure, which
// identifies the actor whose message failed.
func failedMessageEvent(t *testing.T, id, actorID, name, message string) []byte {
	t.Helper()
	return eventJSON(t, eventMessageUpdated, map[string]any{"info": map[string]any{
		"id": id, "sessionID": testSessionID, "role": roleAssistant, "agentID": actorID,
		"error": map[string]any{"name": name, "data": map[string]any{"message": message}},
	}})
}

// A subagent runs in the agent's own session, so its failure arrives as the
// session's. The failed message that follows identifies the subagent.
// The main turn does not fail with it.
func TestSubagentFailureDoesNotFailTheMainTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)

	feed(a,
		sessionErrorEvent(t, "APIError", "rate limited"),
		failedMessageEvent(t, "msg_c2", actorID, "APIError", "rate limited"),
		statusEvent(t, contracts.MiMoStatusTypeIdle),
	)
	messages := sink.Messages()
	turnEnd := messages[len(messages)-1]
	require.True(t, turnEnd.TurnEnd)
	assert.Equal(t, agent.MessageCompletionComplete, turnEnd.Completion)
	assert.Equal(t, contracts.MiMoEventSessionStatus, rowTypes(t, messages[len(messages)-1:])[0])
	assert.Zero(t, sink.NotificationCount())
}

func TestMainFailureClaimedByItsMessageFailsTheTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	failure := sessionErrorEvent(t, "APIError", "bad request")

	feed(a,
		statusEvent(t, contracts.MiMoStatusTypeBusy),
		failure,
		failedMessageEvent(t, "msg_1", mainActorID, "APIError", "bad request"),
		statusEvent(t, contracts.MiMoStatusTypeIdle),
	)
	turnEnd := sink.Messages()[0]
	require.True(t, turnEnd.TurnEnd)
	assert.Equal(t, agent.MessageCompletionError, turnEnd.Completion)
	assert.JSONEq(t, string(failure), string(turnEnd.Content))
}

// MiMo compacts an overflowed context and goes on with the same turn.
func TestContextOverflowDoesNotFailTheTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feed(a,
		statusEvent(t, contracts.MiMoStatusTypeBusy),
		sessionErrorEvent(t, errorNameContextOverflow, "prompt is too long"),
		statusEvent(t, contracts.MiMoStatusTypeIdle),
	)
	assert.Equal(t, agent.MessageCompletionComplete, sink.Messages()[0].Completion)
	assert.Zero(t, sink.NotificationCount())
}

func TestFailureWhileOnlyASubagentRuns(t *testing.T) {
	t.Parallel()

	t.Run("the subagent's own failure stays in its transcript", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newSinkTestAgent(t)
		spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
		feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))

		feed(a,
			sessionErrorEvent(t, "APIError", "overloaded"),
			failedMessageEvent(t, "msg_c2", actorID, "APIError", "overloaded"),
			actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeFailure, 1, "overloaded"),
		)
		assert.Zero(t, sink.NotificationCount())
		childRows := sink.Child("child-of-" + spawnSpanID).Messages()
		_, text, _ := assembledText(t, childRows[len(childRows)-1].Content)
		assert.Equal(t, "overloaded", text)
	})

	t.Run("a failure no message claims is reported when the subagent ends", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newSinkTestAgent(t)
		spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
		feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
		failure := sessionErrorEvent(t, "ProviderModelNotFoundError", "no such model")

		feed(a, failure)
		assert.Zero(t, sink.NotificationCount(), "a running subagent could still claim it")
		feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeSuccess, 1, ""))
		require.Equal(t, 1, sink.NotificationCount())
		assert.JSONEq(t, string(failure), string(sink.LastNotification().Content))
	})

	t.Run("a held failure survives the next turn while the subagent runs", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newSinkTestAgent(t)
		spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
		feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
		failure := sessionErrorEvent(t, "ProviderModelNotFoundError", "no such model")

		feed(a, failure, statusEvent(t, contracts.MiMoStatusTypeBusy))
		assert.Zero(t, sink.NotificationCount(), "the running child can still claim the earlier failure")
		feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeSuccess, 1, ""),
			statusEvent(t, contracts.MiMoStatusTypeIdle))
		require.Equal(t, 1, sink.NotificationCount())
		assert.JSONEq(t, string(failure), string(sink.LastNotification().Content))
	})
}

func TestReviewFallbackOpeningKeepsItsSpanAcrossRootTurns(t *testing.T) {
	t.Parallel()
	for _, startNext := range []bool{false, true} {
		t.Run(fmt.Sprint(startNext), func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			recovering := &recoveringChildPartSink{ProviderServices: a.sink}
			recovering.creationFails.Store(true)
			a.sink = recovering
			feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
				actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""),
				messageEvent(t, "msg_bound_fallback", roleAssistant, actorID, false),
				toolPartEvent(t, "part_bound_fallback", "msg_bound_fallback", contracts.MiMoToolBash, "call_bound_fallback", toolState{
					Status: contracts.MiMoToolStatusRunning, Input: map[string]any{"command": "printf owned"},
				}))
			require.Len(t, sink.Messages(), 1)
			recovering.creationFails.Store(false)
			feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
			if startNext {
				feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy))
			}
			final := toolPartEvent(t, "part_bound_fallback", "msg_bound_fallback", contracts.MiMoToolBash, "call_bound_fallback", toolState{
				Status: contracts.MiMoToolStatusCompleted, Input: map[string]any{"command": "printf owned"}, Output: "owned",
			})
			feed(a, final)
			rows := sink.Messages()
			require.Len(t, rows, 3)
			assert.Equal(t, []agenttest.SpanOpen{{SpanID: "part_bound_fallback"}}, rows[2].SpansOpenAtPersist)
			assert.Contains(t, sink.ClosedSpans(), "part_bound_fallback")
		})
	}
}

func TestReviewFallbackOpeningEndsOnClearOrStop(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"clear", "stop"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			recovering := &recoveringChildPartSink{ProviderServices: a.sink}
			recovering.creationFails.Store(true)
			a.sink = recovering
			feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
				actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""),
				messageEvent(t, "msg_bound_end", roleAssistant, actorID, false),
				toolPartEvent(t, "part_bound_end", "msg_bound_end", contracts.MiMoToolBash, "call_bound_end", toolState{
					Status: contracts.MiMoToolStatusRunning, Input: map[string]any{"command": "printf owned"}, Metadata: map[string]any{"output": "owned"},
				}))
			require.Len(t, sink.Messages(), 1)
			require.EqualValues(t, 5, sink.ProgressSnapshot().OutputBytes)
			recovering.creationFails.Store(false)
			if operation == "clear" {
				_, err := a.ClearContext()
				require.NoError(t, err)
			} else {
				a.SimulateExitForTest()
				a.Stop()
			}
			rows := sink.Messages()
			require.Len(t, rows, 2)
			assert.True(t, rows[1].Closing)
			assert.Equal(t, agent.MessageCompletionInterrupted, rows[1].Completion)
			assert.Equal(t, testSessionID, rows[1].AgentSessionID)
			assert.Equal(t, []agenttest.SpanOpen{{SpanID: "part_bound_end"}}, rows[1].SpansOpenAtPersist)
			assert.Contains(t, sink.ClosedSpans(), "part_bound_end")
			assert.Zero(t, sink.ProgressSnapshot().OutputBytes)
		})
	}
}

func TestReviewStopEndsTheFallbackSpanAfterAClosingWriteFailure(t *testing.T) {
	t.Parallel()
	assertReviewProcessEndAfterAClosingWriteFailure(t, true)
}

func TestReviewWaitEndsTheFallbackSpanAfterAClosingWriteFailure(t *testing.T) {
	t.Parallel()
	assertReviewProcessEndAfterAClosingWriteFailure(t, false)
}

func assertReviewProcessEndAfterAClosingWriteFailure(t *testing.T, stop bool) {
	t.Helper()
	completion := agent.MessageCompletionError
	if stop {
		completion = agent.MessageCompletionInterrupted
	}
	a, sink, _ := newSinkTestAgent(t)
	recovering := &recoveringChildPartSink{ProviderServices: a.sink}
	recovering.creationFails.Store(true)
	controlled := &controlledPartSink{ProviderServices: recovering, rejectText: "part_stop_fallback"}
	a.sink = controlled
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
		actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""),
		messageEvent(t, "msg_stop_fallback", roleAssistant, actorID, false),
		toolPartEvent(t, "part_stop_fallback", "msg_stop_fallback", contracts.MiMoToolBash, "call_stop_fallback", toolState{
			Status: contracts.MiMoToolStatusRunning, Input: map[string]any{"command": "printf owned"},
		}))
	require.Len(t, sink.Messages(), 1)
	recovering.creationFails.Store(false)
	controlled.reject.Store(10)
	a.SimulateExitForTest()
	if stop {
		a.Stop()
	} else {
		require.NoError(t, a.Wait())
	}
	require.NotEmpty(t, a.messages["msg_stop_fallback"].pendingParts)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		agent.MessageContent{Original: []byte(`{"content":"inspect the stopped transcript"}`)}, agent.SpanInfo{}))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.Empty(t, rows[1].SpansOpenAtPersist, "process stop ends the native span even when its closing row waits")
	controlled.reject.Store(0)
	a.finishOutput(completion)
	rows = sink.Messages()
	require.Len(t, rows, 3)
	assert.True(t, rows[2].Closing)
	assert.Equal(t, testSessionID, rows[2].AgentSessionID)
	assert.Equal(t, completion, rows[2].Completion)
}

func TestReviewFallbackChildModelScopeSurvivesRootBoundaries(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	recovering := &recoveringChildPartSink{ProviderServices: a.sink}
	recovering.creationFails.Store(true)
	a.sink = recovering
	beginPartialAnswer(t, a, "msg_root_fallback_models", "part_root_fallback_models", "ROOTROOT")
	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""),
		messageEvent(t, "msg_child_fallback_models", roleAssistant, actorID, false),
		textPartEvent(t, partTypeText, "part_child_fallback_models", "msg_child_fallback_models", "", false),
		deltaEvent(t, "part_child_fallback_models", "msg_child_fallback_models", "CHILDCHILDCHILDCHILD"))
	require.EqualValues(t, 7, sink.ProgressSnapshot().ThinkingTokens)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
	assert.EqualValues(t, 5, sink.ProgressSnapshot().ThinkingTokens, "only the completed root model scope resets")
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy))
	assert.EqualValues(t, 5, sink.ProgressSnapshot().ThinkingTokens, "the next root turn preserves the unfinished child prefix")
	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeSuccess, 1, ""))
	assert.Zero(t, sink.ProgressSnapshot().ThinkingTokens)
	rows := assembledRows(sink)
	require.Len(t, rows, 2)
	_, text, completion := assembledText(t, rows[1].Content)
	assert.Equal(t, "CHILDCHILDCHILDCHILD", text)
	assert.Equal(t, string(agent.MessageCompletionComplete), completion)
}

func TestReviewChildCreationRecoveryTransfersThePrefixBeforeItsNextDelta(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	recovering := &recoveringChildPartSink{ProviderServices: a.sink}
	recovering.creationFails.Store(true)
	a.sink = recovering
	beginPartialAnswer(t, a, "msg_root_recovery", "part_root_recovery", "ROOTROOT")
	feed(a, messageEvent(t, "msg_child_recovery", roleAssistant, actorID, false),
		textPartEvent(t, partTypeText, "part_child_recovery", "msg_child_recovery", "", false),
		deltaEvent(t, "part_child_recovery", "msg_child_recovery", "PREFIX!!"))
	require.EqualValues(t, 4, sink.ProgressSnapshot().ThinkingTokens)
	recovering.creationFails.Store(false)
	feed(a, deltaEvent(t, "part_child_recovery", "msg_child_recovery", "NEXT"))
	assert.EqualValues(t, 2, sink.ProgressSnapshot().ThinkingTokens)
	ids := sink.ChildAgentIDs()
	require.Len(t, ids, 1)
	child := sink.Child(ids[0])
	assert.EqualValues(t, 3, child.ProgressSnapshot().ThinkingTokens, "the old prefix and next delta count once")
	feed(a, textPartEvent(t, partTypeText, "part_child_recovery", "msg_child_recovery", "PREFIX!!NEXT", true))
	assert.EqualValues(t, 2, sink.ProgressSnapshot().ThinkingTokens)
	assert.Zero(t, child.ProgressSnapshot().ThinkingTokens)
	rows := assembledRows(child)
	require.Len(t, rows, 1)
	_, text, _ := assembledText(t, rows[0].Content)
	assert.Equal(t, "PREFIX!!NEXT", text)
}

func TestReviewUnresolvedUpdateKeepsTheExistingStreamSettlement(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	server.respond("GET /session/ses_test/message/msg_unknown_stream", http.StatusInternalServerError, `{}`)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
		textPartEvent(t, partTypeText, "part_unknown_stream", "msg_unknown_stream", "an unended native update", false),
		deltaEvent(t, "part_unknown_stream", "msg_unknown_stream", "The native streamed text."),
		textPartEvent(t, partTypeText, "part_unknown_stream", "msg_unknown_stream", "a later unended native update", false),
		statusEvent(t, contracts.MiMoStatusTypeIdle))
	rows := assembledRows(sink)
	require.Len(t, rows, 1)
	_, text, completion := assembledText(t, rows[0].Content)
	assert.Equal(t, "The native streamed text.", text)
	assert.Equal(t, string(agent.MessageCompletionComplete), completion)
	assert.NotContains(t, a.parts, "part_unknown_stream")
}

func TestLatePartEnvelopeCannotOverrideAConflictingOuterSession(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feed(a, messageEvent(t, "msg_part_owner", roleAssistant, mainActorID, false))
	valid := toolPartEvent(t, "part_part_owner", "msg_part_owner", contracts.MiMoToolBash, "call_part_owner", toolState{Status: contracts.MiMoToolStatusRunning})
	var event map[string]any
	require.NoError(t, json.Unmarshal(valid, &event))
	event["properties"].(map[string]any)["sessionID"] = "foreign"
	raw, err := json.Marshal(event)
	require.NoError(t, err)
	feed(a, raw)
	assert.Empty(t, sink.Messages())
	feed(a, valid)
	require.Len(t, sink.Messages(), 1)
}

func TestLatePartDeltaRequiresItsNativeMessageIdentity(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), messageEvent(t, "msg_delta_owner", roleAssistant, mainActorID, false),
		textPartEvent(t, partTypeText, "part_delta_owner", "msg_delta_owner", "", false),
		deltaEvent(t, "part_delta_owner", "foreign-message", "FOREIGN TEXT"))
	assert.Zero(t, sink.ProgressSnapshot().ThinkingTokens)
	feed(a, deltaEvent(t, "part_delta_owner", "msg_delta_owner", "Owned text."), statusEvent(t, contracts.MiMoStatusTypeIdle))
	rows := assembledRows(sink)
	require.Len(t, rows, 1)
	_, text, _ := assembledText(t, rows[0].Content)
	assert.Equal(t, "Owned text.", text)
}

func TestLateUnknownRoleCannotDiscardTheRetainedModelStream(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	server.respond("GET /session/ses_test/message/msg_unknown_role", http.StatusInternalServerError, `{}`)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
		textPartEvent(t, partTypeText, "part_unknown_role", "msg_unknown_role", "", false),
		deltaEvent(t, "part_unknown_role", "msg_unknown_role", "The retained native stream."),
		messageEvent(t, "msg_unknown_role", "unknown-role", mainActorID, false),
		messageEvent(t, "msg_unknown_role", roleAssistant, mainActorID, false),
		textPartEvent(t, partTypeText, "part_unknown_role", "msg_unknown_role", "The retained native stream.", true))
	rows := assembledRows(sink)
	require.Len(t, rows, 1)
	_, text, _ := assembledText(t, rows[0].Content)
	assert.Equal(t, "The retained native stream.", text)
}

func TestLateIncomingMessageKeepsExactSessionOwnership(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		outer    string
		info     string
		accepted bool
	}{
		{testSessionID, testSessionID, true}, {"", testSessionID, true}, {testSessionID, "", true}, {"", "", true},
		{"foreign", testSessionID, false}, {testSessionID, "foreign", false},
	} {
		t.Run(scenario.outer+"/"+scenario.info, func(t *testing.T) {
			t.Parallel()
			a, _, _ := newSinkTestAgent(t)
			feed(a, eventJSON(t, eventMessageUpdated, map[string]any{"sessionID": scenario.outer, "info": map[string]any{
				"id": "msg_session_ownership", "sessionID": scenario.info, "role": roleAssistant, "agentID": mainActorID,
			}}))
			if scenario.accepted {
				require.Contains(t, a.messages, "msg_session_ownership")
				assert.Equal(t, testSessionID, a.messages["msg_session_ownership"].sessionID)
			} else {
				assert.NotContains(t, a.messages, "msg_session_ownership")
			}
		})
	}
}

func TestLateRootToolCountRequiresExactCurrentMainOwnership(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, actor                                string
		guessed, fallback, laterEpoch, interrupted bool
		want                                       int
	}{
		{name: "known child", actor: actorID},
		{name: "known child root fallback", actor: actorID, fallback: true},
		{name: "failed read then child", actor: actorID, guessed: true},
		{name: "failed read then main", actor: mainActorID, guessed: true, want: 1},
		{name: "old main resolution", actor: mainActorID, guessed: true, laterEpoch: true},
		{name: "interrupted main", actor: mainActorID, interrupted: true, want: 1},
		{name: "main native final", actor: mainActorID, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			controlled := &controlledPartSink{ProviderServices: a.sink}
			if tc.fallback {
				a.sink = &recoveringChildPartSink{ProviderServices: a.sink, creationFails: atomic.Bool{}}
				a.sink.(*recoveringChildPartSink).creationFails.Store(true)
			}
			if tc.guessed {
				controlled.reject.Store(10)
				a.sink = controlled
			}
			feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy))
			if !tc.guessed {
				feed(a, messageEvent(t, "msg_count_owner", roleAssistant, tc.actor, false))
			}
			native := toolPartEvent(t, "part_count_owner", "msg_count_owner", contracts.MiMoToolBash, "call_count_owner", toolState{Status: contracts.MiMoToolStatusCompleted, Input: map[string]any{"command": "printf native"}, Output: "native"})
			if tc.interrupted {
				native = toolPartEvent(t, "part_count_owner", "msg_count_owner", contracts.MiMoToolBash, "call_count_owner", toolState{Status: contracts.MiMoToolStatusRunning, Input: map[string]any{"command": "printf native"}})
			}
			feed(a, native, native)
			if tc.laterEpoch {
				feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle), statusEvent(t, contracts.MiMoStatusTypeBusy))
			}
			if tc.guessed {
				assert.Zero(t, a.TurnToolUses, "a failed-read guess supplies no root ownership")
				controlled.reject.Store(0)
				metadata := messageEvent(t, "msg_count_owner", roleAssistant, tc.actor, false)
				feed(a, metadata, metadata)
			}
			if tc.interrupted {
				feed(a, actorAbortMessageEvent(t, "msg_count_owner", mainActorID))
			}
			feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
			var dividers []agenttest.Message
			for _, row := range sink.Messages() {
				if row.TurnEnd {
					dividers = append(dividers, row)
				}
			}
			require.NotEmpty(t, dividers)
			for _, divider := range dividers {
				var metadata map[string]any
				require.NoError(t, json.Unmarshal(divider.Metadata, &metadata))
				assert.EqualValues(t, tc.want, metadata[contracts.MessageMetadataFieldToolUses])
			}
			target := sink
			if tc.actor != mainActorID && !tc.fallback {
				ids := sink.ChildAgentIDs()
				require.Len(t, ids, 1)
				target = sink.Child(ids[0])
			}
			var closers []agenttest.Message
			for _, row := range target.Messages() {
				if row.Closing {
					closers = append(closers, row)
				}
			}
			require.Len(t, closers, 1)
			assert.Equal(t, string(native), string(closers[0].Content))
			if tc.interrupted {
				assert.Equal(t, agent.MessageCompletionInterrupted, closers[0].Completion)
			}
		})
	}
}

func TestLatePartUpdateRequiresItsCapturedMessageIdentity(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"text", "tool"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
				messageEvent(t, "msg_part_child", roleAssistant, actorID, false),
				messageEvent(t, "msg_part_main", roleAssistant, mainActorID, false))
			if kind == "text" {
				feed(a, textPartEvent(t, partTypeText, "part_identity", "msg_part_child", "", false),
					deltaEvent(t, "part_identity", "msg_part_child", "The native prefix."))
				before := sink.Child(a.actors[actorID].childAgentID).ProgressUpdates()
				feed(a, textPartEvent(t, partTypeText, "part_identity", "msg_part_main", "The foreign final.", true))
				require.Contains(t, a.parts, "part_identity")
				assert.Nil(t, a.parts["part_identity"].final)
				assert.Equal(t, before, sink.Child(a.actors[actorID].childAgentID).ProgressUpdates())
				feed(a, textPartEvent(t, partTypeText, "part_identity", "msg_part_child", "The exact native final.", true))
				rows := assembledRows(sink.Child(a.actors[actorID].childAgentID))
				require.Len(t, rows, 1)
				_, text, _ := assembledText(t, rows[0].Content)
				assert.Equal(t, "The exact native final.", text)
				assert.Empty(t, assembledRows(sink))
			} else {
				opening := toolPartEvent(t, "part_identity", "msg_part_child", contracts.MiMoToolBash, "native_call", toolState{Status: contracts.MiMoToolStatusRunning, Input: map[string]any{"command": "printf native"}, Metadata: map[string]any{"output": "prefix"}})
				feed(a, opening)
				child := sink.Child(a.actors[actorID].childAgentID)
				before := child.ProgressUpdates()
				feed(a, toolPartEvent(t, "part_identity", "msg_part_main", contracts.MiMoToolBash, "native_call", toolState{Status: contracts.MiMoToolStatusCompleted, Output: "foreign"}))
				assert.False(t, a.tools["part_identity"].final)
				assert.Equal(t, string(opening), string(a.tools["part_identity"].lastFrame))
				assert.Equal(t, before, child.ProgressUpdates())
				assert.Len(t, child.Messages(), 1)
				final := toolPartEvent(t, "part_identity", "msg_part_child", contracts.MiMoToolBash, "native_call", toolState{Status: contracts.MiMoToolStatusCompleted, Output: "native"})
				feed(a, final)
				rows := child.Messages()
				require.Len(t, rows, 2)
				assert.True(t, rows[1].Closing)
				assert.Equal(t, string(final), string(rows[1].Content))
				feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
				rows = sink.Messages()
				require.Len(t, rows, 1)
				require.True(t, rows[0].TurnEnd)
				var metadata map[string]any
				require.NoError(t, json.Unmarshal(rows[0].Metadata, &metadata))
				assert.EqualValues(t, 0, metadata[contracts.MessageMetadataFieldToolUses])
			}
		})
	}
}

func TestLatePartUpdateKeepsItsNativeTypeAndToolIdentity(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"call ID", "tool name", "tool to text", "text to tool", "empty message ID"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), messageEvent(t, "msg_shape_child", roleAssistant, actorID, false))
			text := change == "text to tool"
			opening := toolPartEvent(t, "part_shape", "msg_shape_child", contracts.MiMoToolBash, "call_shape", toolState{Status: contracts.MiMoToolStatusRunning, Input: map[string]any{"command": "printf native"}})
			if text {
				opening = textPartEvent(t, partTypeText, "part_shape", "msg_shape_child", "", false)
			}
			feed(a, opening)
			if text {
				feed(a, deltaEvent(t, "part_shape", "msg_shape_child", "The native prefix."))
			}
			child := sink.Child(a.actors[actorID].childAgentID)
			beforeRows, beforeProgress := child.Messages(), child.ProgressUpdates()
			callID, tool, messageID := "call_shape", contracts.MiMoToolBash, "msg_shape_child"
			if change == "call ID" {
				callID = "foreign_call"
			}
			if change == "tool name" {
				tool = contracts.MiMoToolActor
			}
			if change == "empty message ID" {
				messageID = ""
			}
			invalid := toolPartEvent(t, "part_shape", messageID, tool, callID, toolState{Status: contracts.MiMoToolStatusCompleted, Output: "foreign"})
			if change == "tool to text" {
				invalid = textPartEvent(t, partTypeText, "part_shape", "msg_shape_child", "foreign", true)
			}
			feed(a, invalid)
			assert.Equal(t, beforeRows, child.Messages())
			assert.Equal(t, beforeProgress, child.ProgressUpdates())
			if text {
				assert.NotContains(t, a.tools, "part_shape")
				feed(a, textPartEvent(t, partTypeText, "part_shape", "msg_shape_child", "The exact text.", true))
				rows := assembledRows(child)
				require.Len(t, rows, 1)
				_, actual, _ := assembledText(t, rows[0].Content)
				assert.Equal(t, "The exact text.", actual)
			} else {
				assert.NotContains(t, a.parts, "part_shape")
				assert.False(t, a.tools["part_shape"].final)
				assert.Equal(t, string(opening), string(a.tools["part_shape"].lastFrame))
				final := toolPartEvent(t, "part_shape", "msg_shape_child", contracts.MiMoToolBash, "call_shape", toolState{Status: contracts.MiMoToolStatusCompleted, Output: "native"})
				feed(a, final)
				rows := child.Messages()
				require.Len(t, rows, 2)
				assert.True(t, rows[1].Closing)
				assert.Equal(t, string(final), string(rows[1].Content))
			}
		})
	}
}

func TestLateNewToolRequiresItsNativeToolName(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), messageEvent(t, "msg_empty_tool", roleAssistant, mainActorID, false))
	before := sink.ProgressUpdates()
	feed(a, toolPartEvent(t, "part_empty_tool", "msg_empty_tool", "", "call_empty_tool", toolState{Status: contracts.MiMoToolStatusRunning}))
	assert.NotContains(t, a.tools, "part_empty_tool")
	assert.Empty(t, sink.Messages())
	assert.Equal(t, before, sink.ProgressUpdates())
	final := toolPartEvent(t, "part_empty_tool", "msg_empty_tool", contracts.MiMoToolBash, "call_empty_tool", toolState{Status: contracts.MiMoToolStatusCompleted, Output: "The exact native output."})
	feed(a, final, statusEvent(t, contracts.MiMoStatusTypeIdle))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.True(t, rows[0].Closing)
	assert.Equal(t, string(final), string(rows[0].Content))
}

func TestLateCurrentSessionMetadataCannotResolveAnOldRetainedMessage(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	controlled := &controlledPartSink{ProviderServices: a.sink, rejectText: "The retained old native text."}
	controlled.reject.Store(10)
	a.sink = controlled
	server.respond("GET /session/ses_test/message/msg_old_metadata", http.StatusInternalServerError, `{}`)
	feed(a, textPartEvent(t, partTypeText, "part_old_metadata", "msg_old_metadata", "The retained old native text.", true))
	original := a.messages["msg_old_metadata"]
	newSession, err := a.ClearContext()
	require.NoError(t, err)
	feed(a, eventWithSession(t, messageEvent(t, "msg_old_metadata", roleUser, mainActorID, false), newSession))
	assert.Same(t, original, a.messages["msg_old_metadata"])
	assert.False(t, a.messages["msg_old_metadata"].identityKnown)
	controlled.reject.Store(0)
	a.flushUnfinishedOutput(agent.MessageCompletionComplete)
	rows := assembledRows(sink)
	require.Len(t, rows, 1)
	_, text, completion := assembledText(t, rows[0].Content)
	assert.Equal(t, "The retained old native text.", text)
	assert.Equal(t, testSessionID, rows[0].AgentSessionID)
	assert.Equal(t, string(agent.MessageCompletionComplete), completion)
}

func TestLateCompletedNativeToolCannotCountInAReplacementEpoch(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	server.respond("GET /session/ses_test/message/msg_finished_count", http.StatusOK, `{"info":{"id":"msg_finished_count","sessionID":"ses_test","role":"assistant","agentID":"main","time":{"completed":2}},"parts":[]}`)
	final := toolPartEvent(t, "part_finished_count", "msg_finished_count", contracts.MiMoToolBash, "call_finished_count", toolState{Status: contracts.MiMoToolStatusCompleted, Input: map[string]any{"command": "printf native"}, Output: "native"})
	metadata := messageEvent(t, "msg_finished_count", roleAssistant, mainActorID, true)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), messageEvent(t, "msg_finished_count", roleAssistant, mainActorID, false), final, metadata, statusEvent(t, contracts.MiMoStatusTypeIdle))
	for range 3 {
		feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), final, metadata, final, statusEvent(t, contracts.MiMoStatusTypeIdle))
	}
	var dividers, closers []agenttest.Message
	for _, row := range sink.Messages() {
		if row.TurnEnd {
			dividers = append(dividers, row)
		}
		if row.Closing {
			closers = append(closers, row)
		}
	}
	require.Len(t, dividers, 4)
	for i, row := range dividers {
		var data map[string]any
		require.NoError(t, json.Unmarshal(row.Metadata, &data))
		want := 0
		if i == 0 {
			want = 1
		}
		assert.EqualValues(t, want, data[contracts.MessageMetadataFieldToolUses])
	}
	require.Len(t, closers, 1)
	assert.Equal(t, string(final), string(closers[0].Content))
}

func TestLateOldMainFailureCannotClaimANewChildFailure(t *testing.T) {
	t.Parallel()
	for _, earlierChild := range []bool{false, true} {
		t.Run(fmt.Sprint(earlierChild), func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			old := failedMessageEvent(t, "msg_old_same_failure", mainActorID, "APIError", "The identical native failure.")
			feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), sessionErrorEvent(t, "APIError", "The identical native failure."), old)
			startChild := func() {
				feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""), messageEvent(t, "msg_current_child_failure", roleAssistant, actorID, false))
			}
			if earlierChild {
				startChild()
			}
			feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle), statusEvent(t, contracts.MiMoStatusTypeBusy))
			if !earlierChild {
				startChild()
			}
			feed(a, sessionErrorEvent(t, "APIError", "The identical native failure."), old,
				failedMessageEvent(t, "msg_current_child_failure", actorID, "APIError", "The identical native failure."),
				actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeFailure, 1, "The identical native failure."),
				statusEvent(t, contracts.MiMoStatusTypeIdle))
			var dividers []agenttest.Message
			for _, row := range sink.Messages() {
				if row.TurnEnd {
					dividers = append(dividers, row)
				}
			}
			require.Len(t, dividers, 2)
			assert.Equal(t, agent.MessageCompletionError, dividers[0].Completion)
			assert.Equal(t, agent.MessageCompletionComplete, dividers[1].Completion)
			child := sink.Child(a.actors[actorID].childAgentID)
			rows := assembledRows(child)
			require.Len(t, rows, 1)
			_, text, completion := assembledText(t, rows[0].Content)
			assert.Equal(t, "The identical native failure.", text)
			assert.Equal(t, string(agent.MessageCompletionError), completion)
			assert.Equal(t, testSessionID, rows[0].AgentSessionID)
		})
	}
}

func TestLateOldChildFailureCannotClaimANewMainFailure(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	old := failedMessageEvent(t, "msg_old_child_error", actorID, "APIError", "The repeated failure text.")
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""),
		sessionErrorEvent(t, "APIError", "The repeated failure text."), old,
		actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeFailure, 1, "The repeated failure text."),
		statusEvent(t, contracts.MiMoStatusTypeIdle), statusEvent(t, contracts.MiMoStatusTypeBusy))
	native := sessionErrorEvent(t, "APIError", "The repeated failure text.")
	feed(a, native, old, failedMessageEvent(t, "msg_new_main_error", mainActorID, "APIError", "The repeated failure text."), statusEvent(t, contracts.MiMoStatusTypeIdle))
	var dividers []agenttest.Message
	for _, row := range sink.Messages() {
		if row.TurnEnd {
			dividers = append(dividers, row)
		}
	}
	require.Len(t, dividers, 2)
	assert.Equal(t, agent.MessageCompletionComplete, dividers[0].Completion)
	assert.Equal(t, agent.MessageCompletionError, dividers[1].Completion)
	assert.Equal(t, string(native), string(dividers[1].Content))
}

func TestLateCompletedGetStillAllowsTheFirstNativeFailureClaim(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	server.respond("GET /session/ses_test/message/msg_get_child_error", http.StatusOK, `{"info":{"id":"msg_get_child_error","sessionID":"ses_test","role":"assistant","agentID":"general-1","error":{"name":"APIError","data":{"message":"The current child failure."}}},"parts":[]}`)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""),
		toolPartEvent(t, "part_get_child_error", "msg_get_child_error", contracts.MiMoToolBash, "call_get_child_error", toolState{Status: contracts.MiMoToolStatusRunning}),
		sessionErrorEvent(t, "APIError", "The current child failure."), failedMessageEvent(t, "msg_get_child_error", actorID, "APIError", "The current child failure."),
		actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeFailure, 1, "The current child failure."), statusEvent(t, contracts.MiMoStatusTypeIdle))
	root := sink.Messages()
	require.Len(t, root, 1)
	assert.True(t, root[0].TurnEnd)
	assert.Equal(t, agent.MessageCompletionComplete, root[0].Completion)
	rows := assembledRows(sink.Child(a.actors[actorID].childAgentID))
	require.Len(t, rows, 1)
	_, text, completion := assembledText(t, rows[0].Content)
	assert.Equal(t, "The current child failure.", text)
	assert.Equal(t, string(agent.MessageCompletionError), completion)
}

func TestLateConcurrentNativeFailuresKeepTheirExactActors(t *testing.T) {
	t.Parallel()
	for _, mainFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(mainFirst), func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""),
				messageEvent(t, "msg_queue_main", roleAssistant, mainActorID, false), messageEvent(t, "msg_queue_child", roleAssistant, actorID, false))
			mainError := sessionErrorEvent(t, "APIError", "The distinct main failure.")
			childError := sessionErrorEvent(t, "APIError", "The distinct child failure.")
			if mainFirst {
				feed(a, mainError, childError)
			} else {
				feed(a, childError, mainError)
			}
			feed(a, failedMessageEvent(t, "msg_queue_main", mainActorID, "APIError", "The distinct main failure."),
				failedMessageEvent(t, "msg_queue_child", actorID, "APIError", "The distinct child failure."),
				actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeFailure, 1, "The distinct child failure."), statusEvent(t, contracts.MiMoStatusTypeIdle))
			root := sink.Messages()
			require.Len(t, root, 1)
			assert.True(t, root[0].TurnEnd)
			assert.Equal(t, agent.MessageCompletionError, root[0].Completion)
			assert.Equal(t, string(mainError), string(root[0].Content))
			child := assembledRows(sink.Child(a.actors[actorID].childAgentID))
			require.Len(t, child, 1)
			_, text, completion := assembledText(t, child[0].Content)
			assert.Equal(t, "The distinct child failure.", text)
			assert.Equal(t, string(agent.MessageCompletionError), completion)
			assert.Zero(t, sink.NotificationCount())
		})
	}
}

func TestLateClosureKeepsEveryUnmatchedNativeFailure(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"stop", "clear", "wait"} {
		for _, reverse := range []bool{false, true} {
			t.Run(action+fmt.Sprint(reverse), func(t *testing.T) {
				t.Parallel()
				a, sink, _ := newSinkTestAgent(t)
				one := sessionErrorEvent(t, "APIError", "The first native failure.")
				two := sessionErrorEvent(t, "UnknownError", "The second native failure.")
				if reverse {
					one, two = two, one
				}
				feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), one, two)
				switch action {
				case "stop":
					a.SimulateExitForTest()
					a.Stop()
				case "clear":
					_, err := a.ClearContext()
					require.NoError(t, err)
				case "wait":
					a.SimulateExitForTest()
					require.NoError(t, a.Wait())
				}
				notifications := sink.PersistedNotifications()
				require.Len(t, notifications, 2)
				assert.Equal(t, string(one), string(notifications[0].Content))
				assert.Equal(t, string(two), string(notifications[1].Content))
			})
		}
	}
}

func TestLateSecondFailureSurvivesAnExistingMainClaim(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	main := sessionErrorEvent(t, "APIError", "The claimed main failure.")
	later := sessionErrorEvent(t, "UnknownError", "The later unmatched failure.")
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), main,
		failedMessageEvent(t, "msg_claim_before_second", mainActorID, "APIError", "The claimed main failure."), later)
	a.SimulateExitForTest()
	a.Stop()
	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 2)
	assert.Equal(t, string(main), string(notifications[0].Content))
	assert.Equal(t, string(later), string(notifications[1].Content))
}

func TestLateUnresolvedChildFailureSurvivesParentSettlementAndRestart(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""),
		messageEvent(t, "msg_live_queue_child", roleAssistant, actorID, false), sessionErrorEvent(t, "APIError", "The live child's native failure."),
		statusEvent(t, contracts.MiMoStatusTypeIdle), statusEvent(t, contracts.MiMoStatusTypeBusy),
		failedMessageEvent(t, "msg_live_queue_child", actorID, "APIError", "The live child's native failure."),
		actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeFailure, 1, "The live child's native failure."), statusEvent(t, contracts.MiMoStatusTypeIdle))
	root := sink.Messages()
	require.Len(t, root, 2)
	for _, row := range root {
		assert.True(t, row.TurnEnd)
		assert.Equal(t, agent.MessageCompletionComplete, row.Completion)
	}
	assert.Zero(t, sink.NotificationCount())
	rows := assembledRows(sink.Child(a.actors[actorID].childAgentID))
	require.Len(t, rows, 1)
	_, text, completion := assembledText(t, rows[0].Content)
	assert.Equal(t, "The live child's native failure.", text)
	assert.Equal(t, string(agent.MessageCompletionError), completion)
}

type rejectedFailureReceiptSink struct {
	agent.ProviderServices
	dividerReject       atomic.Int32
	notificationReject  atomic.Int32
	notificationBlocked atomic.Bool
}

func (s *rejectedFailureReceiptSink) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	if s.dividerReject.Load() > 0 {
		s.dividerReject.Add(-1)
		return errors.New("the controlled divider write failed")
	}
	return s.ProviderServices.PersistTurnEnd(content, span)
}

func (s *rejectedFailureReceiptSink) PersistNotification(source leapmuxv1.MessageSource, content agent.MessageContent) (bool, error) {
	if s.notificationBlocked.Load() {
		return false, errors.New("the controlled notification write remains blocked")
	}
	if s.notificationReject.Load() > 0 {
		s.notificationReject.Add(-1)
		return false, errors.New("the controlled failure notification write failed")
	}
	return s.ProviderServices.PersistNotification(source, content)
}

func TestLateRejectedMainDividerRetainsItsOriginalFinalization(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &rejectedFailureReceiptSink{ProviderServices: a.sink}
	a.sink = controlled
	native := sessionErrorEvent(t, "APIError", "The original failed main turn.")
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
		messageEvent(t, "msg_rejected_divider", roleAssistant, mainActorID, false),
		toolPartEvent(t, "part_rejected_divider", "msg_rejected_divider", contracts.MiMoToolBash, "call_rejected_divider", toolState{Status: contracts.MiMoToolStatusCompleted, Output: "native"}),
		native, failedMessageEvent(t, "msg_rejected_divider", mainActorID, "APIError", "The original failed main turn."))
	controlled.dividerReject.Store(1)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
	feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle), statusEvent(t, contracts.MiMoStatusTypeIdle))
	var dividers []agenttest.Message
	for _, row := range sink.Messages() {
		if row.TurnEnd {
			dividers = append(dividers, row)
		}
	}
	require.Len(t, dividers, 1)
	assert.Equal(t, string(native), string(dividers[0].Content))
	assert.Equal(t, testSessionID, dividers[0].AgentSessionID)
	assert.Equal(t, agent.MessageCompletionError, dividers[0].Completion)
	var metadata map[string]any
	require.NoError(t, json.Unmarshal(dividers[0].Metadata, &metadata))
	assert.EqualValues(t, 1, metadata[contracts.MessageMetadataFieldToolUses])
}

func TestLateRejectedFailureNotificationRetainsItsNativeBytes(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &rejectedFailureReceiptSink{ProviderServices: a.sink}
	a.sink = controlled
	native := sessionErrorEvent(t, "APIError", "The original unreported failure.")
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), native)
	controlled.notificationReject.Store(1)
	a.SimulateExitForTest()
	a.Stop()
	assert.Zero(t, sink.NotificationCount())
	a.Stop()
	a.Stop()
	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 1)
	assert.Equal(t, string(native), string(notifications[0].Content))
}

func TestLateDistinctFailureAfterFailedIdleIsNotARepeat(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	first := sessionErrorEvent(t, "APIError", "The original main failure.")
	later := sessionErrorEvent(t, "UnknownError", "The distinct later native failure.")
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), first,
		failedMessageEvent(t, "msg_first_idle_failure", mainActorID, "APIError", "The original main failure."),
		statusEvent(t, contracts.MiMoStatusTypeIdle), later)
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.True(t, rows[0].TurnEnd)
	assert.Equal(t, agent.MessageCompletionError, rows[0].Completion)
	assert.Equal(t, string(first), string(rows[0].Content))
	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 1)
	assert.Equal(t, string(later), string(notifications[0].Content))
}

func TestLateUnidentifiedFailuresAfterIdleKeepEveryNativeObservation(t *testing.T) {
	t.Parallel()
	for _, identical := range []bool{false, true} {
		t.Run(fmt.Sprint(identical), func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			first := sessionErrorEvent(t, "APIError", "The first unidentified failure.")
			later := sessionErrorEvent(t, "UnknownError", "The later unidentified failure.")
			if identical {
				later = append([]byte(nil), first...)
			}
			feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), first,
				statusEvent(t, contracts.MiMoStatusTypeIdle), later)
			rows := sink.Messages()
			require.Len(t, rows, 1)
			assert.True(t, rows[0].TurnEnd)
			assert.Equal(t, agent.MessageCompletionError, rows[0].Completion)
			assert.Equal(t, string(first), string(rows[0].Content))
			notifications := sink.PersistedNotifications()
			require.Len(t, notifications, 1)
			assert.Equal(t, string(later), string(notifications[0].Content))
		})
	}
}

func TestLateAttributedChildFailureSurvivesMissingActorSettlement(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	native := sessionErrorEvent(t, "APIError", "The child failure before shutdown.")
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
		actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""),
		messageEvent(t, "msg_child_error_before_stop", roleAssistant, actorID, false),
		native, failedMessageEvent(t, "msg_child_error_before_stop", actorID, "APIError", "The child failure before shutdown."))
	child := sink.Child(a.actors[actorID].childAgentID)
	a.SimulateExitForTest()
	a.Stop()
	notifications := child.PersistedNotifications()
	require.Len(t, notifications, 1)
	assert.Equal(t, string(native), string(notifications[0].Content))
	assert.Zero(t, sink.NotificationCount())
}

func TestLateInterruptedDividerKeepsAnEarlierMainFailure(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	clock := useMockClock(t, a)
	native := sessionErrorEvent(t, "APIError", "The earlier failed main request.")
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), native,
		failedMessageEvent(t, "msg_main_failure_before_abort", mainActorID, "APIError", "The earlier failed main request."))
	assert.Equal(t, mimoAbortGrace, interruptOnClock(t, a, clock))
	idle := statusEvent(t, contracts.MiMoStatusTypeIdle)
	feed(a, idle)
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.True(t, rows[0].TurnEnd)
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[0].Completion)
	assert.Equal(t, string(idle), string(rows[0].Content))
	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 1)
	assert.Equal(t, string(native), string(notifications[0].Content))
}

func TestLateUnresolvedUserInstructionSurvivesClosure(t *testing.T) {
	t.Parallel()
	for _, clearContext := range []bool{false, true} {
		t.Run(fmt.Sprint(clearContext), func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			const messageID = "msg_unresolved_instruction_at_close"
			const partID = "part_unresolved_instruction_at_close"
			const instruction = "The original instruction whose actor remains unresolved."
			feed(a, messageEvent(t, messageID, roleUser, "", false),
				textPartEvent(t, partTypeText, partID, messageID, instruction, false))
			record := a.messages[messageID]
			require.Len(t, record.pendingParts, 1)
			order := record.pendingParts[0].order
			if clearContext {
				_, err := a.ClearContext()
				require.NoError(t, err)
			} else {
				a.SimulateExitForTest()
				a.Stop()
			}
			retained := a.messages[messageID]
			require.NotNil(t, retained)
			assert.Equal(t, testSessionID, retained.sessionID)
			assert.False(t, retained.actorKnown)
			assert.Nil(t, retained.actor)
			require.Len(t, retained.pendingParts, 1)
			assert.Equal(t, order, retained.pendingParts[0].order)
			assert.Equal(t, partID, retained.pendingParts[0].partID)
			part := a.parts[partID]
			require.NotNil(t, part)
			require.NotNil(t, part.final)
			assert.Equal(t, partID, part.final.ID)
			assert.Equal(t, messageID, part.final.MessageID)
			assert.Equal(t, testSessionID, part.final.SessionID)
			assert.Equal(t, instruction, part.final.Text)
			assert.Empty(t, sink.Messages(), "an absent owner cannot identify a transcript destination")
		})
	}
}

func TestLateUnresolvedWholeTextSurvivesClosure(t *testing.T) {
	t.Parallel()
	for _, clearContext := range []bool{false, true} {
		t.Run(fmt.Sprint(clearContext), func(t *testing.T) {
			t.Parallel()
			a, sink, server := newSinkTestAgent(t)
			const messageID = "msg_unresolved_whole_text_at_close"
			const partID = "part_unresolved_whole_text_at_close"
			const text = "The original whole text whose native role remains unresolved."
			server.respond("GET /session/ses_test/message/"+messageID, http.StatusServiceUnavailable, `{}`)
			feed(a, textPartEvent(t, partTypeText, partID, messageID, text, false))
			original := a.parts[partID]
			require.NotNil(t, original)
			require.NotNil(t, original.unresolved)
			order := original.unresolvedOrder
			if clearContext {
				_, err := a.ClearContext()
				require.NoError(t, err)
			} else {
				a.SimulateExitForTest()
				a.Stop()
			}
			retained := a.messages[messageID]
			require.NotNil(t, retained)
			assert.Equal(t, testSessionID, retained.sessionID)
			assert.False(t, retained.identityKnown)
			assert.False(t, retained.actorKnown)
			assert.Nil(t, retained.actor)
			part := a.parts[partID]
			require.NotNil(t, part)
			require.NotNil(t, part.unresolved)
			assert.Equal(t, order, part.unresolvedOrder)
			assert.Equal(t, partID, part.unresolved.ID)
			assert.Equal(t, messageID, part.unresolved.MessageID)
			assert.Equal(t, testSessionID, part.unresolved.SessionID)
			assert.Equal(t, text, part.unresolved.Text)
			assert.Empty(t, sink.Messages(), "a failed native read cannot identify a role or destination")
		})
	}
}

func TestLateRejectedCompactionNotificationsKeepBothNativePhases(t *testing.T) {
	t.Parallel()
	for _, rejectStart := range []bool{false, true} {
		t.Run(fmt.Sprint(rejectStart), func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			controlled := &rejectedFailureReceiptSink{ProviderServices: a.sink}
			a.sink = controlled
			phase := func(completed bool) []byte {
				part := map[string]any{
					"id": "part_rejected_compaction", "messageID": "msg_rejected_compaction", "sessionID": testSessionID,
					"type": contracts.MiMoPartTypeCompaction, "auto": false,
				}
				if completed {
					part["projection"] = map[string]any{"summary": "The original native summary."}
				}
				return eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": part})
			}
			start, end := phase(false), phase(true)
			feed(a, messageEvent(t, "msg_rejected_compaction", roleUser, mainActorID, false))
			if rejectStart {
				controlled.notificationBlocked.Store(true)
			}
			feed(a, start)
			if !rejectStart {
				controlled.notificationBlocked.Store(true)
			}
			feed(a, end, end)
			before := 1
			if rejectStart {
				before = 0
			}
			assert.Equal(t, before, sink.NotificationCount())
			controlled.notificationBlocked.Store(false)
			a.SimulateExitForTest()
			a.Stop()
			notifications := sink.PersistedNotifications()
			require.Len(t, notifications, 2)
			assert.Equal(t, string(start), string(notifications[0].Content))
			assert.Equal(t, string(end), string(notifications[1].Content))
			a.Stop()
			assert.Equal(t, 2, sink.NotificationCount())
		})
	}
}

func TestLateRejectedRetryNotificationsKeepEveryNativeObservation(t *testing.T) {
	t.Parallel()
	for _, identical := range []bool{false, true} {
		t.Run(fmt.Sprint(identical), func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			controlled := &rejectedFailureReceiptSink{ProviderServices: a.sink}
			a.sink = controlled
			retry := func(attempt int) []byte {
				return eventJSON(t, contracts.MiMoEventSessionStatus, map[string]any{
					"sessionID": testSessionID,
					"status":    map[string]any{"type": contracts.MiMoStatusTypeRetry, "attempt": attempt, "message": "The native retry reason.", "next": attempt},
				})
			}
			one, two := retry(1), retry(2)
			if identical {
				two = append([]byte(nil), one...)
			}
			controlled.notificationBlocked.Store(true)
			feed(a, one, two)
			assert.Zero(t, sink.NotificationCount())
			controlled.notificationBlocked.Store(false)
			a.SimulateExitForTest()
			a.Stop()
			notifications := sink.PersistedNotifications()
			require.Len(t, notifications, 2)
			assert.Equal(t, string(one), string(notifications[0].Content))
			assert.Equal(t, string(two), string(notifications[1].Content))
			a.Stop()
			assert.Equal(t, 2, sink.NotificationCount())
		})
	}
}

func compactionPhaseEvent(t *testing.T, partID, messageID string, completed bool) []byte {
	t.Helper()
	part := map[string]any{
		"id": partID, "messageID": messageID, "sessionID": testSessionID,
		"type": contracts.MiMoPartTypeCompaction, "auto": false,
	}
	if completed {
		part["projection"] = map[string]any{"summary": "The native summary."}
	}
	return eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": part})
}

func retryStatusEvent(t *testing.T, attempt int) []byte {
	t.Helper()
	return eventJSON(t, contracts.MiMoEventSessionStatus, map[string]any{
		"sessionID": testSessionID,
		"status": map[string]any{
			"type": contracts.MiMoStatusTypeRetry, "attempt": attempt,
			"message": "The native retry reason.", "next": attempt,
		},
	})
}

func TestLateCapturedUnknownFailureKeepsRootNotificationOrder(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	failure := sessionErrorEvent(t, "APIError", "The unresolved main failure.")
	compaction := compactionPhaseEvent(t, "part_unknown_root_compaction", "msg_unknown_root_compaction", false)
	retry := retryStatusEvent(t, 1)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
		actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""),
		messageEvent(t, "msg_unknown_root_compaction", roleUser, mainActorID, false),
		failure, compaction, retry)
	assert.Zero(t, sink.NotificationCount(), "the unresolved destination precedes later root notifications")
	feed(a, failedMessageEvent(t, "msg_unknown_main_failure", mainActorID, "APIError", "The unresolved main failure."),
		statusEvent(t, contracts.MiMoStatusTypeIdle))
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.True(t, rows[0].TurnEnd)
	assert.Equal(t, string(failure), string(rows[0].Content))
	assert.Equal(t, agent.MessageCompletionError, rows[0].Completion)
	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 2)
	assert.Equal(t, string(compaction), string(notifications[0].Content))
	assert.Equal(t, string(retry), string(notifications[1].Content))
}

func TestLateCapturedChildAttributionReleasesRootNotifications(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	failure := sessionErrorEvent(t, "APIError", "The unresolved child failure.")
	compaction := compactionPhaseEvent(t, "part_unknown_child_root_compaction", "msg_unknown_child_root_compaction", false)
	retry := retryStatusEvent(t, 1)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
		actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""),
		messageEvent(t, "msg_unknown_child_root_compaction", roleUser, mainActorID, false),
		failure, compaction, retry)
	assert.Zero(t, sink.NotificationCount(), "root notifications wait while the earlier destination remains unknown")
	feed(a, failedMessageEvent(t, "msg_exact_child_failure", actorID, "APIError", "The unresolved child failure."))
	assert.True(t, a.actors[actorID].running)
	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 2, "exact child attribution releases the independent root destination")
	assert.Equal(t, string(compaction), string(notifications[0].Content))
	assert.Equal(t, string(retry), string(notifications[1].Content))
	childID := a.actors[actorID].childAgentID
	assert.Zero(t, sink.Child(childID).NotificationCount())
	assert.Empty(t, sink.Messages(), "an active child failure creates no root divider")
	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeFailure, 1, "The unresolved child failure."))
	childRows := assembledRows(sink.Child(childID))
	require.Len(t, childRows, 1)
	_, text, completion := assembledText(t, childRows[0].Content)
	assert.Equal(t, "The unresolved child failure.", text)
	assert.Equal(t, string(agent.MessageCompletionError), completion)
	assert.Equal(t, 2, sink.NotificationCount(), "native child settlement adds no root failure report")
}

func TestLateCapturedLiveChildFailureDoesNotHoldRootNotifications(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	failure := sessionErrorEvent(t, "APIError", "The known child failure.")
	compaction := compactionPhaseEvent(t, "part_live_child_root_compaction", "msg_live_child_root_compaction", false)
	retry := retryStatusEvent(t, 1)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
		actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""), failure,
		failedMessageEvent(t, "msg_live_child_failure", actorID, "APIError", "The known child failure."),
		messageEvent(t, "msg_live_child_root_compaction", roleUser, mainActorID, false), compaction, retry)
	assert.True(t, a.actors[actorID].running)
	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 2)
	assert.Equal(t, string(compaction), string(notifications[0].Content))
	assert.Equal(t, string(retry), string(notifications[1].Content))
	assert.Empty(t, sink.Messages(), "the known live child failure creates no root divider")
	childID := a.actors[actorID].childAgentID
	assert.Zero(t, sink.Child(childID).NotificationCount())
	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeFailure, 1, "The known child failure."))
	childRows := assembledRows(sink.Child(childID))
	require.Len(t, childRows, 1)
	_, text, completion := assembledText(t, childRows[0].Content)
	assert.Equal(t, "The known child failure.", text)
	assert.Equal(t, string(agent.MessageCompletionError), completion)
	assert.Equal(t, 2, sink.NotificationCount(), "the child error remains on the exact child")
}

func TestLateCapturedNotificationQueueKeepsDestinationOrder(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &rejectedFailureReceiptSink{ProviderServices: a.sink}
	a.sink = controlled
	rootOne, rootTwo := retryStatusEvent(t, 1), retryStatusEvent(t, 2)
	childCompaction := compactionPhaseEvent(t, "part_independent_child_compaction", "msg_independent_child_compaction", false)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
		actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""),
		messageEvent(t, "msg_independent_child_compaction", roleUser, actorID, false))
	childID := a.actors[actorID].childAgentID
	controlled.notificationBlocked.Store(true)
	feed(a, rootOne, childCompaction, rootTwo)
	assert.Zero(t, sink.NotificationCount())
	childNotifications := sink.Child(childID).PersistedNotifications()
	require.Len(t, childNotifications, 1, "a rejected root destination does not block its child's destination")
	assert.Equal(t, string(childCompaction), string(childNotifications[0].Content))
	controlled.notificationBlocked.Store(false)
	a.SimulateExitForTest()
	a.Stop()
	rootNotifications := sink.PersistedNotifications()
	require.Len(t, rootNotifications, 2)
	assert.Equal(t, string(rootOne), string(rootNotifications[0].Content))
	assert.Equal(t, string(rootTwo), string(rootNotifications[1].Content))
	a.Stop()
	assert.Equal(t, 2, sink.NotificationCount())
	assert.Equal(t, 1, sink.Child(childID).NotificationCount())
}

func usageMessageEvent(t *testing.T, messageID, actor string, cost float64, tokens any) []byte {
	t.Helper()
	info := map[string]any{
		"id": messageID, "sessionID": testSessionID, "role": roleAssistant,
		"agentID": actor, "providerID": "mock", "modelID": "alpha", "cost": cost,
	}
	if tokens != nil {
		info["tokens"] = tokens
	}
	return eventJSON(t, eventMessageUpdated, map[string]any{"info": info})
}

func TestLateRetainedMainUsageKeepsCurrentContextOwnership(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		replacement bool
		actor       string
		messageID   string
		cost        float64
		input       int64
		context     int64
		totalCost   float64
	}{
		{name: "latest completed turn", actor: mainActorID, messageID: "msg_usage_original", cost: 0.75, input: 1500, context: 1500, totalCost: 0.75},
		{name: "old main message", replacement: true, actor: mainActorID, messageID: "msg_usage_original", cost: 1, input: 1500, context: 2000, totalCost: 1.75},
		{name: "child message", replacement: true, actor: actorID, messageID: "msg_usage_child", cost: 0.25, input: 9000, context: 2000, totalCost: 1.5},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
				usageMessageEvent(t, "msg_usage_original", mainActorID, 0.5, map[string]any{"input": int64(1000)}),
				statusEvent(t, contracts.MiMoStatusTypeIdle))
			if test.replacement {
				feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
					usageMessageEvent(t, "msg_usage_replacement", mainActorID, 0.75, map[string]any{"input": int64(2000)}))
			}
			feed(a, usageMessageEvent(t, test.messageID, test.actor, test.cost, map[string]any{"input": test.input}))
			usage := a.usageSnapshot()
			assert.EqualValues(t, test.context, usage.contextUsage[contracts.ContextUsageFieldContextTokens])
			assert.InDelta(t, test.totalCost, usage.costUSD, 1e-9)
			assert.InDelta(t, test.totalCost, sink.LastSessionInfo()[contracts.SessionInfoKeyTotalCostUsd], 1e-9)
			if test.replacement {
				assert.NotContains(t, sink.LastSessionInfo(), contracts.SessionInfoKeyContextUsage, "old main or child usage cannot replace current root context")
			}
		})
	}
}

func TestLateMainUsageKeepsOptionalCounterSemantics(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		tokens  any
		context int64
		cost    float64
	}{
		{name: "zero", tokens: map[string]any{"input": 0, "output": 0}, context: 1000, cost: 0.75},
		{name: "absent tokens", context: 1000, cost: 0.75},
		{name: "absent optional counts", tokens: map[string]any{"input": 1500}, context: 1500, cost: 0.75},
		{name: "negative total", tokens: map[string]any{"input": -1}, context: 1000, cost: 0.75},
		{name: "malformed counter", tokens: map[string]any{"input": "invalid"}, context: 1000, cost: 0.5},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
				usageMessageEvent(t, "msg_usage_optional", mainActorID, 0.5, map[string]any{"input": 1000}),
				usageMessageEvent(t, "msg_usage_optional", mainActorID, 0.75, test.tokens))
			usage := a.usageSnapshot()
			assert.EqualValues(t, test.context, usage.contextUsage[contracts.ContextUsageFieldContextTokens])
			assert.InDelta(t, test.cost, usage.costUSD, 1e-9)
			assert.InDelta(t, test.cost, sink.LastSessionInfo()[contracts.SessionInfoKeyTotalCostUsd], 1e-9)
			if test.name == "absent optional counts" {
				assert.EqualValues(t, 0, usage.contextUsage[contracts.ContextUsageFieldCacheCreationInputTokens])
				assert.EqualValues(t, 0, usage.contextUsage[contracts.ContextUsageFieldCacheReadInputTokens])
				assert.EqualValues(t, 0, usage.contextUsage[contracts.ContextUsageFieldOutputTokens])
			}
		})
	}
}

func beginPartialAnswerInSession(t *testing.T, a *Agent, sessionID, messageID, partID, text string) {
	t.Helper()
	feed(a,
		eventWithSession(t, statusEvent(t, contracts.MiMoStatusTypeBusy), sessionID),
		eventWithSession(t, messageEvent(t, messageID, roleAssistant, mainActorID, false), sessionID),
		eventWithSession(t, textPartEvent(t, partTypeText, partID, messageID, "", false), sessionID),
		eventWithSession(t, deltaEvent(t, partID, messageID, text), sessionID))
}

func TestLateRetainedFinalTextKeepsReplacementProgress(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &controlledPartSink{ProviderServices: a.sink, rejectText: "The old native final."}
	a.sink = controlled
	beginPartialAnswer(t, a, "msg_retained_progress_text", "part_retained_progress_text", "The old prefix.")
	controlled.reject.Store(10)
	feed(a, textPartEvent(t, partTypeText, "part_retained_progress_text", "msg_retained_progress_text", "The old native final.", true))
	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	beginPartialAnswerInSession(t, a, sessionID, "msg_replacement_progress_text", "part_replacement_progress_text", "NEWNEWNEWNEW")
	before := sink.ProgressSnapshot()
	require.EqualValues(t, 3, before.ThinkingTokens)
	controlled.reject.Store(0)
	require.True(t, a.flushPendingMessage("msg_retained_progress_text", ""))
	assert.Equal(t, before, sink.ProgressSnapshot(), "an old native final cannot reset the replacement model scope")
	rows := assembledRows(sink)
	require.Len(t, rows, 1)
	_, text, completion := assembledText(t, rows[0].Content)
	assert.Equal(t, "The old native final.", text)
	assert.Equal(t, string(agent.MessageCompletionComplete), completion)
	assert.Equal(t, testSessionID, rows[0].AgentSessionID)
}

func TestLateRetainedToolCloserKeepsReplacementProgress(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	const oldPart = "part_retained_progress_tool"
	controlled := &controlledPartSink{ProviderServices: a.sink, rejectText: oldPart}
	a.sink = controlled
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
		messageEvent(t, "msg_retained_progress_tool", roleAssistant, mainActorID, false),
		toolPartEvent(t, oldPart, "msg_retained_progress_tool", contracts.MiMoToolBash, "call_retained_progress_tool", toolState{
			Status: contracts.MiMoToolStatusRunning, Input: map[string]any{"command": "printf old"},
		}))
	controlled.reject.Store(10)
	closing := toolPartEvent(t, oldPart, "msg_retained_progress_tool", contracts.MiMoToolBash, "call_retained_progress_tool", toolState{
		Status: contracts.MiMoToolStatusCompleted, Input: map[string]any{"command": "printf old"}, Output: "The old native output.",
	})
	feed(a, closing)
	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	feed(a,
		eventWithSession(t, statusEvent(t, contracts.MiMoStatusTypeBusy), sessionID),
		eventWithSession(t, messageEvent(t, "msg_replacement_progress_tool", roleAssistant, mainActorID, false), sessionID),
		eventWithSession(t, toolPartEvent(t, "part_replacement_progress_tool", "msg_replacement_progress_tool", contracts.MiMoToolBash, "call_replacement_progress_tool", toolState{
			Status: contracts.MiMoToolStatusRunning, Input: map[string]any{"command": "printf new"}, Metadata: map[string]any{"output": "NEW-OUTPUT"},
		}), sessionID))
	beginPartialAnswerInSession(t, a, sessionID, "msg_replacement_progress_tool", "part_replacement_progress_model", "NEWNEWNEWNEW")
	before := sink.ProgressSnapshot()
	require.EqualValues(t, 3, before.ThinkingTokens)
	require.EqualValues(t, len("NEW-OUTPUT"), before.OutputBytes)
	controlled.reject.Store(0)
	require.True(t, a.flushPendingMessage("msg_retained_progress_tool", ""))
	assert.Equal(t, before, sink.ProgressSnapshot(), "the old closer cannot reset either replacement scope")
	var oldClosers []agenttest.Message
	for _, row := range sink.Messages() {
		if row.Closing && row.SpanID == oldPart {
			oldClosers = append(oldClosers, row)
		}
	}
	require.Len(t, oldClosers, 1)
	assert.Equal(t, string(closing), string(oldClosers[0].Content))
	assert.Equal(t, testSessionID, oldClosers[0].AgentSessionID)
}

package mimo

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
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
	assert.Equal(t, "call-1", messages[0].SpanID)
	assert.Equal(t, contracts.MiMoToolBash, messages[0].SpanType)
	assert.False(t, messages[0].Closing)
	assert.Empty(t, messages[0].SpansOpenAtPersist, "the opener persists before its span opens")
	assert.Equal(t, []agenttest.SpanOpen{{SpanID: "call-1"}}, sink.OpenSpans())

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
	assert.Equal(t, "call-1", messages[1].SpanID)
	assert.Equal(t, []agenttest.SpanOpen{{SpanID: "call-1"}}, messages[1].SpansOpenAtPersist, "the closer persists inside its span")
	assert.Equal(t, []string{"call-1"}, sink.ClosedSpans())
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

// A tool part that names no call, or states no state, cannot be tied to a row,
// and a part that states no id or carries no conversation adds none.
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

// A turn end drops what the finished turn no longer needs. A subagent that
// still runs keeps its message and its call, and a call that the turn end cut
// keeps its record until the next turn end, so its late final update closes
// nothing a second time.
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
	assert.ElementsMatch(t, []string{"msg_sub"}, sortedKeys(a.messages), "a completed message goes, and a running one stays")
	assert.ElementsMatch(t, []string{"call-cut", "call-sub"}, sortedKeys(a.tools))
	assert.True(t, a.tools["call-cut"].final, "the cut call closes nothing a second time")

	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), statusEvent(t, contracts.MiMoStatusTypeIdle))
	assert.ElementsMatch(t, []string{"call-sub"}, sortedKeys(a.tools), "the cut call's record outlives one turn end only")
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
						"error": map[string]any{"name": errorNameAborted, "data": map[string]any{"message": "The operation was aborted."}}}),
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
			assert.Equal(t, "call-1", messages[2].SpanID)
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

// MiMo repeats a turn's failure after the turn ends. The divider already
// states it, so the repeat is dropped; a failure after new input is not.
func TestRepeatedTurnFailureIsDropped(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	failure := eventJSON(t, contracts.MiMoEventSessionError, map[string]any{"sessionID": testSessionID,
		"error": map[string]any{"name": "APIError", "data": map[string]any{"message": "rate limited"}}})

	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), failure, statusEvent(t, contracts.MiMoStatusTypeIdle), failure)
	assert.Zero(t, sink.NotificationCount(), "the repeat after the failed turn is dropped")

	require.NoError(t, a.SendInput("again", nil))
	feed(a, failure)
	assert.Equal(t, 1, sink.NotificationCount(), "a failure after new input is a new failure")
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

// The turn end persists its divider before it clears the flag, whatever
// ended it: the clear is the settle edge that spends the turn's tool count.
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
// names the actor whose message failed.
func failedMessageEvent(t *testing.T, id, actorID, name, message string) []byte {
	t.Helper()
	return eventJSON(t, eventMessageUpdated, map[string]any{"info": map[string]any{
		"id": id, "sessionID": testSessionID, "role": roleAssistant, "agentID": actorID,
		"error": map[string]any{"name": name, "data": map[string]any{"message": message}},
	}})
}

// A subagent runs in the agent's own session, so its failure arrives as the
// session's. The failed message that follows names the subagent, and the main
// turn does not fail with it.
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
		childRows := sink.Child("child-of-" + spawnCallID).Messages()
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

	t.Run("a held failure is reported before the next turn", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newSinkTestAgent(t)
		spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
		feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
		failure := sessionErrorEvent(t, "ProviderModelNotFoundError", "no such model")

		feed(a, failure, statusEvent(t, contracts.MiMoStatusTypeBusy))
		require.Equal(t, 1, sink.NotificationCount())
		assert.JSONEq(t, string(failure), string(sink.LastNotification().Content))
	})
}

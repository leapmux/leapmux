package kiro

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// turnStart is Kiro's marker of the start of one execution.
func turnStart(t *testing.T) []byte {
	t.Helper()
	return infoUpdate(t, map[string]any{"turnStart": true, "kind": kiroKindTurnStart, "messageId": "m-turn-start"})
}

// turnEndObject is the update object of Kiro's marker of the end of one
// execution, as the probe recorded it.
func turnEndObject(stopReason string) map[string]any {
	return infoUpdateObject(map[string]any{
		"turnEnd":    map[string]any{"stopReason": stopReason},
		"kind":       contracts.KiroKindTurnEnd,
		"stopReason": stopReason,
		"messageId":  "m-turn-end",
	})
}

// turnEnd encodes the end marker of one execution of the main session.
func turnEnd(t *testing.T, stopReason string) []byte {
	t.Helper()
	return sessionUpdate(t, kiroTestSession, turnEndObject(stopReason))
}

// displayError is Kiro's report of an error that it shows its own reader.
func displayError(t *testing.T, message string) []byte {
	t.Helper()
	return infoUpdate(t, map[string]any{
		"displayError": map[string]any{"message": message, "errorType": "ServiceThrottleError"},
		"kind":         kiroKindDisplayError,
		"message":      message,
	})
}

// agentInitiated is the `_meta.kiro` of an update of a turn that Kiro started.
var agentInitiated = map[string]any{kiroMetaAgentInitiated: true}

// turnEnds returns the turn-end rows of one transcript.
func turnEnds(messages []agenttest.Message) []agenttest.Message {
	var out []agenttest.Message
	for _, message := range messages {
		if message.TurnEnd {
			out = append(out, message)
		}
	}
	return out
}

// replay is the `_meta.kiro` of one chunk of the answer that id identifies.
func replay(id string) map[string]any {
	return map[string]any{kiroMetaReplayID: id}
}

// thoughtChunk is one agent_thought_chunk of the main session.
func thoughtChunk(t *testing.T, text string, kiro map[string]any) []byte {
	t.Helper()
	return sessionUpdate(t, kiroTestSession, map[string]any{
		"sessionUpdate": "agent_thought_chunk",
		"content":       map[string]any{"type": "text", "text": text},
		"_meta":         map[string]any{"kiro": kiro},
	})
}

// assembled returns the texts of the assembled rows of one kind, in order.
func assembled(messages []agenttest.Message, kind agent.AssembledMessageKind) []string {
	var texts []string
	for _, message := range messages {
		var envelope struct {
			Type string `json:"type"`
			Kind string `json:"kind"`
			Text string `json:"text"`
		}
		if json.Unmarshal(message.Content, &envelope) == nil && envelope.Type == "assembled_message" && envelope.Kind == string(kind) {
			texts = append(texts, envelope.Text)
		}
	}
	return texts
}

// Kiro gives each answer of the model its own `_meta.kiro.replayId`. The last
// answer of the plan mode and the first answer of the mode that runs the plan
// arrive in one turn with no update between them that ends a message, and each
// is a message of its own.
func TestKiroSplitsTheAnswersOfOneTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)

	a.HandleOutput(messageChunk(t, "The plan ", replay("answer-1-say")))
	a.HandleOutput(messageChunk(t, "is ready.", replay("answer-1-say")))
	a.HandleOutput(sessionUpdate(t, kiroTestSession, map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": contracts.KiroModeDefault}))
	a.HandleOutput(messageChunk(t, "Running the plan.", replay("answer-2-say")))
	a.FinishPromptRequestForTest(kiroTestSession, json.RawMessage(`{"stopReason":"end_turn"}`), nil)

	assert.Equal(t, []string{"The plan is ready.", "Running the plan."}, assembled(sink.Messages(), agent.AssembledMessageKindText))
}

func TestKiroSplitsTheThoughtsOfTwoAnswers(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)

	a.HandleOutput(thoughtChunk(t, "First thought.", replay("answer-1-say")))
	a.HandleOutput(thoughtChunk(t, "Second thought.", replay("answer-2-say")))
	a.FinishPromptRequestForTest(kiroTestSession, json.RawMessage(`{"stopReason":"end_turn"}`), nil)

	assert.Equal(t, []string{"First thought.", "Second thought."}, assembled(sink.Messages(), agent.AssembledMessageKindReasoning))
}

func TestKiroJoinsTheChunksOfOneAnswer(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)

	a.HandleOutput(messageChunk(t, "One, ", replay("answer-1-say")))
	a.HandleOutput(messageChunk(t, "two, ", replay("answer-1-say")))
	// A chunk that states no answer continues the one before it.
	a.HandleOutput(messageChunk(t, "three.", nil))
	a.FinishPromptRequestForTest(kiroTestSession, json.RawMessage(`{"stopReason":"end_turn"}`), nil)

	assert.Equal(t, []string{"One, two, three."}, assembled(sink.Messages(), agent.AssembledMessageKindText))
}

func TestKiroAgentStartedTurnOpensAndEnds(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	// A workflow that finished wakes the parent, with no prompt of LeapMux's.
	a.HandleOutput(turnStart(t))
	assert.True(t, a.PromptActive(), "Kiro's own turn refuses a second prompt")
	assert.True(t, a.AgentTurnActive())

	a.HandleOutput(messageChunk(t, "The workflow finished.", agentInitiated))
	a.HandleOutput(turnEnd(t, "end_turn"))

	assert.False(t, a.PromptActive(), "the turn_end marker is the only end of such a turn")
	ends := turnEnds(sink.Messages())
	require.Len(t, ends, 1)
	expected, err := json.Marshal(turnEndObject("end_turn"))
	require.NoError(t, err)
	assert.JSONEq(t, string(expected), string(ends[0].Content), "Kiro's own marker is the turn-end row")
}

func TestKiroTurnMarkersOfALeapMuxPromptOpenNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)

	a.HandleOutput(turnStart(t))
	a.HandleOutput(messageChunk(t, "Hello.", nil))
	a.HandleOutput(turnEnd(t, "end_turn"))

	assert.False(t, a.AgentTurnActive(), "the running prompt is LeapMux's own")
	assert.True(t, a.PromptActive(), "the prompt response, not turn_end, ends a LeapMux turn")
	assert.Empty(t, turnEnds(sink.Messages()))
}

func TestKiroAgentTurnAfterALeapMuxPromptWaitsForItsEnd(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(turnStart(t))
	a.HandleOutput(turnEnd(t, "end_turn"))

	// Kiro starts a turn of its own before the base processed the response of
	// LeapMux's prompt. The turn_start cannot tell it apart from a
	// continuation, and its first update carries the mark.
	a.HandleOutput(turnStart(t))
	assert.False(t, a.AgentTurnActive())
	a.HandleOutput(messageChunk(t, "A step warned.", agentInitiated))
	assert.False(t, a.AgentTurnActive(), "the agent turn waits for the prompt's end")

	a.FinishPromptRequestForTest(kiroTestSession, json.RawMessage(`{"stopReason":"end_turn"}`), nil)
	assert.True(t, a.AgentTurnActive(), "the prompt's end hands the busy state over")
	assert.True(t, a.PromptActive())

	a.HandleOutput(turnEnd(t, "end_turn"))
	assert.False(t, a.PromptActive())
	assert.Len(t, turnEnds(sink.Messages()), 2)
}

func TestKiroContinuationOfALeapMuxPromptOpensNoTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(turnStart(t))
	a.HandleOutput(turnEnd(t, "end_turn"))

	// A steer that the turn never read continues the same prompt, and its
	// updates carry no mark.
	a.HandleOutput(turnStart(t))
	a.HandleOutput(messageChunk(t, "About the steer.", nil))
	a.HandleOutput(turnEnd(t, "end_turn"))

	assert.False(t, a.AgentTurnActive())
	assert.True(t, a.PromptActive())
	assert.Empty(t, turnEnds(sink.Messages()))
}

// kiroThrottleMessage is the display error and the JSON-RPC error message of a
// prompt that the model service throttled, as the probe recorded them: the two
// state the same text.
const kiroThrottleMessage = "Too many requests, please wait before trying again. (Request ID: c791fc4e)"

// failPrompt ends the running prompt with a JSON-RPC error, as Kiro's response
// to a failed session/prompt does.
func failPrompt(a *Agent, message string) {
	a.FinishPromptRequestForTest(kiroTestSession, nil, &providerkit.JSONRPCResponseError{Code: -32000, Message: message})
}

func TestKiroDisplayErrorOfAFailedPromptWaitsForThePromptError(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(turnStart(t))

	a.HandleOutput(displayError(t, kiroThrottleMessage))
	assert.Empty(t, errorTexts(sink), "the error waits for the end of the turn")
	a.HandleOutput(turnEnd(t, kiroStopError))
	assert.Empty(t, errorTexts(sink), "the error waits for the end of the prompt")

	failPrompt(a, kiroThrottleMessage)
	assert.Equal(t, []string{"prompt failed: json-rpc error -32000: " + kiroThrottleMessage}, errorTexts(sink),
		"the prompt's own error states the same text, so the reader reads it once")
}

// A prompt whose JSON-RPC error states another text leaves the display error
// the only record of the reason, so the reader sees both.
func TestKiroDisplayErrorOfAPromptWithAnotherErrorReachesTheTranscript(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(turnStart(t))
	a.HandleOutput(displayError(t, kiroThrottleMessage))
	a.HandleOutput(turnEnd(t, kiroStopError))

	failPrompt(a, "Internal error")

	assert.Equal(t, []string{kiroThrottleMessage, "prompt failed: json-rpc error -32000: Internal error"}, errorTexts(sink))
}

// A prompt that answers a result with the stop reason `error` writes no
// failure note of its own, so the display error states the reason.
func TestKiroDisplayErrorOfAPromptThatReturnsAnErrorResultReachesTheTranscript(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(turnStart(t))
	a.HandleOutput(displayError(t, kiroThrottleMessage))
	a.HandleOutput(turnEnd(t, kiroStopError))

	a.FinishPromptRequestForTest(kiroTestSession, json.RawMessage(`{"stopReason":"error"}`), nil)

	assert.Equal(t, []string{kiroThrottleMessage}, errorTexts(sink))
}

// A prompt that the reader stopped writes no failure note, so a display error
// of that prompt still reaches the transcript.
func TestKiroDisplayErrorOfAStoppedPromptReachesTheTranscript(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(displayError(t, kiroThrottleMessage))
	a.NoteInterruptRequestedForTest()

	failPrompt(a, "The operation was cancelled")

	assert.Equal(t, []string{kiroThrottleMessage}, errorTexts(sink))
}

func TestKiroDisplayErrorOfAPromptThatEndedWellReachesTheTranscript(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(turnStart(t))

	a.HandleOutput(displayError(t, "MCP server weather needs authorization."))
	a.HandleOutput(turnEnd(t, "end_turn"))

	assert.Equal(t, []string{"MCP server weather needs authorization."}, errorTexts(sink))
}

func TestKiroSecondDisplayErrorOfAPromptReleasesTheFirst(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)

	a.HandleOutput(displayError(t, "first"))
	a.HandleOutput(displayError(t, "second"))
	assert.Equal(t, []string{"first"}, errorTexts(sink), "only the last error can be the prompt's failure")

	a.HandleOutput(turnEnd(t, kiroStopError))
	assert.Equal(t, []string{"first"}, errorTexts(sink))
}

// A display error that arrives before the execution of the prompt starts --
// an MCP server that failed while the turn prepared -- stays held through the
// start, and a turn that ends well writes it.
func TestKiroDisplayErrorBeforeTheTurnStartStaysHeld(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)

	a.HandleOutput(displayError(t, "MCP server weather failed to connect."))
	a.HandleOutput(turnStart(t))
	assert.Empty(t, errorTexts(sink))
	a.HandleOutput(turnEnd(t, "end_turn"))

	assert.Equal(t, []string{"MCP server weather failed to connect."}, errorTexts(sink))
}

// A prompt that fails before its execution starts states its own error, and
// the display error of the same text reaches the reader once.
func TestKiroPromptThatFailsBeforeItsTurnStatesItsErrorOnce(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(displayError(t, kiroThrottleMessage))

	failPrompt(a, kiroThrottleMessage)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(turnStart(t))
	a.HandleOutput(turnEnd(t, "end_turn"))

	assert.Equal(t, []string{"prompt failed: json-rpc error -32000: " + kiroThrottleMessage}, errorTexts(sink),
		"the next prompt finds no error of the last one")
}

func TestKiroDisplayErrorOutsideAPromptReachesTheTranscriptAtOnce(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(displayError(t, "MCP server weather failed to connect."))

	assert.Equal(t, []string{"MCP server weather failed to connect."}, errorTexts(sink))
}

func TestKiroDisplayErrorOfAnAgentTurnIsNotHeld(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(turnStart(t))
	require.True(t, a.AgentTurnActive())

	a.HandleOutput(displayError(t, "The model call failed."))
	a.HandleOutput(turnEnd(t, kiroStopError))

	assert.Equal(t, []string{"The model call failed."}, errorTexts(sink), "no prompt response reports the failure of Kiro's own turn")
}

func TestKiroDisplayErrorFallsBackToItsType(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(infoUpdate(t, map[string]any{
		"kind": kiroKindDisplayError, "displayError": map[string]any{"message": "  ", "errorType": "ServiceThrottleError"},
	}))
	a.HandleOutput(infoUpdate(t, map[string]any{"kind": kiroKindDisplayError, "displayError": map[string]any{}}))

	assert.Equal(t, []string{"ServiceThrottleError"}, errorTexts(sink), "an error with no text at all states nothing")
}

func TestKiroMaxTokensStopStatesTheLimit(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)

	a.HandleOutput(turnEnd(t, kiroStopMaxTokens))

	assert.Equal(t, []string{"The response stopped at the output token limit of the model"}, statusTexts(sink))
}

func TestKiroContextUsageBroadcastsThePercentage(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	// The probe's frame: the percentage twice, and a breakdown by source.
	a.HandleOutput(infoUpdate(t, map[string]any{
		"contextUsage": map[string]any{"usagePercentage": 0.8},
		"breakdown":    map[string]any{"tools": map[string]any{"tokens": 3781, "percent": 0.4}},
		"kind":         kiroKindContextUsage, "usagePercentage": 0.8,
	}))

	value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	assert.Equal(t, map[string]any{contracts.ContextUsageFieldUsagePercent: 0.8}, value)
}

func TestKiroContextUsageReadsTheTopLevelPercentageAlone(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(infoUpdate(t, map[string]any{"kind": kiroKindContextUsage, "usagePercentage": 0}))

	value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok, "an empty context is a reading too")
	assert.Equal(t, map[string]any{contracts.ContextUsageFieldUsagePercent: 0.0}, value)
}

func TestKiroContextUsageWithoutAPercentageBroadcastsNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(infoUpdate(t, map[string]any{"kind": kiroKindContextUsage}))
	a.HandleOutput(infoUpdate(t, map[string]any{"kind": kiroKindContextUsage, "usagePercentage": -1}))

	_, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	assert.False(t, ok)
}

func TestKiroSummarizationStatesACompaction(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(infoUpdate(t, map[string]any{"kind": kiroKindSummarizationStart}))
	a.HandleOutput(infoUpdate(t, map[string]any{"kind": kiroKindSummarizationDone, "summarization": map[string]any{"status": "success"}}))
	a.HandleOutput(infoUpdate(t, map[string]any{"kind": kiroKindSummarizationFailed, "summarization": map[string]any{"status": "context_too_small"}}))
	a.HandleOutput(infoUpdate(t, map[string]any{"kind": kiroKindSummarizationFailed}))

	assert.Equal(t, 1, countNotifications(sink, contracts.NotificationTypeCompacting))
	assert.Equal(t, []string{
		"Context compacted",
		"Context compaction failed: context too small",
		"Context compaction failed",
	}, statusTexts(sink))
}

func TestKiroHookUpdatesStateTheirEnd(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	hook := func(status, output string) []byte {
		return infoUpdate(t, map[string]any{
			"kind": kiroKindHookUpdate,
			"hook": map[string]any{"name": "lint", "status": status, "output": output},
		})
	}

	a.HandleOutput(hook("running", ""))
	a.HandleOutput(hook("completed", "ok"))
	a.HandleOutput(hook("failed", "exit 1"))
	a.HandleOutput(hook("failed", ""))
	a.HandleOutput(hook("canceled", ""))
	a.HandleOutput(infoUpdate(t, map[string]any{"kind": kiroKindHookUpdate, "hook": map[string]any{"status": "completed"}}))

	assert.Equal(t, []string{
		"Hook lint completed",
		"Hook lint failed: exit 1",
		"Hook lint failed",
		"Hook lint canceled",
		"Hook a hook completed",
	}, statusTexts(sink))
}

func TestKiroRecapStatesItsText(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(infoUpdate(t, map[string]any{"kind": kiroKindRecap, "recap": map[string]any{"text": " You fixed the parser. "}}))
	a.HandleOutput(infoUpdate(t, map[string]any{"kind": kiroKindRecap, "recap": map[string]any{"text": ""}}))

	assert.Equal(t, []string{"Recap: You fixed the parser."}, statusTexts(sink))
}

func TestKiroSessionInfoOfEveryKindIsConsumed(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{kiroKindTurnCompletion, "focus_update", "a_kind_from_a_later_build"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

			a.HandleOutput(infoUpdate(t, map[string]any{"kind": kind, "promptTurnSummaries": []any{map[string]any{"usage": 0.01, "unitPlural": "credits"}}}))

			assert.Empty(t, sink.Messages(), "session info is no conversation")
			assert.Empty(t, sink.Notifications())
		})
	}
}

// A session_info_update whose Kiro metadata LeapMux cannot read is still
// consumed, and it states nothing: no row, no notice and no reading.
func TestKiroUnreadableSessionInfoStatesNothing(t *testing.T) {
	t.Parallel()
	for name, kiro := range map[string]any{
		"metadata that is not an object": "x",
		"a turn completion":              map[string]any{"kind": kiroKindTurnCompletion, "promptTurnSummaries": "x"},
		"a context usage":                map[string]any{"kind": kiroKindContextUsage, "usagePercentage": "high"},
		"a display error":                map[string]any{"kind": kiroKindDisplayError, "displayError": "x"},
		"a hook update":                  map[string]any{"kind": kiroKindHookUpdate, "hook": "x"},
		"a recap":                        map[string]any{"kind": kiroKindRecap, "recap": "x"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

			a.HandleOutput(sessionUpdate(t, kiroTestSession, map[string]any{
				"sessionUpdate": "session_info_update",
				"_meta":         map[string]any{"kiro": kiro},
			}))

			assert.Empty(t, sink.Messages(), "session info is no conversation")
			assert.Empty(t, sink.Notifications())
			_, broadcast := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
			assert.False(t, broadcast)
		})
	}
}

func TestKiroSilentKindsStateTheirReason(t *testing.T) {
	t.Parallel()
	for kind, reason := range kiroSilentKinds {
		assert.NotEmpty(t, strings.TrimSpace(reason), kind)
	}
}

// toolCall encodes one tool_call of the main session.
func toolCall(t *testing.T, toolCallID, status string) []byte {
	t.Helper()
	return sessionUpdate(t, kiroTestSession, map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": toolCallID, "title": "Running: ls", "kind": "execute", "status": status,
	})
}

// contentChunk is one piece of a running command's output.
func contentChunk(t *testing.T, sessionID, toolCallID, text string) []byte {
	t.Helper()
	return frame(t, map[string]any{
		"method": kiroToolContentChunkMethod,
		"params": map[string]any{
			"sessionId": sessionID, "toolCallId": toolCallID,
			"content": map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": text}},
		},
	})
}

// progressSource is a recording sink, of the main transcript or of a child.
type progressSource interface {
	ProgressUpdates() []agent.ProgressUpdate
}

// outputProgress returns the live output that the sink holds for one call.
func outputProgress(sink progressSource, toolCallID string) (int64, string, bool) {
	var total int64
	var tail string
	var found bool
	for _, update := range liveOutputUpdates(sink) {
		if update.ScopeID != toolCallID {
			continue
		}
		// liveOutputUpdates keeps these two operations and no other.
		if update.Operation == agent.ProgressOutputTotal {
			total, found = update.Value, true
		} else {
			tail = update.Text
		}
	}
	return total, tail, found
}

// liveOutputUpdates returns the live-output updates that the sink received.
func liveOutputUpdates(sink progressSource) []agent.ProgressUpdate {
	var out []agent.ProgressUpdate
	for _, update := range sink.ProgressUpdates() {
		if update.Operation == agent.ProgressOutputTotal || update.Operation == agent.ProgressOutputTail {
			out = append(out, update)
		}
	}
	return out
}

func TestKiroContentChunksStreamIntoTheRunningRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(toolCall(t, "run_command_t_sh", "in_progress"))

	// Each chunk is a delta, which the row joins.
	a.HandleOutput(contentChunk(t, kiroTestSession, "run_command_t_sh", "line 1\n"))
	a.HandleOutput(contentChunk(t, kiroTestSession, "run_command_t_sh", "line 2\n"))

	total, tail, found := outputProgress(sink, "run_command_t_sh")
	require.True(t, found)
	assert.Equal(t, int64(len("line 1\nline 2\n")), total)
	assert.Equal(t, "line 1\nline 2\n", tail)
}

func TestKiroContentChunkOfAnotherSessionOrAnUnknownCallStreamsNowhere(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(toolCall(t, "run_command_t_sh", "in_progress"))

	a.HandleOutput(contentChunk(t, "sess-of-a-workflow-step", "run_command_t_sh", "not ours"))
	a.HandleOutput(contentChunk(t, kiroTestSession, "never-opened", "no row"))
	a.HandleOutput(contentChunk(t, kiroTestSession, "run_command_t_sh", ""))
	a.HandleOutput(contentChunk(t, kiroTestSession, "", "no call"))
	// A piece that is not text, and a chunk that LeapMux cannot read.
	a.HandleOutput(frame(t, map[string]any{
		"method": kiroToolContentChunkMethod,
		"params": map[string]any{
			"sessionId": kiroTestSession, "toolCallId": "run_command_t_sh",
			"content": map[string]any{"type": "content", "content": map[string]any{"type": "image", "text": "not text"}},
		},
	}))
	a.HandleOutput(frame(t, map[string]any{"method": kiroToolContentChunkMethod, "params": "unreadable"}))

	assert.Empty(t, liveOutputUpdates(sink))
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	assert.Empty(t, a.output.running, "a chunk that streams nowhere keeps no output")
}

// The live output of a call is state that only the running call needs. The end
// of the call drops it, so the agent keeps no output of a call that ended.
func TestKiroEndedCallForgetsItsLiveOutput(t *testing.T) {
	t.Parallel()
	a, _, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(toolCall(t, "run_command_t_sh", "in_progress"))
	a.HandleOutput(contentChunk(t, kiroTestSession, "run_command_t_sh", "line 1\n"))
	a.stateMu.Lock()
	require.Contains(t, a.output.running, "run_command_t_sh")
	a.stateMu.Unlock()

	a.HandleOutput(sessionUpdate(t, kiroTestSession, map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "run_command_t_sh", "status": "completed",
	}))

	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	assert.NotContains(t, a.output.running, "run_command_t_sh")
}

func TestKiroContentChunkAfterTheCallEndedStreamsNowhere(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(toolCall(t, "run_command_t_sh", "in_progress"))
	a.HandleOutput(sessionUpdate(t, kiroTestSession, map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "run_command_t_sh", "status": "completed",
	}))

	a.HandleOutput(contentChunk(t, kiroTestSession, "run_command_t_sh", "late"))

	_, _, found := outputProgress(sink, "run_command_t_sh")
	assert.False(t, found)
}

func TestKiroContentChunkOfAFinishedCallStreamsNowhere(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(toolCall(t, "run_command_t_sh", "completed"))

	a.HandleOutput(contentChunk(t, kiroTestSession, "run_command_t_sh", "late"))

	_, _, found := outputProgress(sink, "run_command_t_sh")
	assert.False(t, found)
}

// A workflow step runs in a session of its own, whose transcript opens in the
// step's tab. A running command of the step streams its output into that tab,
// and none of it reaches the main transcript.
func TestKiroContentChunksOfAStepStreamIntoTheStepsTab(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(runStart(t, kiroGoalWorkflowName))
	a.HandleOutput(nodeStart(t, 0, kiroStepSession))
	step, ok := sink.BackgroundTask(kiroStepSession)
	require.True(t, ok)
	require.NotEmpty(t, step.ChildAgentID)
	a.HandleOutput(sessionUpdate(t, kiroStepSession, map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": "run_command_step", "title": "Running: make", "kind": "execute", "status": "in_progress",
	}))

	a.HandleOutput(contentChunk(t, kiroStepSession, "run_command_step", "building\n"))
	a.HandleOutput(contentChunk(t, kiroStepSession, "run_command_step", "done\n"))

	total, tail, found := outputProgress(sink.Child(step.ChildAgentID), "run_command_step")
	require.True(t, found, "the step's tab draws the output")
	assert.Equal(t, int64(len("building\ndone\n")), total)
	assert.Equal(t, "building\ndone\n", tail)
	_, _, inMain := outputProgress(sink, "run_command_step")
	assert.False(t, inMain, "the main transcript holds no row of the step")
}

func TestKiroContentChunkKeepsALimitedTail(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(toolCall(t, "run_command_t_sh", "in_progress"))
	big := strings.Repeat("x", kiroLiveOutputLimit)

	a.HandleOutput(contentChunk(t, kiroTestSession, "run_command_t_sh", big))
	a.HandleOutput(contentChunk(t, kiroTestSession, "run_command_t_sh", "end"))

	total, tail, found := outputProgress(sink, "run_command_t_sh")
	require.True(t, found)
	assert.Equal(t, int64(kiroLiveOutputLimit+3), total, "the count keeps every byte")
	assert.Len(t, tail, kiroLiveOutputLimit, "the tail is full to the limit")
	assert.True(t, strings.HasSuffix(tail, "end"), "the tail keeps the newest output")
	assert.Equal(t, []bool{false, true}, tailTruncations(sink, "run_command_t_sh"),
		"output that fills the limit loses nothing, and the next byte drops the start of the output")
}

// tailTruncations returns, for each live tail of one call in order, whether
// that tail lost the start of the output.
func tailTruncations(sink progressSource, toolCallID string) []bool {
	var out []bool
	for _, update := range liveOutputUpdates(sink) {
		if update.ScopeID == toolCallID && update.Operation == agent.ProgressOutputTail {
			out = append(out, update.Truncated)
		}
	}
	return out
}

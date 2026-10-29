package kiro

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func TestKiroNativeNotificationsKeepTheirSourceAndSession(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"refusal", "future_kind", contracts.KiroKindSummarizationCompleted, kiroKindHookUpdate} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
			updateType := "session_info_update"
			metadata := map[string]any{"kind": kind}
			if kind == "refusal" {
				updateType = "agent_message_chunk"
				metadata = map[string]any{"refusal": map[string]any{"category": "CYBER", "explanation": "The native refusal."}}
			}
			if kind == kiroKindHookUpdate {
				metadata["hook"] = map[string]any{"status": "failed", "output": "The native hook failure."}
			}
			update := map[string]any{"sessionUpdate": updateType, "_meta": map[string]any{"kiro": metadata}}
			a.HandleOutput(notification(t, "session/update", map[string]any{"sessionId": kiroTestSession, "update": update}))
			rows := sink.PersistedNotifications()
			require.Len(t, rows, 1)
			assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, rows[0].Source)
			assert.Equal(t, kiroTestSession, rows[0].AgentSessionID)
			expected, err := json.Marshal(update)
			require.NoError(t, err)
			assert.JSONEq(t, string(expected), string(rows[0].Content))
		})
	}
}

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
	// A chunk that states no answer continues the one before it: the kiro
	// block is present, but it states no replayId.
	a.HandleOutput(messageChunk(t, "three.", map[string]any{}))
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
	assert.Empty(t, agenttest.AgentErrorTexts(sink.Notifications()), "the error waits for the end of the turn")
	a.HandleOutput(turnEnd(t, kiroStopError))
	assert.Empty(t, agenttest.AgentErrorTexts(sink.Notifications()), "the error waits for the end of the prompt")

	failPrompt(a, kiroThrottleMessage)
	assert.Equal(t, []string{"prompt failed: json-rpc error -32000: " + kiroThrottleMessage}, agenttest.AgentErrorTexts(sink.Notifications()),
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

	assert.Equal(t, []string{kiroThrottleMessage, "prompt failed: json-rpc error -32000: Internal error"}, agenttest.AgentErrorTexts(sink.Notifications()))
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

	assert.Equal(t, []string{kiroThrottleMessage}, agenttest.AgentErrorTexts(sink.Notifications()))
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

	assert.Equal(t, []string{kiroThrottleMessage}, agenttest.AgentErrorTexts(sink.Notifications()))
}

func TestKiroDisplayErrorOfAPromptThatEndedWellReachesTheTranscript(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(turnStart(t))

	a.HandleOutput(displayError(t, "MCP server weather needs authorization."))
	a.HandleOutput(turnEnd(t, "end_turn"))

	assert.Equal(t, []string{"MCP server weather needs authorization."}, agenttest.AgentErrorTexts(sink.Notifications()))
}

func TestKiroSecondDisplayErrorOfAPromptReleasesTheFirst(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)

	a.HandleOutput(displayError(t, "first"))
	a.HandleOutput(displayError(t, "second"))
	assert.Equal(t, []string{"first"}, agenttest.AgentErrorTexts(sink.Notifications()), "only the last error can be the prompt's failure")

	a.HandleOutput(turnEnd(t, kiroStopError))
	assert.Equal(t, []string{"first"}, agenttest.AgentErrorTexts(sink.Notifications()))
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
	assert.Empty(t, agenttest.AgentErrorTexts(sink.Notifications()))
	a.HandleOutput(turnEnd(t, "end_turn"))

	assert.Equal(t, []string{"MCP server weather failed to connect."}, agenttest.AgentErrorTexts(sink.Notifications()))
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

	assert.Equal(t, []string{"prompt failed: json-rpc error -32000: " + kiroThrottleMessage}, agenttest.AgentErrorTexts(sink.Notifications()),
		"the next prompt finds no error of the last one")
}

func TestKiroDisplayErrorOutsideAPromptReachesTheTranscriptAtOnce(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(displayError(t, "MCP server weather failed to connect."))

	assert.Equal(t, []string{"MCP server weather failed to connect."}, agenttest.AgentErrorTexts(sink.Notifications()))
}

func TestKiroDisplayErrorOfAnAgentTurnIsNotHeld(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(turnStart(t))
	require.True(t, a.AgentTurnActive())

	a.HandleOutput(displayError(t, "The model call failed."))
	a.HandleOutput(turnEnd(t, kiroStopError))

	assert.Equal(t, []string{"The model call failed."}, agenttest.AgentErrorTexts(sink.Notifications()), "no prompt response reports the failure of Kiro's own turn")
}

func TestKiroDisplayErrorFallsBackToItsType(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(infoUpdate(t, map[string]any{
		"kind": kiroKindDisplayError, "displayError": map[string]any{"message": "  ", "errorType": "ServiceThrottleError"},
	}))
	a.HandleOutput(infoUpdate(t, map[string]any{"kind": kiroKindDisplayError, "displayError": map[string]any{}}))

	assert.Equal(t, []string{"ServiceThrottleError"}, agenttest.AgentErrorTexts(sink.Notifications()), "an error with no text at all states nothing")
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

	a.HandleOutput(infoUpdate(t, map[string]any{"kind": kiroKindContextUsage, "usagePercentage": 0.8}))

	// No breakdown states no token count, so the meter keeps the percentage
	// alone.
	assert.Equal(t, map[string]any{contracts.ContextUsageFieldUsagePercent: 0.8},
		sink.LastSessionInfo()[contracts.SessionInfoKeyContextUsage])
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
	a.HandleOutput(infoUpdate(t, map[string]any{"kind": contracts.KiroKindSummarizationCompleted, "summarization": map[string]any{"status": "success"}}))
	a.HandleOutput(infoUpdate(t, map[string]any{"kind": contracts.KiroKindSummarizationFailed, "summarization": map[string]any{"status": "context_too_small"}}))
	a.HandleOutput(infoUpdate(t, map[string]any{"kind": contracts.KiroKindSummarizationFailed}))

	// The start is live state with no row; each end stores its native frame
	// and ends the live key.
	assert.Equal(t, []interface{}{
		map[string]interface{}{contracts.CompactionFieldTrigger: "auto"},
		nil, nil, nil,
	}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
	rows := sink.PersistedNotifications()
	require.Len(t, rows, 3)
	assert.Contains(t, string(rows[0].Content), "summarization_completed")
	assert.Contains(t, string(rows[1].Content), "context_too_small")
	assert.Contains(t, string(rows[2].Content), "summarization_failed")
	assert.Empty(t, sink.Notifications(), "no worker status row")
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

	// A v2 completed hook, a running hook, and a hook with no name state
	// nothing: a v2 failure looks the same as a success.
	a.HandleOutput(hook("running", ""))
	a.HandleOutput(hook("completed", "ok"))
	a.HandleOutput(infoUpdate(t, map[string]any{"kind": kiroKindHookUpdate, "hook": map[string]any{"status": "completed"}}))
	assert.Empty(t, sink.PersistedNotifications(), "a v2 completed hook states nothing")

	// A v1 failure and cancellation persist their native frames.
	a.HandleOutput(hook("failed", "exit 1"))
	a.HandleOutput(hook("canceled", ""))
	rows := sink.PersistedNotifications()
	require.Len(t, rows, 2)
	assert.Contains(t, string(rows[0].Content), "exit 1")
	assert.Contains(t, string(rows[1].Content), "canceled")
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

			update := infoUpdateObject(map[string]any{"kind": kind, "promptTurnSummaries": []any{map[string]any{"usage": 0.01, "unitPlural": "credits"}}})
			a.HandleOutput(sessionUpdate(t, kiroTestSession, update))

			assert.Empty(t, sink.Messages(), "session info is no conversation")
			assert.Empty(t, sink.Notifications())
			rows := sink.PersistedNotifications()
			if kind == "a_kind_from_a_later_build" {
				require.Len(t, rows, 1)
				original, err := json.Marshal(update)
				require.NoError(t, err)
				assert.Equal(t, original, rows[0].Content)
				assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, rows[0].Source)
				assert.Equal(t, kiroTestSession, rows[0].AgentSessionID)
			} else {
				assert.Empty(t, rows)
			}
		})
	}
}

// Malformed known payloads publish no reading or notification. Unreadable Kiro
// metadata remains a native notification because its kind cannot be identified.
func TestKiroMalformedSessionInfoPreservesUnknownMetadata(t *testing.T) {
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

			update := map[string]any{
				"sessionUpdate": "session_info_update",
				"_meta":         map[string]any{"kiro": kiro},
			}
			a.HandleOutput(sessionUpdate(t, kiroTestSession, update))

			assert.Empty(t, sink.Messages(), "session info is no conversation")
			assert.Empty(t, sink.Notifications())
			rows := sink.PersistedNotifications()
			if name == "metadata that is not an object" {
				require.Len(t, rows, 1)
				original, err := json.Marshal(update)
				require.NoError(t, err)
				assert.Equal(t, original, rows[0].Content)
				assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, rows[0].Source)
				assert.Equal(t, kiroTestSession, rows[0].AgentSessionID)
			} else {
				assert.Empty(t, rows)
			}
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

// A content-filter refusal rides a text chunk of its own: the model's answer
// keeps its text, and the refusal frame becomes its own row. Frames 31-32 of
// the round-2 refusal probe.
func TestKiroRefusalIsItsOwnRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)

	a.HandleOutput(frame(t, map[string]any{
		"method": "session/update",
		"params": map[string]any{
			"sessionId": kiroTestSession,
			"update": map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"content":       map[string]any{"type": "text", "text": "Partial answer."},
				"_meta":         map[string]any{"kiro": map[string]any{"replayId": "probe-say"}},
			},
		},
	}))
	refusal := frame(t, map[string]any{
		"method": "session/update",
		"params": map[string]any{
			"sessionId": kiroTestSession,
			"update": map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"content":       map[string]any{"type": "text", "text": "Probe refusal explanation."},
				"_meta": map[string]any{"kiro": map[string]any{"refusal": map[string]any{
					"category": "CYBER", "explanation": "Probe refusal explanation.", "recommendedModel": "kiro-probe-alt",
				}}},
			},
		},
	})
	a.HandleOutput(refusal)
	a.FinishPromptRequestForTest(kiroTestSession, json.RawMessage(`{"stopReason":"content_filtered"}`), nil)

	texts := kiroAssembledTexts(t, sink)
	require.Len(t, texts, 1, "the refusal leaves the model's own text alone")
	assert.Equal(t, "Partial answer.", texts[0])
	rows := sink.PersistedNotifications()
	require.Len(t, rows, 1, "the refusal chunk persists its native frame")
	assert.Contains(t, string(rows[0].Content), "CYBER")
}

// hooks-1 frames 28-29: a v2 completed hook states nothing a reader can use.
// A v2 failure looks the same as a success, so neither reaches a row.
func TestKiroHookCompletedWritesNoRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	for _, name := range []string{"probe-ok", "probe-fail"} {
		a.HandleOutput(frame(t, map[string]any{
			"method": "session/update",
			"params": map[string]any{
				"sessionId": kiroTestSession,
				"update": map[string]any{
					"sessionUpdate": "session_info_update",
					"_meta": map[string]any{"kiro": map[string]any{
						"kind": "hook_update",
						"hook": map[string]any{"name": name, "status": "completed", "actionType": "runCommand"},
					}},
				},
			},
		}))
	}
	assert.Empty(t, sink.Notifications(), "a v2 completed hook states nothing")
}

// A v1 hook failure carries its command result, and the native frame becomes
// the row the hook entry renders.
func TestKiroHookFailurePersistsNative(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	failed := frame(t, map[string]any{
		"method": "session/update",
		"params": map[string]any{
			"sessionId": kiroTestSession,
			"update": map[string]any{
				"sessionUpdate": "session_info_update",
				"_meta": map[string]any{"kiro": map[string]any{
					"kind": "hook_update",
					"hook": map[string]any{
						"name": "probe", "status": "failed", "command": "echo probe",
						"result": map[string]any{"output": "boom", "exitCode": 1},
					},
				}},
			},
		},
	})
	a.HandleOutput(failed)
	rows := sink.PersistedNotifications()
	require.Len(t, rows, 1, "the failed hook persists its native frame")
	assert.Contains(t, string(rows[0].Content), "boom")
	// A canceled hook persists its native frame too.
	canceled := frame(t, map[string]any{
		"method": "session/update",
		"params": map[string]any{
			"sessionId": kiroTestSession,
			"update": map[string]any{
				"sessionUpdate": "session_info_update",
				"_meta": map[string]any{"kiro": map[string]any{
					"kind": "hook_update",
					"hook": map[string]any{"name": "probe", "status": "canceled", "command": "echo probe"},
				}},
			},
		},
	})
	a.HandleOutput(canceled)
	rows = sink.PersistedNotifications()
	require.Len(t, rows, 2)
}

// protocol-1 frame 16: the meter sums the breakdown's categories and keeps
// the percentage. The window arrives only when the current model states one.
func TestKiroContextUsageSumsTheBreakdown(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(frame(t, map[string]any{
		"method": "session/update",
		"params": map[string]any{
			"sessionId": kiroTestSession,
			"update": map[string]any{
				"sessionUpdate": "session_info_update",
				"_meta": map[string]any{"kiro": map[string]any{
					"kind": "context_usage", "usagePercentage": 4.4,
					"contextUsage": map[string]any{"usagePercentage": 4.4},
					"breakdown": map[string]any{
						"contextFiles":  map[string]any{"tokens": 0},
						"tools":         map[string]any{"tokens": 4335},
						"kiroResponses": map[string]any{"tokens": 0},
						"yourPrompts":   map[string]any{"tokens": 4326},
						"sessionFiles":  map[string]any{"tokens": 0},
					},
				}},
			},
		},
	}))
	usage, ok := sink.LastSessionInfo()[contracts.SessionInfoKeyContextUsage].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, 8661.0, usage[contracts.ContextUsageFieldContextTokens], "contextFiles+tools+kiroResponses+yourPrompts+sessionFiles")
	assert.Equal(t, 4.4, usage[contracts.ContextUsageFieldUsagePercent])
	_, hasWindow := usage[contracts.ContextUsageFieldContextWindow]
	assert.False(t, hasWindow, "no window until the model states one")
}

// throttle-1 frames 29-34: a failed prompt writes its error, then the native
// turn end as the divider.
func TestKiroFailedPromptDivider(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(frame(t, map[string]any{
		"method": "session/update",
		"params": map[string]any{
			"sessionId": kiroTestSession,
			"update": map[string]any{
				"sessionUpdate": "session_info_update",
				"_meta": map[string]any{"kiro": map[string]any{
					"kind": "display_error",
					"displayError": map[string]any{
						"message": "Kiro Probe is experiencing high traffic.", "errorType": "ModelThrottleError",
					},
				}},
			},
		},
	}))
	endFrame := json.RawMessage(`{"sessionUpdate":"session_info_update","_meta":{"kiro":{"kind":"turn_end","stopReason":"error","messageId":"probe-turn-end"}}}`)
	a.HandleOutput(frame(t, map[string]any{
		"method": "session/update",
		"params": map[string]any{
			"sessionId": kiroTestSession,
			"update":    json.RawMessage(endFrame),
		},
	}))
	a.FinishPromptRequestForTest(kiroTestSession, nil, fmt.Errorf("throttled"))

	notifications := sink.Notifications()
	require.NotEmpty(t, notifications, "the error row lands")
	assert.Equal(t, "Kiro Probe is experiencing high traffic.", notifications[0][contracts.NotificationFieldError])
	messages := sink.Messages()
	ended := false
	for _, message := range messages {
		if message.TurnEnd {
			ended = true
			assert.Contains(t, string(message.Content), "turn_end", "the native turn end is the divider")
		}
	}
	assert.True(t, ended, "the failed prompt writes the native turn end divider")
}

// An aborted execution's answer arrives as a plain chunk with no _meta.kiro:
// Kiro dropped the execution, so its text reaches no reader.
func TestKiroAbortTextReachesNoTranscript(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)

	a.HandleOutput(frame(t, map[string]any{
		"method": "session/update",
		"params": map[string]any{
			"sessionId": kiroTestSession,
			"update": map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"content":       map[string]any{"type": "text", "text": "text of the aborted execution"},
			},
		},
	}))
	a.FinishPromptRequestForTest(kiroTestSession, json.RawMessage(`{"stopReason":"end_turn"}`), nil)

	assert.Empty(t, kiroAssembledTexts(t, sink), "the aborted execution's text reaches no transcript")
}

// The JSON-RPC errors that Kiro CLI 2.24.0 answered to a failed prompt
// (R/cursor-kiro-grok.md and .tmp/probe/kiro/run-*.summary), verbatim.
const (
	kiroMonthlyLimitResponse    = `{"jsonrpc":"2.0","id":3,"error":{"code":-32000,"message":"You've reached your monthly usage limit. Please return next month to continue building. (Request ID: probe-request-1)","data":{"errorType":"UsageLimitReachedError","retryErrorType":"CLIENT_ERROR","requestId":"probe-request-1","actions":["upgradePlan"]}}}`
	kiroDailyLimitResponse      = `{"jsonrpc":"2.0","id":3,"error":{"code":-32000,"message":"You've reached your daily usage limit. Please return tomorrow to continue building. (Request ID: probe-request-1)","data":{"errorType":"UsageLimitReachedError","retryErrorType":"CLIENT_ERROR","requestId":"probe-request-1","actions":["viewUsage"]}}}`
	kiroOverageLimitResponse    = `{"jsonrpc":"2.0","id":3,"error":{"code":-32000,"message":"You've reached your overage limit. (Request ID: probe-request-1)","data":{"errorType":"OverageLimitReachedError","retryErrorType":"CLIENT_ERROR","requestId":"probe-request-1","actions":["viewUsage"]}}}`
	kiroInternalServerResponse  = `{"jsonrpc":"2.0","id":3,"error":{"code":-32000,"message":"The model service returned a temporary error. Please try again. (Request ID: probe-request-3)","data":{"errorType":"InternalServerError","retryErrorType":"TRANSIENT","requestId":"probe-request-3"}}}`
	kiroAccessDeniedResponse    = `{"jsonrpc":"2.0","id":3,"error":{"code":-32000,"message":"Access denied. Please check your authentication. (Request ID: probe-request-2)","data":{"errorType":"AccessDeniedError","retryErrorType":"CLIENT_ERROR","requestId":"probe-request-2"}}}`
	kiroServiceThrottleResponse = `{"jsonrpc":"2.0","id":3,"error":{"code":-32000,"message":"Too many requests, please wait before trying again. (Request ID: probe-request-3)","data":{"errorType":"ServiceThrottleError","retryErrorType":"THROTTLING","requestId":"probe-request-3"}}}`
	kiroModelThrottleResponse   = `{"jsonrpc":"2.0","id":3,"error":{"code":-32000,"message":"Kiro Probe is experiencing high traffic. Try again, or select another model. (Request ID: probe-request-3)","data":{"errorType":"ModelThrottleError","retryErrorType":"THROTTLING","requestId":"probe-request-3"}}}`
)

// kiroTranslatedResponse is a failure that no probe provoked, in the shape of
// the probed errors, with the message and the classes that Kiro's error
// translator states for errorType (.tmp/probe/kiro/errtranslator.js).
func kiroTranslatedResponse(message, errorType, retryErrorType string) string {
	data, _ := json.Marshal(map[string]any{
		"code": -32000, "message": message + " (Request ID: probe-request-1)",
		"data": map[string]any{"errorType": errorType, "retryErrorType": retryErrorType, "requestId": "probe-request-1"},
	})
	return `{"jsonrpc":"2.0","id":3,"error":` + string(data) + `}`
}

// kiroResponseError reads the error of one JSON-RPC response line, as the base
// hands it to Hooks.PromptEnded.
func kiroResponseError(t *testing.T, line string) *providerkit.JSONRPCResponseError {
	t.Helper()
	var response struct {
		Error struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(line), &response))
	return &providerkit.JSONRPCResponseError{Code: response.Error.Code, Message: response.Error.Message, Data: response.Error.Data}
}

// kiroResponseErrorObject returns the error member of one response line,
// verbatim.
func kiroResponseErrorObject(t *testing.T, line string) json.RawMessage {
	t.Helper()
	var response struct {
		Error json.RawMessage `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(line), &response))
	return response.Error
}

func TestKiroTurnFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		response   string
		wantKind   providerkit.TurnFailureKind
		wantWindow providerkit.UsageWindow
	}{
		{name: "the monthly usage limit states a window longer than a week", response: kiroMonthlyLimitResponse, wantKind: providerkit.TurnFailureUsageLimited, wantWindow: providerkit.UsageWindowLonger},
		{name: "the daily usage limit states a window within a week", response: kiroDailyLimitResponse, wantKind: providerkit.TurnFailureUsageLimited, wantWindow: providerkit.UsageWindowWeekOrLess},
		{
			name:     "the hourly usage limit states a window within a week",
			response: kiroTranslatedResponse("You've reached your hourly usage limit. Please return soon to continue building.", "UsageLimitReachedError", "CLIENT_ERROR"),
			wantKind: providerkit.TurnFailureUsageLimited, wantWindow: providerkit.UsageWindowWeekOrLess,
		},
		{
			name:     "the weekly usage limit states a window within a week",
			response: kiroTranslatedResponse("You've reached your weekly usage limit. Please return next week to continue building.", "UsageLimitReachedError", "CLIENT_ERROR"),
			wantKind: providerkit.TurnFailureUsageLimited, wantWindow: providerkit.UsageWindowWeekOrLess,
		},
		{
			name:     "a usage limit of no named window states none",
			response: kiroTranslatedResponse("You've reached your usage limit. Thanks for using Kiro - please return soon to continue building.", "UsageLimitReachedError", "CLIENT_ERROR"),
			wantKind: providerkit.TurnFailureUsageLimited, wantWindow: providerkit.UsageWindowUnstated,
		},
		{name: "the overage limit states no window", response: kiroOverageLimitResponse, wantKind: providerkit.TurnFailureUsageLimited, wantWindow: providerkit.UsageWindowUnstated},
		{name: "an internal server error is transient", response: kiroInternalServerResponse, wantKind: providerkit.TurnFailureTransient},
		{name: "a throttled service is a rate limit", response: kiroServiceThrottleResponse, wantKind: providerkit.TurnFailureRateLimited},
		{name: "a throttled model is a rate limit", response: kiroModelThrottleResponse, wantKind: providerkit.TurnFailureRateLimited},
		{name: "a denied access is not retryable", response: kiroAccessDeniedResponse, wantKind: providerkit.TurnFailureNotRetryable},
		{
			name:     "a throttled client is a rate limit",
			response: kiroTranslatedResponse("Too many requests, please wait before trying again.", "ClientThrottleError", "THROTTLING"),
			wantKind: providerkit.TurnFailureRateLimited,
		},
		{
			name:     "a reset connection is transient",
			response: kiroTranslatedResponse("Your connection was interrupted. Please try again in a moment.", "ServerConnectionResetError", "TRANSIENT"),
			wantKind: providerkit.TurnFailureTransient,
		},
		{
			name:     "a server error class is transient",
			response: kiroTranslatedResponse("The model service returned an error.", "ServiceUnavailableError", "SERVER_ERROR"),
			wantKind: providerkit.TurnFailureTransient,
		},
		{
			// Kiro labels it TRANSIENT, but only the proxy settings lift it.
			name:     "a proxy that wants credentials is not retryable",
			response: kiroTranslatedResponse("Your network proxy refused the connection because it requires authentication (HTTP 407). Check your proxy settings or contact your network administrator.", "ProxyAuthenticationRequiredError", "TRANSIENT"),
			wantKind: providerkit.TurnFailureNotRetryable,
		},
		{
			name:     "an unsupported account is a billing block",
			response: kiroTranslatedResponse("This feature is not available for your account.", "AccountNotSupportedError", "CLIENT_ERROR"),
			wantKind: providerkit.TurnFailureBillingBlocked,
		},
		{
			name:     "paused access of a new user is a billing block",
			response: kiroTranslatedResponse("Kiro access not available for this account.", "NewUserAccessPausedError", "CLIENT_ERROR"),
			wantKind: providerkit.TurnFailureBillingBlocked,
		},
		{
			name:     "a suspended account is a billing block",
			response: kiroTranslatedResponse("Your account has been temporarily suspended.", "TemporarilySuspendedError", "CLIENT_ERROR"),
			wantKind: providerkit.TurnFailureBillingBlocked,
		},
		{
			name:     "an internal error with details only is not retryable",
			response: `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Internal error","data":{"details":"Cannot read properties of undefined"}}}`,
			wantKind: providerkit.TurnFailureNotRetryable,
		},
		{
			name:     "an error with no data is not retryable",
			response: `{"jsonrpc":"2.0","id":3,"error":{"code":-32000,"message":"Authentication required"}}`,
			wantKind: providerkit.TurnFailureNotRetryable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			failure, payload := kiroTurnFailure(kiroResponseError(t, tc.response), false)
			assert.Equal(t, tc.wantKind, failure.Kind)
			assert.Equal(t, tc.wantWindow, failure.Window)
			assert.True(t, failure.ResetAt.IsZero(), "Kiro states no reset instant")
			if failure.Kind != providerkit.TurnFailureNotRetryable {
				assert.NotEmpty(t, failure.Cause)
			}
			assert.JSONEq(t, string(kiroResponseErrorObject(t, tc.response)), string(payload), "the payload is the wire's error")
		})
	}

	notRetryable := providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
	t.Run("a stop is not retryable", func(t *testing.T) {
		t.Parallel()
		failure, payload := kiroTurnFailure(kiroResponseError(t, kiroInternalServerResponse), true)
		assert.Equal(t, notRetryable, failure)
		assert.Nil(t, payload)
	})
	t.Run("a success is not retryable", func(t *testing.T) {
		t.Parallel()
		failure, payload := kiroTurnFailure(nil, false)
		assert.Equal(t, notRetryable, failure)
		assert.Nil(t, payload)
	})
	t.Run("a failure with no JSON-RPC error is not retryable", func(t *testing.T) {
		t.Parallel()
		failure, payload := kiroTurnFailure(fmt.Errorf("throttled"), false)
		assert.Equal(t, notRetryable, failure)
		assert.Nil(t, payload)
	})
	t.Run("a wrapped JSON-RPC error is read", func(t *testing.T) {
		t.Parallel()
		failure, _ := kiroTurnFailure(fmt.Errorf("session/prompt: %w", kiroResponseError(t, kiroInternalServerResponse)), false)
		assert.Equal(t, providerkit.TurnFailureTransient, failure.Kind)
	})
}

// newKiroRetryAgent builds a Kiro agent with its own hooks, whose transcript
// is a recording sink that keeps the order of every row and retry operation.
func newKiroRetryAgent(t *testing.T) (*Agent, *agenttest.Sink) {
	t.Helper()
	a, _, _ := newKiroAgent(t, agent.Options{}, nil)
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	return a, sink
}

func kiroTimelineOps(timeline []agenttest.TimelineEntry) []string {
	ops := make([]string, 0, len(timeline))
	for _, entry := range timeline {
		ops = append(ops, entry.Op)
	}
	return ops
}

// kiroProbedDisplayError is the display error that Kiro states before the
// error of a failed prompt, with the message and the classes of that error, as
// the probe recorded it (.tmp/probe/kiro/run-daily.summary).
func kiroProbedDisplayError(t *testing.T, response string) []byte {
	t.Helper()
	rpcErr := kiroResponseError(t, response)
	var data struct {
		ErrorType      string `json:"errorType"`
		RetryErrorType string `json:"retryErrorType"`
	}
	require.NoError(t, json.Unmarshal(rpcErr.Data, &data))
	return infoUpdate(t, map[string]any{
		"displayError": map[string]any{"message": rpcErr.Message, "errorType": data.ErrorType, "retryErrorType": data.RetryErrorType},
		"kind":         kiroKindDisplayError, "message": rpcErr.Message, "errorType": data.ErrorType, "retryErrorType": data.RetryErrorType,
	})
}

// failKiroPrompt plays a failed prompt in the probed order: the turn starts,
// Kiro states the display error, the turn ends with the stop reason error,
// and the prompt answers response.
func failKiroPrompt(t *testing.T, a *Agent, display []byte, response string) {
	t.Helper()
	a.SetPromptActiveForTest(true)
	a.HandleOutput(turnStart(t))
	a.HandleOutput(display)
	a.HandleOutput(turnEnd(t, kiroStopError))
	a.FinishPromptRequestForTest(kiroTestSession, nil, kiroResponseError(t, response))
}

// The held display error states the same text as the prompt's error, so the
// reader reads the reason once. The native error turn end is the divider, and
// the verdict follows the divider.
func TestKiroFailedPromptStatesItsReasonOnceAndAppliesTheVerdict(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		response string
		wantOps  []string
	}{
		{name: "a daily limit schedules a server-error retry", response: kiroDailyLimitResponse, wantOps: []string{"leapmux_notification", "turn_end", "schedule_server_error"}},
		{
			name:     "an hourly limit schedules a server-error retry",
			response: kiroTranslatedResponse("You've reached your hourly usage limit. Please return soon to continue building.", "UsageLimitReachedError", "CLIENT_ERROR"),
			wantOps:  []string{"leapmux_notification", "turn_end", "schedule_server_error"},
		},
		{name: "a throttled service schedules a server-error retry", response: kiroServiceThrottleResponse, wantOps: []string{"leapmux_notification", "turn_end", "schedule_server_error"}},
		{name: "an internal server error schedules a server-error retry", response: kiroInternalServerResponse, wantOps: []string{"leapmux_notification", "turn_end", "schedule_server_error"}},
		{name: "a denied access cancels the server-error retry", response: kiroAccessDeniedResponse, wantOps: []string{"leapmux_notification", "turn_end", "cancel_server_error"}},
		{
			name:     "a proxy that wants credentials cancels the server-error retry",
			response: kiroTranslatedResponse("Your network proxy refused the connection because it requires authentication (HTTP 407). Check your proxy settings or contact your network administrator.", "ProxyAuthenticationRequiredError", "TRANSIENT"),
			wantOps:  []string{"leapmux_notification", "turn_end", "cancel_server_error"},
		},
		{
			name:     "a suspended account cancels both retries",
			response: kiroTranslatedResponse("Your account has been temporarily suspended.", "TemporarilySuspendedError", "CLIENT_ERROR"),
			wantOps:  []string{"leapmux_notification", "turn_end", "cancel_usage_limit", "cancel_server_error"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := newKiroRetryAgent(t)

			failKiroPrompt(t, a, kiroProbedDisplayError(t, tc.response), tc.response)

			require.Equal(t, tc.wantOps, kiroTimelineOps(sink.Timeline()))
			rpcErr := kiroResponseError(t, tc.response)
			assert.Equal(t, []string{"prompt failed: " + rpcErr.Error()}, agenttest.AgentErrorTexts(sink.LeapMuxNotifications()),
				"the prompt's own error states the display error's text, so the reader reads it once")
			messages := sink.Messages()
			require.NotEmpty(t, messages)
			divider := messages[len(messages)-1]
			assert.True(t, divider.TurnEnd, "the failed prompt ends with a divider")
			nativeEnd, err := json.Marshal(turnEndObject(kiroStopError))
			require.NoError(t, err)
			assert.JSONEq(t, string(nativeEnd), string(divider.Content), "the native error turn end is the divider")
			if tc.wantOps[len(tc.wantOps)-1] == "schedule_server_error" {
				assert.JSONEq(t, string(kiroResponseErrorObject(t, tc.response)), string(sink.LastAutoSchedule().SourcePayload))
			}
		})
	}
}

// The monthly limit lifts only next month, so the turn schedules no retry of
// either kind. Its one call ends the backoff of a server-error retry.
func TestKiroMonthlyLimitSchedulesNoRetry(t *testing.T) {
	t.Parallel()
	a, sink := newKiroRetryAgent(t)

	failKiroPrompt(t, a, kiroProbedDisplayError(t, kiroMonthlyLimitResponse), kiroMonthlyLimitResponse)

	timeline := sink.Timeline()
	require.Equal(t, []string{"leapmux_notification", "turn_end", "cancel_server_error"}, kiroTimelineOps(timeline))
	assert.Equal(t, "the turn ended on a usage limit that states no reset within a week", timeline[2].Reason)
	assert.Zero(t, sink.AutoScheduleCount())
}

// A display error of another text stays the only record of its reason, the
// native error turn end is the divider, and the verdict follows the divider.
func TestKiroFailedPromptWithAnotherDisplayErrorAppliesTheVerdictAfterTheDivider(t *testing.T) {
	t.Parallel()
	a, sink := newKiroRetryAgent(t)

	failKiroPrompt(t, a, displayError(t, "MCP server weather failed to connect."), kiroInternalServerResponse)

	require.Equal(t, []string{"leapmux_notification", "leapmux_notification", "turn_end", "schedule_server_error"}, kiroTimelineOps(sink.Timeline()))
	assert.Equal(t, []string{"MCP server weather failed to connect.", "prompt failed: " + kiroResponseError(t, kiroInternalServerResponse).Error()},
		agenttest.AgentErrorTexts(sink.LeapMuxNotifications()))
}

// A stopped prompt states its held display error, and cancels the
// server-error retry.
func TestKiroStoppedPromptCancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()
	a, sink := newKiroRetryAgent(t)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(kiroProbedDisplayError(t, kiroInternalServerResponse))
	a.NoteInterruptRequestedForTest()

	a.FinishPromptRequestForTest(kiroTestSession, nil, kiroResponseError(t, kiroInternalServerResponse))

	require.Equal(t, []string{"leapmux_notification", "cancel_server_error"}, kiroTimelineOps(sink.Timeline()))
	assert.Equal(t, providerkit.TurnEndedWithoutRetryableFailure, sink.LastAutoCancelReason())
}

// A turn that Kiro started by itself ends at its own turn_end marker, and the
// default verdict cancels the server-error retry after that row.
func TestKiroAgentTurnEndCancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()
	a, sink := newKiroRetryAgent(t)
	a.HandleOutput(turnStart(t))
	require.True(t, a.AgentTurnActive())

	a.HandleOutput(turnEnd(t, kiroStopError))

	require.Equal(t, []string{"turn_end", "cancel_server_error"}, kiroTimelineOps(sink.Timeline()))
	assert.Equal(t, providerkit.TurnEndedWithoutRetryableFailure, sink.LastAutoCancelReason())
}

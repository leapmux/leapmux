package pi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPiControlPublicationFailureCancelsEachDialog(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"select", "input", "confirm", "editor"} {
		t.Run(method, func(t *testing.T) {
			var output bytes.Buffer
			sink := &agenttest.ControlSink{PublicationError: errors.New("storage unavailable")}
			a := newPiAgentWithSink(agent.NewProviderServices(sink))
			a.SetStdinForTest(agenttest.NopStdin(&output))
			a.handlePiExtensionUIRequest([]byte(`{"type":"extension_ui_request","id":"dialog-1","method":"` + method + `","title":"Choose"}`))
			assert.JSONEq(t, `{"type":"extension_ui_response","id":"dialog-1","cancelled":true}`, output.String())
			assert.Empty(t, sink.PublishedControls())
		})
	}
}

func TestPiQuestionControlKeepsItsOriginalAndLinksTheToolRequest(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	tool := []byte(`{"type":"tool_execution_start","toolCallId":"question-tool","toolName":"ask_user_question","args":{"questions":[{"question":"Choose a layout","header":"Layout","options":[{"label":"Compact","description":"Small","preview":"complete preview"},{"label":"Wide","description":"Large"}]}]}}`)
	handlePiOutput(a, providerkit.ParseLine(tool))
	dialog := []byte(` {"type":"extension_ui_request","id":"dialog","method":"select","title":"[Layout] Choose a layout\n\n--- 1. Compact preview ---\ncomplete preview","options":["1. Compact — Small","2. Wide — Large","3. Type something."]} `)
	handlePiOutput(a, providerkit.ParseLine(dialog))
	require.Len(t, sink.PublishedControls(), 1)
	request := sink.LastPublishedControl()
	assert.Equal(t, dialog, request.Payload)
	assert.Equal(t, int64(1), request.SourceSeq)
	assert.Equal(t, tool, sink.Messages()[0].Content)
}

func TestPiMCPPermissionLinksItsToolRequest(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	tool := []byte(`{"type":"tool_execution_start","toolCallId":"mcp-call","toolName":"mcp","args":{"tool":"probe_write","args":{"path":"sample.py","content":"complete arguments"}}}`)
	handlePiOutput(a, providerkit.ParseLine(tool))
	dialog := []byte(`{"type":"extension_ui_request","id":"permission","method":"select","title":"MCP: probe wants to run write\n\nArguments:\n{ \"path\": \"sample.py\", \"content\": \"complete arguments\" }","options":["Allow once","Allow for session","Deny"]}`)
	handlePiOutput(a, providerkit.ParseLine(dialog))
	require.Len(t, sink.PublishedControls(), 1)
	assert.Equal(t, int64(1), sink.LastPublishedControl().SourceSeq)
	assert.Equal(t, dialog, sink.LastPublishedControl().Payload)
}

func newPiAgentWithSink(sink agent.ProviderServices) *Agent {
	a := &Agent{
		Process:     providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "test-agent"}),
		sink:        sink,
		sessionFile: "/tmp/pi-session.jsonl",
	}
	a.sink = agent.NewModelProgressResetSink(newPiToolTranscript(context.Background(), a.sink))
	return a
}

func TestHandlePiOutput_AgentStart_SetsTurnFlag(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_start"}`)))

	a.Mu.Lock()
	turnActive := a.currentTurnActive
	a.Mu.Unlock()
	assert.True(t, turnActive, "agent_start should mark turn active")

	statusActiveCount := sink.StatusActiveCount()
	assert.Equal(t, 0, statusActiveCount, "agent_start must NOT re-broadcast full status — that's a startup-only call")
}

func TestHandlePiOutput_AgentEnd_PersistsResultDividerAndResets(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.currentTurnActive = true
	a.TurnToolUses = 3

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","messages":[]}`)))

	a.Mu.Lock()
	turnActive := a.currentTurnActive
	toolUses := a.TurnToolUses
	a.Mu.Unlock()
	assert.False(t, turnActive)
	assert.Equal(t, 0, toolUses)

	require.Equal(t, 1, sink.MessageCount())
	msg := sink.Messages()[0]
	assert.True(t, msg.TurnEnd, "agent_end must route through PersistTurnEnd")

	assert.Equal(t, 1, sink.ResetSpanCount(), "agent_end should reset spans")
	// agent_end broadcasts no session info of its own. The usage snapshot is
	// empty here and the get_session_stats refresh needs a live stdin, so a
	// count above zero would mean a key came back.
	assert.Equal(t, 0, sink.SessionInfoCount())
}

// piFakeClock pins the agent's clock at start and returns the function that
// moves it. The caller advances between events, so a test reads as the timeline
// it means and stays correct however many times a handler reads the clock --
// a step-per-read clock silently re-times every case the moment a handler
// gains or loses one call.
func piFakeClock(a *Agent, start time.Time) (advance func(time.Duration)) {
	now := start
	a.nowFn = func() time.Time { return now }
	return func(d time.Duration) { now = now.Add(d) }
}

// piEpoch is an arbitrary fixed instant. Only the differences matter.
var piEpoch = time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)

// piPersistedAgentEnd decodes the nth persisted message as a JSON object.
func piPersistedAgentEnd(t *testing.T, sink *agenttest.ControlSink, index int) map[string]any {
	t.Helper()
	msgs := sink.Messages()
	require.Greater(t, len(msgs), index)
	var persisted map[string]any
	message := msgs[index]
	resolved := agent.ResolveMessageContent(piProvider{}, agent.MessageContent{Original: message.Content, Supplemental: message.SupplementalContent, Metadata: message.Metadata})
	require.NoError(t, json.Unmarshal(resolved, &persisted))
	return persisted
}

// agent_settled reports only that Pi will not continue on its own after the
// agent_end that already drew the divider. It must leave no trace at all,
// because the dispatch's default case would persist it as a raw-JSON row.
func TestHandlePiOutput_AgentSettled_PersistsNothing(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_settled"}`)))

	assert.Equal(t, 0, sink.MessageCount(), "agent_settled must not reach the transcript")
	assert.Equal(t, 0, sink.NotificationCount())
	assert.Equal(t, 0, len(sink.PublishedControls()))
	assert.Equal(t, 0, sink.ResetSpanCount())
	assert.Equal(t, 0, sink.SessionInfoCount())
}

func TestPiTurnCountMetadata(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_start"}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_start","toolCallId":"read-call","toolName":"read","args":{"path":"sample.txt"}}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_end","toolCallId":"read-call","toolName":"read","result":{"content":[]}}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","messages":[],"willRetry":true}`)))
	assert.Equal(t, float64(1), piPersistedAgentEnd(t, sink, 2)["num_tool_uses"])
	assert.False(t, sink.Messages()[2].TurnEnd)
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_start"}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","messages":[]}`)))
	assert.Equal(t, float64(1), piPersistedAgentEnd(t, sink, 3)["num_tool_uses"])
	assert.True(t, sink.Messages()[3].TurnEnd)
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_start"}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","messages":[]}`)))
	assert.Equal(t, float64(0), piPersistedAgentEnd(t, sink, 4)["num_tool_uses"])
}

func TestHandlePiOutput_AgentEnd_ReportsTurnDuration(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	advance := piFakeClock(a, piEpoch)

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_start"}`)))
	advance(2500 * time.Millisecond)
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","messages":[]}`)))

	assert.Equal(t, float64(2500), piPersistedAgentEnd(t, sink, 0)["duration_ms"])
}

// A zero-length turn is a real measurement, so it persists 0 rather than
// dropping the field: the frontend draws "(0ms)" for it and nothing at all for
// an absent field.
func TestHandlePiOutput_AgentEnd_ZeroLengthTurnReportsZero(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	// The clock never advances, so the turn takes no measurable time at all.
	piFakeClock(a, piEpoch)

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_start"}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","messages":[]}`)))

	persisted := piPersistedAgentEnd(t, sink, 0)
	require.Contains(t, persisted, "duration_ms")
	assert.Equal(t, float64(0), persisted["duration_ms"])
}

// An agent_end this worker did not see start (a process it adopted mid-turn)
// has nothing to measure from, so it must omit the field rather than claim 0.
func TestHandlePiOutput_AgentEnd_WithoutStartOmitsDuration(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","messages":[]}`)))

	assert.NotContains(t, piPersistedAgentEnd(t, sink, 0), "duration_ms")
}

// The mark is consumed by the agent_end that ends the turn, so a stray second
// agent_end reports no duration instead of the time since the turn began.
func TestHandlePiOutput_AgentEnd_SecondEndOmitsDuration(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	advance := piFakeClock(a, piEpoch)

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_start"}`)))
	advance(time.Second)
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","messages":[]}`)))
	advance(time.Second)
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","messages":[]}`)))

	assert.Equal(t, float64(1000), piPersistedAgentEnd(t, sink, 0)["duration_ms"])
	assert.NotContains(t, piPersistedAgentEnd(t, sink, 1), "duration_ms")
}

// A retried run continues the same turn, so the final divider reports the whole
// elapsed time rather than the last attempt alone.
func TestHandlePiOutput_AgentEnd_RetryKeepsTurnStartMark(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	advance := piFakeClock(a, piEpoch)

	// One turn, two runs: the attempt takes 1s, the backoff 1s, the retry 1s.
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_start"}`)))
	advance(time.Second)
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","messages":[],"willRetry":true}`)))
	advance(time.Second)
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_start"}`)))
	advance(time.Second)
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","messages":[]}`)))

	assert.Equal(t, float64(1000), piPersistedAgentEnd(t, sink, 0)["duration_ms"],
		"the retried attempt reports the elapsed time so far")
	assert.Equal(t, float64(3000), piPersistedAgentEnd(t, sink, 1)["duration_ms"],
		"the final divider spans from the FIRST agent_start")
}

// A run Pi restarts itself is not a turn end: it must not fire the turn-end
// event, which drives the completion sound and the off-screen tab's dot.
func TestHandlePiOutput_AgentEnd_WillRetryDoesNotEndTurn(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.TurnToolUses = 3
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_start"}`)))

	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error","errorMessage":"overloaded"}],"willRetry":true}`)))

	require.Equal(t, 1, sink.MessageCount())
	assert.False(t, sink.Messages()[0].TurnEnd, "a retried run must not route through PersistTurnEnd")

	a.Mu.Lock()
	turnActive, toolUses := a.currentTurnActive, a.TurnToolUses
	a.Mu.Unlock()
	assert.True(t, turnActive, "the turn stays open so Interrupt and steering still work")
	assert.Equal(t, 3, toolUses, "a retried run keeps the turn's tool-use count")
}

func TestHandlePiOutput_AgentEnd_DiscardsBufferedTextBeforeProviderRetry(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"abandoned attempt"}}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error"}],"willRetry":true}`)))

	for _, message := range sink.Messages() {
		assert.NotContains(t, string(message.Content), "abandoned attempt")
	}
}

func TestHandlePiOutput_AgentEnd_MarksRetainedTextAsError(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"partial answer"}}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error","errorMessage":"failed"}]}`)))

	require.NotEmpty(t, sink.Messages())
	assert.JSONEq(t, `{
		"type":"assembled_message",
		"kind":"text",
		"text":"partial answer",
		"completion":"error"
	}`, string(sink.Messages()[0].Content))
}

func TestHandlePiOutput_AgentEnd_PersistsIncompleteToolOutput(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_start","toolCallId":"tool-1","toolName":"bash","args":{"command":"printf partial"}}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_update","toolCallId":"tool-1","partialResult":{"content":[{"type":"text","text":"partial output"}],"details":{}}}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error"}]}`)))

	require.GreaterOrEqual(t, sink.MessageCount(), 3)
	result := sink.Messages()[1]
	assert.True(t, result.Closing)
	// The row is the agent's own tool_execution_start frame, byte for byte. An
	// earlier build wrote a tool_execution_end event Pi never sent, and declared
	// `isError: true` although no failure was reported.
	assert.JSONEq(t,
		`{"type":"tool_execution_start","toolCallId":"tool-1","toolName":"bash","args":{"command":"printf partial"}}`,
		string(result.Content))
	assert.JSONEq(t, `{
		"toolCallId":"tool-1",
		"toolName":"bash",
		"partialResult":{"content":[{"type":"text","text":"partial output"}],"details":{}}
	}`, string(result.SupplementalContent))
	assert.Equal(t, agent.MessageCompletionError, result.Completion)

	// Both halves read back as one finished call, so every extractor sees the output.
	assert.JSONEq(t, `{
		"type":"tool_execution_start",
		"toolCallId":"tool-1",
		"toolName":"bash",
		"args":{"command":"printf partial"},
		"result":{"content":[{"type":"text","text":"partial output"}],"details":{}}
	}`, string(Registration().Plugin.ResolveProviderData(agent.MessageContent{
		Original: result.Content, Supplemental: result.SupplementalContent,
	})))
}

func TestHandlePiOutput_AgentEndClosesToolWithoutPartialOutput(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_start","toolCallId":"tool-empty","toolName":"bash","args":{"command":"sleep 10"}}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error"}]}`)))

	require.Len(t, sink.Messages(), 3)
	closing := sink.Messages()[1]
	assert.True(t, closing.Closing)
	assert.Equal(t, "tool-empty", closing.SpanID)
	assert.JSONEq(t,
		`{"type":"tool_execution_start","toolCallId":"tool-empty","toolName":"bash","args":{"command":"sleep 10"}}`,
		string(closing.Content))
	assert.Empty(t, closing.SupplementalContent, "the call reported nothing, so nothing was recovered")
	assert.Equal(t, agent.MessageCompletionError, closing.Completion)
}

// A supplement that identifies another call cannot reach this row's result.
func TestPiResolveProviderData_RefusesASupplementForAnotherCall(t *testing.T) {
	t.Parallel()

	original := []byte(`{"type":"tool_execution_start","toolCallId":"tool-1","toolName":"bash"}`)
	for _, supplement := range []string{
		`{"toolCallId":"tool-2","toolName":"bash","partialResult":{"content":[]}}`,
		`{"toolCallId":"tool-1","toolName":"read","partialResult":{"content":[]}}`,
		`{"toolCallId":"tool-1","toolName":"bash"}`,
	} {
		assert.JSONEq(t, string(original),
			string(Registration().Plugin.ResolveProviderData(agent.MessageContent{
				Original: original, Supplemental: []byte(supplement),
			})), supplement)
	}
}

func TestHandlePiOutput_InterruptedGenerationPreservesContentOrder(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	for _, raw := range []string{
		`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","contentIndex":0,"delta":"first reason"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":1,"delta":"answer"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","contentIndex":2,"delta":"second reason"}}`,
		`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"aborted"}]}`,
	} {
		handlePiOutput(a, providerkit.ParseLine([]byte(raw)))
	}

	require.GreaterOrEqual(t, sink.MessageCount(), 4)
	assert.Contains(t, string(sink.Messages()[0].Content), "first reason")
	assert.Contains(t, string(sink.Messages()[1].Content), "answer")
	assert.Contains(t, string(sink.Messages()[2].Content), "second reason")
}

func TestHandlePiOutput_KeepsThinkingDeltasVerbatim(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	for _, raw := range []string{
		`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","contentIndex":0,"delta":"**Verifying terminal release synchronization"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","contentIndex":0,"delta":"Analyzing lock acquisition order and concurrency**"}}`,
		`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"aborted"}]}`,
	} {
		handlePiOutput(a, providerkit.ParseLine([]byte(raw)))
	}

	require.NotEmpty(t, sink.Messages())
	assert.Contains(t, string(sink.Messages()[0].Content),
		`"text":"**Verifying terminal release synchronizationAnalyzing lock acquisition order and concurrency**"`)
}

func TestHandlePiOutput_MessagePersistFailureKeepsFallbackText(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{PersistErr: errors.New("database unavailable")}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"recover me"}}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"recover me"}]}}`)))

	sink.PersistErr = nil
	a.flushPiGeneration(agent.MessageCompletionError)
	require.Len(t, sink.Messages(), 2)
	assert.Contains(t, string(sink.Messages()[1].Content), "recover me")
}

func TestHandlePiOutput_DiscardedTurnDoesNotPersistIncompleteTool(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_start","toolCallId":"discarded","toolName":"bash"}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_update","toolCallId":"discarded","partialResult":{"content":[{"type":"text","text":"old output"}]}}`)))
	before := sink.MessageCount()
	a.DiscardOutput()
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"aborted"}]}`)))

	assert.Equal(t, before+1, sink.MessageCount(), "only the agent-end divider can persist")
	for _, message := range sink.Messages()[before:] {
		assert.NotContains(t, string(message.Content), "tool_execution_end")
	}
}

func TestPiPromptFailureKeepsActiveTurnForRejectedSteer(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.generationBuffer.Append("answer", agent.AssembledMessageKindText, "still active", providerkit.JoinVerbatim)
	a.handlePiPromptFailure(errors.New("steer rejected"), true)

	assert.Equal(t, 0, sink.MessageCount())
	assert.Len(t, sink.Notifications(), 1)
	a.flushPiGeneration(agent.MessageCompletionInterrupted)
	require.Len(t, sink.Messages(), 1)
	assert.Contains(t, string(sink.Messages()[0].Content), "still active")
}

func TestPiPromptFailureSuppressesIntentionalStopError(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.SetStoppedForTest(true)
	a.handlePiPromptFailure(errors.New("process closed"), false)

	assert.Empty(t, sink.Notifications())
}

// Pi retries the WebSocket failure itself when it says willRetry, so LeapMux
// must not schedule a second continuation for the same failure. willRetry is
// the ONLY difference between these two envelopes: Pi reports false once its
// own retry budget is spent, and that is where LeapMux's auto-continue takes
// over as the last resort.
func TestHandlePiOutput_AgentEnd_WillRetryDecidesAutoContinue(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		willRetry bool
		schedules int
		cancels   int
	}{
		{"Pi retries, so LeapMux stands down", true, 0, 1},
		{"Pi is done retrying, so LeapMux continues", false, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := &agenttest.ControlSink{}
			a := newPiAgentWithSink(agent.NewProviderServices(sink))
			raw := fmt.Appendf(nil,
				`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error","errorMessage":"WebSocket error"}],"willRetry":%t}`,
				tc.willRetry)

			handlePiOutput(a, providerkit.ParseLine(raw))

			assert.Equal(t, tc.schedules, sink.AutoScheduleCount())
			assert.Equal(t, tc.cancels, sink.AutoCancelCount())
		})
	}
}

func TestPiTurnDurationMs(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)

	assert.Nil(t, piTurnDurationMs(time.Time{}, base), "an unobserved start has nothing to measure")
	assert.Nil(t, piTurnDurationMs(base, base.Add(-time.Second)), "a backwards clock reports nothing")

	require.NotNil(t, piTurnDurationMs(base, base))
	assert.Equal(t, int64(0), *piTurnDurationMs(base, base))

	require.NotNil(t, piTurnDurationMs(base, base.Add(1500*time.Millisecond)))
	assert.Equal(t, int64(1500), *piTurnDurationMs(base, base.Add(1500*time.Millisecond)))
}

func TestHandlePiOutput_MessageUpdate_OtherDeltaTypesIgnored(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	for _, deltaType := range []string{
		"start", "text_start", "text_end",
		"thinking_start", "thinking_end",
		"toolcall_start", "toolcall_delta", "toolcall_end",
		"done", "error",
	} {
		raw := []byte(`{"type":"message_update","assistantMessageEvent":{"type":"` + deltaType + `"}}`)
		handlePiOutput(a, providerkit.ParseLine(raw))
	}

	assert.Equal(t, 0, sink.MessageCount())
}

func TestHandlePiOutput_MessageEnd_PersistsAssistantMessage(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	raw := []byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Hello"}]}}`)
	handlePiOutput(a, providerkit.ParseLine(raw))

	require.Equal(t, 1, sink.MessageCount())
	msg := sink.Messages()[0]
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, msg.Source)
	assert.JSONEq(t, string(raw), string(msg.Content))
}

func TestHandlePiOutput_MessageEnd_PreservesContentAndStoresUsageMetadata(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.model = "gpt-5.5"
	a.availableModels = []*agent.ModelInfo{{Id: "gpt-5.5", ContextWindow: 200000}}

	raw := []byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Hello"}],"usage":{"input":100,"output":10,"cacheRead":20,"cacheWrite":5,"totalTokens":135,"cost":{"input":0.0001,"output":0.0002,"cacheRead":0.00001,"cacheWrite":0.00002,"total":0.00033}}}}`)
	handlePiOutput(a, providerkit.ParseLine(raw))

	require.Equal(t, 1, sink.MessageCount())
	msg := sink.Messages()[0]
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, msg.Source)
	assert.Equal(t, raw, msg.Content)

	var persisted map[string]any
	require.NoError(t, json.Unmarshal(msg.Metadata, &persisted))
	assert.Equal(t, 0.00033, persisted["total_cost_usd"])
	usage, ok := persisted["context_usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(100), usage["input_tokens"])
	assert.Equal(t, float64(5), usage["cache_creation_input_tokens"])
	assert.Equal(t, float64(20), usage["cache_read_input_tokens"])
	assert.Equal(t, float64(10), usage["output_tokens"])
	assert.Equal(t, float64(200000), usage["context_window"])

	require.Equal(t, 1, sink.SessionInfoCount())
	info := sink.LastSessionInfo()
	assert.Equal(t, 0.00033, info["total_cost_usd"])
	broadcastUsage, ok := info["context_usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, int64(100), broadcastUsage["input_tokens"])
	assert.Equal(t, int64(5), broadcastUsage["cache_creation_input_tokens"])
	assert.Equal(t, int64(20), broadcastUsage["cache_read_input_tokens"])
	assert.Equal(t, int64(10), broadcastUsage["output_tokens"])
	assert.Equal(t, int64(200000), broadcastUsage["context_window"])
}

func TestHandlePiOutput_AgentEnd_AugmentsWithLatestUsageSnapshot(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.model = "gpt-5.5"
	a.availableModels = []*agent.ModelInfo{{Id: "gpt-5.5", ContextWindow: 200000}}
	advance := piFakeClock(a, piEpoch)

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_start"}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"message_end","message":{"role":"assistant","usage":{"input":100,"output":10,"cacheRead":20,"cacheWrite":5,"totalTokens":135,"cost":{"total":0.00033}}}}`)))
	advance(750 * time.Millisecond)
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","messages":[]}`)))

	msgs := sink.Messages()
	require.Equal(t, 2, len(msgs))
	result := msgs[1]
	assert.True(t, result.TurnEnd, "agent_end must route through PersistTurnEnd")

	persisted := piPersistedAgentEnd(t, sink, 1)
	assert.Equal(t, "agent_end", persisted["type"])
	assert.Equal(t, 0.00033, persisted["total_cost_usd"])
	assert.Equal(t, float64(750), persisted["duration_ms"],
		"the supplement carries the duration and usage fields")
	usage, ok := persisted["context_usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(100), usage["input_tokens"])
	assert.Equal(t, float64(5), usage["cache_creation_input_tokens"])
	assert.Equal(t, float64(20), usage["cache_read_input_tokens"])
	assert.Equal(t, float64(10), usage["output_tokens"])
	assert.Equal(t, float64(200000), usage["context_window"])
}

func TestHandlePiOutput_AgentEnd_WebSocketErrorSchedulesAutoContinue(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	raw := []byte(`{"type":"agent_end","messages":[{"role":"user"},{"role":"assistant","stopReason":"error","errorMessage":"WebSocket error"}]}`)

	handlePiOutput(a, providerkit.ParseLine(raw))

	require.Equal(t, 1, sink.AutoScheduleCount())
	schedule := sink.LastAutoSchedule()
	assert.Equal(t, agent.AutoContinueReasonAPIError, schedule.Reason)
	assert.False(t, schedule.DueAt.IsZero())
	assert.JSONEq(t, string(raw), string(schedule.SourcePayload))
	assert.Equal(t, 0, sink.AutoCancelCount())
}

func TestHandlePiOutput_AgentEnd_NonRetryableResultCancelsAutoContinue(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	raw := []byte(`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"stop"}]}`)

	handlePiOutput(a, providerkit.ParseLine(raw))

	require.Equal(t, 1, sink.AutoCancelCount())
	assert.Equal(t, agent.AutoContinueReasonAPIError, sink.LastAutoCancel())
	assert.Equal(t, 0, sink.AutoScheduleCount())
}

func TestHandlePiOutput_AgentEnd_NonWebSocketErrorMessageCancelsAutoContinue(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	raw := []byte(`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error","errorMessage":"rate limited"}]}`)

	handlePiOutput(a, providerkit.ParseLine(raw))

	require.Equal(t, 1, sink.AutoCancelCount())
	assert.Equal(t, agent.AutoContinueReasonAPIError, sink.LastAutoCancel())
	assert.Equal(t, 0, sink.AutoScheduleCount())
}

func TestHandlePiOutput_AgentEnd_UnexpectedMessagesShapeCancelsAutoContinue(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	// messages is a string instead of an array — the envelope unmarshal in
	// handlePiAgentEnd fails, so isRetryableFailure sees no messages and we
	// fall through to cancel.
	raw := []byte(`{"type":"agent_end","messages":"unexpected"}`)

	handlePiOutput(a, providerkit.ParseLine(raw))

	require.Equal(t, 1, sink.AutoCancelCount())
	assert.Equal(t, agent.AutoContinueReasonAPIError, sink.LastAutoCancel())
	assert.Equal(t, 0, sink.AutoScheduleCount())
}

func TestHandlePiOutput_ToolExecutionLifecycle(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	startRaw := []byte(`{"type":"tool_execution_start","toolCallId":"call-1","toolName":"bash","args":{"command":"ls"}}`)
	updateRaw := []byte(`{"type":"tool_execution_update","toolCallId":"call-1","toolName":"bash","partialResult":{"content":[{"type":"text","text":"file1\n"}]}}`)
	endRaw := []byte(`{"type":"tool_execution_end","toolCallId":"call-1","toolName":"bash","result":{"content":[{"type":"text","text":"file1\nfile2\n"}],"details":{"exitCode":0}},"isError":false}`)

	handlePiOutput(a, providerkit.ParseLine(startRaw))
	// SetSpanType is recorded at the start, and asserted here rather than at the
	// end of the test: the close below ends the span, and an ended span forgets
	// its type.
	assert.Equal(t, "bash", sink.GetSpanType("call-1"))

	handlePiOutput(a, providerkit.ParseLine(updateRaw))
	handlePiOutput(a, providerkit.ParseLine(endRaw))

	// Two persisted messages: start (open) and end (closing).
	msgs := sink.Messages()
	require.Equal(t, 2, len(msgs), "tool_execution start/end should persist two messages")
	assert.Equal(t, "call-1", msgs[0].SpanID)
	assert.Equal(t, "bash", msgs[0].SpanType)
	assert.False(t, msgs[0].Closing, "start should not be marked closing")
	assert.Equal(t, "call-1", msgs[1].SpanID)
	assert.True(t, msgs[1].Closing, "end should be marked closing")

	// Span lifecycle: open then close.
	assert.Equal(t, []agenttest.SpanOpen{{SpanID: "call-1", ParentSpanID: ""}}, sink.OpenSpans())
	assert.Equal(t, []string{"call-1"}, sink.ClosedSpans())

	updates := sink.ProgressUpdates()
	assert.Contains(t, updates, agent.OutputTotalProgress("call-1", 6, false))
	assert.Contains(t, updates, agent.CompleteOutputProgress("call-1"))

	// Tool count incremented.
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, 1, a.TurnToolUses)
}

func TestHandlePiOutput_NativeTruncationTotalIsExact(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{
		"type":"tool_execution_update",
		"toolCallId":"call-1",
		"partialResult":{
			"content":[{"type":"text","text":"limited tail"}],
			"details":{"truncation":{"totalBytes":164440,"truncated":true}}
		}
	}`)))

	assert.Contains(t, sink.ProgressUpdates(), agent.OutputExactTotalProgress("call-1", 164440))
}

// Pi's tool_execution_update events carry the *cumulative* partialResult.
// Verify that successive updates only broadcast the new delta, and that the
// per-span tracking is reset when the tool ends so a fresh tool with the same
// id starts from empty.
func TestHandlePiOutput_ToolExecutionUpdate_BroadcastsDeltaOnly(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	startRaw := []byte(`{"type":"tool_execution_start","toolCallId":"call-1","toolName":"bash"}`)
	update1 := []byte(`{"type":"tool_execution_update","toolCallId":"call-1","partialResult":{"content":[{"type":"text","text":"line1\n"}]}}`)
	update2 := []byte(`{"type":"tool_execution_update","toolCallId":"call-1","partialResult":{"content":[{"type":"text","text":"line1\nline2\n"}]}}`)
	updateNoNew := []byte(`{"type":"tool_execution_update","toolCallId":"call-1","partialResult":{"content":[{"type":"text","text":"line1\nline2\n"}]}}`)
	endRaw := []byte(`{"type":"tool_execution_end","toolCallId":"call-1","toolName":"bash","result":{"content":[{"type":"text","text":"line1\nline2\n"}]}}`)

	handlePiOutput(a, providerkit.ParseLine(startRaw))
	handlePiOutput(a, providerkit.ParseLine(update1))
	handlePiOutput(a, providerkit.ParseLine(update2))
	handlePiOutput(a, providerkit.ParseLine(updateNoNew))
	handlePiOutput(a, providerkit.ParseLine(endRaw))

	updates := sink.ProgressUpdates()
	var totals []int64
	for _, update := range updates {
		if update.Operation == agent.ProgressOutputTotal {
			totals = append(totals, update.Value)
		}
	}
	assert.Equal(t, []int64{6, 12, 12}, totals)
	assert.Contains(t, updates, agent.CompleteOutputProgress("call-1"))

	// After tool_execution_end, per-span state should be cleared so that a new
	// tool reusing the same id starts fresh.
	present := a.HasCumulativeOutputForTest("call-1")
	assert.False(t, present, "tool_execution_end should clear cumulativeBroadcast entry")
}

func TestHandlePiOutput_ToolExecutionStart_DropsLineWithoutToolCallID(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_start","toolName":"bash"}`)))

	assert.Equal(t, 0, sink.MessageCount())
	assert.Empty(t, sink.OpenSpans())
}

func TestHandlePiOutput_QueueUpdate_BroadcastsDepth(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"queue_update","steering":["a","b"],"followUp":["c"]}`,
	)))

	require.Equal(t, 1, sink.SessionInfoCount())
	info := sink.LastSessionInfo()
	assert.Equal(t, 3, info["pi_queue_depth"])
	assert.Equal(t, 2, info["pi_steering_depth"])
	assert.Equal(t, 1, info["pi_follow_up_depth"])
}

func TestHandlePiOutput_CompactionEvents_PersistAsAgentNotification(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	for _, line := range []string{
		`{"type":"compaction_start","reason":"threshold"}`,
		`{"type":"compaction_end","reason":"threshold","aborted":false,"willRetry":false,"result":{"summary":"...","tokensBefore":150000}}`,
	} {
		handlePiOutput(a, providerkit.ParseLine([]byte(line)))
	}

	require.Equal(t, 2, sink.NotificationCount())
	for _, n := range sink.PersistedNotifications() {
		assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, n.Source,
			"Pi-emitted lifecycle events must persist as AGENT (LEAPMUX is reserved for worker-synthesized envelopes)")
	}
}

func TestHandlePiOutput_AutoRetryEvents_PersistAsAgentNotification(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"auto_retry_start","attempt":1,"maxAttempts":3,"delayMs":2000,"errorMessage":"overloaded"}`,
	)))
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"auto_retry_end","success":true,"attempt":2}`,
	)))

	require.Equal(t, 2, sink.NotificationCount())
	for _, n := range sink.PersistedNotifications() {
		assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, n.Source)
	}
}

func TestHandlePiOutput_ExtensionError_PersistAsAgentNotification(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"extension_error","extensionPath":"/path/ext.ts","event":"tool_call","error":"boom"}`,
	)))

	require.Equal(t, 1, sink.NotificationCount())
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, sink.LastNotification().Source)
}

func TestHandlePiOutput_ExtensionUIRequest_DialogPersistsControlRequest(t *testing.T) {
	t.Parallel()

	cases := []struct {
		method string
		id     string
		raw    string
	}{
		{
			"select", "uuid-select",
			`{"type":"extension_ui_request","id":"uuid-select","method":"select","title":"Allow?","options":["Allow","Block"],"timeout":10000}`,
		},
		{
			"confirm", "uuid-confirm",
			`{"type":"extension_ui_request","id":"uuid-confirm","method":"confirm","title":"Clear?","message":"all gone","timeout":5000}`,
		},
		{
			"input", "uuid-input",
			`{"type":"extension_ui_request","id":"uuid-input","method":"input","title":"Enter","placeholder":"text"}`,
		},
		{
			"editor", "uuid-editor",
			`{"type":"extension_ui_request","id":"uuid-editor","method":"editor","title":"Edit","prefill":"line1\nline2"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			sink := &agenttest.ControlSink{}
			a := newPiAgentWithSink(agent.NewProviderServices(sink))

			handlePiOutput(a, providerkit.ParseLine([]byte(tc.raw)))

			persisted := sink.PublishedControls()
			require.Equal(t, 1, len(persisted), "should persist one control request")
			assert.Equal(t, tc.id, persisted[0].RequestID)
			// Payload must round-trip the raw line verbatim.
			assert.JSONEq(t, tc.raw, string(persisted[0].Payload))

			// Should NOT be persisted as a regular message or notification.
			assert.Equal(t, 0, sink.MessageCount())
			assert.Equal(t, 0, sink.NotificationCount())
		})
	}
}

func TestHandlePiOutput_ExtensionUIRequest_DialogWithoutIDIsDropped(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"extension_ui_request","method":"select","title":"x","options":["a"]}`,
	)))

	assert.Empty(t, sink.PublishedControls(), "missing id must not persist a control request")
}

func TestHandlePiOutput_ExtensionUIRequest_NotifyPersistsRawAsAgent(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	rawLine := `{"type":"extension_ui_request","id":"x","method":"notify","message":"Hello","notifyType":"warning"}`
	handlePiOutput(a, providerkit.ParseLine([]byte(rawLine)))

	// Single raw passthrough — no synthesized agent_notify wrapper.
	require.Equal(t, 1, sink.NotificationCount(), "single raw extension_ui_request notification persisted")
	last := sink.LastNotification()
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, last.Source,
		"Pi-emitted notify must persist as AGENT (the agent is the source)")
	assert.JSONEq(t, rawLine, string(last.Content),
		"raw envelope must be preserved verbatim so renderers can read every method-specific field")

	// Synthesis dropped — the frontend renderer derives level/message from the raw envelope.
	assert.Empty(t, sink.Notifications(),
		"synthesized agent_notify must no longer be emitted; raw passthrough alone carries the same info")

	// Should NOT be a control request.
	assert.Empty(t, sink.PublishedControls())
}

func TestHandlePiOutput_ExtensionUIRequest_NotifyMissingNotifyTypePreservesRaw(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"extension_ui_request","id":"x","method":"notify","message":"hi"}`,
	)))

	require.Equal(t, 1, sink.NotificationCount())
	last := sink.LastNotification()
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, last.Source)
	// notifyType absent in the raw — renderer is responsible for defaulting to "info".
	assert.NotContains(t, string(last.Content), `"notifyType"`)
	assert.Empty(t, sink.Notifications(), "no synthesized agent_notify on the side channel")
}

func TestHandlePiOutput_ExtensionUIRequest_SetStatus_BroadcastsSessionInfo(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	statusValue := "Turn 3 running…"
	body := map[string]any{
		"type":       "extension_ui_request",
		"id":         "x",
		"method":     "setStatus",
		"statusKey":  "my-ext",
		"statusText": statusValue,
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)

	handlePiOutput(a, providerkit.ParseLine(raw))

	require.Equal(t, 1, sink.SessionInfoCount())
	status, ok := sink.LastSessionInfo()["pi_status"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, statusValue, status["my-ext"])
}

func TestHandlePiOutput_ExtensionUIRequest_SetStatus_NilClearsKey(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	// statusText omitted → broadcast a nil value so the frontend clears the key.
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"extension_ui_request","id":"x","method":"setStatus","statusKey":"my-ext"}`,
	)))

	require.Equal(t, 1, sink.SessionInfoCount())
	status, ok := sink.LastSessionInfo()["pi_status"].(map[string]any)
	require.True(t, ok)
	val, present := status["my-ext"]
	assert.True(t, present, "key should be present so the frontend can clear it")
	assert.Nil(t, val)
}

func TestHandlePiOutput_ExtensionUIRequest_SetWidget_BroadcastsSessionInfo(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"extension_ui_request","id":"x","method":"setWidget","widgetKey":"my-ext","widgetLines":["a","b"],"widgetPlacement":"belowEditor"}`,
	)))

	require.Equal(t, 1, sink.SessionInfoCount())
	widgets, ok := sink.LastSessionInfo()["pi_widget"].(map[string]any)
	require.True(t, ok)
	widget, ok := widgets["my-ext"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "belowEditor", widget["placement"])
	assert.NotNil(t, widget["lines"])
}

func TestHandlePiOutput_ExtensionUIRequest_SetTitle_BroadcastsSessionInfo(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"extension_ui_request","id":"x","method":"setTitle","title":"pi - my project"}`,
	)))

	require.Equal(t, 1, sink.SessionInfoCount())
	assert.Equal(t, "pi - my project", sink.LastSessionInfo()["pi_terminal_title"])
}

func TestHandlePiOutput_ExtensionUIRequest_SetEditorText_BroadcastsSessionInfo(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"extension_ui_request","id":"x","method":"set_editor_text","text":"prefilled"}`,
	)))

	require.Equal(t, 1, sink.SessionInfoCount())
	assert.Equal(t, "prefilled", sink.LastSessionInfo()["pi_editor_text"])
}

func TestHandlePiOutput_ExtensionUIRequest_UnknownMethod_PersistAsNotification(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"extension_ui_request","id":"x","method":"someFutureMethod","payload":"opaque"}`,
	)))

	assert.Equal(t, 1, sink.NotificationCount())
	assert.Empty(t, sink.PublishedControls())
}

func TestHandlePiOutput_ResponseLineWithoutPendingID_LoggedNotPersisted(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	// A response that wasn't intercepted (no pending caller). Should be
	// logged and dropped, not persisted.
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"response","id":"orphan","command":"prompt","success":true}`,
	)))

	assert.Equal(t, 0, sink.MessageCount())
	assert.Equal(t, 0, sink.NotificationCount())
}

func TestHandlePiOutput_UnknownEventType_PersistedAsAgent(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"future_event","stuff":1}`)))

	require.Equal(t, 1, sink.MessageCount())
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, sink.Messages()[0].Source)
}

func TestHandlePiOutput_TextAndThinkingDeltasAccumulateThinkingTokens(t *testing.T) {
	t.Parallel()

	for _, deltaType := range []string{"text_delta", "thinking_delta"} {
		t.Run(deltaType, func(t *testing.T) {
			sink := &agenttest.ControlSink{}
			a := newPiAgentWithSink(agent.NewProviderServices(sink))

			// 8-char delta -> 2 tokens; a second 8-char delta accumulates to 4.
			handlePiOutput(a, providerkit.ParseLine([]byte(
				`{"type":"message_update","assistantMessageEvent":{"type":"`+deltaType+`","delta":"abcdefgh"}}`)))
			assert.Equal(t, int64(2), sink.LastThinkingTokens())
			handlePiOutput(a, providerkit.ParseLine([]byte(
				`{"type":"message_update","assistantMessageEvent":{"type":"`+deltaType+`","delta":"ijklmnop"}}`)))
			assert.Equal(t, int64(4), sink.LastThinkingTokens())

			assert.Equal(t, 0, sink.MessageCount(), "thinking_tokens deltas must not persist")
		})
	}
}

func TestHandlePiOutput_ThinkingThenTextInOneMessageSharePhase(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	// Pi streams thinking then the visible answer within a single message and
	// persists both as one message_end -- there is no per-phase split or AGENT
	// commit between them (unlike ACP, which hands off and resets). So a
	// thinking->text transition inside one message must keep accumulating into one
	// thinking-token phase rather than restarting at the text delta.
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","delta":"abcdefghijklmnop"}}`)))
	require.Equal(t, int64(4), sink.LastThinkingTokens())

	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"qrstuvwx"}}`)))
	assert.Equal(t, int64(6), sink.LastThinkingTokens(), "text after thinking keeps climbing (24/4), it does not reset")
}

func TestHandlePiOutput_EmptyDeltaDoesNotBroadcastThinkingTokens(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":""}}`)))

	assert.Equal(t, int64(-1), sink.LastThinkingTokens(), "empty delta is a no-op")
}

func TestHandlePiOutput_MessageEndResetsThinkingTokens(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"abcdefghijklmnop"}}`)))
	require.Equal(t, int64(4), sink.LastThinkingTokens())

	// Committing the assistant message is a phase boundary; the estimate resets.
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"x"}]}}`)))

	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"abcdefgh"}}`)))
	assert.Equal(t, int64(2), sink.LastThinkingTokens(), "next phase restarts at 8/4")
}

func TestHandlePiOutput_DialogRequestResetsThinkingTokens(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"abcdefghijklmnop"}}`)))
	require.Equal(t, int64(4), sink.LastThinkingTokens())

	// A blocking dialog (confirm) is a live control request the frontend clears
	// its counter on, so the backend resets to mirror it.
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"extension_ui_request","id":"d1","method":"confirm","title":"Clear?","message":"all gone"}`)))

	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"abcdefgh"}}`)))
	assert.Equal(t, int64(2), sink.LastThinkingTokens(), "a dialog prompt restarts the estimate")
}

func TestHandlePiOutput_TurnAndToolBoundariesResetThinkingTokens(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		reset string
	}{
		{"agent_start", `{"type":"agent_start"}`},
		{"agent_end", `{"type":"agent_end","messages":[]}`},
		{"tool_execution_start", `{"type":"tool_execution_start","toolCallId":"call-1","toolName":"bash"}`},
		{"tool_execution_end", `{"type":"tool_execution_end","toolCallId":"call-1","toolName":"bash"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &agenttest.ControlSink{}
			a := newPiAgentWithSink(agent.NewProviderServices(sink))

			handlePiOutput(a, providerkit.ParseLine([]byte(
				`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"abcdefghijklmnop"}}`)))
			require.Equal(t, int64(4), sink.LastThinkingTokens())

			handlePiOutput(a, providerkit.ParseLine([]byte(tc.reset)))

			handlePiOutput(a, providerkit.ParseLine([]byte(
				`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"abcdefgh"}}`)))
			assert.Equal(t, int64(2), sink.LastThinkingTokens(), "the boundary restarts the estimate")
		})
	}
}

// TestHandlePiOutput_ToolUpdateDetailsUpsertsSubagentActivity verifies that a
// tool_execution_update whose partialResult.details carries the pi-subagents
// shape {status, activity} upserts a Running Subagent registry row keyed by
// details.agentId (falling back to the toolCallId) with the activity as
// ActiveForm.
func TestHandlePiOutput_ToolUpdateDetailsUpsertsSubagentActivity(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	// A tool_execution_start records the spawn prompt as the title (read from
	// the `input` field) so the subagent row has a label.
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"tool_execution_start","toolCallId":"call-sub-1","toolName":"Agent","input":{"description":"build the feature","prompt":"build it"}}`)))

	// An update whose partialResult.details carries the subagent shape.
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"tool_execution_update","toolCallId":"call-sub-1","partialResult":{"content":[{"type":"text","text":"working\n"}],"details":{"status":"running","activity":"running tests","agentId":"agent-xyz"}}}`)))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1, "details shape must upsert exactly one registry row")
	row := tasks[0]
	assert.Equal(t, "agent-xyz", row.RowKey, "row keys by details.agentId when present")
	assert.Equal(t, bgtask.KindSubagent, row.Kind)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, "running tests", row.ActiveForm, "ActiveForm comes from details.activity")
	assert.Equal(t, "build the feature", row.Title, "Title comes from tool_execution_start input.description")
}

// TestHandlePiOutput_ToolUpdateDetails_FallsBackToToolCallID verifies that when
// details has the subagent shape but no agentId, the row is keyed by toolCallId.
func TestHandlePiOutput_ToolUpdateDetails_FallsBackToToolCallID(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"tool_execution_start","toolCallId":"call-no-id","toolName":"Agent","input":{"description":"work"}}`)))

	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"tool_execution_update","toolCallId":"call-no-id","partialResult":{"content":[{"type":"text","text":"x\n"}],"details":{"status":"running","activity":"thinking"}}}`)))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "call-no-id", tasks[0].RowKey, "row keys by toolCallId when details.agentId is absent")
}

// TestHandlePiOutput_ToolEndBackgroundRekeysToAgentID verifies that a
// tool_execution_end whose result carries status:"background" and an agentId
// re-keys the registry row from toolCallId to details.agentId and leaves it
// running. The registry rename removes the provisional key.
func TestHandlePiOutput_ToolEndBackgroundRekeysToAgentID(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	// Seed a running row keyed by toolCallId.
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"tool_execution_start","toolCallId":"call-bg","toolName":"Agent","input":{"description":"bg work"}}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"tool_execution_update","toolCallId":"call-bg","partialResult":{"content":[{"type":"text","text":"x\n"}],"details":{"status":"running","activity":"starting"}}}`)))

	// Pi nests child status under result.details.
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"tool_execution_end","toolCallId":"call-bg","toolName":"Agent","result":{"content":[],"details":{"status":"background","agentId":"agent-bg-1"}}}`)))

	tasks := sink.BackgroundTasks()
	// The stable ID replaces the provisional key without a duplicate row.
	require.Len(t, tasks, 1)
	assert.Equal(t, "agent-bg-1", tasks[0].RowKey)
	assert.Equal(t, bgtask.StatusRunning, tasks[0].Status)
	assert.Equal(t, bgtask.KindSubagent, tasks[0].Kind)

}

// TestHandlePiOutput_SubagentNotificationMessageClosesRegistryEntry verifies
// that a message_end with customType:"subagent-notification" closes the registry
// row from its details AND still persists to the parent transcript (it is real
// conversational context).
func TestHandlePiOutput_SubagentNotificationMessageClosesRegistryEntry(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	// The Agent result uses agentId. Its completion notification uses id.
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"tool_execution_start","toolCallId":"call-notif","toolName":"Agent","args":{"description":"notif task"}}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"tool_execution_update","toolCallId":"call-notif","partialResult":{"content":[{"type":"text","text":"x\n"}],"details":{"status":"running","activity":"busy","agentId":"agent-notif-1"}}}`)))
	require.Len(t, sink.BackgroundTasks(), 1)

	// The custom message carries the final status in details.
	msgEnd := []byte(`{"type":"message_end","message":{"role":"custom","customType":"subagent-notification","content":"subagent finished","details":{"status":"completed","id":"agent-notif-1"}}}`)
	handlePiOutput(a, providerkit.ParseLine(msgEnd))

	// Registry row closed.
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.StatusCompleted, tasks[0].Status,
		"subagent-notification with a final status must close the registry row")
	assert.True(t, tasks[0].Status.IsFinished())

	// The message STILL persisted to the parent transcript (alongside the
	// tool_execution_start message). The notification must be the final
	// persisted message.
	require.GreaterOrEqual(t, sink.MessageCount(), 1, "subagent-notification must still persist to the parent transcript")
	last := sink.Messages()[len(sink.Messages())-1]
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, last.Source)
	assert.Contains(t, string(last.Content), "subagent-notification")
}

// A description that holds nothing a reader can SEE must fall through, not
// swallow both fallbacks.
//
// The emptiness test used to run on the RAW field, so a run of zero-width
// spaces entered the description branch, cleaned to "", and returned it -- the
// prompt fallback and the tool-name fallback were both skipped, and the Pi
// subagent's row reached the sidebar with no label at all.
func TestPiExtractDescriptionFallsThroughAnInvisibleDescription(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"zero-width description falls through to the prompt", "{\"description\":\"\u200b\u200b\",\"prompt\":\"run the suite\"}", "run the suite"},
		{"whitespace description falls through to the prompt", `{"description":"   ","prompt":"run the suite"}`, "run the suite"},
		{"both invisible falls through to the tool name", "{\"description\":\"\u200b\",\"prompt\":\"\u200b\"}", "pi_spawn"},
		{"a visible description still wins", `{"description":"build","prompt":"run the suite"}`, "build"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, piExtractDescription([]byte(tc.input), "pi_spawn"))
		})
	}
}

// piInterruptedAgentEnd is the frame Pi sends when the user stops a turn whose
// tool was still running. It is the SAME shape as a genuine failure: the stop
// reason is `error` and the message is prose. A turn Pi aborts cleanly carries
// `stopReason:"aborted"` instead, so the frame alone cannot tell the two apart.
// Captured from `.tmp/provider-parity/interrupt5` (RL-015).
const piInterruptedAgentEnd = `{"type":"agent_end","messages":[{"role":"assistant","content":[],` +
	`"stopReason":"error","errorMessage":"This operation was aborted"}]}`

func TestHandlePiOutput_AgentEnd_AfterInterrupt_MarksTheTurnInterrupted(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.currentTurnActive = true
	a.noteInterruptRequested()

	handlePiOutput(a, providerkit.ParseLine([]byte(piInterruptedAgentEnd)))

	require.Equal(t, 1, sink.MessageCount())
	msg := sink.Messages()[0]
	assert.True(t, msg.TurnEnd)
	assert.Equal(t, agent.MessageCompletionInterrupted, msg.Completion,
		"a stop LeapMux asked for must not reach the reader as a failure")
}

func TestHandlePiOutput_AgentEnd_WithoutInterrupt_KeepsTheError(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.currentTurnActive = true

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","messages":[{"role":"assistant",`+
		`"stopReason":"error","errorMessage":"rate limit"}]}`)))

	require.Equal(t, 1, sink.MessageCount())
	assert.Empty(t, sink.Messages()[0].Completion,
		"a genuine failure carries no worker outcome, so the reader sees the frame own stop reason")
}

// The note belongs to ONE turn. A stop that Pi never ended -- the process died,
// the session was replaced -- must not label the next turn's divider.
func TestHandlePiOutput_AgentStart_DropsAStaleInterruptNote(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.currentTurnActive = true
	a.noteInterruptRequested()

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_start"}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","messages":[{"role":"assistant",`+
		`"stopReason":"error","errorMessage":"rate limit"}]}`)))

	require.Equal(t, 1, sink.MessageCount())
	assert.Empty(t, sink.Messages()[0].Completion)
}

// A run Pi retries itself keeps the turn open, so the note must survive to the
// agent_end that really ends it.
func TestHandlePiOutput_AgentEnd_RetryKeepsTheInterruptNote(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.currentTurnActive = true
	a.noteInterruptRequested()

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","willRetry":true,"messages":[{"role":"assistant",`+
		`"stopReason":"error","errorMessage":"WebSocket error"}]}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(piInterruptedAgentEnd)))

	require.Equal(t, 2, sink.MessageCount())
	assert.Equal(t, agent.MessageCompletionInterrupted, sink.Messages()[1].Completion)
}

func TestPiIncompleteToolsReleaseOutputForACallWithNoStartFrame(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	// A partial result can arrive for a call whose start frame this worker never saw.
	// The call opens no row, and the counter still holds its whole text.
	a.HandleOutput([]byte(`{"type":"tool_execution_update","toolCallId":"orphan","toolName":"bash","partialResult":{"content":[{"type":"text","text":"a long line of output"}]}}`))
	observed := a.HasCumulativeOutputForTest("orphan")
	require.True(t, observed, "the partial result must reach the output counter")

	a.persistIncompletePiTools(agent.MessageCompletionError)
	retained := a.HasCumulativeOutputForTest("orphan")
	assert.False(t, retained, "a call with no start frame must still release its output")
	assert.Empty(t, sink.Messages(), "a call that opened no row persists nothing")
	assert.Contains(t, sink.ProgressUpdates(), agent.CompleteOutputProgress("orphan"))
}

func TestPiAgentStartProbesTheSessionOnlyOnTheFirstAttempt(t *testing.T) {
	t.Parallel()
	rig := newPiTestRig(t, agent.NewProviderServices(&agenttest.Sink{}))
	rig.agent.Mu.Lock()
	rig.agent.sessionID = "sess"
	rig.agent.Mu.Unlock()
	rig.setResponder(func(req piRecordedRequest) (json.RawMessage, bool, string) {
		return json.RawMessage(`{"sessionId":"sess","sessionFile":"/tmp/pi-test.jsonl","cost":1}`), true, ""
	})
	rig.agent.HandleOutput([]byte(`{"type":"agent_start"}`))
	require.Eventually(t, func() bool { return len(rig.requests()) == 1 }, time.Second, time.Millisecond)
	assert.Equal(t, CommandGetSessionStats, rig.requests()[0].Type)
	// Pi restarts a failed run itself. A retry continues the SAME turn, so it cannot
	// have replaced the session, and a retry storm must not repeat the probe.
	rig.agent.HandleOutput([]byte(`{"type":"agent_start"}`))
	rig.agent.HandleOutput([]byte(`{"type":"agent_start"}`))
	assert.Never(t, func() bool { return len(rig.requests()) > 1 }, 100*time.Millisecond, 5*time.Millisecond)
}

// TestHandlePiOutput_BashExecutionUpdate_PersistsNothing pins the drop that keeps a
// single shell command from writing one transcript row per output chunk. Pi emits the
// event once per chunk, and the dispatch's `default` branch persisted every one.
func TestHandlePiOutput_BashExecutionUpdate_PersistsNothing(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	for _, delta := range []string{"first\n", "second\n", "third\n"} {
		raw, err := json.Marshal(map[string]any{"type": "bash_execution_update", "id": "cmd-1", "delta": delta})
		require.NoError(t, err)
		handlePiOutput(a, providerkit.ParseLine(raw))
	}

	assert.Empty(t, sink.Messages(), "a bash output chunk is not a transcript row")
	assert.Empty(t, sink.PersistedNotifications())
}

// TestHandlePiOutput_SessionInfoChanged_PersistsNothing pins the drop for Pi's own
// session name, which LeapMux never shows.
func TestHandlePiOutput_SessionInfoChanged_PersistsNothing(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"session_info_changed","name":"renamed session"}`)))

	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.PersistedNotifications())
}

// TestHandlePiOutput_ThinkingLevelChanged_RefreshesTheEffortSetting covers the clamp
// Pi applies on a model switch: it lowers the level the new model cannot serve and
// announces the one it settled on. LeapMux must adopt that value, because its own
// field is what the effort segment reads.
func TestHandlePiOutput_ThinkingLevelChanged_RefreshesTheEffortSetting(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.model = "gpt-5"
	a.provider = "openai"
	a.thinkingLevel = "high"

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"thinking_level_changed","level":"low"}`)))

	a.Mu.Lock()
	level := a.thinkingLevel
	a.Mu.Unlock()
	assert.Equal(t, "low", level)
	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "low", refresh.Effort)
	assert.Equal(t, "gpt-5", refresh.Model)
	assert.Equal(t, "openai", refresh.Options[OptionProvider])
	assert.Empty(t, sink.Messages(), "the settings notification states the change; a raw row would repeat it")
}

// TestHandlePiOutput_ThinkingLevelChanged_IgnoresTheEchoOfItsOwnRequest keeps the
// transcript quiet for the level LeapMux itself asked for: `applyThinkingLevel` already
// stored it, so Pi's announcement states nothing new.
func TestHandlePiOutput_ThinkingLevelChanged_IgnoresTheEchoOfItsOwnRequest(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.thinkingLevel = "medium"

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"thinking_level_changed","level":"medium"}`)))

	assert.Zero(t, sink.SettingsRefreshCount())
	assert.Empty(t, sink.Messages())
}

// TestHandlePiOutput_ThinkingLevelChanged_IgnoresAFrameWithNoLevel refuses to store an
// empty level, which would blank the effort segment.
func TestHandlePiOutput_ThinkingLevelChanged_IgnoresAFrameWithNoLevel(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.thinkingLevel = "high"

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"thinking_level_changed"}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"thinking_level_changed","level":`)))

	a.Mu.Lock()
	level := a.thinkingLevel
	a.Mu.Unlock()
	assert.Equal(t, "high", level)
	assert.Zero(t, sink.SettingsRefreshCount())
}

// TestHandlePiOutput_SummarizationRetries_PersistAsNotifications pins the three retry
// events onto the notification channel, beside the compaction and auto-retry events
// they belong with. Each one used to draw a raw JSON row.
func TestHandlePiOutput_SummarizationRetries_PersistAsNotifications(t *testing.T) {
	t.Parallel()

	for name, raw := range map[string]string{
		"scheduled":    `{"type":"summarization_retry_scheduled","attempt":1,"maxAttempts":3,"delayMs":2000,"errorMessage":"overloaded"}`,
		"attemptStart": `{"type":"summarization_retry_attempt_start","source":"compaction","reason":"threshold"}`,
		"finished":     `{"type":"summarization_retry_finished"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newPiAgentWithSink(agent.NewProviderServices(sink))
			handlePiOutput(a, providerkit.ParseLine([]byte(raw)))

			require.Len(t, sink.PersistedNotifications(), 1)
			assert.JSONEq(t, raw, string(sink.PersistedNotifications()[0].Content))
			assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, sink.PersistedNotifications()[0].Source)
			assert.Empty(t, sink.Messages(), "a notification is not a plain message row")
		})
	}
}

// Pi sends the partial result WHOLE on every update, so the tail the running row
// draws is that text. The truncation flag travels beside it, because the row must
// say when the bytes it shows are a window on more.
func TestHandlePiOutput_ToolExecutionUpdateReportsTheOutputTail(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_start","toolCallId":"call-1","toolName":"bash","args":{"command":"ls"}}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_update","toolCallId":"call-1","toolName":"bash","partialResult":{"content":[{"type":"text","text":"file1\n"}]}}`)))
	assert.Contains(t, sink.ProgressUpdates(), agent.OutputTailProgress("call-1", "file1\n", false))

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_update","toolCallId":"call-1","toolName":"bash","partialResult":{"content":[{"type":"text","text":"file1\nfile2\n"}],"details":{"truncation":{"totalBytes":4096,"truncated":true}}}}`)))
	assert.Contains(t, sink.ProgressUpdates(), agent.OutputTailProgress("call-1", "file1\nfile2\n", true))
}

// A goal marker refreshes the goal panel whatever entry type carries it.
//
// Pi states the marker in `customType`, which is a field of EVERY entry, so the check
// sits ABOVE the switch on the entry type. Under that switch, a marker written on an
// entry type this worker does not know left the panel showing a goal the session had
// already moved past.
func TestHandlePiOutput_EntryAppendedGoalMarkerReachesThePanel(t *testing.T) {
	t.Parallel()

	for _, entryType := range []string{"custom", "goal_event"} {
		t.Run(entryType, func(t *testing.T) {
			t.Parallel()
			a := newPiAgentWithSink(agent.NewProviderServices(&agenttest.ControlSink{}))
			a.SetContextForTest(context.Background())
			a.Mu.Lock()
			a.sessionID = "session"
			a.extensionCommands = map[string]bool{piGoalCommands[agent.GoalActionSet]: true}
			// A refresh that already runs absorbs this one, so the hint lands on the
			// revision and no refresh goroutine starts.
			a.goal.running = true
			a.Mu.Unlock()

			handlePiOutput(a, providerkit.ParseLine([]byte(
				`{"type":"entry_appended","entry":{"type":"`+entryType+`","id":"e1","customType":"pi-goal-focus"}}`)))

			a.Mu.Lock()
			revision := a.goal.revision
			a.Mu.Unlock()
			assert.Equal(t, uint64(1), revision, "the goal marker asks for a refresh")
		})
	}
}

// piOutputTail reports the tail one update broadcast for a tool call.
func piOutputTail(t *testing.T, sink *agenttest.ControlSink, scopeID string) agent.ProgressUpdate {
	t.Helper()
	for _, update := range sink.ProgressUpdates() {
		if update.Operation == agent.ProgressOutputTail && update.ScopeID == scopeID {
			return update
		}
	}
	require.FailNow(t, "no output tail reached the sink", "scope %q", scopeID)
	return agent.ProgressUpdate{}
}

// piToolUpdateLine builds one tool_execution_update whose partial result holds text.
func piToolUpdateLine(t *testing.T, text, details string) []byte {
	t.Helper()
	encoded, err := json.Marshal(text)
	require.NoError(t, err)
	line := `{"type":"tool_execution_update","toolCallId":"call-1","toolName":"bash",` +
		`"partialResult":{"content":[{"type":"text","text":` + string(encoded) + `}]`
	if details != "" {
		line += `,"details":` + details
	}
	return []byte(line + `}}`)
}

// Only the last bytes of a running tool reach a reader, so the tail is capped here.
// The cap is what keeps the join off the accumulated output, which Pi re-sends whole
// on every update -- and the cap drops head bytes, which the report must state.
func TestHandlePiOutput_ToolExecutionUpdateCapsTheOutputTail(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	long := strings.Repeat("x", piLiveOutputLimit+512)
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_start","toolCallId":"call-1","toolName":"bash","args":{"command":"ls"}}`)))
	handlePiOutput(a, providerkit.ParseLine(piToolUpdateLine(t, long, "")))

	tail := piOutputTail(t, sink, "call-1")
	assert.Equal(t, piLiveOutputLimit, len(tail.Text), "the tail carries the cap and no more")
	assert.Equal(t, long[len(long)-piLiveOutputLimit:], tail.Text, "the LAST bytes are the tail")
	assert.True(t, tail.Truncated, "the cap dropped the head of the output")
	// The counter still sees the whole snapshot, so the total is exact.
	assert.Contains(t, sink.ProgressUpdates(), agent.OutputTotalProgress("call-1", int64(len(long)), false))
}

// The cap cuts at a byte offset, and that offset can land inside a rune. A cut that
// split one would send a replacement character to the browser.
func TestHandlePiOutput_ToolExecutionUpdateCutsTheTailAtARuneBoundary(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	long := strings.Repeat("\uac00", piLiveOutputLimit) // three bytes each
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_start","toolCallId":"call-1","toolName":"bash","args":{"command":"ls"}}`)))
	handlePiOutput(a, providerkit.ParseLine(piToolUpdateLine(t, long, "")))

	tail := piOutputTail(t, sink, "call-1")
	assert.True(t, utf8.ValidString(tail.Text), "the tail starts on a rune boundary")
	assert.LessOrEqual(t, len(tail.Text), piLiveOutputLimit)
	assert.True(t, strings.HasSuffix(long, tail.Text))
}

// A snapshot Pi TRUNCATED is not append-only: its head is gone, so its length is not
// the total. The counter measures growth by the overlap of two snapshots, and the
// counter must be told -- the flag used to be a hard-coded false, so a window that
// had lost its head reported its own length as the exact byte total.
func TestHandlePiOutput_ToolExecutionUpdateCountsATruncatedSnapshotByItsGrowth(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	truncated := `{"truncation":{"truncated":true}}`
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_start","toolCallId":"call-1","toolName":"bash","args":{"command":"ls"}}`)))
	handlePiOutput(a, providerkit.ParseLine(piToolUpdateLine(t, "abcdef", truncated)))
	handlePiOutput(a, providerkit.ParseLine(piToolUpdateLine(t, "cdefgh", truncated)))

	// Six bytes, then two more that the overlap of the two windows shows. A counter
	// that read the second window as an append-only snapshot answered six again.
	assert.Contains(t, sink.ProgressUpdates(), agent.OutputTotalProgress("call-1", 8, true))
}

// Pi appends one session-file entry for every step of its own bookkeeping. Most
// of them repeat a fact the event stream already stated, and each used to reach
// the transcript as an unrecognized row that drew raw JSON.
func TestHandlePiOutput_EntryAppendedDrawsOnlyWhatTheStreamDoesNotState(t *testing.T) {
	t.Parallel()

	t.Run("keeps an extension's own entry", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.ControlSink{}
		a := newPiAgentWithSink(agent.NewProviderServices(sink))
		raw := []byte(`{"type":"entry_appended","entry":{"type":"custom","id":"e1","customType":"pi-subagents","data":{}}}`)
		handlePiOutput(a, providerkit.ParseLine(raw))
		require.Len(t, sink.Messages(), 1)
		assert.Equal(t, raw, sink.Messages()[0].Content)
	})

	for _, entryType := range []string{"message", "compaction", "branch_summary", "label", "session_info", "thinking_level_change"} {
		t.Run("draws no row for "+entryType, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newPiAgentWithSink(agent.NewProviderServices(sink))
			handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"entry_appended","entry":{"type":"`+entryType+`","id":"e1"}}`)))
			assert.Empty(t, sink.Messages())
		})
	}

	// An entry type this worker does not know still reaches the transcript. A drop
	// left the reader nothing and the worker a log line the reader never sees, which
	// is the same case the unmarshal path already answers with the raw frame.
	t.Run("keeps an entry type it does not know", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.ControlSink{}
		a := newPiAgentWithSink(agent.NewProviderServices(sink))
		raw := []byte(`{"type":"entry_appended","entry":{"type":"goal_event","id":"e1"}}`)
		handlePiOutput(a, providerkit.ParseLine(raw))
		require.Len(t, sink.Messages(), 1)
		assert.Equal(t, raw, sink.Messages()[0].Content)
	})

	// A model change is a SETTINGS fact, and it goes through the same pipeline
	// every other axis uses -- a transcript row beside that notification would
	// state it twice.
	t.Run("announces a model change through the settings pipeline", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.ControlSink{}
		a := newPiAgentWithSink(agent.NewProviderServices(sink))
		a.model, a.provider, a.thinkingLevel = "old-model", "anthropic", "medium"
		handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"entry_appended","entry":{"type":"model_change","id":"e1","provider":"openai","modelId":"gpt-5"}}`)))
		assert.Empty(t, sink.Messages())
		require.Equal(t, 1, sink.SettingsRefreshCount())
		refresh := sink.LastSettingsRefresh()
		assert.Equal(t, "gpt-5", refresh.Model)
		assert.Equal(t, "medium", refresh.Effort)
		assert.Equal(t, "openai", refresh.Options[OptionProvider])
	})

	// A repeat of the model the agent already runs announces nothing.
	t.Run("stays quiet when the model did not move", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.ControlSink{}
		a := newPiAgentWithSink(agent.NewProviderServices(sink))
		a.model, a.provider = "gpt-5", "openai"
		handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"entry_appended","entry":{"type":"model_change","id":"e1","provider":"openai","modelId":"gpt-5"}}`)))
		assert.Equal(t, 0, sink.SettingsRefreshCount())
	})
}

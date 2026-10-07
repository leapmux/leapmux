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

	"github.com/leapmux/leapmux/generated/contracts"
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
	tool := []byte(`{"type":"tool_execution_start","toolCallId":"mcp-call","toolName":"mcp__probe__write","args":{"path":"sample.py","content":"complete arguments"}}`)
	handlePiOutput(a, providerkit.ParseLine(tool))
	dialog := []byte(`{"type":"extension_ui_request","id":"permission","method":"confirm","title":"Allow mcp__probe__write?","message":"Run the native MCP tool?"}`)
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
	a.sink = agent.NewModelProgressResetSink(a.sink)
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
	// agent_end broadcasts no session information.
	// The usage snapshot is empty, and get_session_stats requires live stdin.
	// Any broadcast here reports an unexpected key.
	assert.Equal(t, 0, sink.SessionInfoCount())
}

// piFakeClock returns a clock and a function that advances it.
// The caller advances the clock between events.
// Handler reads must not advance it.
// Otherwise, added or removed reads change the expected durations.
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

// agent_settled states that Pi will not continue after agent_end.
// agent_end already drew the divider.
// The handler must drop agent_settled so the default case cannot persist a raw JSON row.
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

// A zero-duration turn records zero.
// The frontend displays (0ms) for that value.
// An absent duration displays no time.
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

// The worker cannot measure a turn whose start it did not observe.
// This includes a process adopted during a turn.
// Omit the duration field.
func TestHandlePiOutput_AgentEnd_WithoutStartOmitsDuration(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","messages":[]}`)))

	assert.NotContains(t, piPersistedAgentEnd(t, sink, 0), "duration_ms")
}

// agent_end clears the start time when the turn ends.
// A second agent_end must omit the duration.
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

// A retry continues the same turn.
// The final divider must report the complete duration, including earlier attempts.
func TestHandlePiOutput_AgentEnd_RetryKeepsTurnStartMark(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	advance := piFakeClock(a, piEpoch)

	// The turn contains two runs.
	// The first attempt takes one second.
	// The backoff and retry each take one second.
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

// Pi's own retry keeps the turn open.
// The handler must not emit the turn-end event.
// That event controls the completion sound and the inactive tab's dot.
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
	// Keep Pi's original tool_execution_start bytes.
	// A previous implementation wrote an end frame that Pi did not send.
	// It also marked an error that Pi did not report.
	assert.JSONEq(t,
		`{"type":"tool_execution_start","toolCallId":"tool-1","toolName":"bash","args":{"command":"printf partial"}}`,
		string(result.Content))
	assert.JSONEq(t, `{
		"toolCallId":"tool-1",
		"toolName":"bash",
		"partialResult":{"content":[{"type":"text","text":"partial output"}],"details":{}}
	}`, string(result.SupplementalContent))
	assert.Equal(t, agent.MessageCompletionError, result.Completion)

	// The start frame and supplement resolve to one completed call.
	// Every extractor must receive its output.
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

// Pi retries the WebSocket failure when willRetry is true.
// LeapMux must not schedule another continuation for that failure.
// These envelopes differ only in willRetry.
// Pi reports false after it exhausts its retry budget.
// LeapMux may then continue the session.
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

// Pi ends the assistant message that a stop cut with its partial text and a stop
// reason: `aborted` for a clean abort, and `error` while a tool still ran (see
// Interrupt). The row keeps Pi's frame and states the stop.
func TestHandlePiOutput_MessageEnd_MarksTheTextThatAStopCut(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		stopReason string
		noted      bool
	}{
		"a clean abort":                        {stopReason: "aborted"},
		"a clean abort that LeapMux asked for": {stopReason: "aborted", noted: true},
		"an abort while a tool ran":            {stopReason: contracts.PiStopReasonError, noted: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newPiAgentWithSink(agent.NewProviderServices(sink))
			a.Mu.Lock()
			a.currentTurnActive = true
			a.Mu.Unlock()
			if tc.noted {
				noteDeliveredPiInterrupt(a)
			}

			raw := []byte(`{"type":"message_end","message":{"role":"assistant","stopReason":"` + tc.stopReason + `","content":[{"type":"text","text":"Half a sen"}]}}`)
			handlePiOutput(a, providerkit.ParseLine(raw))

			require.Equal(t, 1, sink.MessageCount())
			assert.JSONEq(t, string(raw), string(sink.Messages()[0].Content), "the row keeps Pi's own frame")
			assert.Equal(t, agent.MessageCompletionInterrupted, sink.Messages()[0].Completion)
		})
	}
}

// The marker states truncated TEXT, so a message whose text a stop did not cut keeps
// none: a real failure, a message that finished, and a cut message with no text.
func TestHandlePiOutput_MessageEnd_LeavesAMessageThatNoStopCut(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		message string
		noted   bool
	}{
		"a failure that nobody asked for":     {message: `{"role":"assistant","stopReason":"error","content":[{"type":"text","text":"Half"}]}`},
		"a message that finished at the stop": {message: `{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"Whole."}]}`, noted: true},
		"a cut message with a tool call only": {message: `{"role":"assistant","stopReason":"aborted","content":[{"type":"toolCall","id":"call-1","name":"bash"}]}`, noted: true},
		"a cut message with blank text":       {message: `{"role":"assistant","stopReason":"aborted","content":[{"type":"text","text":"  "}]}`, noted: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newPiAgentWithSink(agent.NewProviderServices(sink))
			a.Mu.Lock()
			a.currentTurnActive = true
			a.Mu.Unlock()
			if tc.noted {
				noteDeliveredPiInterrupt(a)
			}

			handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"message_end","message":`+tc.message+`}`)))

			require.Equal(t, 1, sink.MessageCount())
			assert.Equal(t, agent.MessageCompletion(""), sink.Messages()[0].Completion)
		})
	}
}

// piReasoningRow is the assembled-message envelope of one completed thinking row.
func piReasoningRow(text string) map[string]string {
	return map[string]string{
		contracts.AssembledMessageFieldType:       contracts.AssembledMessageType,
		contracts.AssembledMessageFieldKind:       contracts.AssembledMessageKindReasoning,
		contracts.AssembledMessageFieldText:       text,
		contracts.AssembledMessageFieldCompletion: contracts.AssembledMessageCompletionComplete,
	}
}

// piAssembledRow decodes a persisted assembled-message envelope.
func piAssembledRow(t *testing.T, message agenttest.Message) map[string]string {
	t.Helper()
	var row map[string]string
	require.NoError(t, json.Unmarshal(message.Content, &row))
	return row
}

// Pi stores reasoning, text, and tool calls in one assistant message.
// Each transcript row displays one text kind.
// The worker must persist reasoning separately before the assistant reply.
func TestHandlePiOutput_MessageEnd_PersistsThinkingAsARowOfItsOwn(t *testing.T) {
	t.Parallel()

	run := func(t *testing.T, frame string) []agenttest.Message {
		t.Helper()
		sink := &agenttest.ControlSink{}
		a := newPiAgentWithSink(agent.NewProviderServices(sink))
		a.model = "gpt-5.5"
		a.availableModels = []*agent.ModelInfo{{Id: "gpt-5.5", ContextWindow: 200000}}
		handlePiOutput(a, providerkit.ParseLine([]byte(frame)))
		return sink.Messages()
	}

	t.Run("the thinking row comes before the reply, which keeps the usage", func(t *testing.T) {
		t.Parallel()
		frame := `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"The user wants a greeting.","thinkingSignature":"sig"},{"type":"text","text":"Hello"}],"usage":{"input":100,"output":10,"cacheRead":0,"cacheWrite":0,"totalTokens":110,"cost":{"total":0.0001}}}}`
		messages := run(t, frame)
		require.Len(t, messages, 2)
		assert.Equal(t, piReasoningRow("The user wants a greeting."), piAssembledRow(t, messages[0]))
		assert.Empty(t, messages[0].Metadata, "the usage rides on the reply's own row")
		assert.JSONEq(t, frame, string(messages[1].Content), "Pi's own frame is still the reply's row")
		assert.NotEmpty(t, messages[1].Metadata)
	})

	t.Run("several thinking blocks join into one row", func(t *testing.T) {
		t.Parallel()
		messages := run(t, `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"First."},{"type":"text","text":"Hi."},{"type":"thinking","thinking":"Second."}]}}`)
		require.Len(t, messages, 2)
		assert.Equal(t, piReasoningRow("First.\n\nSecond."), piAssembledRow(t, messages[0]))
	})

	t.Run("thinking beside a tool call is a row of its own too", func(t *testing.T) {
		t.Parallel()
		messages := run(t, `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Run it."},{"type":"toolCall","id":"call_1","name":"bash","arguments":{"command":"ls"}}],"stopReason":"toolUse"}}`)
		require.Len(t, messages, 2)
		assert.Equal(t, piReasoningRow("Run it."), piAssembledRow(t, messages[0]))
	})

	// Pi supplies a placeholder for redacted reasoning.
	// The reasoning row must display that placeholder.
	t.Run("a redacted block shows Pi's placeholder", func(t *testing.T) {
		t.Parallel()
		messages := run(t, `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"[Reasoning redacted]","thinkingSignature":"opaque","redacted":true},{"type":"text","text":"Hi."}]}}`)
		require.Len(t, messages, 2)
		assert.Equal(t, piReasoningRow("[Reasoning redacted]"), piAssembledRow(t, messages[0]))
	})

	t.Run("a thinking block with no visible text adds no row", func(t *testing.T) {
		t.Parallel()
		frame := `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"  ","thinkingSignature":"sig"},{"type":"text","text":"Hi."}]}}`
		messages := run(t, frame)
		require.Len(t, messages, 1)
		assert.JSONEq(t, frame, string(messages[0].Content))
	})

	t.Run("a message of another role adds no row", func(t *testing.T) {
		t.Parallel()
		frame := `{"type":"message_end","message":{"role":"user","content":[{"type":"thinking","thinking":"Not the model's."},{"type":"text","text":"Hi."}]}}`
		messages := run(t, frame)
		require.Len(t, messages, 1)
		assert.JSONEq(t, frame, string(messages[0].Content))
	})

	t.Run("a message whose content is a string adds no row", func(t *testing.T) {
		t.Parallel()
		frame := `{"type":"message_end","message":{"role":"assistant","content":"Hi."}}`
		messages := run(t, frame)
		require.Len(t, messages, 1)
		assert.JSONEq(t, frame, string(messages[0].Content))
	})
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

func TestHandlePiOutput_AgentEnd_UnexpectedMessagesShapeLeavesAutoContinueUnchanged(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	// messages contains a string instead of an array.
	// handlePiAgentEndObserved cannot decode the envelope.
	// An invalid envelope must not change continuation state.
	raw := []byte(`{"type":"agent_end","messages":"unexpected"}`)

	handlePiOutput(a, providerkit.ParseLine(raw))

	require.Zero(t, sink.AutoCancelCount())
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
	// Check the span type before closing the span.
	// Closing removes the stored type.
	assert.Equal(t, "bash", sink.GetSpanType("call-1"))

	handlePiOutput(a, providerkit.ParseLine(updateRaw))
	handlePiOutput(a, providerkit.ParseLine(endRaw))

	// Persist the start frame and the closing result.
	msgs := sink.Messages()
	require.Equal(t, 2, len(msgs), "tool_execution start/end should persist two messages")
	assert.Equal(t, "call-1", msgs[0].SpanID)
	assert.Equal(t, "bash", msgs[0].SpanType)
	assert.False(t, msgs[0].Closing, "start should not be marked closing")
	assert.Equal(t, "call-1", msgs[1].SpanID)
	assert.True(t, msgs[1].Closing, "end should be marked closing")

	// Open the span, then close it.
	assert.Equal(t, []agenttest.SpanOpen{{SpanID: "call-1", ParentSpanID: ""}}, sink.OpenSpans())
	assert.Equal(t, []string{"call-1"}, sink.ClosedSpans())

	updates := sink.ProgressUpdates()
	assert.Contains(t, updates, agent.OutputTotalProgress("call-1", 6, false))
	assert.Contains(t, updates, agent.CompleteOutputProgress("call-1"))

	// The handler increments the tool count.
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

// Pi sends a cumulative partialResult on each tool_execution_update.
// Broadcast only the new delta.
// Clear the span's counter when the tool ends.
// A later call with the same ID must start at zero.
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

	// tool_execution_end clears the span's counter.
	// A new call with the same ID must start at zero.
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
			// Keep the raw line unchanged in the payload.
			assert.JSONEq(t, tc.raw, string(persisted[0].Payload))

			// Do not persist the dialog as a message or notification.
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

	// Persist the raw frame without an agent_notify wrapper.
	require.Equal(t, 1, sink.NotificationCount(), "single raw extension_ui_request notification persisted")
	last := sink.LastNotification()
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, last.Source,
		"Pi-emitted notify must persist as AGENT (the agent is the source)")
	assert.JSONEq(t, rawLine, string(last.Content),
		"raw envelope must be preserved verbatim so renderers can read every method-specific field")

	// The frontend reads the notification level and message from the raw envelope.
	assert.Empty(t, sink.Notifications(),
		"synthesized agent_notify must no longer be emitted; raw passthrough alone carries the same info")

	// The notification must not create a control request.
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
	// The frame omits notifyType.
	// The renderer must use info as the default.
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

	// The frame omits statusText.
	// Broadcast nil so the frontend clears the key.
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

	// No pending caller accepts this response.
	// Log it and drop it.
	// Do not persist it.
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

			// The first eight-character delta adds two tokens.
			// A second delta of the same length raises the total to four.
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

	// Pi streams reasoning and text within one message.
	// One message_end persists both.
	// No phase split or assistant commit separates them.
	// ACP resets at that separation.
	// Pi must keep one reasoning-token phase across its transition to text.
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

	// Persisting the assistant message ends the phase.
	// Reset the estimate.
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

	// A blocking confirm dialog creates a live control request.
	// The frontend clears its counter for that request.
	// The backend must also reset its counter.
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

// TestHandlePiOutput_ToolUpdateDetailsUpsertsSubagentActivity checks the pi-subagents update shape.
// partialResult.details contains status and activity.
// Upsert a running registry row with activity as ActiveForm.
// Use details.agentId as its key, or toolCallId when agentId is absent.
func TestHandlePiOutput_ToolUpdateDetailsUpsertsSubagentActivity(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	// tool_execution_start reads the spawn prompt from input and uses it as the child title.
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"tool_execution_start","toolCallId":"call-sub-1","toolName":"Agent","input":{"description":"build the feature","prompt":"build it"}}`)))

	// The update carries the subagent fields in partialResult.details.
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

// TestHandlePiOutput_ToolUpdateDetails_FallsBackToToolCallID checks an update without agentId.
// Use toolCallId as the registry key when the remaining subagent fields match.
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

// TestHandlePiOutput_ToolEndBackgroundRekeysToAgentID checks a background result with agentId.
// Replace the provisional toolCallId key with details.agentId.
// Keep the row running and remove the provisional key.
func TestHandlePiOutput_ToolEndBackgroundRekeysToAgentID(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	// Create a running registry row with toolCallId as its key.
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

// TestHandlePiOutput_SubagentNotificationMessageClosesRegistryEntry checks the native completion notification.
// Read the final status from its details and close the registry row.
// Also persist the message in the parent transcript because it supplies conversation context.
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

	// The registry row must close.
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.StatusCompleted, tasks[0].Status,
		"subagent-notification with a final status must close the registry row")
	assert.True(t, tasks[0].Status.IsFinished())

	// Keep the notification after the tool_execution_start row in the parent transcript.
	// It must be the last persisted message.
	require.GreaterOrEqual(t, sink.MessageCount(), 1, "subagent-notification must still persist to the parent transcript")
	last := sink.Messages()[len(sink.Messages())-1]
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, last.Source)
	assert.Contains(t, string(last.Content), "subagent-notification")
}

// A description with no visible characters must use the prompt or tool-name fallback.
// Clean the description before checking whether it is empty.
// Otherwise, zero-width spaces select an empty description and leave the sidebar row without a label.
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

// piInterruptedAgentEnd records Pi's response when the user stops a turn with a running tool.
// Its error stop reason and prose also match a real failure.
// A clean abort uses stopReason aborted.
// The frame alone cannot distinguish an interruption from a failure.
// Capture: .tmp/provider-parity/interrupt5 (RL-015).
const piInterruptedAgentEnd = `{"type":"agent_end","messages":[{"role":"assistant","content":[],` +
	`"stopReason":"error","errorMessage":"This operation was aborted"}]}`

func TestHandlePiOutput_AgentEnd_AfterInterrupt_MarksTheTurnInterrupted(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.currentTurnActive = true
	noteDeliveredPiInterrupt(a)

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

// The interruption note belongs to one turn.
// If the process dies or the session changes before Pi ends that turn, clear the note.
// It must not label the next divider.
func TestHandlePiOutput_AgentStart_DropsAStaleInterruptNote(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.currentTurnActive = true
	noteDeliveredPiInterrupt(a)

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_start"}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","messages":[{"role":"assistant",`+
		`"stopReason":"error","errorMessage":"rate limit"}]}`)))

	require.Equal(t, 1, sink.MessageCount())
	assert.Empty(t, sink.Messages()[0].Completion)
}

// Pi's retry keeps the turn open.
// Retain the interruption note until the final agent_end.
func TestHandlePiOutput_AgentEnd_RetryKeepsTheInterruptNote(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.currentTurnActive = true
	noteDeliveredPiInterrupt(a)

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
	// A partial result can arrive without an observed start frame.
	// The call opens no row.
	// Its counter must still include the complete text.
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
	// Pi restarts a failed run within the same turn.
	// A retry cannot replace the session.
	// Repeated retries must not repeat the session probe.
	rig.agent.HandleOutput([]byte(`{"type":"agent_start"}`))
	rig.agent.HandleOutput([]byte(`{"type":"agent_start"}`))
	assert.Never(t, func() bool { return len(rig.requests()) > 1 }, 100*time.Millisecond, 5*time.Millisecond)
}

// TestHandlePiOutput_BashExecutionUpdate_PersistsNothing checks that shell output chunks create no transcript rows.
// Pi emits one event per chunk.
// The default case must not persist those events.
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

// TestHandlePiOutput_SessionInfoChanged_PersistsNothing checks that Pi's session-name event creates no row.
// LeapMux does not display that session name.
func TestHandlePiOutput_SessionInfoChanged_PersistsNothing(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"session_info_changed","name":"renamed session"}`)))

	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.PersistedNotifications())
}

// TestHandlePiOutput_ThinkingLevelChanged_RefreshesTheEffortSetting checks a model's lower effort limit.
// Pi reduces an unsupported level and reports the selected level.
// LeapMux must adopt that value because the effort control reads its stored field.
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
	assert.Empty(t, refresh.Model)
	assert.NotContains(t, refresh.Options, OptionProvider)
	assert.Empty(t, sink.Messages(), "the settings notification states the change; a raw row would repeat it")
}

// Pi sends a thinking event before set_model replies. The local model still holds
// the previous model. An effort event must not overwrite the requested model.
func TestHandlePiOutput_ThinkingLevelChanged_ReportsOnlyTheConfirmedAxis(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.model, a.provider, a.thinkingLevel = "previous-model", "previous-provider", "low"

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"thinking_level_changed","level":"off"}`)))

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "off", refresh.Effort)
	assert.Empty(t, refresh.Model, "an effort event confirms no model")
	assert.NotContains(t, refresh.Options, OptionProvider, "an effort event confirms no provider")
}

// A model event confirms no thinking level. Keep a concurrent effort selection.
func TestHandlePiOutput_ModelChange_ReportsOnlyTheConfirmedAxes(t *testing.T) {
	t.Parallel()

	for _, provider := range []string{"", "openai"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newPiAgentWithSink(agent.NewProviderServices(sink))
			a.model, a.provider, a.thinkingLevel = "old-model", "anthropic", "medium"

			a.handlePiModelChangeEntry(provider, "new-model")

			require.Equal(t, 1, sink.SettingsRefreshCount())
			refresh := sink.LastSettingsRefresh()
			assert.Equal(t, "new-model", refresh.Model)
			assert.Empty(t, refresh.Effort, "a model event confirms no effort")
			expected := map[string]string{}
			if provider != "" {
				expected[OptionProvider] = provider
			}
			assert.Equal(t, expected, refresh.Options)
		})
	}
}

// TestHandlePiOutput_ThinkingLevelChanged_IgnoresTheEchoOfItsOwnRequest checks Pi's reply to a selected effort.
// applyThinkingLevel already stores that level.
// The repeated announcement must create no transcript row.
func TestHandlePiOutput_ThinkingLevelChanged_IgnoresTheEchoOfItsOwnRequest(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.thinkingLevel = "medium"

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"thinking_level_changed","level":"medium"}`)))

	assert.Zero(t, sink.SettingsRefreshCount())
	assert.Empty(t, sink.Messages())
}

// TestHandlePiOutput_ThinkingLevelChanged_IgnoresAFrameWithNoLevel rejects an empty level.
// Storing it would clear the effort control.
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

// TestHandlePiOutput_SummarizationRetries_PersistAsNotifications checks all three summarization retry events.
// Send them through the notification channel with compaction and automatic retries.
// They must not create raw JSON rows.
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

// Pi sends the complete partial result on each update.
// The running row displays its final bytes.
// Include the truncation flag so the row can identify an incomplete output view.
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

// A goal marker must refresh the panel for every entry type.
// Pi can place customType on any entry.
// Check the marker before dispatching by entry type.
// Otherwise, an unknown entry type can leave an obsolete goal in the panel.
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
			// If a refresh already runs, update its revision hint.
			// Do not start another refresh goroutine.
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

// The running row receives only the output's final bytes.
// Cap that tail without joining the complete text that Pi repeats on every update.
// Report any removed bytes.
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

// The byte limit can divide a rune.
// Keep complete runes so the browser receives no replacement character.
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

// A truncated snapshot lacks its initial bytes, so its length is not the complete output size.
// The counter measures growth through overlapping snapshots.
// Pass the truncation flag to that counter.
// Otherwise, it reports the clipped length as the exact total.
func TestHandlePiOutput_ToolExecutionUpdateCountsATruncatedSnapshotByItsGrowth(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	truncated := `{"truncation":{"truncated":true}}`
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_start","toolCallId":"call-1","toolName":"bash","args":{"command":"ls"}}`)))
	handlePiOutput(a, providerkit.ParseLine(piToolUpdateLine(t, "abcdef", truncated)))
	handlePiOutput(a, providerkit.ParseLine(piToolUpdateLine(t, "cdefgh", truncated)))

	// The first snapshot contains six bytes.
	// The second reveals two more through overlap.
	// An append-only count would incorrectly report six again.
	assert.Contains(t, sink.ProgressUpdates(), agent.OutputTotalProgress("call-1", 8, true))
}

// Pi appends session entries for its internal operations.
// Most repeat facts that the event stream already reported.
// Those entries must not create duplicate raw JSON rows.
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

	// Keep an unknown entry type in the transcript.
	// A log alone gives the reader no message.
	// The decode-failure path also preserves the raw frame.
	t.Run("keeps an entry type it does not know", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.ControlSink{}
		a := newPiAgentWithSink(agent.NewProviderServices(sink))
		raw := []byte(`{"type":"entry_appended","entry":{"type":"goal_event","id":"e1"}}`)
		handlePiOutput(a, providerkit.ParseLine(raw))
		require.Len(t, sink.Messages(), 1)
		assert.Equal(t, raw, sink.Messages()[0].Content)
	})

	// A model change follows the shared settings pipeline.
	// An additional transcript row would repeat the notification.
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
		assert.Empty(t, refresh.Effort)
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

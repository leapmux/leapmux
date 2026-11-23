package goose

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newGooseOutputAgent(sink agent.ProviderServices) *Agent {
	a := &Agent{}
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "goose-1", ProviderName: "goose"})
	a.SetSinkForTest(sink)
	a.SetSessionIDForTest("s1")
	// The start's Configure installs these hooks; a HandleOutput-driven test needs
	// the same bindings.
	a.HooksForTest().ExtraMethod = a.handleGooseExtraMethod
	a.HooksForTest().SessionUpdateHandler = a.observeGooseSessionUpdate
	a.HooksForTest().PromptEnded = a.finishGoosePrompt
	return a
}

// gooseExtraMethod feeds one `_goose/unstable/session/update` and reports
// whether the provider claimed it.
func gooseExtraMethod(a *Agent, params string) bool {
	return a.handleGooseExtraMethod(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","method":"_goose/unstable/session/update","params":` + params + `}`)))
}

// A frame of another session is ignored: Goose resumes one session per agent,
// and a frame that names a different one belongs to no transcript here.
func TestGooseSessionUpdateIgnoresAnotherSession(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newGooseOutputAgent(agent.NewProviderServices(sink))
	claimed := gooseExtraMethod(a, `{"sessionId":"s2","update":{"sessionUpdate":"usage_update","used":1200,"contextLimit":200000,
		"accumulatedInputTokens":900,"accumulatedOutputTokens":300,"accumulatedCost":0.0125}}`)
	assert.True(t, claimed, "a foreign frame must not fall through and persist as a row of this agent")
	assert.Zero(t, sink.SessionInfoCount())
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.Notifications())
}

// goal-1.log line 25: the status notice of a session goal, verbatim.
func TestGooseSessionUpdateNotice(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newGooseOutputAgent(agent.NewProviderServices(sink))
	claimed := gooseExtraMethod(a, `{"sessionId":"s1","update":{"sessionUpdate":"status_message","status":{"type":"notice","message":"Goal: Reply with the single word OK."}}}`)
	assert.True(t, claimed)
	require.Len(t, sink.Notifications(), 1)
	assert.Equal(t, contracts.NotificationTypeAgentStatus, sink.Notifications()[0]["type"])
	assert.Equal(t, "Goal: Reply with the single word OK.", sink.Notifications()[0]["text"])
}

// `message_usage` states a per-message cost that the cumulative total beside it
// already carries, and a replay resends every one of them, so adding them up
// would count the turn twice. It is claimed and dropped.
func TestGooseSessionUpdateDropsPerMessageUsage(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newGooseOutputAgent(agent.NewProviderServices(sink))
	claimed := gooseExtraMethod(a, `{"sessionId":"s1","update":{"sessionUpdate":"message_usage","messageId":"msg_1",
		"usage":{"inputTokens":100,"outputTokens":20,"totalTokens":120,"cost":0.004,"elapsedMs":1500}}}`)
	assert.True(t, claimed)
	assert.Zero(t, sink.SessionInfoCount())
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.Notifications())
}

// `live_voice_interaction_ended` cannot arrive, because LeapMux starts no live
// voice. The case states the claim so the variant has a verdict.
func TestGooseSessionUpdateClaimsTheLiveVoiceEnd(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newGooseOutputAgent(agent.NewProviderServices(sink))
	claimed := gooseExtraMethod(a, `{"sessionId":"s1","update":{"sessionUpdate":"live_voice_interaction_ended","interactionId":"iv_1","outcome":"stopped"}}`)
	assert.True(t, claimed)
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.Notifications())
	assert.Zero(t, sink.SessionInfoCount())
}

// An unknown variant persists byte for byte: the handler releases the line and
// the shared dispatcher stores it as a raw row (decision 1).
func TestGooseSessionUpdatePersistsAnUnknownVariant(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newGooseOutputAgent(agent.NewProviderServices(sink))
	claimed := gooseExtraMethod(a, `{"sessionId":"s1","update":{"sessionUpdate":"future_thing","payload":{"kind":"new"}}}`)
	assert.False(t, claimed, "an unknown variant must reach the shared dispatcher, which persists the raw line")
	assert.Empty(t, sink.Messages(), "the unit contract is the release; the dispatcher test persists the row")
}

// The `message` type carries an MCP log line for the running call. A string
// `params.data` becomes the running row's tail, with a level prefix for warning
// and above; any other value is claimed and dropped, because a log line of
// another shape states nothing a reader can use.
func TestGooseToolNotificationMessage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		params   string
		wantTail string
	}{
		{"an info line carries no prefix", `{"level":"info","logger":"db","data":"cache warmed"}`, "cache warmed"},
		{"a warning line carries its level", `{"level":"warning","data":"cache miss"}`, "[warning] cache miss"},
		{"an error line carries its level", `{"level":"error","data":"query failed"}`, "[error] query failed"},
		{"an emergency line carries its level", `{"level":"emergency","data":"connection lost"}`, "[emergency] connection lost"},
		{"a notice line carries no prefix", `{"level":"notice","data":"reconnected"}`, "reconnected"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newGooseOutputAgent(agent.NewProviderServices(sink))
			claimed := a.observeGooseToolNotification(acp.ToolCallUpdateEnvelope{
				ToolCallID: "tool-1",
				Meta:       json.RawMessage(`{"toolNotification":{"type":"message","params":` + testCase.params + `}}`),
			})
			require.True(t, claimed, "the message type is claimed")
			require.Len(t, sink.ProgressUpdates(), 1)
			last := sink.ProgressUpdates()[0]
			assert.Equal(t, agent.ProgressOutputTail, last.Operation)
			assert.Equal(t, "tool-1", last.ScopeID)
			assert.Equal(t, testCase.wantTail, last.Text)
			assert.Empty(t, sink.Messages(), "a log line is live output, not a transcript row")
		})
	}
	t.Run("a non-string data value is dropped", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newGooseOutputAgent(agent.NewProviderServices(sink))
		claimed := a.observeGooseToolNotification(acp.ToolCallUpdateEnvelope{
			ToolCallID: "tool-1",
			Meta:       json.RawMessage(`{"toolNotification":{"type":"message","params":{"level":"info","data":{"object":true}}}}`),
		})
		assert.True(t, claimed)
		assert.Empty(t, sink.ProgressUpdates())
		assert.Empty(t, sink.Messages())
	})
}

// gooseNoticeOrderSink records the assembled text rows at the moment each
// notice persists, so the test can state which text the notice follows.
type gooseNoticeOrderSink struct {
	*agenttest.ControlSink
	textsAtNotice [][]string
}

func (s *gooseNoticeOrderSink) PersistLeapMuxNotification(content map[string]interface{}) {
	s.textsAtNotice = append(s.textsAtNotice, gooseAssembledTexts(&s.Sink))
	s.ControlSink.PersistLeapMuxNotification(content)
}

func gooseAssembledTexts(sink *agenttest.Sink) []string {
	var texts []string
	for _, message := range sink.Messages() {
		if kind, text, _, ok := agenttest.DecodeAssembledMessage(message.Content); ok && kind == agent.AssembledMessageKindText {
			texts = append(texts, text)
		}
	}
	return texts
}

// goal-1.log lines 18, 21, 25 and 27: a text, a second text of its own message,
// the goal notice, then a third text. The notice must follow the text before
// it, not land above both.
func TestGooseNoticeOrder(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	ordered := &gooseNoticeOrderSink{ControlSink: sink}
	a := newGooseOutputAgent(agent.NewProviderServices(ordered))
	a.SetPromptActiveForTest(true)
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Goal set. The agent will verify this goal is met before finishing:\n\n> Reply with the single word OK."},"messageId":"msg_ac7bb71d-b59a-444f-b673-34190813765e"}}}`))
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"OK"},"messageId":"probe-2"}}}`))
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"_goose/unstable/session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"status_message","status":{"type":"notice","message":"Goal: Reply with the single word OK."}}}}`))
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"OK"},"messageId":"probe-3"}}}`))
	a.FinishPromptRequestForTest("s1", json.RawMessage(`{"stopReason":"end_turn","usage":{"totalTokens":10}}`), nil)
	require.Len(t, ordered.textsAtNotice, 1)
	assert.Equal(t, []string{
		"Goal set. The agent will verify this goal is met before finishing:\n\n> Reply with the single word OK.",
		"OK",
	}, ordered.textsAtNotice[0], "the notice must follow the text of its own messages")
	assert.Equal(t, []string{
		"Goal set. The agent will verify this goal is met before finishing:\n\n> Reply with the single word OK.",
		"OK",
		"OK",
	}, gooseAssembledTexts(&sink.Sink))
}

// gooseUnknownUpdateLine builds one `_goose/unstable/session/update` line.
func gooseUnknownUpdateLine(t *testing.T, sessionID string, update map[string]any) []byte {
	t.Helper()
	params, err := json.Marshal(map[string]any{"sessionId": sessionID, "update": update})
	require.NoError(t, err)
	return []byte(`{"jsonrpc":"2.0","method":"_goose/unstable/session/update","params":` + string(params) + `}`)
}

// An unknown `_goose/unstable/session/update` variant of the current session
// persists byte for byte; one of another session reaches nothing.
func TestGooseUnknownSessionUpdatePersists(t *testing.T) {
	t.Parallel()
	agenttest.AssertUnknownFramePersists(t, []agenttest.UnknownFrameCase{
		{Name: "current session", Frame: gooseUnknownUpdateLine(t, "s1", map[string]any{"sessionUpdate": "future_thing", "value": 1})},
		{Name: "other session", Frame: gooseUnknownUpdateLine(t, "s-other", map[string]any{"sessionUpdate": "future_thing", "value": 1}), OtherSession: true},
	}, func(t *testing.T, tc agenttest.UnknownFrameCase) *agenttest.Sink {
		sink := &agenttest.Sink{}
		a := newGooseOutputAgent(agent.NewProviderServices(sink))
		a.HandleOutput(tc.Frame)
		return sink
	})
}

func TestGooseLiveOutputKeepsTheShellIdentity(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newGooseOutputAgent(agent.NewProviderServices(sink))
	a.HooksForTest().ToolNotification = a.observeGooseToolNotification
	a.HooksForTest().ToolOutput = a.gooseToolOutput
	opening := `{"sessionUpdate":"tool_call","toolCallId":"shell-1","rawInput":{"command":"printf hello"},"_meta":{"goose":{"toolCall":{"toolName":"shell","extensionName":"developer"}}}}`
	live := `{"sessionUpdate":"tool_call_update","toolCallId":"shell-1","status":"in_progress","_meta":{"toolNotification":{"type":"live_output","params":{"sequence":1,"chunks":[{"stream":"stdout","output":"hello"}]}}}}`
	final := `{"sessionUpdate":"tool_call_update","toolCallId":"shell-1","status":"completed","content":[{"type":"content","content":{"type":"text","text":"hello"}}]}`
	for _, frame := range []string{opening, live, live, final} {
		a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":` + frame + `}}`))
	}
	messages := sink.Messages()
	require.Len(t, messages, 2)
	assert.JSONEq(t, final, string(messages[1].Content))
	resolved := acp.ResolveMessageContent(agent.MessageContent{Original: messages[1].Content, Supplemental: messages[1].SupplementalContent})
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(resolved, &fields))
	assert.JSONEq(t, `{"goose":{"toolCall":{"toolName":"shell","extensionName":"developer"}}}`, string(fields["_meta"]))
	progress := sink.ProgressUpdates()
	require.Len(t, progress, 3)
	assert.Equal(t, agent.ProgressOutputTotal, progress[0].Operation)
	assert.Equal(t, agent.ProgressOutputTail, progress[1].Operation)
	assert.Equal(t, "hello", progress[1].Text)
}

func TestGoosePromptFailureDoesNotRepeatObservedNativeError(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newGooseOutputAgent(agent.NewProviderServices(sink))
	a.SetPromptActiveForTest(true)
	const text = "Ran into this error: Failure42."
	for _, chunk := range []string{"Ran into this error: ", "Failure42."} {
		a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","messageId":"native-error","content":{"type":"text","text":` + jsonString(t, chunk) + `}}}}`))
	}
	a.FinishPromptRequestForTest("s1", nil, fmt.Errorf("wrapped request: %w", &providerkit.JSONRPCResponseError{Code: -32603, Message: text, Data: json.RawMessage(`{"reason":"turn_failed"}`)}))
	errors := 0
	for _, note := range sink.Notifications() {
		if note[contracts.NotificationFieldType] == contracts.NotificationTypeAgentError {
			errors++
		}
	}
	assert.Zero(t, errors, "the native error is already the conversation's failure note")
	retained := false
	for _, message := range sink.Messages() {
		retained = retained || strings.Contains(string(message.Content), "Failure42")
	}
	assert.True(t, retained, "deduplication keeps the native failure text")
}

func TestGooseNativeMessageAssemblyReusesItsBuffer(t *testing.T) {
	// Allocation counts include other goroutines. Keep this test sequential.
	const count = 512
	chunk := strings.Repeat("x", 128)
	update := func(id string) json.RawMessage {
		return json.RawMessage(`{"sessionUpdate":"agent_message_chunk","messageId":"` + id + `","content":{"type":"text","text":` + jsonString(t, chunk) + `}}`)
	}
	first, second := update("a"), update("b")
	measure := func(changeIdentity bool) float64 {
		a := newGooseOutputAgent(agent.NewProviderServices(&agenttest.Sink{}))
		return testing.AllocsPerRun(2, func() {
			a.Mu.Lock()
			a.promptOutput = goosePromptOutputState{}
			a.Mu.Unlock()
			for index := range count {
				raw := first
				if changeIdentity && index%2 != 0 {
					raw = second
				}
				a.observeGooseSessionUpdate("s1", a.Sink(), raw)
			}
		})
	}
	assert.Less(t, measure(false), measure(true), "one message should reuse its buffer across chunks")
}

func TestGoosePromptFailureNeedsExactObservedEvidence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, id, text, message, data string
		code                          int
	}{
		{"different message", "native", "partial answer", "failure", `{"reason":"turn_failed"}`, -32603},
		{"missing message ID", "", "failure", "failure", `{"reason":"turn_failed"}`, -32603},
		{"empty text", "native", "", "", `{"reason":"turn_failed"}`, -32603},
		{"different code", "native", "failure", "failure", `{"reason":"turn_failed"}`, -32000},
		{"missing data", "native", "failure", "failure", `null`, -32603},
		{"different reason", "native", "failure", "failure", `{"reason":"credits_exhausted"}`, -32603},
		{"malformed reason", "native", "failure", "failure", `{"reason":false}`, -32603},
		{"different long suffix", "native", strings.Repeat("x", 128*1024) + "a", strings.Repeat("x", 128*1024) + "b", `{"reason":"turn_failed"}`, -32603},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newGooseOutputAgent(agent.NewProviderServices(&agenttest.Sink{}))
			update, err := json.Marshal(map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": tc.id, "content": map[string]any{"type": "text", "text": tc.text}})
			require.NoError(t, err)
			a.observeGooseSessionUpdate("s1", a.Sink(), update)
			ending := a.finishGoosePrompt(&providerkit.JSONRPCResponseError{Code: tc.code, Message: tc.message, Data: json.RawMessage(tc.data)}, false)
			assert.False(t, ending.NoteWritten, "the host must retain errors without exact native evidence")
		})
	}
}

func TestGoosePromptFailureDoesNotReuseAnotherPromptsMessage(t *testing.T) {
	t.Parallel()
	a := newGooseOutputAgent(agent.NewProviderServices(&agenttest.Sink{}))
	a.observeGooseSessionUpdate("s1", a.Sink(), json.RawMessage(`{"sessionUpdate":"agent_message_chunk","messageId":"native-error","content":{"type":"text","text":"failure"}}`))
	err := &providerkit.JSONRPCResponseError{Code: -32603, Message: "failure", Data: json.RawMessage(`{"reason":"turn_failed"}`)}
	assert.True(t, a.finishGoosePrompt(err, false).NoteWritten)
	assert.False(t, a.finishGoosePrompt(err, false).NoteWritten, "a later failure without native text still needs a host note")
}

func TestGooseFinishPromptClosesOpenSubagents(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		err     error
		stopped bool
		want    bgtask.Status
	}{
		{name: "prompt error closes open subagents as failed", err: errors.New("turn failed"), stopped: false, want: bgtask.StatusFailed},
		{name: "prompt cancel closes open subagents as interrupted", err: errors.New("cancelled"), stopped: true, want: bgtask.StatusInterrupted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newGooseOutputAgent(agent.NewProviderServices(sink))
			a.HooksForTest().SubagentFromToolCall = a.observeGooseSubagentCall
			a.HooksForTest().SubagentFromToolCallUpdate = a.observeGooseSubagentUpdate
			a.HandleToolCallForTest(json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"delegate-1","title":"delegate","status":"pending","rawInput":{"instructions":"work"},"_meta":{"goose":{"toolCall":{"toolName":"delegate","extensionName":"summon"}}}}`))
			a.HandleToolCallUpdateForTest(json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"delegate-1","status":"completed","content":[{"type":"content","content":{"type":"text","text":"Delegated to subagent 20261019_2"}}]}`))
			a.HandleToolCallForTest(json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"delegate-pending","title":"delegate","status":"pending","rawInput":{"instructions":"queued"},"_meta":{"goose":{"toolCall":{"toolName":"delegate","extensionName":"summon"}}}}`))
			rows := sink.BackgroundTasks()
			require.Len(t, rows, 2)

			a.finishGoosePrompt(tc.err, tc.stopped)
			rows = sink.BackgroundTasks()
			require.Len(t, rows, 2)
			for _, row := range rows {
				assert.Equal(t, tc.want, row.Status, row.RowKey)
			}
			a.Mu.Lock()
			assert.Empty(t, a.activeDelegations)
			a.Mu.Unlock()
		})
	}

	t.Run("normal prompt end keeps open subagents running until idle interrupt", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newGooseOutputAgent(agent.NewProviderServices(sink))
		a.HooksForTest().SubagentFromToolCall = a.observeGooseSubagentCall
		a.HooksForTest().SubagentFromToolCallUpdate = a.observeGooseSubagentUpdate
		a.HandleToolCallForTest(json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"delegate-1","title":"delegate","status":"pending","rawInput":{"instructions":"work"},"_meta":{"goose":{"toolCall":{"toolName":"delegate","extensionName":"summon"}}}}`))
		a.HandleToolCallUpdateForTest(json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"delegate-1","status":"completed","content":[{"type":"content","content":{"type":"text","text":"Delegated to subagent 20261019_2"}}]}`))

		a.finishGoosePrompt(nil, false)
		rows := sink.BackgroundTasks()
		require.Len(t, rows, 1)
		assert.Equal(t, bgtask.StatusRunning, rows[0].Status)

		a.cancelActiveDelegations()
		rows = sink.BackgroundTasks()
		require.Len(t, rows, 1)
		assert.Equal(t, bgtask.StatusInterrupted, rows[0].Status)
	})

	t.Run("completed subagent stays succeeded across prompt end", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newGooseOutputAgent(agent.NewProviderServices(sink))
		a.HooksForTest().SubagentFromToolCall = a.observeGooseSubagentCall
		a.HooksForTest().SubagentFromToolCallUpdate = a.observeGooseSubagentUpdate
		a.HandleToolCallForTest(json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"delegate-1","title":"delegate","status":"pending","rawInput":{"instructions":"work"},"_meta":{"goose":{"toolCall":{"toolName":"delegate","extensionName":"summon"}}}}`))
		a.HandleToolCallUpdateForTest(json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"delegate-1","status":"completed","content":[{"type":"content","content":{"type":"text","text":"Delegated to subagent 20261019_2"}}]}`))
		a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"_goose/unstable/session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"status_message","status":{"type":"notice","message":"Subagent 20261019_2 completed"}}}}`))

		a.finishGoosePrompt(errors.New("later error"), false)
		rows := sink.BackgroundTasks()
		require.Len(t, rows, 1)
		assert.Equal(t, bgtask.StatusSucceeded, rows[0].Status)
	})
}

func TestGooseClearProviderStateResetsMaps(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newGooseOutputAgent(agent.NewProviderServices(sink))
	a.HooksForTest().ClearProviderState = a.clearGooseState
	a.Mu.Lock()
	a.gooseChildRenames = map[string]string{"delegate-1": "20261019_2"}
	a.gooseOutput = map[string]gooseOutputState{"shell-1": {sequence: 1, bytes: 5, tail: "hello"}}
	a.activeDelegations = map[string]struct{}{"20261019_2": {}}
	a.promptOutput = goosePromptOutputState{goalArmed: true, goalObjective: "Finish", lastMessageID: "msg-1", lastMessageText: []byte("hi"), compactPending: true}
	a.Mu.Unlock()

	a.HooksForTest().ClearProviderState()

	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Empty(t, a.gooseChildRenames)
	assert.Empty(t, a.gooseOutput)
	assert.Empty(t, a.activeDelegations)
	assert.Equal(t, goosePromptOutputState{}, a.promptOutput)
}

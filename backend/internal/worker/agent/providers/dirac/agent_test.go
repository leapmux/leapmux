package dirac

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
)

const diracTestSession = "session-1"

// newDiracAgent builds a Dirac agent over a fake peer that answers each
// request with `{}`, with Dirac's own hooks.
func newDiracAgent(t *testing.T) (*Agent, func() []agenttest.RecordedRequest) {
	t.Helper()
	a, requests := acptest.NewAgentForRPC(t,
		func() *Agent { return &Agent{} },
		func(a *Agent) *acp.Base { return &a.Base },
	)
	*a.HooksForTest() = a.configure(nil)
	a.SetSessionIDForTest(diracTestSession)
	return a, requests
}

// syncPeer returns once the peer recorded every line the agent wrote before
// the call, so a test that asserts a line is PRESENT sees it and a test that
// asserts it is ABSENT does not pass only because the peer did not read it yet.
func syncPeer(t *testing.T, a *Agent) {
	t.Helper()
	_, err := a.SendRequest("test/sync", nil, 30*time.Second)
	require.NoError(t, err)
}

// requestsFor returns the recorded requests of one method.
func requestsFor(requests []agenttest.RecordedRequest, method string) []agenttest.RecordedRequest {
	var out []agenttest.RecordedRequest
	for _, request := range requests {
		if request.Method == method {
			out = append(out, request)
		}
	}
	return out
}

func TestDiracAdvertisedSteerMethodReadsTheWhisperFlag(t *testing.T) {
	t.Parallel()
	advertised := `{"agentCapabilities":{"_meta":{"dev.dirac/whisper":true,"dev.dirac/seq":true}}}`
	assert.Equal(t, diracSteerMethod, diracAdvertisedSteerMethod([]byte(advertised)))

	absent := `{"agentCapabilities":{"_meta":{"dev.dirac/seq":true}}}`
	assert.Empty(t, diracAdvertisedSteerMethod([]byte(absent)))

	disabled := `{"agentCapabilities":{"_meta":{"dev.dirac/whisper":false}}}`
	assert.Empty(t, diracAdvertisedSteerMethod([]byte(disabled)))

	assert.Empty(t, diracAdvertisedSteerMethod([]byte("{")))
}

func TestDiracSteerInputSendsAWhisper(t *testing.T) {
	t.Parallel()
	a, requests := newDiracAgent(t)
	a.SetPromptActiveForTest(true)

	require.NoError(t, a.SteerInput("also mention bananas", nil))
	syncPeer(t, a)

	whispers := requestsFor(requests(), diracSteerMethod)
	require.Len(t, whispers, 1, "the steer goes out as a dev.dirac/whisper")
	assert.Equal(t, diracTestSession, whispers[0].Params["sessionId"])
	assert.Equal(t, "also mention bananas", whispers[0].Params["text"])
}

func TestDiracSteerInputRefusesWhenNoTurnRuns(t *testing.T) {
	t.Parallel()
	a, _ := newDiracAgent(t)
	assert.ErrorIs(t, a.SteerInput("late", nil), agent.ErrNoActiveTurn)
}

func TestDiracSubagentFromToolCallMapsTheUseSubagentsCard(t *testing.T) {
	t.Parallel()
	tc := acp.ToolCallEnvelope{
		ToolCallID: "card-1",
		Title:      "Run Subagents",
		RawInput:   json.RawMessage(`{"tool":"use_subagents","task_title":"Explore","prompt":"look around"}`),
	}
	obs := diracSubagentFromToolCall(tc)
	require.NotNil(t, obs)
	assert.Equal(t, "card-1", obs.RowKey)
	assert.Equal(t, "Run Subagents", obs.Title)
}

func TestDiracSubagentFromToolCallIgnoresOtherTools(t *testing.T) {
	t.Parallel()
	assert.Nil(t, diracSubagentFromToolCall(acp.ToolCallEnvelope{
		ToolCallID: "cmd-1",
		RawInput:   json.RawMessage(`{"tool":"execute_command","command":"ls"}`),
	}))
	assert.Nil(t, diracSubagentFromToolCall(acp.ToolCallEnvelope{ToolCallID: "bare"}))
}

// diracUpdateFrame is a session/update frame of one session that carries the
// given update object.
func diracUpdateFrame(t *testing.T, sessionID, update string) []byte {
	t.Helper()
	frame, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": "session/update",
		"params": map[string]any{"sessionId": sessionID, "update": json.RawMessage(update)},
	})
	require.NoError(t, err)
	return frame
}

// diracReplayConversation is one update of each kind that the idle rule
// consumes, as a replay of a loaded session states it.
var diracReplayConversation = []string{
	`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"OLDANSWER"}}`,
	`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"OLDTHOUGHT"}}`,
	`{"sessionUpdate":"tool_call","toolCallId":"old-call","status":"pending","kind":"execute","title":"Executing: OLDCOMMAND","rawInput":{"tool":"execute_command","command":"ls"}}`,
	`{"sessionUpdate":"tool_call_update","toolCallId":"old-call","status":"completed","kind":"execute","rawOutput":{"output":"OLDOUTPUT"}}`,
	`{"sessionUpdate":"plan","entries":[{"content":"OLDPLANSTEP","priority":"medium","status":"pending"}]}`,
}

// Dirac 0.5.17 replays the history of a loaded session after its session/load
// reply, while no prompt runs. In cli/src/acp/AcpAgent.ts, loadSession awaits
// replayLoadedSessionHistory, and the journal emitter flushes behind the reply.
// The Worker already stores that transcript, so an idle conversation update of
// the current session draws nothing. Before this rule, the replayed answer
// persisted beside the stored row, and the resumed chat showed the old answer
// twice.
//
// The test marks the prompt active directly. The peer can answer a prompt at
// once, and the prompt could then end before the live chunk arrives. The rule
// would consume that chunk. The running turn assembles its text in the base,
// so the text that the base holds shows which updates reached it.
func TestDiracIdleReplayDrawsNothing(t *testing.T) {
	t.Parallel()
	a, _ := newDiracAgent(t)
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))

	for _, update := range diracReplayConversation {
		a.HandleOutput(diracUpdateFrame(t, diracTestSession, update))
	}
	assert.Empty(t, sink.Messages(), "the replay that a loaded session sent while idle stores no row")
	assert.Empty(t, a.TurnAssistantTextForTest().String(), "the replayed answer reaches no turn")
	assert.Zero(t, a.TurnToolUsesForTest(), "the replayed tool call counts for no turn")

	a.SetPromptActiveForTest(true)
	a.HandleOutput(acptest.Chunk(diracTestSession, contracts.ACPUpdateAgentMessageChunk, "NEWANSWER"))

	assert.Equal(t, "NEWANSWER", a.TurnAssistantTextForTest().String(), "the live answer of the resumed turn reaches its turn")
}

// The hook consumes an idle conversation update of the session that the agent
// serves, and nothing else. A state update, an update of another session, and
// each update while a prompt runs reach the base. An update of another session
// belongs to a child, and a consumed one would vanish from the child's tab.
func TestDiracSessionUpdateHandlerConsumesOnlyIdleConversation(t *testing.T) {
	t.Parallel()
	state := []string{
		`{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"PROBE"}}`,
		`{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"memory","description":"Manage memory."}]}`,
		`{"sessionUpdate":"current_mode_update","currentModeId":"plan"}`,
		`{"sessionUpdate":"config_option_update","configOptions":[]}`,
		`{"sessionUpdate":"usage_update","used":2,"size":1048576}`,
		`{"sessionUpdate":"session_info_update","title":"Old title"}`,
	}
	for _, update := range diracReplayConversation {
		idle := &Agent{}
		idle.SetSessionIDForTest(diracTestSession)
		assert.True(t, idle.handleSessionUpdate(diracTestSession, nil, json.RawMessage(update)), "an idle update of the current session: %s", update)
		assert.False(t, idle.handleSessionUpdate("another-session", nil, json.RawMessage(update)), "an idle update of another session: %s", update)

		active := &Agent{}
		active.SetSessionIDForTest(diracTestSession)
		active.SetPromptActiveForTest(true)
		assert.False(t, active.handleSessionUpdate(diracTestSession, nil, json.RawMessage(update)), "an update while a prompt runs: %s", update)
	}
	for _, update := range state {
		idle := &Agent{}
		idle.SetSessionIDForTest(diracTestSession)
		assert.False(t, idle.handleSessionUpdate(diracTestSession, nil, json.RawMessage(update)), "an idle state update: %s", update)
	}
	idle := &Agent{}
	idle.SetSessionIDForTest(diracTestSession)
	assert.False(t, idle.handleSessionUpdate(diracTestSession, nil, json.RawMessage(`{`)), "an update that is not JSON")
}

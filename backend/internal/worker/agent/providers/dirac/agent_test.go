package dirac

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// The hook consumes an idle conversation update of the session that the agent
// serves, and nothing else. A state update, an update of another session, and
// each update while a prompt runs reach the base. An update of another session
// belongs to a child, and a consumed one would vanish from the child's tab.

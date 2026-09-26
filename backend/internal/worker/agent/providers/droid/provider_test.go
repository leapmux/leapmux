package droid

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestRegistrationWiresThePlugin(t *testing.T) {
	t.Parallel()
	reg := Registration()
	_, ok := reg.Plugin.(droidProvider)
	assert.True(t, ok, "the registration hands out this package's plugin")
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_DROID, reg.Provider)
	assert.NotNil(t, reg.Start)
	assert.True(t, reg.Locator.Valid())
	_, err := agent.NewRegistry(reg)
	assert.NoError(t, err, "the registration passes the registry's checks")
}

func TestRegistrationStatesTheSettings(t *testing.T) {
	t.Parallel()
	reg := Registration()
	assert.Equal(t, "default", reg.PermissionDefaults.NewSession[agent.OptionIDPermissionMode])
	assert.Equal(t, "default", reg.PermissionDefaults.Fallback)
	assert.True(t, reg.FixedPermissionModes)
	assert.Equal(t, "LEAPMUX_DROID_DEFAULT_MODEL", reg.EnvModelKey)
	assert.Equal(t, "LEAPMUX_DROID_DEFAULT_EFFORT", reg.EnvEffortKey)
}

func TestResumeHandleKeepsTheTokenRule(t *testing.T) {
	t.Parallel()
	agenttest.AssertTokenResumeRule(t, droidProvider{})
}

func TestControlResponseSuites(t *testing.T) {
	t.Parallel()
	agenttest.AssertPreservesTheResponseWithoutARequest(t, droidProvider{})
	agenttest.AssertWithholdsTheResponseForAMalformedRequest(t, droidProvider{})
}

func TestChildCapabilities(t *testing.T) {
	t.Parallel()
	agenttest.AssertChildCapabilities(t, Registration().Plugin, (*Agent)(nil))
}

func TestIsInterruptRecognizesTheRawFrame(t *testing.T) {
	t.Parallel()
	assert.True(t, droidProvider{}.IsInterrupt(`{"type":"interrupt"}`))
	assert.False(t, droidProvider{}.IsInterrupt(`{"type":"create_message"}`))
}

func TestResolveControlResponseAnswersAPermissionRequest(t *testing.T) {
	t.Parallel()
	request, _ := json.Marshal(map[string]any{
		"type":      "permission_request",
		"requestId": "droid-perm-1",
		"rpcId":     "rpc-1",
	})
	response, _ := json.Marshal(map[string]any{
		"response": map[string]any{
			"request_id": "droid-perm-1",
			"response": map[string]any{
				"behavior":       "allow",
				"selectedOption": "proceed_always",
			},
		},
	})
	resolution := droidProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestID:       "droid-perm-1",
		RequestPayload:  request,
		ResponseContent: response,
	})
	assert.False(t, resolution.Withhold)
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(resolution.Content, &envelope))
	var result map[string]string
	require.NoError(t, json.Unmarshal(envelope.Result, &result))
	assert.Equal(t, "proceed_always", result["selectedOption"])
}

func TestResolveControlResponseAnswersAQuestion(t *testing.T) {
	t.Parallel()
	request, _ := json.Marshal(map[string]any{
		"type":       "ask_user_request",
		"requestId":  "droid-ask-1",
		"toolCallId": "call-1",
	})
	response, _ := json.Marshal(map[string]any{
		"response": map[string]any{
			"request_id": "droid-ask-1",
			"response": map[string]any{
				"behavior": "allow",
				"answers":  map[string]string{"Which color?": "Blue"},
			},
		},
	})
	resolution := droidProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestID:       "droid-ask-1",
		RequestPayload:  request,
		ResponseContent: response,
	})
	assert.False(t, resolution.Withhold)
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(resolution.Content, &envelope))
	var result struct {
		Cancelled bool `json:"cancelled"`
		Answers   []struct {
			Question string `json:"question"`
			Answer   string `json:"answer"`
		} `json:"answers"`
	}
	require.NoError(t, json.Unmarshal(envelope.Result, &result))
	assert.False(t, result.Cancelled)
	require.Len(t, result.Answers, 1)
	assert.Equal(t, "Blue", result.Answers[0].Answer)
}

func TestListStoredSessionsReadsTheStore(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	work := filepath.Join(home, "workspace", "project")
	require.NoError(t, os.MkdirAll(work, 0o755))
	dir := filepath.Join(home, ".factory", "sessions", droidSanitizeCwd(work))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	transcript := filepath.Join(dir, "e2b678f5-0bef-4f1f-8a10-39c542ea031d.jsonl")
	require.NoError(t, os.WriteFile(transcript, []byte(
		`{"type":"session_start","id":"e2b678f5-0bef-4f1f-8a10-39c542ea031d","title":"Test session","cwd":"`+work+`"}`+"\n",
	), 0o644))

	got, err := droidProvider{}.ListStoredSessions(t.Context(), agent.StoredSessionQuery{
		WorkingDir: work,
		HomeDir:    home,
		Getenv:     agenttest.FixtureEnv(map[string]string{"FACTORY_HOME_OVERRIDE": filepath.Join(home, ".factory")}),
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "e2b678f5-0bef-4f1f-8a10-39c542ea031d", got[0].Handle)
	assert.Equal(t, "Test session", got[0].Title)
}

func TestReadsSessionStoreThroughTheSuite(t *testing.T) {
	t.Parallel()
	agenttest.RequireReadsSessionStore(t, droidProvider{}, func(t *testing.T, home, workingDir string) string {
		dir := filepath.Join(home, ".factory", "sessions", droidSanitizeCwd(workingDir))
		require.NoError(t, os.MkdirAll(dir, 0o755))
		const handle = "11111111-2222-3333-4444-555555555555"
		require.NoError(t, os.WriteFile(filepath.Join(dir, handle+".jsonl"), []byte(
			`{"type":"session_start","id":"`+handle+`","title":"Seeded","cwd":"`+workingDir+`"}`+"\n",
		), 0o644))
		return handle
	})
}

func TestWorkingStateMovesTheTurnFlag(t *testing.T) {
	t.Parallel()
	// Each line is a stream-jsonrpc envelope whose params carry the
	// notification payload, exactly as the CLI writes it.
	wrap := func(state string) string {
		return `{"jsonrpc":"2.0","type":"notification","method":"droid.session_notification","params":{"sessionId":"s-1","notification":{"type":"droid_working_state_changed","newState":"` + state + `"}}}`
	}
	cases := []agenttest.TurnFrameCase{
		{Name: "thinking", Line: wrap("thinking"), Moves: true},
		{Name: "streaming", Line: wrap("streaming_assistant_message"), Moves: true},
		{Name: "executing", Line: wrap("executing_tool"), Moves: true},
		{Name: "waiting", Line: wrap("waiting_for_tool_confirmation"), Moves: true},
		{Name: "compacting", Line: wrap("compacting_conversation"), Moves: true},
		{Name: "idle", Line: wrap("idle"), Moves: false},
		{Name: "unknown state", Line: wrap("brand_new_state"), Moves: false},
		{Name: "unknown notification", Line: `{"jsonrpc":"2.0","type":"notification","method":"droid.session_notification","params":{"sessionId":"s-1","notification":{"type":"something_new"}}}`, Moves: false},
	}
	agenttest.AssertTurnFrames(t, cases, func(t *testing.T, tc agenttest.TurnFrameCase) []bool {
		a := newTestAgent(t)
		return a.feedAndCollect(tc.Line)
	})
}

// newTestAgent builds an agent with a test sink and no process.
func newTestAgent(t *testing.T) *testAgent {
	t.Helper()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	return &testAgent{agent: a, sink: sink}
}

// testAgent drives HandleOutput against a bare Agent.
type testAgent struct {
	agent *Agent
	sink  *agenttest.Sink
}

// feedAndCollect returns the turn-state publishes one frame produced.
func (h *testAgent) feedAndCollect(line string) []bool {
	h.agent.HandleOutput([]byte(line))
	return h.sink.TurnActives()
}

func TestTurnTokensRise(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	agenttest.AssertRisingTurnTokens(t, sink, a)
}

func TestBusyRefusalRepublishesTheTurn(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	a.armTurn()
	err := a.SendInput("hello", nil)
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, sink, a, err)
}

func TestRejectsMissingAndReplacedSessions(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	a.Mu.Lock()
	a.sessionID = "current"
	a.Mu.Unlock()
	agenttest.AssertRejectsMissingAndReplacedSessions(t, a)
}

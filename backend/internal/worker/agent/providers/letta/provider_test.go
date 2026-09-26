package letta

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestRegistrationWiresThePlugin(t *testing.T) {
	t.Parallel()
	reg := Registration()
	_, ok := reg.Plugin.(lettaProvider)
	assert.True(t, ok, "the registration hands out this package's plugin")
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_LETTA, reg.Provider)
	assert.NotNil(t, reg.Start)
	assert.True(t, reg.Locator.Valid())
	_, err := agent.NewRegistry(reg)
	assert.NoError(t, err, "the registration passes the registry's checks")
}

func TestRegistrationStatesTheSettings(t *testing.T) {
	t.Parallel()
	reg := Registration()
	assert.Equal(t, "standard", reg.PermissionDefaults.NewSession[agent.OptionIDPermissionMode])
	assert.Equal(t, "standard", reg.PermissionDefaults.Fallback)
	assert.True(t, reg.FixedPermissionModes)
	assert.Equal(t, "LEAPMUX_LETTA_DEFAULT_MODEL", reg.EnvModelKey)
	assert.Equal(t, "LEAPMUX_LETTA_DEFAULT_EFFORT", reg.EnvEffortKey)
}

func TestResumeHandleKeepsTheTokenRule(t *testing.T) {
	t.Parallel()
	agenttest.AssertTokenResumeRule(t, lettaProvider{})
}

func TestControlResponseSuites(t *testing.T) {
	t.Parallel()
	agenttest.AssertPreservesTheResponseWithoutARequest(t, lettaProvider{})
	agenttest.AssertWithholdsTheResponseForAMalformedRequest(t, lettaProvider{})
}

func TestChildCapabilities(t *testing.T) {
	t.Parallel()
	agenttest.AssertChildCapabilities(t, Registration().Plugin, (*Agent)(nil))
}

func TestIsInterruptRecognizesTheRawFrame(t *testing.T) {
	t.Parallel()
	assert.True(t, lettaProvider{}.IsInterrupt(`{"kind":"abort_message"}`))
	assert.False(t, lettaProvider{}.IsInterrupt(`{"kind":"input"}`))
}

// The approval payload is FLAT: `kind`, `request_id` and `decision` sit at the
// top level. The nested form produces a protocol violation on the server.
func TestResolveControlResponseWritesTheFlatApprovalPayload(t *testing.T) {
	t.Parallel()
	request, _ := json.Marshal(map[string]any{
		"type":                             "permission",
		"requestId":                        "perm-call_ask_1",
		contracts.LettaDeltaFieldToolName:  "AskUserQuestion",
		contracts.LettaDeltaFieldToolInput: map[string]any{"questions": []any{map[string]any{"question": "Which color?"}}},
	})
	response, _ := json.Marshal(map[string]any{
		"response": map[string]any{
			"request_id": "perm-call_ask_1",
			"response": map[string]any{
				"behavior": "allow",
				"answers":  map[string]string{"Which color?": "Blue"},
			},
		},
	})
	resolution := lettaProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestID:       "perm-call_ask_1",
		RequestPayload:  request,
		ResponseContent: response,
	})
	assert.False(t, resolution.Withhold)

	var flat map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(resolution.Content, &flat))
	assert.Contains(t, flat, "kind", "the approval payload is flat: kind sits at the top level")
	assert.Contains(t, flat, "request_id", "the approval payload is flat: request_id sits at the top level")
	assert.Contains(t, flat, "decision", "the approval payload is flat: decision sits at the top level")
	assert.NotContains(t, flat, "response", "the nested form is a protocol violation")

	var decision struct {
		Behavior     string          `json:"behavior"`
		UpdatedInput json.RawMessage `json:"updated_input"`
	}
	require.NoError(t, json.Unmarshal(flat["decision"], &decision))
	assert.Equal(t, "allow", decision.Behavior)
	var updated map[string]any
	require.NoError(t, json.Unmarshal(decision.UpdatedInput, &updated))
	assert.Contains(t, updated, "answers")
}

// A deny must state `decision.message` as a STRING. Letta's
// `isValidApprovalResponseBody` rejects a deny whose message is ABSENT, and an
// `omitempty` that dropped the empty message turned every deny into a protocol
// violation: the tool call hung and the turn never reached the model again.
func TestResolveControlResponseKeepsTheDenyMessage(t *testing.T) {
	t.Parallel()
	request, _ := json.Marshal(map[string]any{
		"type":                             "permission",
		"requestId":                        "perm-deny-call",
		contracts.LettaDeltaFieldToolName:  "Bash",
		contracts.LettaDeltaFieldToolInput: map[string]any{"command": "echo hi"},
	})
	response, _ := json.Marshal(map[string]any{
		"response": map[string]any{
			"request_id": "perm-deny-call",
			"response": map[string]any{
				"behavior": "deny",
				// The bare-deny placeholder, which NormalizeRejectionMessage
				// collapses to the empty string.
				"message": "Rejected by user.",
			},
		},
	})
	resolution := lettaProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestID:       "perm-deny-call",
		RequestPayload:  request,
		ResponseContent: response,
	})
	require.False(t, resolution.Withhold)

	var flat map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(resolution.Content, &flat))
	// The field must be PRESENT. Its value may be empty; its absence is the
	// protocol violation.
	assert.Contains(t, string(flat["decision"]), `"message"`, "a deny states its message field")
	var decision struct {
		Behavior string `json:"behavior"`
		Message  string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(flat["decision"], &decision))
	assert.Equal(t, "deny", decision.Behavior)
}

func TestListStoredSessionsReadsTheStore(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	work := filepath.Join(home, "workspace", "project")
	require.NoError(t, os.MkdirAll(work, 0o755))
	backend := filepath.Join(home, "backend")
	conv := filepath.Join(backend, "conversations", "local-conv-1")
	require.NoError(t, os.MkdirAll(conv, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(conv, "conversation.json"), []byte(
		`{"id":"local-conv-1","agent_id":"agent-local-1"}`,
	), 0o644))

	got, err := lettaProvider{}.ListStoredSessions(t.Context(), agent.StoredSessionQuery{
		WorkingDir: work,
		HomeDir:    home,
		Getenv:     agenttest.FixtureEnv(map[string]string{"LETTA_LOCAL_BACKEND_DIR": backend}),
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "local-conv-1", got[0].Handle)
}

func TestReadsSessionStoreThroughTheSuite(t *testing.T) {
	agenttest.RequireReadsSessionStore(t, lettaProvider{}, func(t *testing.T, home, workingDir string) string {
		// The reader's fallback store path under the temp home.
		backend := filepath.Join(home, ".letta-backend")
		conv := filepath.Join(backend, "conversations", "local-conv-9")
		require.NoError(t, os.MkdirAll(conv, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(conv, "conversation.json"), []byte(
			`{"id":"local-conv-9","agent_id":"agent-local-9"}`,
		), 0o644))
		return "local-conv-9"
	})
}

func TestLoopStatusMovesTheTurnFlag(t *testing.T) {
	t.Parallel()
	// The real App Server puts the body in `loop_status` and the discriminator
	// in `type`. A `payload` body is a shape no frame carries.
	cases := []agenttest.TurnFrameCase{
		{Name: "sending api request", Line: `{"type":"update_loop_status","loop_status":{"status":"SENDING_API_REQUEST"}}`, Moves: true},
		{Name: "waiting for api response", Line: `{"type":"update_loop_status","loop_status":{"status":"WAITING_FOR_API_RESPONSE"}}`, Moves: true},
		{Name: "processing api response", Line: `{"type":"update_loop_status","loop_status":{"status":"PROCESSING_API_RESPONSE"}}`, Moves: true},
		{Name: "waiting on input", Line: `{"type":"update_loop_status","loop_status":{"status":"WAITING_ON_INPUT"}}`, Moves: false},
		{Name: "unknown status", Line: `{"type":"update_loop_status","loop_status":{"status":"BRAND_NEW"}}`, Moves: false},
		{Name: "unknown message", Line: `{"type":"brand_new_message","payload":{}}`, Moves: false},
	}
	agenttest.AssertTurnFrames(t, cases, func(t *testing.T, tc agenttest.TurnFrameCase) []bool {
		sink := &agenttest.Sink{}
		a := &Agent{sink: agent.NewProviderServices(sink)}
		a.HandleOutput([]byte(tc.Line))
		return sink.TurnActives()
	})
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
	a.conversationID = "current"
	a.Mu.Unlock()
	agenttest.AssertRejectsMissingAndReplacedSessions(t, a)
}

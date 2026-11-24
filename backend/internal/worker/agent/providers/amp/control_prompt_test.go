package amp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// ampTestRequestID is the stored id of the request that each test answers.
const ampTestRequestID = "r1"

// ampControlPayload is the envelope that decidePermission publishes for one
// helper run.
func ampControlPayload(t *testing.T, tool string, input any) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"type":        "leapmux_amp_permission",
		"tool_name":   tool,
		"tool_use_id": "TU-034UC14fL0WVIuQhmDl0qN",
		"input":       input,
	})
	require.NoError(t, err)
	return payload
}

func TestReadControlRequest_AmpShellCallStatesItsCommandAndDirectory(t *testing.T) {
	got := agenttest.ControlPromptOf(ampProvider{}, ampControlPayload(t, "shell_command", map[string]any{"command": "rm -rf build", "workdir": "/work"}))
	require.NotNil(t, got.GetPermission())
	assert.JSONEq(t, `{"command":"rm -rf build","workdir":"/work"}`, got.GetPermission().GetInputJson())
	got.GetPermission().InputJson = ""
	agenttest.RequireProto(t, agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{
		Title:            "shell_command",
		Command:          "rm -rf build",
		WorkingDirectory: "/work",
		Options:          agent.AllowDenyOptions(),
	}), got)
}

// The current Bash registration states its command as cmd and its
// directory as cwd.
func TestReadControlRequest_AmpBashCallReadsCmdAndCwd(t *testing.T) {
	permission := agenttest.ControlPromptOf(ampProvider{}, ampControlPayload(t, "Bash", map[string]any{"cmd": "make clean", "cwd": "/bash-work"})).GetPermission()
	assert.Equal(t, "make clean", permission.GetCommand())
	assert.Equal(t, "/bash-work", permission.GetWorkingDirectory())
}

// The executor runs `shell_command` as `async_shell_command`, so a helper
// run can state either name.
func TestReadControlRequest_AmpEveryShellNameStatesItsCommand(t *testing.T) {
	for _, tool := range []string{"shell_command", "async_shell_command", "Bash"} {
		permission := agenttest.ControlPromptOf(ampProvider{}, ampControlPayload(t, tool, map[string]any{"command": "npm test", "workdir": "/work"})).GetPermission()
		assert.Equal(t, "npm test", permission.GetCommand(), tool)
		assert.Equal(t, "/work", permission.GetWorkingDirectory(), tool)
	}
}

// The first spelling wins only when it holds text: an empty `command` or
// `workdir` gives way to `cmd` or `cwd`.
func TestReadControlRequest_AmpAnEmptyFirstSpellingFallsBackToTheSecond(t *testing.T) {
	permission := agenttest.ControlPromptOf(ampProvider{}, ampControlPayload(t, "Bash", map[string]any{"command": "", "cmd": "make", "workdir": "", "cwd": "/w"})).GetPermission()
	assert.Equal(t, "make", permission.GetCommand())
	assert.Equal(t, "/w", permission.GetWorkingDirectory())
}

func TestReadControlRequest_AmpBlankCommandStatesNone(t *testing.T) {
	got := agenttest.ControlPromptOf(ampProvider{}, ampControlPayload(t, "shell_command", map[string]any{"command": "", "workdir": "/work"}))
	assert.JSONEq(t, `{"command":"","workdir":"/work"}`, got.GetPermission().GetInputJson())
	got.GetPermission().InputJson = ""
	agenttest.RequireProto(t, agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{
		Title:            "shell_command",
		WorkingDirectory: "/work",
		Options:          agent.AllowDenyOptions(),
	}), got)
}

func TestReadControlRequest_AmpAnotherToolStatesItsArgumentsAndNoCommand(t *testing.T) {
	got := agenttest.ControlPromptOf(ampProvider{}, ampControlPayload(t, "apply_patch", map[string]any{"patchText": "*** Begin Patch\n*** End Patch"}))
	assert.JSONEq(t, `{"patchText":"*** Begin Patch\n*** End Patch"}`, got.GetPermission().GetInputJson())
	got.GetPermission().InputJson = ""
	agenttest.RequireProto(t, agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{Title: "apply_patch", Options: agent.AllowDenyOptions()}), got)

	permission := agenttest.ControlPromptOf(ampProvider{}, ampControlPayload(t, "mcp__ci__run", map[string]any{"command": "deploy"})).GetPermission()
	assert.Empty(t, permission.GetCommand(), "a `command` argument of a tool that is not a shell is an argument alone")
}

func TestReadControlRequest_AmpRequestWithNoInputAndNoTool(t *testing.T) {
	agenttest.RequireProto(t, agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{
		Title: "Tool", InputJson: "{}", Options: agent.AllowDenyOptions(),
	}), agenttest.ControlPromptOf(ampProvider{}, []byte(`{"type":"leapmux_amp_permission"}`)))

	// An input that is no object states an empty object.
	agenttest.RequireProto(t, agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{
		Title: "shell_command", InputJson: "{}", Options: agent.AllowDenyOptions(),
	}), agenttest.ControlPromptOf(ampProvider{}, ampControlPayload(t, "shell_command", "not an object")))
}

func TestReadControlRequest_AmpAnotherPayloadIsGeneric(t *testing.T) {
	for _, payload := range []string{
		`{}`,
		`{"request":{"tool_name":"Bash","input":{}}}`,
		`{"type":"leapmux_amp_other","tool_name":"Bash"}`,
		`{"type":7}`,
		`not json`,
		`[]`,
	} {
		agenttest.RequireProto(t, agent.GenericPermissionPrompt(), agenttest.ControlPromptOf(ampProvider{}, []byte(payload)))
	}
}

// Allow sends the request's `request.input` back (agent.AnswerAllowDeny);
// Amp's envelope has none, so the input is empty.
func TestAnswerControlRequest_AmpAllowSendsAnEmptyInput(t *testing.T) {
	got, err := agenttest.AnswerControl(ampProvider{}, ampControlPayload(t, "shell_command", map[string]any{"command": "pwd"}), ampTestRequestID, agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"r1","response":{"behavior":"allow","updatedInput":{}}}}`, string(got.Content))
	assert.Nil(t, got.PlanApproval)
	assert.Empty(t, got.FollowUp)
	requestID, behavior, _, ok := agent.DecodeControlBehavior(got.Content)
	require.True(t, ok)
	assert.Equal(t, "r1", requestID, "SendRawInput finds the request by this id")
	assert.Equal(t, contracts.ControlAnswerBehaviorAllow, behavior)
}

// The composer's send refuses the call, and its words ride the refusal as
// the reason. A refusal with no words states the placeholder.
func TestAnswerControlRequest_AmpRefusalCarriesTheReason(t *testing.T) {
	payload := ampControlPayload(t, "shell_command", map[string]any{"command": "pwd"})
	got, err := agenttest.AnswerControl(ampProvider{}, payload, ampTestRequestID, agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, "Use the clean target."))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"r1","response":{"behavior":"deny","message":"Use the clean target."}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp, "the reason rides the answer, so no message follows it")
	_, behavior, message, ok := agent.DecodeControlBehavior(got.Content)
	require.True(t, ok)
	assert.Equal(t, contracts.ControlAnswerBehaviorDeny, behavior)
	assert.Equal(t, "Use the clean target.", message, "SendRawInput hands this reason to the model")

	got, err = agenttest.AnswerControl(ampProvider{}, payload, ampTestRequestID, agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"r1","response":{"behavior":"deny","message":"Rejected by user."}}}`, string(got.Content))
	_, _, message, _ = agent.DecodeControlBehavior(got.Content)
	assert.Empty(t, message, "SendRawInput reads the placeholder as no reason, and Amp refuses in its own words")
}

func TestAnswerControlRequest_AmpRefusesAnAnswerThatThePromptDoesNotOffer(t *testing.T) {
	payload := ampControlPayload(t, "shell_command", map[string]any{"command": "pwd"})
	_, err := agenttest.AnswerControl(ampProvider{}, payload, ampTestRequestID, agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, "a note"))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "an approval carries no feedback")
	_, err = agenttest.AnswerControl(ampProvider{}, payload, ampTestRequestID, agenttest.PermissionDecision("always", ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "Amp offers no other option")
	_, err = agenttest.AnswerControl(ampProvider{}, payload, ampTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a permission takes no plan decision")
	_, err = agenttest.AnswerControl(ampProvider{}, payload, ampTestRequestID, &leapmuxv1.ControlDecision{})
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a decision of no kind answers nothing")
}

// The generic prompt of a payload that is not Amp's envelope answers the same
// way.
func TestAnswerControlRequest_AmpGenericPromptTakesTheSameAnswers(t *testing.T) {
	got, err := agenttest.AnswerControl(ampProvider{}, []byte(`{"request":{"input":{"a":1}}}`), ampTestRequestID, agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"r1","response":{"behavior":"allow","updatedInput":{"a":1}}}}`, string(got.Content))
}

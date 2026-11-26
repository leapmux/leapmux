package codex

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The option kinds, under short names for the expected option lists.
const (
	kindAllowOnce    = leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE
	kindAllowAlways  = leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS
	kindRejectOnce   = leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE
	kindRejectAlways = leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ALWAYS
)

// The scopes, under short names for the expected option lists.
const (
	scopeSession = leapmuxv1.PermissionScope_PERMISSION_SCOPE_SESSION
	scopeUser    = leapmuxv1.PermissionScope_PERMISSION_SCOPE_USER
)

// codexJSON encodes value as a test payload.
func codexJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}

// codexRequestPayload is a JSON-RPC request that Codex sends, with id 7.
func codexRequestPayload(t *testing.T, method string, params any) []byte {
	t.Helper()
	return codexJSON(t, map[string]any{"jsonrpc": "2.0", "id": 7, "method": method, "params": params})
}

// codexDecisionsPayload is a command approval that offers decisions.
func codexDecisionsPayload(t *testing.T, decisions ...any) []byte {
	t.Helper()
	return codexRequestPayload(t, contracts.CodexMethodCommandExecutionRequestApproval, map[string]any{"availableDecisions": decisions})
}

// codexPermissionsPayload is a permissions request that asks for permissions.
func codexPermissionsPayload(t *testing.T, permissions any) []byte {
	t.Helper()
	return codexRequestPayload(t, contracts.CodexMethodPermissionsRequestApproval, map[string]any{"permissions": permissions})
}

// codexUserInputPayload is a requestUserInput request that asks questions.
func codexUserInputPayload(t *testing.T, questions ...any) []byte {
	t.Helper()
	return codexRequestPayload(t, contracts.CodexMethodToolRequestUserInput, map[string]any{"questions": questions})
}

// codexToolPayload is a control request that LeapMux published itself.
func codexToolPayload(t *testing.T, toolName string, input any) []byte {
	t.Helper()
	return codexJSON(t, map[string]any{
		"type":       "control_request",
		"request_id": "req-1",
		"request":    map[string]any{"tool_name": toolName, "input": input},
	})
}

// codexAsyncPayload is an async question request whose input is input.
func codexAsyncPayload(t *testing.T, input any) []byte {
	t.Helper()
	return codexToolPayload(t, contracts.CodexControlToolAsyncQuestion, input)
}

// codexColorQuestion is an async question with two options.
func codexColorQuestion(t *testing.T) []byte {
	t.Helper()
	return codexAsyncPayload(t, map[string]any{
		contracts.CodexAsyncQuestionInputQuestionItemID: `["request_user_input_async","call_abc",0]`,
		contracts.CodexAsyncQuestionInputTitle:          "Which color should I use?",
		contracts.CodexAsyncQuestionInputOptions:        []any{"Blue", "Red"},
	})
}

func requireCodexOptions(t *testing.T, want []*leapmuxv1.PermissionOption, payload []byte) {
	t.Helper()
	permission := agenttest.ControlPromptOf(codexProvider{}, payload).GetPermission()
	require.NotNil(t, permission, "an approval reads as a permission")
	agenttest.RequireProto(t,
		agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{Options: want}),
		agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{Options: permission.GetOptions()}))
}

// requireCodexAnswer answers payload and requires the answer to succeed.
func requireCodexAnswer(t *testing.T, payload []byte, requestID string, decision *leapmuxv1.ControlDecision) agent.ControlAnswer {
	t.Helper()
	answer, err := agenttest.AnswerControl(codexProvider{}, payload, requestID, decision)
	require.NoError(t, err)
	return answer
}

// codexAnswerResult decodes the result of a JSON-RPC answer, after it checks
// that the answer keeps the Worker's request id as a string.
func codexAnswerResult(t *testing.T, content []byte, requestID string) string {
	t.Helper()
	var frame struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(content, &frame))
	assert.Equal(t, "2.0", frame.JSONRPC)
	assert.JSONEq(t, string(codexJSON(t, requestID)), string(frame.ID), "the answer keeps the Worker's request id as a string")
	return string(frame.Result)
}

// --- Reading an approval ---

// TestCodexReadControlRequest_TitlesAnApproval covers a command and a file
// change approval: each states its operation, its reason, its command and
// its working directory, and no arguments, because the command and the reason
// already state the ask.
func TestCodexReadControlRequest_TitlesAnApproval(t *testing.T) {
	t.Parallel()

	for method, title := range map[string]string{
		contracts.CodexMethodCommandExecutionRequestApproval: "Command Execution",
		contracts.CodexMethodFileChangeRequestApproval:       "File Change",
	} {
		permission := agenttest.ControlPromptOf(codexProvider{}, codexRequestPayload(t, method, map[string]any{
			"reason": "needs approval", "command": "ls", "cwd": "/repo",
		})).GetPermission()
		require.NotNil(t, permission, method)
		assert.Equal(t, title, permission.GetTitle(), method)
		assert.Equal(t, "needs approval", permission.GetReason(), method)
		assert.Equal(t, "ls", permission.GetCommand(), method)
		assert.Equal(t, "/repo", permission.GetWorkingDirectory(), method)
		assert.Empty(t, permission.GetInputJson(), method)
		assert.False(t, permission.GetIndependentOptions(), method)
	}
}

// TestCodexReadControlRequest_ReadsAShellCommandAsEveryProviderDoes covers
// the cross-provider parity of a shell approval: the prompt states the
// command and identifies the operation.
func TestCodexReadControlRequest_ReadsAShellCommandAsEveryProviderDoes(t *testing.T) {
	t.Parallel()

	const command = "npm test -- --runInBand"
	permission := agenttest.ControlPromptOf(codexProvider{}, codexRequestPayload(t, contracts.CodexMethodCommandExecutionRequestApproval, map[string]any{"command": command})).GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, command, permission.GetCommand())
	assert.NotEmpty(t, permission.GetTitle())
}

// TestCodexReadControlRequest_ReadsATextFieldOfTheWrongTypeAsAbsent covers a
// reason, a command and a working directory that are no text.
func TestCodexReadControlRequest_ReadsATextFieldOfTheWrongTypeAsAbsent(t *testing.T) {
	t.Parallel()

	permission := agenttest.ControlPromptOf(codexProvider{}, codexRequestPayload(t, contracts.CodexMethodCommandExecutionRequestApproval, map[string]any{
		"reason": 7, "command": []any{"ls"}, "cwd": nil,
	})).GetPermission()
	require.NotNil(t, permission)
	assert.Empty(t, permission.GetReason())
	assert.Empty(t, permission.GetCommand())
	assert.Empty(t, permission.GetWorkingDirectory())
}

// TestCodexReadControlRequest_StatesNoTitleForAnotherMethod covers a method
// that identifies no approval. A method that spells a member of JavaScript's
// Object.prototype is an unknown method as well.
func TestCodexReadControlRequest_StatesNoTitleForAnotherMethod(t *testing.T) {
	t.Parallel()

	for _, method := range []string{"toString", "constructor", "valueOf", "hasOwnProperty", "item/unknown/requestApproval"} {
		permission := agenttest.ControlPromptOf(codexProvider{}, codexRequestPayload(t, method, map[string]any{})).GetPermission()
		require.NotNil(t, permission, method)
		assert.Empty(t, permission.GetTitle(), method)
	}
}

// TestCodexReadControlRequest_AnUnreadableRequestOffersAcceptAndCancel covers
// a payload that states nothing that the reader knows. The browser drew
// Codex's decision buttons for it too, never the generic Allow and Deny.
func TestCodexReadControlRequest_AnUnreadableRequestOffersAcceptAndCancel(t *testing.T) {
	t.Parallel()

	want := []*leapmuxv1.PermissionOption{
		{OptionId: "accept", Kind: kindAllowOnce, Name: "Allow"},
		{OptionId: "cancel", Kind: kindRejectOnce, Name: "Cancel", EndsTurn: true},
	}
	for _, payload := range []string{`{}`, `not json`, `{"method":7,"params":"invalid"}`} {
		requireCodexOptions(t, want, []byte(payload))
		assert.Empty(t, agenttest.ControlPromptOf(codexProvider{}, []byte(payload)).GetPermission().GetTitle(), payload)
	}
}

// --- Reading a permissions request ---

// TestCodexReadControlRequest_APermissionsApprovalStatesTheSetItAsksFor covers
// item/permissions/requestApproval: no operation title, the requested set as
// its arguments, and a grant for the turn or for the session.
func TestCodexReadControlRequest_APermissionsApprovalStatesTheSetItAsksFor(t *testing.T) {
	t.Parallel()

	payload := codexRequestPayload(t, contracts.CodexMethodPermissionsRequestApproval, map[string]any{
		"reason":      "Publish the package.",
		"permissions": map[string]any{"network": map[string]any{"outbound": true}},
	})
	permission := agenttest.ControlPromptOf(codexProvider{}, payload).GetPermission()
	require.NotNil(t, permission)
	assert.Empty(t, permission.GetTitle())
	assert.Equal(t, "Publish the package.", permission.GetReason())
	assert.JSONEq(t, `{"network":{"outbound":true}}`, permission.GetInputJson())
	requireCodexOptions(t, []*leapmuxv1.PermissionOption{
		{OptionId: "turn", Kind: kindAllowOnce, Name: "Allow"},
		{OptionId: "session", Kind: kindAllowAlways, Name: "Allow for Session", Scope: leapmuxv1.PermissionScope_PERMISSION_SCOPE_SESSION},
		{OptionId: "deny", Kind: kindRejectOnce, Name: "Deny"},
	}, payload)
}

// TestCodexReadControlRequest_APermissionsApprovalKeepsOnlyTheNativeGrants
// covers the grants that the prompt keeps: the network and the file system,
// each as Codex states it, and nothing else.
func TestCodexReadControlRequest_APermissionsApprovalKeepsOnlyTheNativeGrants(t *testing.T) {
	t.Parallel()

	fileSystem := map[string]any{"read": []any{"/native path/read.txt"}, "write": []any{}}
	permission := agenttest.ControlPromptOf(codexProvider{}, codexPermissionsPayload(t, map[string]any{
		"fileSystem": fileSystem,
		"unrelated":  map[string]any{"approve": true},
	})).GetPermission()
	assert.JSONEq(t, `{"fileSystem":{"read":["/native path/read.txt"],"write":[]}}`, permission.GetInputJson())
}

// TestCodexReadControlRequest_AMalformedPermissionsApprovalAsksForNothing
// covers params, a grant set, and grants that are no object: each reads as an
// empty set, never as an absent one.
func TestCodexReadControlRequest_AMalformedPermissionsApprovalAsksForNothing(t *testing.T) {
	t.Parallel()

	malformed := []any{nil, []any{}, false, 0, "invalid"}
	for _, params := range malformed {
		permission := agenttest.ControlPromptOf(codexProvider{}, codexRequestPayload(t, contracts.CodexMethodPermissionsRequestApproval, params)).GetPermission()
		assert.JSONEq(t, `{}`, permission.GetInputJson(), "params %v", params)
	}
	for _, permissions := range malformed {
		permission := agenttest.ControlPromptOf(codexProvider{}, codexPermissionsPayload(t, permissions)).GetPermission()
		assert.JSONEq(t, `{}`, permission.GetInputJson(), "permissions %v", permissions)
	}
	permission := agenttest.ControlPromptOf(codexProvider{}, codexPermissionsPayload(t, map[string]any{"network": false, "fileSystem": []any{}})).GetPermission()
	assert.JSONEq(t, `{}`, permission.GetInputJson(), "a grant that is no object is dropped")
}

// --- Reading the plan-mode prompt ---

// TestCodexReadControlRequest_ThePlanModePromptIsAPlan covers LeapMux's own
// plan approval: a plan with no permissions, details or choices, because
// Codex's plan lives in the transcript.
func TestCodexReadControlRequest_ThePlanModePromptIsAPlan(t *testing.T) {
	t.Parallel()

	for _, payload := range [][]byte{
		[]byte(`{"request":{"tool_name":"CodexPlanModePrompt"}}`),
		codexToolPayload(t, contracts.CodexControlToolPlanModePrompt, map[string]any{}),
	} {
		agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{}}}, agenttest.ControlPromptOf(codexProvider{}, payload))
	}
}

// --- Reading a question ---

// TestCodexReadControlRequest_RecognizesEachQuestion covers the two question
// requests and three requests that are no question.
func TestCodexReadControlRequest_RecognizesEachQuestion(t *testing.T) {
	t.Parallel()

	for name, payload := range map[string][]byte{
		"an async question":   codexColorQuestion(t),
		"a requestUserInput":  codexRequestPayload(t, contracts.CodexMethodToolRequestUserInput, map[string]any{"questions": []any{}}),
		"an untitled async":   codexAsyncPayload(t, map[string]any{}),
		"a free-text async":   codexAsyncPayload(t, map[string]any{contracts.CodexAsyncQuestionInputTitle: "Which port?"}),
		"a question, no list": []byte(`{"method":"item/tool/requestUserInput"}`),
	} {
		question := agenttest.ControlPromptOf(codexProvider{}, payload).GetQuestion()
		require.NotNil(t, question, name)
		assert.True(t, question.GetPreservesSelectionNotes(), "%s: Codex reads a selection and a note together", name)
	}
	for name, payload := range map[string][]byte{
		"an approval method":       codexRequestPayload(t, contracts.CodexMethodCommandExecutionRequestApproval, map[string]any{}),
		"Claude's question tool":   codexToolPayload(t, "AskUserQuestion", map[string]any{}),
		"the plan-mode prompt":     codexToolPayload(t, contracts.CodexControlToolPlanModePrompt, map[string]any{}),
		"a request with no method": []byte(`{}`),
	} {
		assert.Nil(t, agenttest.ControlPromptOf(codexProvider{}, payload).GetQuestion(), name)
	}
}

// TestCodexReadControlRequest_ReadsAnAsyncQuestionWithItsOptions covers the
// one question of an async request.
func TestCodexReadControlRequest_ReadsAnAsyncQuestionWithItsOptions(t *testing.T) {
	t.Parallel()

	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{{
			Question: "Which color should I use?",
			Options:  []*leapmuxv1.QuestionOption{{Label: "Blue"}, {Label: "Red"}},
		}},
		PreservesSelectionNotes: true,
	}}}, agenttest.ControlPromptOf(codexProvider{}, codexColorQuestion(t)))
}

// TestCodexReadControlRequest_ReadsAnAsyncQuestionDefensively covers an async
// question with no options, with options that cannot be drawn, and with no
// title. A question with no title states nothing to answer, so a client can
// only skip it.
func TestCodexReadControlRequest_ReadsAnAsyncQuestionDefensively(t *testing.T) {
	t.Parallel()

	free := agenttest.ControlPromptOf(codexProvider{}, codexAsyncPayload(t, map[string]any{contracts.CodexAsyncQuestionInputTitle: "Which port should I use?"})).GetQuestion()
	require.Len(t, free.GetQuestions(), 1)
	assert.Equal(t, "Which port should I use?", free.GetQuestions()[0].GetQuestion())
	assert.Empty(t, free.GetQuestions()[0].GetOptions(), "a question with no options takes typed text")

	filtered := agenttest.ControlPromptOf(codexProvider{}, codexAsyncPayload(t, map[string]any{
		contracts.CodexAsyncQuestionInputTitle:   "Which?",
		contracts.CodexAsyncQuestionInputOptions: []any{"Blue", " ", "", 7, nil, "Red"},
	})).GetQuestion()
	require.Len(t, filtered.GetQuestions(), 1)
	labels := []string{}
	for _, option := range filtered.GetQuestions()[0].GetOptions() {
		labels = append(labels, option.GetLabel())
	}
	assert.Equal(t, []string{"Blue", "Red"}, labels, "a blank option and one that is no text are dropped")

	for name, input := range map[string]any{
		"no title":        map[string]any{},
		"a blank title":   map[string]any{contracts.CodexAsyncQuestionInputTitle: ""},
		"a title of 7":    map[string]any{contracts.CodexAsyncQuestionInputTitle: 7},
		"input as a text": "not an object",
	} {
		question := agenttest.ControlPromptOf(codexProvider{}, codexAsyncPayload(t, input)).GetQuestion()
		require.NotNil(t, question, name)
		assert.Empty(t, question.GetQuestions(), name)
	}
	assert.Empty(t, codexAsyncQuestions([]byte(`{}`)), "a payload with no input states no question")
}

// TestCodexReadControlRequest_ReadsEachUserInputQuestion covers
// item/tool/requestUserInput: each question with its options.
func TestCodexReadControlRequest_ReadsEachUserInputQuestion(t *testing.T) {
	t.Parallel()

	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{{
			Id: "q1", Question: "Which one?", Header: "Pick",
			Options: []*leapmuxv1.QuestionOption{{Label: "A", Description: "first"}},
		}},
		PreservesSelectionNotes: true,
	}}}, agenttest.ControlPromptOf(codexProvider{}, codexUserInputPayload(t, map[string]any{
		"id": "q1", "question": "Which one?", "header": "Pick", "isOther": true,
		"options": []any{map[string]any{"label": "A", "description": "first"}},
	})))
}

// TestCodexReadControlRequest_DropsAUserInputQuestionThatCannotBeDrawn covers
// an element that is no object, and a list that is no list.
func TestCodexReadControlRequest_DropsAUserInputQuestionThatCannotBeDrawn(t *testing.T) {
	t.Parallel()

	questions := agenttest.ControlPromptOf(codexProvider{}, codexUserInputPayload(t, nil, "plain text", 7, map[string]any{"question": "Which one?", "options": []any{}})).GetQuestion().GetQuestions()
	require.Len(t, questions, 1)
	assert.Equal(t, "Which one?", questions[0].GetQuestion())
	assert.Empty(t, questions[0].GetOptions())

	for _, payload := range [][]byte{
		[]byte(`{"method":"item/tool/requestUserInput"}`),
		codexRequestPayload(t, contracts.CodexMethodToolRequestUserInput, map[string]any{"questions": "nope"}),
		codexRequestPayload(t, contracts.CodexMethodToolRequestUserInput, []any{}),
	} {
		question := agenttest.ControlPromptOf(codexProvider{}, payload).GetQuestion()
		require.NotNil(t, question, string(payload))
		assert.Empty(t, question.GetQuestions(), string(payload))
	}
}

// TestCodexQuestionsWithOtherAlignsWithTheQuestions covers the flag that the
// answer reads beside each question: it skips each element that the question
// reader drops, so a flag never lands on the wrong question.
func TestCodexQuestionsWithOtherAlignsWithTheQuestions(t *testing.T) {
	t.Parallel()

	raw := codexJSON(t, []any{
		nil,
		map[string]any{"question": "Free", "isOther": true},
		"plain text",
		map[string]any{"question": "Pick", "isOther": true, "options": []any{map[string]any{"label": "A"}}},
		map[string]any{"question": "Flag of the wrong type", "isOther": "true", "options": []any{map[string]any{"label": "A"}}},
		map[string]any{"question": "No flag", "options": []any{map[string]any{"label": "A"}}},
	})
	assert.Len(t, providerkit.QuestionsFromWire(raw), 4)
	assert.Equal(t, []bool{false, true, false, false}, codexQuestionsWithOther(raw))
	assert.Empty(t, codexQuestionsWithOther(json.RawMessage(`"nope"`)))
}

// --- Reading an MCP elicitation ---

// TestCodexReadControlRequest_AnMCPElicitationIsAForm covers a form that asks
// for data: its fields, with no purpose and no accept choices.
func TestCodexReadControlRequest_AnMCPElicitationIsAForm(t *testing.T) {
	t.Parallel()

	payload := codexRequestPayload(t, contracts.MCPElicitationMethodCodex, map[string]any{
		"mode": "form", "message": "Choose the settings.", "serverName": "forms",
		"requestedSchema": map[string]any{"type": "object"},
	})
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: &leapmuxv1.ElicitationPrompt{
		Mode: "form", Message: "Choose the settings.", Server: "forms", SchemaJson: `{"type":"object"}`,
	}}}, agenttest.ControlPromptOf(codexProvider{}, payload))

	payload = codexRequestPayload(t, contracts.MCPElicitationMethodCodex, map[string]any{
		"mode": "url", "url": "https://example.invalid/login", "message": "Sign in.",
		"title": "Login", "description": "Sign in to the server.", "requestedSchema": nil,
	})
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: &leapmuxv1.ElicitationPrompt{
		Mode: "url", Url: "https://example.invalid/login", Message: "Sign in.", Title: "Login", Description: "Sign in to the server.",
	}}}, agenttest.ControlPromptOf(codexProvider{}, payload))
}

// TestCodexReadControlRequest_ReadsCodexsFormModesAsTheMCPForm covers the two
// spellings of a form that Codex uses, an absent mode, and a mode of the
// wrong type.
func TestCodexReadControlRequest_ReadsCodexsFormModesAsTheMCPForm(t *testing.T) {
	t.Parallel()

	for _, params := range []map[string]any{
		{"mode": "openai/form"},
		{"mode": "openaiForm"},
		{},
		{"mode": 7},
	} {
		assert.Equal(t, "form", agenttest.ControlPromptOf(codexProvider{}, codexRequestPayload(t, contracts.MCPElicitationMethodCodex, params)).GetElicitation().GetMode(), "%v", params)
	}
	assert.Equal(t, "form", agenttest.ControlPromptOf(codexProvider{}, codexRequestPayload(t, contracts.MCPElicitationMethodCodex, "invalid")).GetElicitation().GetMode(),
		"params that are no object read as an empty form")
}

// TestCodexReadControlRequest_AnMCPToolApprovalIsAPermission covers an
// elicitation that approves an MCP tool call: it states the tool's arguments,
// Codex's description of the tool, a default title, and an accept choice for
// each scope that the request offers.
func TestCodexReadControlRequest_AnMCPToolApprovalIsAPermission(t *testing.T) {
	t.Parallel()

	payload := codexRequestPayload(t, contracts.MCPElicitationMethodCodex, map[string]any{
		"mode": "form", "message": "Allow this tool?", "serverName": "github",
		"requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		"_meta": map[string]any{
			"codex_approval_kind": contracts.MCPElicitationApprovalKindToolCall,
			"tool_params":         map[string]any{"path": "sample.py", "count": 0},
			"tool_description":    "Reads a file.",
			"persist":             []any{"session", "always"},
		},
	})
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: &leapmuxv1.ElicitationPrompt{
		Purpose: leapmuxv1.ElicitationPurpose_ELICITATION_PURPOSE_PERMISSION,
		Mode:    "form", Message: "Allow this tool?", Server: "github",
		SchemaJson:    `{"properties":{},"type":"object"}`,
		ArgumentsJson: `{"count":0,"path":"sample.py"}`,
		Title:         "Permission Required",
		Description:   "Reads a file.",
		AcceptChoices: []*leapmuxv1.ElicitationAcceptChoice{
			{Key: "once", Label: "Once"},
			{Key: "session", Label: "Session"},
			{Key: "always", Label: "Always"},
		},
	}}}, agenttest.ControlPromptOf(codexProvider{}, payload))
}

// TestCodexReadControlRequest_AnMCPToolApprovalKeepsItsOwnTitle covers a tool
// suggestion that states its own title and description, and offers no scope.
func TestCodexReadControlRequest_AnMCPToolApprovalKeepsItsOwnTitle(t *testing.T) {
	t.Parallel()

	elicitation := agenttest.ControlPromptOf(codexProvider{}, codexRequestPayload(t, contracts.MCPElicitationMethodCodex, map[string]any{
		"title": "", "description": "Install the connector.",
		"_meta": map[string]any{"codex_approval_kind": contracts.MCPElicitationApprovalKindToolSuggestion, "tool_description": "unused"},
	})).GetElicitation()
	assert.Equal(t, leapmuxv1.ElicitationPurpose_ELICITATION_PURPOSE_PERMISSION, elicitation.GetPurpose())
	assert.Empty(t, elicitation.GetTitle(), "a title that the request states wins, even an empty one")
	assert.Equal(t, "Install the connector.", elicitation.GetDescription())
	assert.Empty(t, elicitation.GetArgumentsJson())
	require.Len(t, elicitation.GetAcceptChoices(), 1)
	assert.Equal(t, "once", elicitation.GetAcceptChoices()[0].GetKey())
}

// TestCodexReadControlRequest_OffersOnlyTheScopesThatAToolApprovalLists
// covers a persist list with values of the wrong type, and a persist value
// that is no list.
func TestCodexReadControlRequest_OffersOnlyTheScopesThatAToolApprovalLists(t *testing.T) {
	t.Parallel()

	keys := func(persist any) []string {
		elicitation := agenttest.ControlPromptOf(codexProvider{}, codexRequestPayload(t, contracts.MCPElicitationMethodCodex, map[string]any{
			"_meta": map[string]any{"codex_approval_kind": contracts.MCPElicitationApprovalKindToolCall, "persist": persist},
		})).GetElicitation()
		keys := []string{}
		for _, choice := range elicitation.GetAcceptChoices() {
			keys = append(keys, choice.GetKey())
		}
		return keys
	}
	assert.Equal(t, []string{"once", "always"}, keys([]any{7, "always", nil, "forever"}))
	assert.Equal(t, []string{"once"}, keys("session"))
	assert.Equal(t, []string{"once"}, keys(nil))
}

// TestCodexReadControlRequest_AnElicitationOfNoApprovalOffersNoChoice covers
// an elicitation whose kind is no tool approval: a plain form, which lists
// scopes in vain.
func TestCodexReadControlRequest_AnElicitationOfNoApprovalOffersNoChoice(t *testing.T) {
	t.Parallel()

	for _, kind := range []any{"other_kind", "", 7} {
		elicitation := agenttest.ControlPromptOf(codexProvider{}, codexRequestPayload(t, contracts.MCPElicitationMethodCodex, map[string]any{
			"_meta": map[string]any{"codex_approval_kind": kind, "persist": []any{"session"}, "tool_params": map[string]any{"a": 1}},
		})).GetElicitation()
		require.NotNil(t, elicitation, "kind %v", kind)
		assert.Equal(t, leapmuxv1.ElicitationPurpose_ELICITATION_PURPOSE_UNSPECIFIED, elicitation.GetPurpose(), "kind %v", kind)
		assert.Empty(t, elicitation.GetAcceptChoices(), "kind %v", kind)
		assert.Empty(t, elicitation.GetArgumentsJson(), "kind %v", kind)
		assert.Empty(t, elicitation.GetTitle(), "kind %v", kind)
	}
}

// --- Reading the decisions of an approval ---

// TestCodexReadControlRequest_OffersEachDecisionAsAnOption covers the
// decisions that the browser drew as Deny, Allow with its allow-as choices,
// and the menu: every one is an option, in Codex's order.
func TestCodexReadControlRequest_OffersEachDecisionAsAnOption(t *testing.T) {
	t.Parallel()

	requireCodexOptions(t, []*leapmuxv1.PermissionOption{
		{OptionId: "accept", Kind: kindAllowOnce, Name: "Allow"},
		{OptionId: "decline", Kind: kindRejectOnce, Name: "Deny"},
		{OptionId: "acceptWithExecpolicyAmendment:2", Kind: kindAllowAlways, Name: "Allow & Remember: rm", Scope: scopeUser, ScopeLabel: "Command rule"},
	}, codexDecisionsPayload(t, "accept", "decline", map[string]any{
		"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": []any{"rm"}},
	}))

	requireCodexOptions(t, []*leapmuxv1.PermissionOption{
		{OptionId: "accept", Kind: kindAllowOnce, Name: "Allow"},
		{OptionId: "decline", Kind: kindRejectOnce, Name: "Deny"},
		{OptionId: "cancel", Kind: kindRejectOnce, Name: "Cancel", EndsTurn: true},
		{OptionId: "acceptForSession", Kind: kindAllowAlways, Name: "Allow for Session", Scope: scopeSession},
		{OptionId: "applyNetworkPolicyAmendment:4", Kind: kindAllowAlways, Name: "Allow Host & Remember: example.com", Scope: scopeUser, ScopeLabel: "Host rule"},
	}, codexDecisionsPayload(t, "accept", "decline", "cancel", "acceptForSession", map[string]any{
		"applyNetworkPolicyAmendment": map[string]any{"network_policy_amendment": map[string]any{"host": "example.com", "action": "allow"}},
	}))
}

// TestCodexReadControlRequest_ABlockedHostIsARememberedRefusal covers a
// network amendment that blocks a host: it refuses, and it remembers. It is
// the list's refusal, so the prompt adds no cancel.
func TestCodexReadControlRequest_ABlockedHostIsARememberedRefusal(t *testing.T) {
	t.Parallel()

	requireCodexOptions(t, []*leapmuxv1.PermissionOption{
		{OptionId: "accept", Kind: kindAllowOnce, Name: "Allow"},
		{OptionId: "applyNetworkPolicyAmendment:1", Kind: kindRejectAlways, Name: "Block Host & Remember: example.com", Scope: scopeUser, ScopeLabel: "Host rule"},
	}, codexDecisionsPayload(t, "accept", map[string]any{
		"applyNetworkPolicyAmendment": map[string]any{"network_policy_amendment": map[string]any{"host": "example.com", "action": "deny"}},
	}))
}

// TestCodexReadControlRequest_StatesTheScopeOfEachRememberedDecision covers
// how far each remembered decision reaches. acceptForSession lasts for the
// session. Codex writes each amendment into the user's own configuration, so
// an amendment reaches every project of the user, and its scope label states
// the kind of rule that it keeps. A blocked host states the scope and the
// label of the host rule, so a client pairs it with the Host rule choice
// alone, and never with the Session choice.
func TestCodexReadControlRequest_StatesTheScopeOfEachRememberedDecision(t *testing.T) {
	t.Parallel()

	network := func(action string) map[string]any {
		return map[string]any{"applyNetworkPolicyAmendment": map[string]any{"network_policy_amendment": map[string]any{"host": "example.com", "action": action}}}
	}
	requireCodexOptions(t, []*leapmuxv1.PermissionOption{
		{OptionId: "accept", Kind: kindAllowOnce, Name: "Allow"},
		{OptionId: "acceptForSession", Kind: kindAllowAlways, Name: "Allow for Session", Scope: scopeSession},
		{OptionId: "acceptWithExecpolicyAmendment:2", Kind: kindAllowAlways, Name: "Allow & Remember: git push", Scope: scopeUser, ScopeLabel: "Command rule"},
		{OptionId: "applyNetworkPolicyAmendment:3", Kind: kindAllowAlways, Name: "Allow Host & Remember: example.com", Scope: scopeUser, ScopeLabel: "Host rule"},
		{OptionId: "applyNetworkPolicyAmendment:4", Kind: kindRejectAlways, Name: "Block Host & Remember: example.com", Scope: scopeUser, ScopeLabel: "Host rule"},
		{OptionId: "cancel", Kind: kindRejectOnce, Name: "Cancel", EndsTurn: true},
	}, codexDecisionsPayload(t,
		"accept", "acceptForSession",
		map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": []any{"git", "push"}}},
		network("allow"), network("deny"), "cancel",
	))

	// The decisions that remember nothing state no scope.
	for _, option := range agenttest.ControlPromptOf(codexProvider{}, codexDecisionsPayload(t, "accept", "decline", "cancel")).GetPermission().GetOptions() {
		assert.Equal(t, leapmuxv1.PermissionScope_PERMISSION_SCOPE_UNSPECIFIED, option.GetScope(), option.GetOptionId())
		assert.Empty(t, option.GetScopeLabel(), option.GetOptionId())
	}
}

// TestCodexReadControlRequest_TellsTwoRulesOfOneKindApart covers two host
// rules and two command rules: each keeps an option of its own, named for its
// subject.
func TestCodexReadControlRequest_TellsTwoRulesOfOneKindApart(t *testing.T) {
	t.Parallel()

	host := func(name string) map[string]any {
		return map[string]any{"applyNetworkPolicyAmendment": map[string]any{"network_policy_amendment": map[string]any{"host": name, "action": "allow"}}}
	}
	rule := func(command ...any) map[string]any {
		return map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": append([]any{}, command...)}}
	}
	requireCodexOptions(t, []*leapmuxv1.PermissionOption{
		{OptionId: "accept", Kind: kindAllowOnce, Name: "Allow"},
		{OptionId: "applyNetworkPolicyAmendment:1", Kind: kindAllowAlways, Name: "Allow Host & Remember: a.example.com", Scope: scopeUser, ScopeLabel: "Host rule"},
		{OptionId: "applyNetworkPolicyAmendment:2", Kind: kindAllowAlways, Name: "Allow Host & Remember: b.example.com", Scope: scopeUser, ScopeLabel: "Host rule"},
		{OptionId: "acceptWithExecpolicyAmendment:3", Kind: kindAllowAlways, Name: "Allow & Remember: rm", Scope: scopeUser, ScopeLabel: "Command rule"},
		{OptionId: "acceptWithExecpolicyAmendment:4", Kind: kindAllowAlways, Name: "Allow & Remember: mv -f", Scope: scopeUser, ScopeLabel: "Command rule"},
		{OptionId: "acceptWithExecpolicyAmendment:5", Kind: kindAllowAlways, Name: "Allow & Remember", Scope: scopeUser, ScopeLabel: "Command rule"},
		{OptionId: "decline", Kind: kindRejectOnce, Name: "Deny"},
	}, codexDecisionsPayload(t, "accept", host("a.example.com"), host("b.example.com"), rule("rm"), rule("mv", "-f"), rule(), "decline"))
}

// TestCodexReadControlRequest_OffersARepeatedWordOnce covers a decision word
// that the list repeats: both copies send the same answer, so one option
// offers it.
func TestCodexReadControlRequest_OffersARepeatedWordOnce(t *testing.T) {
	t.Parallel()

	requireCodexOptions(t, []*leapmuxv1.PermissionOption{
		{OptionId: "accept", Kind: kindAllowOnce, Name: "Allow"},
		{OptionId: "acceptForSession", Kind: kindAllowAlways, Name: "Allow for Session", Scope: leapmuxv1.PermissionScope_PERMISSION_SCOPE_SESSION},
		{OptionId: "decline", Kind: kindRejectOnce, Name: "Deny"},
	}, codexDecisionsPayload(t, "accept", "accept", "acceptForSession", "decline", "decline"))
}

// TestCodexReadControlRequest_OffersEveryRuleBeyondTheBrowsersPills covers a
// list longer than the four allow pills that the browser's Codex control drew.
// That control kept the rest in its menu, and the prompt offers each one.
func TestCodexReadControlRequest_OffersEveryRuleBeyondTheBrowsersPills(t *testing.T) {
	t.Parallel()

	decisions := []any{"accept"}
	for _, name := range []string{"a", "b", "c", "d"} {
		decisions = append(decisions, map[string]any{"applyNetworkPolicyAmendment": map[string]any{
			"network_policy_amendment": map[string]any{"host": name + ".example.com", "action": "allow"},
		}})
	}
	decisions = append(decisions, "decline")
	options := agenttest.ControlPromptOf(codexProvider{}, codexDecisionsPayload(t, decisions...)).GetPermission().GetOptions()
	require.Len(t, options, 6)
	assert.Equal(t, "Allow Host & Remember: d.example.com", options[4].GetName())
}

// TestCodexReadControlRequest_OffersOnlyTheDecisionsThatCodexOffers covers a
// list with one polarity: the prompt invents no approval for a list that
// refuses only, and offers the refusal that Codex offered.
func TestCodexReadControlRequest_OffersOnlyTheDecisionsThatCodexOffers(t *testing.T) {
	t.Parallel()

	requireCodexOptions(t, []*leapmuxv1.PermissionOption{
		{OptionId: "decline", Kind: kindRejectOnce, Name: "Deny"},
		{OptionId: "cancel", Kind: kindRejectOnce, Name: "Cancel", EndsTurn: true},
	}, codexDecisionsPayload(t, "decline", "cancel"))
	requireCodexOptions(t, []*leapmuxv1.PermissionOption{
		{OptionId: "accept", Kind: kindAllowOnce, Name: "Allow"},
		{OptionId: "cancel", Kind: kindRejectOnce, Name: "Cancel", EndsTurn: true},
	}, codexDecisionsPayload(t, "accept", "cancel"))
}

// TestCodexReadControlRequest_AddsCancelToAListThatRefusesNothing covers a
// list with no refusal. The browser's typed refusal still answered it with
// cancel, so the prompt offers cancel as the refusal.
func TestCodexReadControlRequest_AddsCancelToAListThatRefusesNothing(t *testing.T) {
	t.Parallel()

	requireCodexOptions(t, []*leapmuxv1.PermissionOption{
		{OptionId: "accept", Kind: kindAllowOnce, Name: "Allow"},
		{OptionId: "acceptForSession", Kind: kindAllowAlways, Name: "Allow for Session", Scope: leapmuxv1.PermissionScope_PERMISSION_SCOPE_SESSION},
		{OptionId: "cancel", Kind: kindRejectOnce, Name: "Cancel", EndsTurn: true},
	}, codexDecisionsPayload(t, "accept", "acceptForSession"))
}

// TestCodexReadControlRequest_AddsAcceptWhenAnUnknownDecisionMayApprove
// covers a list whose unknown value may have been its approval: the reader
// dropped it, so the prompt offers accept. A list whose known part approves
// gains nothing.
func TestCodexReadControlRequest_AddsAcceptWhenAnUnknownDecisionMayApprove(t *testing.T) {
	t.Parallel()

	sandbox := map[string]any{"acceptWithSandboxAmendment": map[string]any{"sandbox_amendment": []any{"/tmp"}}}
	requireCodexOptions(t, []*leapmuxv1.PermissionOption{
		{OptionId: "decline", Kind: kindRejectOnce, Name: "Deny"},
		{OptionId: "accept", Kind: kindAllowOnce, Name: "Allow"},
	}, codexDecisionsPayload(t, sandbox, "decline"))
	requireCodexOptions(t, []*leapmuxv1.PermissionOption{
		{OptionId: "acceptForSession", Kind: kindAllowAlways, Name: "Allow for Session", Scope: leapmuxv1.PermissionScope_PERMISSION_SCOPE_SESSION},
		{OptionId: "decline", Kind: kindRejectOnce, Name: "Deny"},
	}, codexDecisionsPayload(t, sandbox, "acceptForSession", "decline"))
	// An intact list that refuses only gains no approval.
	requireCodexOptions(t, []*leapmuxv1.PermissionOption{
		{OptionId: "decline", Kind: kindRejectOnce, Name: "Deny"},
		{OptionId: "cancel", Kind: kindRejectOnce, Name: "Cancel", EndsTurn: true},
	}, codexDecisionsPayload(t, "decline", "cancel"))
}

// TestCodexReadControlRequest_IgnoresAMalformedDecision covers values that
// are no decision: each is dropped, and the known ones stay.
func TestCodexReadControlRequest_IgnoresAMalformedDecision(t *testing.T) {
	t.Parallel()

	requireCodexOptions(t, []*leapmuxv1.PermissionOption{
		{OptionId: "accept", Kind: kindAllowOnce, Name: "Allow"},
		{OptionId: "decline", Kind: kindRejectOnce, Name: "Deny"},
	}, codexDecisionsPayload(t,
		"accept", nil, 3, map[string]any{}, "somethingElse", map[string]any{"other": 1},
		map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": []any{"rm", 7}}},
		map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": nil}},
		map[string]any{"acceptWithExecpolicyAmendment": "rm"},
		map[string]any{"applyNetworkPolicyAmendment": map[string]any{"network_policy_amendment": map[string]any{"host": "h", "action": "maybe"}}},
		map[string]any{"applyNetworkPolicyAmendment": map[string]any{"network_policy_amendment": map[string]any{"host": 7, "action": "allow"}}},
		map[string]any{"applyNetworkPolicyAmendment": map[string]any{}},
		map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": []any{}}, "extra": 1},
		"decline",
	))
}

// TestCodexReadControlRequest_AnEmptyOrUnknownListOffersAcceptAndCancel
// covers a list with no known decision: Codex's own accept and cancel answer
// it.
func TestCodexReadControlRequest_AnEmptyOrUnknownListOffersAcceptAndCancel(t *testing.T) {
	t.Parallel()

	want := []*leapmuxv1.PermissionOption{
		{OptionId: "accept", Kind: kindAllowOnce, Name: "Allow"},
		{OptionId: "cancel", Kind: kindRejectOnce, Name: "Cancel", EndsTurn: true},
	}
	requireCodexOptions(t, want, codexRequestPayload(t, contracts.CodexMethodCommandExecutionRequestApproval, map[string]any{"command": "pwd"}))
	requireCodexOptions(t, want, codexDecisionsPayload(t))
	requireCodexOptions(t, want, codexDecisionsPayload(t, "somethingElse", nil))
	requireCodexOptions(t, want, codexRequestPayload(t, contracts.CodexMethodCommandExecutionRequestApproval, map[string]any{"availableDecisions": "accept"}))
}

// --- Answering an approval ---

// TestCodexAnswerControlRequest_SendsTheChosenDecision covers the decision
// that each option sends, in a JSON-RPC result that keeps the Worker's
// request id as a string, whatever its digits.
func TestCodexAnswerControlRequest_SendsTheChosenDecision(t *testing.T) {
	t.Parallel()

	payload := codexDecisionsPayload(t, "accept", "acceptForSession", "decline", "cancel")
	for _, test := range []struct {
		requestID, optionID, want string
	}{
		{requestID: "42", optionID: "accept", want: `{"decision":"accept"}`},
		{requestID: "7", optionID: "decline", want: `{"decision":"decline"}`},
		{requestID: "abc", optionID: "acceptForSession", want: `{"decision":"acceptForSession"}`},
		{requestID: "007", optionID: "cancel", want: `{"decision":"cancel"}`},
		{requestID: "9007199254740993", optionID: "accept", want: `{"decision":"accept"}`},
	} {
		answer := requireCodexAnswer(t, payload, test.requestID, agenttest.PermissionDecision(test.optionID, ""))
		assert.JSONEq(t, test.want, codexAnswerResult(t, answer.Content, test.requestID), test.optionID)
		assert.Empty(t, answer.FollowUp, test.optionID)
		assert.Nil(t, answer.PlanApproval, test.optionID)
	}
}

// TestCodexAnswerControlRequest_SendsAnAmendmentAsCodexOfferedIt covers each
// remembered rule: the answer sends back the object that Codex offered.
func TestCodexAnswerControlRequest_SendsAnAmendmentAsCodexOfferedIt(t *testing.T) {
	t.Parallel()

	payload := codexDecisionsPayload(t,
		"accept",
		map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": []any{"touch"}}},
		map[string]any{"applyNetworkPolicyAmendment": map[string]any{"network_policy_amendment": map[string]any{"host": "a.example.com", "action": "allow"}}},
		map[string]any{"applyNetworkPolicyAmendment": map[string]any{"network_policy_amendment": map[string]any{"host": "b.example.com", "action": "allow"}}},
		map[string]any{"applyNetworkPolicyAmendment": map[string]any{"network_policy_amendment": map[string]any{"host": "c.example.com", "action": "deny"}}},
		"decline",
	)
	for optionID, want := range map[string]string{
		"acceptWithExecpolicyAmendment:1": `{"decision":{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["touch"]}}}`,
		"applyNetworkPolicyAmendment:3":   `{"decision":{"applyNetworkPolicyAmendment":{"network_policy_amendment":{"host":"b.example.com","action":"allow"}}}}`,
		"applyNetworkPolicyAmendment:4":   `{"decision":{"applyNetworkPolicyAmendment":{"network_policy_amendment":{"host":"c.example.com","action":"deny"}}}}`,
	} {
		answer := requireCodexAnswer(t, payload, "9", agenttest.PermissionDecision(optionID, ""))
		assert.JSONEq(t, want, codexAnswerResult(t, answer.Content, "9"), optionID)
	}
}

// TestCodexAnswerControlRequest_SendsTheDecisionsThatLeapMuxAdded covers the
// accept and the cancel that the prompt adds beside Codex's own list.
func TestCodexAnswerControlRequest_SendsTheDecisionsThatLeapMuxAdded(t *testing.T) {
	t.Parallel()

	answer := requireCodexAnswer(t, codexDecisionsPayload(t,
		map[string]any{"acceptWithSandboxAmendment": map[string]any{"sandbox_amendment": []any{"/tmp"}}}, "decline",
	), "7", agenttest.PermissionDecision("accept", ""))
	assert.JSONEq(t, `{"decision":"accept"}`, codexAnswerResult(t, answer.Content, "7"), "the approval that the unknown value may have been")

	answer = requireCodexAnswer(t, codexDecisionsPayload(t, "accept", "acceptForSession"), "7", agenttest.PermissionDecision("cancel", "Use a safer command"))
	assert.JSONEq(t, `{"decision":"cancel"}`, codexAnswerResult(t, answer.Content, "7"), "the refusal of a list that offers none")
	assert.Equal(t, "Use a safer command", answer.FollowUp)
}

// TestCodexAnswerControlRequest_ARefusalWithFeedbackFollowsAsAMessage covers
// the typed refusal: the decision refuses, and the text follows the answer
// as a message, because Codex's answer has no field for it.
func TestCodexAnswerControlRequest_ARefusalWithFeedbackFollowsAsAMessage(t *testing.T) {
	t.Parallel()

	payload := codexDecisionsPayload(t, "accept", "cancel")
	answer := requireCodexAnswer(t, payload, "7", agenttest.PermissionDecision("cancel", "do something else"))
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"7","result":{"decision":"cancel"}}`, string(answer.Content))
	assert.Equal(t, "do something else", answer.FollowUp)

	answer = requireCodexAnswer(t, codexDecisionsPayload(t, "accept", map[string]any{
		"applyNetworkPolicyAmendment": map[string]any{"network_policy_amendment": map[string]any{"host": "h", "action": "deny"}},
	}), "7", agenttest.PermissionDecision("applyNetworkPolicyAmendment:1", "  keep it blocked  "))
	assert.Equal(t, "  keep it blocked  ", answer.FollowUp, "the message is the text as the user typed it")

	answer = requireCodexAnswer(t, payload, "7", agenttest.PermissionDecision("cancel", " \n\t"))
	assert.JSONEq(t, `{"decision":"cancel"}`, codexAnswerResult(t, answer.Content, "7"))
	assert.Empty(t, answer.FollowUp, "text of whitespace alone sends no message")
}

// TestCodexAnswerControlRequest_RefusesAnApprovalThatItCannotSend covers an
// allow with feedback, an option that the prompt does not offer, and a
// decision of another kind.
func TestCodexAnswerControlRequest_RefusesAnApprovalThatItCannotSend(t *testing.T) {
	t.Parallel()

	payload := codexDecisionsPayload(t, "accept", "decline", map[string]any{
		"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": []any{"rm"}},
	})
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"an allow with feedback":         agenttest.PermissionDecision("accept", "but be careful"),
		"a rule with feedback":           agenttest.PermissionDecision("acceptWithExecpolicyAmendment:2", "why"),
		"an option the list lacks":       agenttest.PermissionDecision("acceptForSession", ""),
		"a rule at another place":        agenttest.PermissionDecision("acceptWithExecpolicyAmendment:1", ""),
		"LeapMux's generic allow":        agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""),
		"a plan decision":                agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true}),
		"a question decision":            agenttest.QuestionDecision(),
		"a decision of no kind":          {},
		"no decision at all, a nil kind": nil,
	} {
		_, err := agenttest.AnswerControl(codexProvider{}, payload, "7", decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

// TestCodexAnswerControlRequest_DeniesWithTheDecisionThatTheBrowserDenies
// covers the cross-provider parity of Deny: the first refusal that Codex
// offers is decline.
func TestCodexAnswerControlRequest_DeniesWithTheDecisionThatTheBrowserDenies(t *testing.T) {
	t.Parallel()

	payload := codexRequestPayload(t, contracts.CodexMethodCommandExecutionRequestApproval, map[string]any{
		"command": "pwd", "availableDecisions": []any{"accept", "acceptForSession", "decline", "cancel"},
	})
	var deny *leapmuxv1.PermissionOption
	for _, option := range agenttest.ControlPromptOf(codexProvider{}, payload).GetPermission().GetOptions() {
		if option.GetKind() == kindRejectOnce {
			deny = option
			break
		}
	}
	require.NotNil(t, deny)
	answer := requireCodexAnswer(t, payload, "request", agenttest.PermissionDecision(deny.GetOptionId(), ""))
	assert.JSONEq(t, `{"decision":"decline"}`, codexAnswerResult(t, answer.Content, "request"))
}

// --- Answering a permissions request ---

// TestCodexAnswerControlRequest_GrantsTheRequestedPermissions covers each
// answer to a permissions request: the requested set for the turn or the
// session, or an empty set. A grant that is no object is dropped from the
// answer, as from the prompt.
func TestCodexAnswerControlRequest_GrantsTheRequestedPermissions(t *testing.T) {
	t.Parallel()

	payload := codexPermissionsPayload(t, map[string]any{"network": map[string]any{"enabled": true}, "fileSystem": nil})
	for optionID, want := range map[string]string{
		"turn":    `{"permissions":{"network":{"enabled":true}},"scope":"turn"}`,
		"session": `{"permissions":{"network":{"enabled":true}},"scope":"session"}`,
		"deny":    `{"permissions":{},"scope":"turn"}`,
	} {
		answer := requireCodexAnswer(t, payload, "request-1", agenttest.PermissionDecision(optionID, ""))
		assert.JSONEq(t, want, codexAnswerResult(t, answer.Content, "request-1"), optionID)
		assert.Empty(t, answer.FollowUp, optionID)
	}

	answer := requireCodexAnswer(t, payload, "7", agenttest.PermissionDecision("deny", "do something else"))
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"7","result":{"permissions":{},"scope":"turn"}}`, string(answer.Content))
	assert.Equal(t, "do something else", answer.FollowUp)

	for _, decision := range []*leapmuxv1.ControlDecision{
		agenttest.PermissionDecision("turn", "but only once"),
		agenttest.PermissionDecision("accept", ""),
	} {
		_, err := agenttest.AnswerControl(codexProvider{}, payload, "7", decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
	}
}

// --- Answering the plan-mode prompt ---

// TestCodexAnswerControlRequest_ApprovesThePlanWithItsSettings covers the
// plan approval: an allow with the request's own input, and the LeapMux
// settings beside the answer, never inside it.
func TestCodexAnswerControlRequest_ApprovesThePlanWithItsSettings(t *testing.T) {
	t.Parallel()

	payload := codexToolPayload(t, contracts.CodexControlToolPlanModePrompt, map[string]any{})
	for _, settings := range []*leapmuxv1.PlanApprovalSettings{
		{ClearContext: true},
		{PermissionMode: contracts.CodexPermissionModeNever},
		{PermissionMode: contracts.CodexPermissionModeApproveForMe},
	} {
		answer := requireCodexAnswer(t, payload, "plan-1", agenttest.PlanDecision(&leapmuxv1.PlanDecision{
			Approve: true, PermissionMode: settings.GetPermissionMode(), ClearContext: settings.GetClearContext(),
		}))
		assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"plan-1","response":{"behavior":"allow","updatedInput":{}}}}`, string(answer.Content))
		assert.True(t, proto.Equal(settings, answer.PlanApproval), "%v", settings)
		assert.Empty(t, answer.FollowUp)
	}
}

// TestCodexAnswerControlRequest_RefusesThePlan covers the plan's Reject and
// its typed refusal. The Worker already forwards the plan's feedback, so the
// answer adds no message.
func TestCodexAnswerControlRequest_RefusesThePlan(t *testing.T) {
	t.Parallel()

	payload := codexToolPayload(t, contracts.CodexControlToolPlanModePrompt, map[string]any{})
	answer := requireCodexAnswer(t, payload, "plan-7", agenttest.PlanDecision(&leapmuxv1.PlanDecision{}))
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"plan-7","response":{"behavior":"deny","message":"Rejected by user."}}}`, string(answer.Content))
	assert.Nil(t, answer.PlanApproval)

	answer = requireCodexAnswer(t, payload, "plan-7", agenttest.PlanDecision(&leapmuxv1.PlanDecision{Feedback: "Revise the plan"}))
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"plan-7","response":{"behavior":"deny","message":"Revise the plan"}}}`, string(answer.Content))
	assert.Empty(t, answer.FollowUp, "the Worker forwards the plan's feedback itself")

	_, err := agenttest.AnswerControl(codexProvider{}, payload, "plan-7", agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, ChoiceId: "revise"}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "Codex's plan offers no choices")
	_, err = agenttest.AnswerControl(codexProvider{}, payload, "plan-7", agenttest.PermissionDecision("accept", ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a plan takes no permission decision")
}

// --- Answering a requestUserInput question ---

// TestCodexAnswerControlRequest_AnswersEachUserInputQuestion covers the keys
// and the values of Codex's answers.
func TestCodexAnswerControlRequest_AnswersEachUserInputQuestion(t *testing.T) {
	t.Parallel()

	option := func(label string) map[string]any { return map[string]any{"label": label} }
	for _, test := range []struct {
		name     string
		question map[string]any
		answer   *leapmuxv1.QuestionAnswer
		want     string
	}{
		{
			name:     "a selection under the question's id",
			question: map[string]any{"id": "q1", "header": "Header1", "question": "Pick one", "options": []any{option("A"), option("B")}},
			answer:   &leapmuxv1.QuestionAnswer{Selected: []string{"A"}},
			want:     `{"q1":{"answers":["A"]}}`,
		},
		{
			name:     "a selection under the header of a question with no id",
			question: map[string]any{"header": "MyHeader", "question": "Pick one", "options": []any{option("X")}},
			answer:   &leapmuxv1.QuestionAnswer{Selected: []string{"X"}},
			want:     `{"MyHeader":{"answers":["X"]}}`,
		},
		{
			name:     "a selection under the index of a question with neither",
			question: map[string]any{"question": "Pick one", "options": []any{option("X")}},
			answer:   &leapmuxv1.QuestionAnswer{Selected: []string{"X"}},
			want:     `{"q0":{"answers":["X"]}}`,
		},
		{
			name:     "a typed answer as a user note",
			question: map[string]any{"id": "q1", "question": "Custom input", "options": []any{}},
			answer:   &leapmuxv1.QuestionAnswer{CustomText: "my custom answer"},
			want:     `{"q1":{"answers":["user_note: my custom answer"]}}`,
		},
		{
			name:     "a multiple selection as separate entries",
			question: map[string]any{"id": "q1", "question": "Pick multiple", "multiSelect": true, "options": []any{option("A"), option("B"), option("C")}},
			answer:   &leapmuxv1.QuestionAnswer{Selected: []string{"A", "C"}},
			want:     `{"q1":{"answers":["A","C"]}}`,
		},
		{
			name:     "a selection with a note after it",
			question: map[string]any{"id": "q1", "question": "Pick one", "options": []any{option("A"), option("B")}},
			answer:   &leapmuxv1.QuestionAnswer{Selected: []string{"B"}, CustomText: "note for B"},
			want:     `{"q1":{"answers":["B","user_note: note for B"]}}`,
		},
		{
			name:     "a note on a question with Codex's free-form option",
			question: map[string]any{"id": "q1", "question": "Pick one", "isOther": true, "options": []any{option("A"), option("B")}},
			answer:   &leapmuxv1.QuestionAnswer{CustomText: "my custom answer"},
			want:     `{"q1":{"answers":["None of the above","user_note: my custom answer"]}}`,
		},
		{
			name:     "a note beside a selection on a question with Codex's free-form option",
			question: map[string]any{"id": "q1", "question": "Pick one", "isOther": true, "options": []any{option("A")}},
			answer:   &leapmuxv1.QuestionAnswer{Selected: []string{"A"}, CustomText: "why"},
			want:     `{"q1":{"answers":["A","user_note: why"]}}`,
		},
		{
			name:     "a note whose free-form flag is false",
			question: map[string]any{"id": "q1", "question": "Pick one", "isOther": false, "options": []any{option("A")}},
			answer:   &leapmuxv1.QuestionAnswer{CustomText: "my custom answer"},
			want:     `{"q1":{"answers":["user_note: my custom answer"]}}`,
		},
		{
			name:     "a note whose free-form flag is absent",
			question: map[string]any{"id": "q1", "question": "Pick one", "options": []any{option("A")}},
			answer:   &leapmuxv1.QuestionAnswer{CustomText: "my custom answer"},
			want:     `{"q1":{"answers":["user_note: my custom answer"]}}`,
		},
		{
			name:     "a note on a free-form question with no options",
			question: map[string]any{"id": "q1", "question": "Free text", "isOther": true, "options": []any{}},
			answer:   &leapmuxv1.QuestionAnswer{CustomText: "my custom answer"},
			want:     `{"q1":{"answers":["user_note: my custom answer"]}}`,
		},
		{
			name:     "a note on a free-form question that states no options",
			question: map[string]any{"id": "q1", "question": "Free text", "isOther": true},
			answer:   &leapmuxv1.QuestionAnswer{CustomText: "my custom answer"},
			want:     `{"q1":{"answers":["user_note: my custom answer"]}}`,
		},
		{
			name:     "a note trimmed at its ends only",
			question: map[string]any{"id": "q1", "question": "Pick one", "isOther": true, "options": []any{option("A")}},
			answer:   &leapmuxv1.QuestionAnswer{CustomText: "  \n실제 내용 😀\n  second line  \t"},
			want:     `{"q1":{"answers":["None of the above","user_note: 실제 내용 😀\n  second line"]}}`,
		},
	} {
		answer := requireCodexAnswer(t, codexUserInputPayload(t, test.question), "native-answer", agenttest.QuestionDecision(test.answer))
		assert.JSONEq(t, `{"answers":`+test.want+`}`, codexAnswerResult(t, answer.Content, "native-answer"), test.name)
		assert.Empty(t, answer.FollowUp, test.name)
	}
}

// TestCodexAnswerControlRequest_RefusesAQuestionLeftOpen covers an answer
// that leaves a question with nothing, or with white space alone: the
// browser never sends one, so the Worker refuses it.
func TestCodexAnswerControlRequest_RefusesAQuestionLeftOpen(t *testing.T) {
	t.Parallel()

	payload := codexUserInputPayload(t, map[string]any{"id": "q1", "question": "Pick one", "isOther": true, "options": []any{map[string]any{"label": "A"}}})
	for _, text := range []string{"", " ", "\t\n"} {
		_, err := agenttest.AnswerControl(codexProvider{}, payload, "7", agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{CustomText: text}))
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "%q", text)
	}
}

// TestCodexAnswerControlRequest_KeepsASelectedValueThatLooksFalsy covers an
// option whose value reads as false in the browser: the answer keeps it, and
// adds no empty note.
func TestCodexAnswerControlRequest_KeepsASelectedValueThatLooksFalsy(t *testing.T) {
	t.Parallel()

	for _, selected := range []string{"0", "false", ""} {
		payload := codexUserInputPayload(t, map[string]any{"id": "q1", "question": "Pick one", "isOther": true, "options": []any{map[string]any{"label": selected}}})
		answer := requireCodexAnswer(t, payload, "7", agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{selected}}))
		assert.JSONEq(t, `{"answers":{"q1":{"answers":[`+string(codexJSON(t, selected))+`]}}}`, codexAnswerResult(t, answer.Content, "7"), "%q", selected)
	}
}

// TestCodexAnswerControlRequest_AnswersEveryQuestion covers a request of
// three questions, where a free-form flag lands on its own question after an
// element that the reader dropped.
func TestCodexAnswerControlRequest_AnswersEveryQuestion(t *testing.T) {
	t.Parallel()

	payload := codexUserInputPayload(t,
		map[string]any{"id": "q1", "question": "First", "options": []any{map[string]any{"label": "A"}}},
		"not a question",
		map[string]any{"id": "q2", "question": "Second", "isOther": true, "options": []any{map[string]any{"label": "B"}}},
		map[string]any{"id": "q3", "question": "Third", "options": []any{map[string]any{"label": "C"}}},
	)
	answer := requireCodexAnswer(t, payload, "12", agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{Selected: []string{"A"}},
		&leapmuxv1.QuestionAnswer{CustomText: "other"},
		&leapmuxv1.QuestionAnswer{Selected: []string{"C"}},
	))
	assert.JSONEq(t, `{"answers":{"q1":{"answers":["A"]},"q2":{"answers":["None of the above","user_note: other"]},"q3":{"answers":["C"]}}}`,
		codexAnswerResult(t, answer.Content, "12"))
}

// TestCodexAnswerControlRequest_KeepsTheWorkersIDInAQuestionAnswer covers
// the request ids that a JSON number would round.
func TestCodexAnswerControlRequest_KeepsTheWorkersIDInAQuestionAnswer(t *testing.T) {
	t.Parallel()

	payload := codexUserInputPayload(t, map[string]any{"id": "q1", "header": "Action", "question": "What next?", "options": []any{map[string]any{"label": "Build"}}})
	for _, requestID := range []string{"7", "007", "9007199254740993"} {
		answer := requireCodexAnswer(t, payload, requestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Build"}}))
		assert.JSONEq(t, `{"answers":{"q1":{"answers":["Build"]}}}`, codexAnswerResult(t, answer.Content, requestID), requestID)
	}
}

// TestCodexAnswerControlRequest_DeclinesAUserInputQuestion covers a cancelled
// question: an answer with no entries. Codex's answer has no field for a
// reason, so the feedback goes nowhere.
func TestCodexAnswerControlRequest_DeclinesAUserInputQuestion(t *testing.T) {
	t.Parallel()

	payload := codexUserInputPayload(t, map[string]any{"id": "q1", "question": "Which?", "options": []any{}})
	answer := requireCodexAnswer(t, payload, "7", agenttest.CancelQuestions("User stopped"))
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"7","result":{"answers":{}}}`, string(answer.Content))
	assert.Empty(t, answer.FollowUp)
}

// TestCodexAnswerControlRequest_RefusesAQuestionAnswerThatItCannotSend covers
// an answer to an option that the question lacks, two selections for one
// question, more answers than questions, and a decision of another kind.
func TestCodexAnswerControlRequest_RefusesAQuestionAnswerThatItCannotSend(t *testing.T) {
	t.Parallel()

	payload := codexUserInputPayload(t, map[string]any{"id": "q1", "question": "Pick one", "options": []any{map[string]any{"label": "A"}, map[string]any{"label": "B"}}})
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"an option the question lacks": agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"C"}}),
		"two options for one question": agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"A", "B"}}),
		"more answers than questions":  agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{}, &leapmuxv1.QuestionAnswer{}),
		"a permission decision":        agenttest.PermissionDecision("accept", ""),
	} {
		_, err := agenttest.AnswerControl(codexProvider{}, payload, "7", decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

// --- Answering an async question ---

// TestCodexAnswerControlRequest_AnswersAnAsyncQuestion covers the selection,
// the typed note, and both: the whole answer travels in one message, because
// Codex keeps no pending question state.
func TestCodexAnswerControlRequest_AnswersAnAsyncQuestion(t *testing.T) {
	t.Parallel()

	const requestID = "codex-async-question-call_abc-0"
	for _, test := range []struct {
		name   string
		answer *leapmuxv1.QuestionAnswer
		want   string
	}{
		{name: "a selection", answer: &leapmuxv1.QuestionAnswer{Selected: []string{"Red"}}, want: "Red"},
		{name: "a trimmed note", answer: &leapmuxv1.QuestionAnswer{CustomText: "  A light shade  "}, want: "A light shade"},
		{name: "a selection and a note", answer: &leapmuxv1.QuestionAnswer{Selected: []string{"Red"}, CustomText: "A light shade"}, want: "Red\n\nA light shade"},
		{name: "the automatic text", answer: &leapmuxv1.QuestionAnswer{CustomText: "Go with the recommended option."}, want: "Go with the recommended option."},
	} {
		answer := requireCodexAnswer(t, codexColorQuestion(t), requestID, agenttest.QuestionDecision(test.answer))
		assert.JSONEq(t,
			`{"type":"control_response","response":{"subtype":"success","request_id":"`+requestID+`","response":{"behavior":"allow","answer":`+string(codexJSON(t, test.want))+`}}}`,
			string(answer.Content), test.name)
		assert.Empty(t, answer.FollowUp, test.name)
	}
}

// TestCodexAnswerControlRequest_SkipsAnAsyncQuestion covers a skip: a bare
// deny, even when the decision states a reason, because nothing reads a
// reason for a skip.
func TestCodexAnswerControlRequest_SkipsAnAsyncQuestion(t *testing.T) {
	t.Parallel()

	const requestID = "codex-async-question-call_abc-0"
	for _, payload := range [][]byte{codexColorQuestion(t), codexAsyncPayload(t, map[string]any{})} {
		answer := requireCodexAnswer(t, payload, requestID, agenttest.CancelQuestions("A typed reason the skip sends on to nobody"))
		assert.JSONEq(t,
			`{"type":"control_response","response":{"subtype":"success","request_id":"`+requestID+`","response":{"behavior":"deny","message":"Rejected by user."}}}`,
			string(answer.Content))
		assert.Empty(t, answer.FollowUp)
	}
}

// TestCodexAnswerControlRequest_RefusesAnEmptyAsyncAnswer covers an answer
// with no selection and no text, and an answer to a question with no title,
// which a client can only skip.
func TestCodexAnswerControlRequest_RefusesAnEmptyAsyncAnswer(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		payload  []byte
		decision *leapmuxv1.ControlDecision
	}{
		"no answer":                   {payload: codexColorQuestion(t), decision: agenttest.QuestionDecision()},
		"a note of whitespace":        {payload: codexColorQuestion(t), decision: agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{CustomText: " \n "})},
		"two selections":              {payload: codexColorQuestion(t), decision: agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Blue", "Red"}})},
		"an answer to no question":    {payload: codexAsyncPayload(t, map[string]any{}), decision: agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{CustomText: "Red"})},
		"an option the question lack": {payload: codexColorQuestion(t), decision: agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Green"}})},
	} {
		_, err := agenttest.AnswerControl(codexProvider{}, test.payload, "req-1", test.decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

// TestCodexAsyncQuestionRoundTrip publishes an async question as Codex's
// item/completed states it, reads the published request, answers it, and
// resolves the answer as the Worker does: the answer becomes the question
// answer that QuestionReply sends on to Codex.
func TestCodexAsyncQuestionRoundTrip(t *testing.T) {
	t.Parallel()

	a, sink, _ := newCodexControlAgent(t)
	handleCodexOutput(a, providerkit.ParseLine([]byte(asyncQuestionCompletedLine("main-thread", "call_abc",
		`[{"title":"Which color?","options":["Blue","Red"]}]`))))
	records := sink.PublishedControls()
	require.Len(t, records, 1)
	record := records[0]
	agenttest.AssertPublishedControlKeys(t, codexProvider{}, sink)

	question := agenttest.ControlPromptOf(codexProvider{}, record.Payload).GetQuestion()
	require.Len(t, question.GetQuestions(), 1)
	assert.Equal(t, "Which color?", question.GetQuestions()[0].GetQuestion())

	answer := requireCodexAnswer(t, record.Payload, record.RequestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{
		Selected: []string{"Red"}, CustomText: "A light shade",
	}))
	res := agenttest.ResolveAnswer(codexProvider{}, record.Payload, record.RequestID, answer)
	require.NoError(t, res.Refusal())
	assert.True(t, res.LeapMuxOwned)
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER, res.FollowUp.Kind)
	assert.Equal(t, "Red\n\nA light shade", res.FollowUp.Text)

	reply, err := codexProvider{}.QuestionReply(record.Payload, res.FollowUp.Text)
	require.NoError(t, err)
	replyAnswer, replyQuestion, questionItemID := codexQuestionReplyEntry(t, reply)
	assert.Equal(t, "Red\n\nA light shade", replyAnswer)
	assert.Equal(t, "Which color?", replyQuestion)
	assert.Equal(t, `["request_user_input_async","call_abc",0]`, questionItemID)
}

// --- Answering an MCP elicitation ---

// codexToolApprovalPayload is an MCP tool approval that offers both scopes.
func codexToolApprovalPayload(t *testing.T) []byte {
	t.Helper()
	return codexRequestPayload(t, contracts.MCPElicitationMethodCodex, map[string]any{
		"mode": "form", "message": "Allow this tool?",
		"requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		"_meta": map[string]any{
			"codex_approval_kind": contracts.MCPElicitationApprovalKindToolCall,
			"tool_params":         map[string]any{"path": "sample.py", "count": 0},
			"persist":             []any{"session", "always"},
		},
	})
}

// TestCodexAnswerControlRequest_AnswersAnMCPToolApproval covers each accept
// choice: the scope that it remembers rides the answer's `_meta`, and the
// choice that remembers nothing sends none.
func TestCodexAnswerControlRequest_AnswersAnMCPToolApproval(t *testing.T) {
	t.Parallel()

	payload := codexToolApprovalPayload(t)
	for choice, want := range map[string]string{
		"":        `{"action":"accept","content":{}}`,
		"once":    `{"action":"accept","content":{}}`,
		"session": `{"action":"accept","content":{},"_meta":{"persist":"session"}}`,
		"always":  `{"action":"accept","content":{},"_meta":{"persist":"always"}}`,
	} {
		answer := requireCodexAnswer(t, payload, "mcp-approval", agenttest.ElicitationChoiceDecision(`{}`, choice))
		assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"mcp-approval","response":`+want+`}}`, string(answer.Content), "choice %q", choice)
	}
}

// TestCodexAnswerControlRequest_DeclinesAndCancelsAnMCPElicitation covers
// the two refusals of a form: each sends its action alone.
func TestCodexAnswerControlRequest_DeclinesAndCancelsAnMCPElicitation(t *testing.T) {
	t.Parallel()

	payload := codexRequestPayload(t, contracts.MCPElicitationMethodCodex, map[string]any{
		"mode": "form", "message": "Choose the settings.", "requestedSchema": map[string]any{"type": "object"},
	})
	for action, word := range map[leapmuxv1.ElicitationAction]string{
		leapmuxv1.ElicitationAction_ELICITATION_ACTION_DECLINE: "decline",
		leapmuxv1.ElicitationAction_ELICITATION_ACTION_CANCEL:  "cancel",
	} {
		answer := requireCodexAnswer(t, payload, "001", agenttest.ElicitationDecision(action, ""))
		assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"001","response":{"action":"`+word+`"}}}`, string(answer.Content))
	}
	answer := requireCodexAnswer(t, payload, "001", agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, `{"count":0,"enabled":false,"color":"b"}`))
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"001","response":{"action":"accept","content":{"count":0,"enabled":false,"color":"b"}}}}`, string(answer.Content))
}

// TestCodexAnswerControlRequest_RefusesAnElicitationAnswerThatItCannotSend
// covers a scope that the request does not offer, and a choice on a form
// that offers none.
func TestCodexAnswerControlRequest_RefusesAnElicitationAnswerThatItCannotSend(t *testing.T) {
	t.Parallel()

	narrow := codexRequestPayload(t, contracts.MCPElicitationMethodCodex, map[string]any{
		"_meta": map[string]any{"codex_approval_kind": contracts.MCPElicitationApprovalKindToolCall, "persist": []any{"session"}},
	})
	form := codexRequestPayload(t, contracts.MCPElicitationMethodCodex, map[string]any{"mode": "form"})
	for name, test := range map[string]struct {
		payload []byte
		key     string
	}{
		"a scope the request does not offer": {payload: narrow, key: "always"},
		"a choice on a plain form":           {payload: form, key: "session"},
		"once on a plain form":               {payload: form, key: "once"},
	} {
		_, err := agenttest.AnswerControl(codexProvider{}, test.payload, "001", agenttest.ElicitationChoiceDecision("", test.key))
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
	_, err := agenttest.AnswerControl(codexProvider{}, form, "001", agenttest.PermissionDecision("accept", ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a form takes no permission decision")
}

// TestCodexMCPElicitationMetaAdmitsExactlyTheOfferedScopes covers the
// Worker's resolution of an answer: it admits the scope of each accept choice
// that the prompt offers, and no other, so a choice that the prompt offers is
// never refused, and the reverse.
func TestCodexMCPElicitationMetaAdmitsExactlyTheOfferedScopes(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		meta    map[string]any
		session bool
		always  bool
	}{
		"both scopes":            {meta: map[string]any{"codex_approval_kind": "mcp_tool_call", "persist": []any{"session", "always"}}, session: true, always: true},
		"a mixed list":           {meta: map[string]any{"codex_approval_kind": "tool_suggestion", "persist": []any{7, "session"}}, session: true},
		"a persist of no list":   {meta: map[string]any{"codex_approval_kind": "mcp_tool_call", "persist": "session"}},
		"a kind of no approval":  {meta: map[string]any{"codex_approval_kind": "other", "persist": []any{"session"}}},
		"a kind of another type": {meta: map[string]any{"codex_approval_kind": 7, "persist": []any{"session"}}},
	} {
		payload := codexRequestPayload(t, contracts.MCPElicitationMethodCodex, map[string]any{"_meta": test.meta})
		offered := map[string]bool{}
		for _, choice := range agenttest.ControlPromptOf(codexProvider{}, payload).GetElicitation().GetAcceptChoices() {
			offered[choice.GetKey()] = true
		}
		assert.Equal(t, test.session, offered["session"], name)
		assert.Equal(t, test.always, offered["always"], name)
		assert.Equal(t, test.session, codexMCPElicitationMeta(payload, "session"), name)
		assert.Equal(t, test.always, codexMCPElicitationMeta(payload, "always"), name)
		assert.False(t, codexMCPElicitationMeta(payload, "once"), "%s: once remembers nothing", name)
		assert.False(t, codexMCPElicitationMeta(payload, ""), name)
	}
}

// TestCodexMCPToolApprovalRoundTrip answers an MCP tool approval with a scope
// and resolves the answer as the Worker does: Codex receives its own request
// id and the scope.
func TestCodexMCPToolApprovalRoundTrip(t *testing.T) {
	t.Parallel()

	payload := codexToolApprovalPayload(t)
	answer := requireCodexAnswer(t, payload, "jsonrpc:7", agenttest.ElicitationChoiceDecision(`{}`, "session"))
	res := agenttest.ResolveAnswer(codexProvider{}, payload, "jsonrpc:7", answer)
	require.NoError(t, res.Refusal())
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{"action":"accept","content":{},"_meta":{"persist":"session"}}}`, string(res.Content))
}

// --- The user's Allow, replayed ---

// TestCodexApprovalAnswerConformanceAllow replays the Allow half of
// testdata/codex_approval_answer_conformance.json: for each request that
// states allowResult, the prompt's Allow builds the answer, and the Worker's
// resolution of that answer sends Codex exactly allowResult under Codex's own
// request id. Allow is the prompt's first allow-once option, else its first
// allow option, as a client's decision row reads it. A case that also states a
// standingResult states the same result: the standing approval sends what the
// user's plain Allow sends.
func TestCodexApprovalAnswerConformanceAllow(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(testutil.RepoPath(t, "testdata", "codex_approval_answer_conformance.json"))
	require.NoError(t, err)
	var fixture struct {
		Cases []struct {
			Name           string          `json:"name"`
			Request        json.RawMessage `json:"request"`
			AllowResult    json.RawMessage `json:"allowResult"`
			StandingResult json.RawMessage `json:"standingResult"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	replayed := 0
	for _, tc := range fixture.Cases {
		if len(tc.AllowResult) == 0 {
			continue
		}
		replayed++
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			_, nativeID, ok := agent.ExtractJSONRPCID(tc.Request)
			require.True(t, ok)
			requestID := "jsonrpc:" + nativeID
			prompt := agenttest.ControlPromptOf(codexProvider{}, tc.Request)
			var decision *leapmuxv1.ControlDecision
			switch {
			case prompt.GetElicitation() != nil:
				decision = agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, `{}`)
			case prompt.GetPermission() != nil:
				decision = agenttest.PermissionDecision(codexAllowOption(t, prompt.GetPermission()).GetOptionId(), "")
			default:
				require.Failf(t, "a client's Allow answers no such prompt", "%s", protojson.Format(prompt))
			}
			answer := requireCodexAnswer(t, tc.Request, requestID, decision)
			res := agenttest.ResolveAnswer(codexProvider{}, tc.Request, requestID, answer)
			require.NoError(t, res.Refusal())
			var sent struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
			}
			require.NoError(t, json.Unmarshal(res.Content, &sent))
			assert.JSONEq(t, nativeID, string(sent.ID), "Codex receives its own request id")
			assert.JSONEq(t, string(tc.AllowResult), string(sent.Result))
			if len(tc.StandingResult) > 0 && string(tc.StandingResult) != "null" {
				assert.JSONEq(t, string(tc.AllowResult), string(tc.StandingResult), "the standing approval sends what the user's Allow sends")
			}
		})
	}
	require.Positive(t, replayed, "the corpus holds cases for the user's Allow")
}

// codexAllowOption is the option that a client's Allow sends: the first
// allow-once option, else the first allow option.
func codexAllowOption(t *testing.T, permission *leapmuxv1.PermissionPrompt) *leapmuxv1.PermissionOption {
	t.Helper()
	var allowAlways *leapmuxv1.PermissionOption
	for _, option := range permission.GetOptions() {
		switch option.GetKind() {
		case kindAllowOnce:
			return option
		case kindAllowAlways:
			if allowAlways == nil {
				allowAlways = option
			}
		default:
		}
	}
	require.NotNil(t, allowAlways, "the prompt offers an allow option")
	return allowAlways
}

func TestCodexUserInputAnswerIgnoresNonObjectOptionsForIsOtherAndUsesJSSpace(t *testing.T) {
	t.Parallel()
	// Options array containing only non-object entries yields zero options in QuestionsFromWire,
	// so isOther must not prepend "None of the above".
	payload := codexRequestPayload(t, contracts.CodexMethodToolRequestUserInput, map[string]any{
		"questions": []any{
			map[string]any{
				"id":       "q1",
				"question": "Details?",
				"isOther":  true,
				"options":  []any{nil},
			},
			map[string]any{
				"id":       "q2",
				"question": "Choice?",
				"options":  []any{map[string]any{"label": "Yes"}},
			},
		},
	})
	answer := requireCodexAnswer(t, payload, "req-1", agenttest.QuestionDecision(
		agenttest.QuestionAnswer("\u0085"),
		agenttest.QuestionAnswer("\uFEFF", "Yes"),
	))
	var envelope struct {
		Result struct {
			Answers map[string]struct {
				Answers []string `json:"answers"`
			} `json:"answers"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(answer.Content, &envelope))
	assert.Equal(t, []string{"user_note: \u0085"}, envelope.Result.Answers["q1"].Answers,
		"non-object options must not trigger None of the above, and U+0085 is not JS whitespace")
	assert.Equal(t, []string{"Yes"}, envelope.Result.Answers["q2"].Answers,
		"U+FEFF is JS whitespace and must not emit a user_note")
}

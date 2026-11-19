package mimo

import (
	"encoding/json"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// mimoPermissionRequest is a permission request as controlPayload stores it,
// with properties beside its id and session.
func mimoPermissionRequest(t *testing.T, properties map[string]any) string {
	t.Helper()
	merged := map[string]any{"id": "per_1", "sessionID": "ses_1"}
	maps.Copy(merged, properties)
	permission, _ := properties["permission"].(string)
	payload, err := json.Marshal(map[string]any{
		"type":       "permission.asked",
		"properties": merged,
		"request":    map[string]any{"tool_name": permission, "tool_use_id": "call-1"},
	})
	require.NoError(t, err)
	return string(payload)
}

// mimoPlanApproval is a plan approval as controlPayload stores it, with entry
// as the fields of its one plan_exit question.
func mimoPlanApproval(t *testing.T, entry map[string]any) map[string]any {
	t.Helper()
	question := map[string]any{"key": "plan_exit"}
	maps.Copy(question, entry)
	return map[string]any{
		"type":       "question.asked",
		"properties": map[string]any{"id": "que_1", "questions": []any{question}},
		"request":    map[string]any{"tool_name": "plan_exit"},
	}
}

// mimoElicitationQuestion is MiMo's own question for an MCP server's
// confirmation, as MiMo 0.1.17 asks it, with entry over its fields.
func mimoElicitationQuestion(t *testing.T, entry map[string]any) string {
	t.Helper()
	question := map[string]any{
		"key": "mcp_elicitation", "header": "docs",
		"question": "docs\n\nProceed with the lookup?\n\nThe server reads the public index.",
		"options":  []any{map[string]any{"label": "Accept"}, map[string]any{"label": "Decline"}, map[string]any{"label": "Cancel"}},
		"multiple": false, "custom": false,
	}
	for key, value := range entry {
		if value == nil {
			delete(question, key)
			continue
		}
		question[key] = value
	}
	return mustMiMoJSON(t, map[string]any{
		"type":       "question.asked",
		"properties": map[string]any{"id": "que_1", "sessionID": "ses_1", "questions": []any{question}},
		"request":    map[string]any{"tool_name": "question"},
	})
}

func mustMiMoJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return string(encoded)
}

func mimoOptionIDs(prompt *leapmuxv1.ControlPrompt) []string {
	var ids []string
	for _, option := range prompt.GetPermission().GetOptions() {
		ids = append(ids, option.GetOptionId())
	}
	return ids
}

// The three answers, in the order of MiMo's own prompt, and the two that a
// request without a kept `always` offers.
var (
	mimoAllOptions = []*leapmuxv1.PermissionOption{
		{OptionId: "once", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Allow once"},
		{OptionId: "always", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, Name: "Always allow"},
		{OptionId: "reject", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Reject"},
	}
	mimoOnceAndReject = []*leapmuxv1.PermissionOption{mimoAllOptions[0], mimoAllOptions[2]}
)

func mimoPlanPromptOf(plan *leapmuxv1.PlanPrompt) *leapmuxv1.ControlPrompt {
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: plan}}
}

func TestReadControlRequest_AShellPermissionStatesItsCommandAndWhatAlwaysApproves(t *testing.T) {
	got := agenttest.ControlPromptOf(mimoProvider{}, []byte(mimoPermissionRequest(t, map[string]any{
		"permission": "bash", "patterns": []any{"rm -rf build"}, "always": []any{"rm *"}, "metadata": map[string]any{},
	})))
	agenttest.RequireProto(t, agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{
		Title: "bash", Command: "rm -rf build", InputJson: `{"always":["rm *"]}`, Options: mimoAllOptions,
	}), got)
}

func TestReadControlRequest_ReadsTheCommandOfADeleteFromTheMetadata(t *testing.T) {
	permission := agenttest.ControlPromptOf(mimoProvider{}, []byte(mimoPermissionRequest(t, map[string]any{
		"permission": "bash_delete", "patterns": []any{"rm"},
		"metadata": map[string]any{"command": "rm -rf /tmp/x", "deletes": []any{"/tmp/x"}},
	}))).GetPermission()
	assert.Equal(t, "rm -rf /tmp/x", permission.GetCommand())
	assert.JSONEq(t, `{"command":"rm -rf /tmp/x","deletes":["/tmp/x"]}`, permission.GetInputJson())
}

// MiMo reads `always` as `once` for a delete, which must ask each time, so an
// offer would save an answer that MiMo never keeps.
func TestReadControlRequest_OffersNoAlwaysForADelete(t *testing.T) {
	permission := agenttest.ControlPromptOf(mimoProvider{}, []byte(mimoPermissionRequest(t, map[string]any{
		"permission": "bash_delete", "patterns": []any{"rm -f x"}, "always": []any{"rm *"},
		"metadata": map[string]any{"command": "rm -f x"},
	}))).GetPermission()
	assert.Equal(t, []string{"once", "reject"}, mimoOptionIDs(agenttest.PermissionPrompt(permission)))
	assert.JSONEq(t, `{"command":"rm -f x"}`, permission.GetInputJson(), "a request that offers no always states no always list")
}

// MiMo saves the request's `always` patterns, so a request with no pattern
// saves nothing.
func TestReadControlRequest_OffersNoAlwaysWithoutAPattern(t *testing.T) {
	for name, fields := range map[string]map[string]any{
		"an empty always list":                 {"always": []any{}},
		"no always list":                       {},
		"an always list that holds no pattern": {"always": []any{""}},
		"an always list that holds no string":  {"always": []any{7, nil}},
		"an always field that is no list":      {"always": "/etc/*"},
	} {
		properties := map[string]any{"permission": "external_directory", "patterns": []any{"/etc/*"}, "metadata": map[string]any{}}
		maps.Copy(properties, fields)
		permission := agenttest.ControlPromptOf(mimoProvider{}, []byte(mimoPermissionRequest(t, properties))).GetPermission()
		assert.Equal(t, []string{"once", "reject"}, mimoOptionIDs(agenttest.PermissionPrompt(permission)), name)
		assert.Equal(t, `{"patterns":["/etc/*"]}`, permission.GetInputJson(), name)
	}
}

func TestReadControlRequest_StatesThePatternsOfAPermissionThatIsNoCommand(t *testing.T) {
	got := agenttest.ControlPromptOf(mimoProvider{}, []byte(mimoPermissionRequest(t, map[string]any{
		"permission": "external_directory", "patterns": []any{"/etc/*"}, "always": []any{"/etc/*"},
		"metadata": map[string]any{"filepath": "/etc/hosts"},
	})))
	agenttest.RequireProto(t, agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{
		Title:     "external_directory",
		InputJson: `{"patterns":["/etc/*"],"always":["/etc/*"],"filepath":"/etc/hosts"}`,
		Options:   mimoAllOptions,
	}), got)
}

// The arguments read in the order that the browser's reader stated them: the
// patterns, the always list, then the metadata in its own order. A metadata
// field of the same name takes the earlier place, as an object spread does.
func TestReadControlRequest_StatesTheArgumentsInTheirOrder(t *testing.T) {
	got := agenttest.ControlPromptOf(mimoProvider{}, []byte(`{"type":"permission.asked","properties":{"permission":"webfetch","patterns":["https://a.example/*"],"always":["https://a.example/*"],
		"metadata":{"zeta":1,"patterns":"override","alpha":{"nested":true}}}}`))
	assert.Equal(t, `{"patterns":"override","always":["https://a.example/*"],"zeta":1,"alpha":{"nested":true}}`, got.GetPermission().GetInputJson())

	got = agenttest.ControlPromptOf(mimoProvider{}, []byte(mimoPermissionRequest(t, map[string]any{"permission": "external_directory", "patterns": []any{"a && b <c>"}})))
	assert.Equal(t, `{"patterns":["a && b <c>"]}`, got.GetPermission().GetInputJson(), "a pattern reads as the request states it")
}

// A shell permission states one pattern for each command of a compound line.
// With no command in the metadata, the patterns ARE the command, one to a line.
func TestReadControlRequest_JoinsThePatternsOfAShellPermissionIntoItsCommand(t *testing.T) {
	got := agenttest.ControlPromptOf(mimoProvider{}, []byte(mimoPermissionRequest(t, map[string]any{
		"permission": "bash", "patterns": []any{"cd build", "rm -rf out"}, "always": []any{"cd *", "rm *"},
	})))
	agenttest.RequireProto(t, agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{
		Title: "bash", Command: "cd build\nrm -rf out", InputJson: `{"always":["cd *","rm *"]}`, Options: mimoAllOptions,
	}), got)

	got = agenttest.ControlPromptOf(mimoProvider{}, []byte(mimoPermissionRequest(t, map[string]any{
		"permission": "bash", "patterns": []any{"rm -rf out"}, "metadata": map[string]any{"command": ""},
	})))
	assert.Equal(t, "rm -rf out", got.GetPermission().GetCommand(), "an empty command in the metadata states none")
}

// The metadata states the command MiMo will run, which can differ from the
// patterns MiMo keeps for `always`.
func TestReadControlRequest_PrefersTheCommandOfTheMetadata(t *testing.T) {
	got := agenttest.ControlPromptOf(mimoProvider{}, []byte(mimoPermissionRequest(t, map[string]any{
		"permission": "bash", "patterns": []any{"rm"}, "always": []any{"rm *"}, "metadata": map[string]any{"command": "rm -rf build"},
	})))
	assert.Equal(t, "rm -rf build", got.GetPermission().GetCommand())
}

// Only a shell permission states a command. Another permission keeps its
// patterns as arguments, whatever its metadata holds.
func TestReadControlRequest_StatesNoCommandForAPermissionThatIsNoShellCommand(t *testing.T) {
	got := agenttest.ControlPromptOf(mimoProvider{}, []byte(mimoPermissionRequest(t, map[string]any{
		"permission": "webfetch", "patterns": []any{"https://example.com/*"}, "always": []any{"https://example.com/*"},
		"metadata": map[string]any{"command": "fetch"},
	})))
	agenttest.RequireProto(t, agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{
		Title:     "webfetch",
		InputJson: `{"patterns":["https://example.com/*"],"always":["https://example.com/*"],"command":"fetch"}`,
		Options:   mimoAllOptions,
	}), got)
}

func TestReadControlRequest_KeepsOnlyTheTextPatterns(t *testing.T) {
	got := agenttest.ControlPromptOf(mimoProvider{}, []byte(mimoPermissionRequest(t, map[string]any{
		"permission": "external_directory", "patterns": []any{"/etc/*", 7, nil}, "always": []any{3, "/etc/*"},
	})))
	assert.Equal(t, `{"patterns":["/etc/*"],"always":["/etc/*"]}`, got.GetPermission().GetInputJson())
}

// A metadata of the wrong type states no field.
func TestReadControlRequest_ReadsAMetadataThatIsNoObjectAsNone(t *testing.T) {
	for _, metadata := range []any{[]any{"command"}, "rm -rf /", 7, nil} {
		got := agenttest.ControlPromptOf(mimoProvider{}, []byte(mimoPermissionRequest(t, map[string]any{"permission": "bash", "patterns": []any{"ls"}, "metadata": metadata})))
		assert.Equal(t, "ls", got.GetPermission().GetCommand(), "%v", metadata)
		assert.Equal(t, `{}`, got.GetPermission().GetInputJson(), "%v", metadata)
	}
}

// A request that lost its properties still reaches the banner, with no
// answer that MiMo would not keep.
func TestReadControlRequest_ReadsAPermissionThatStatesNoProperties(t *testing.T) {
	agenttest.RequireProto(t, agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{InputJson: `{}`, Options: mimoOnceAndReject}),
		agenttest.ControlPromptOf(mimoProvider{}, []byte(`{"type":"permission.asked"}`)))
	agenttest.RequireProto(t, agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{InputJson: `{}`, Options: mimoOnceAndReject}),
		agenttest.ControlPromptOf(mimoProvider{}, []byte(`{"type":"permission.asked","properties":["permission"]}`)))
}

// Every provider reads its own shell permission with its command and the
// operation.
func TestReadControlRequest_ReadsAShellPermissionAsEveryProviderDoes(t *testing.T) {
	const command = "printf \"first line\\n\"\nprintf \"second line\\n\""
	got := agenttest.ControlPromptOf(mimoProvider{}, []byte(mimoPermissionRequest(t, map[string]any{
		"permission": "bash", "patterns": []any{command},
		"metadata": map[string]any{"timeout": 0, "quiet": false, "note": ""},
	})))
	permission := got.GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, "bash", permission.GetTitle())
	assert.Equal(t, command, permission.GetCommand())
	assert.JSONEq(t, `{"timeout":0,"quiet":false,"note":""}`, permission.GetInputJson())

	read := agenttest.ControlPromptOf(mimoProvider{}, []byte(mimoPermissionRequest(t, map[string]any{"permission": "read", "patterns": []any{"/a.ts"}, "metadata": map[string]any{}})))
	assert.NotEmpty(t, read.GetPermission().GetOptions(), "MiMo states its own answers")
}

func TestReadControlRequest_ReadsAPlanApprovalWithThePlanThatTheWorkerRead(t *testing.T) {
	payload := mimoPlanApproval(t, nil)
	payload["plan"] = "# Plan"
	agenttest.RequireProto(t, mimoPlanPromptOf(&leapmuxv1.PlanPrompt{Text: "# Plan"}), agenttest.ControlPromptOf(mimoProvider{}, []byte(mustMiMoJSON(t, payload))))
	payload["plan"] = ""
	agenttest.RequireProto(t, mimoPlanPromptOf(&leapmuxv1.PlanPrompt{}), agenttest.ControlPromptOf(mimoProvider{}, []byte(mustMiMoJSON(t, payload))))
}

// The Worker leaves the plan text out when it cannot read the file, and the
// prompt then states where the plan is, or MiMo's own question.
func TestReadControlRequest_StatesWhereThePlanIsWithoutItsText(t *testing.T) {
	const question = "Plan at .mimocode/plans/1-eager-canyon.md is complete. Would you like to switch to the build agent and start implementing?"
	for name, tc := range map[string]struct {
		entry   map[string]any
		plan    any
		details []string
		text    string
	}{
		"the plan file": {
			entry:   map[string]any{"params": map[string]any{"plan": ".mimocode/plans/1-eager-canyon.md"}, "question": question},
			details: []string{"Plan file: .mimocode/plans/1-eager-canyon.md"},
		},
		"the question when the approval gives no file": {
			entry: map[string]any{"params": map[string]any{}, "question": question}, details: []string{question},
		},
		"the question when the plan file is blank": {
			entry: map[string]any{"params": map[string]any{"plan": "  "}, "question": question}, details: []string{question},
		},
		"the plan text alone when the worker read it": {
			entry: map[string]any{"params": map[string]any{"plan": "p.md"}, "question": question}, plan: "# Plan", text: "# Plan",
		},
		"the trimmed plan file": {
			entry: map[string]any{"params": map[string]any{"plan": "  p.md\n"}}, details: []string{"Plan file: p.md"},
		},
		"the trimmed question": {
			entry: map[string]any{"question": "  " + question + "\n"}, details: []string{question},
		},
		"no details for neither": {
			entry: map[string]any{"params": map[string]any{"plan": " "}, "question": "  "},
		},
		"the plan file when the plan field is no text": {
			entry: map[string]any{"params": map[string]any{"plan": "p.md"}}, plan: map[string]any{"text": "# Plan"},
			details: []string{"Plan file: p.md"},
		},
		"no details for params of the wrong type": {
			entry: map[string]any{"params": "p.md", "question": 7},
		},
	} {
		t.Run(name, func(t *testing.T) {
			payload := mimoPlanApproval(t, tc.entry)
			if tc.plan != nil {
				payload["plan"] = tc.plan
			}
			agenttest.RequireProto(t, mimoPlanPromptOf(&leapmuxv1.PlanPrompt{Text: tc.text, Details: tc.details}), agenttest.ControlPromptOf(mimoProvider{}, []byte(mustMiMoJSON(t, payload))))
		})
	}

	for _, payload := range []string{
		`{"type":"question.asked","properties":{"questions":["plan_exit"]},"request":{"tool_name":"plan_exit"}}`,
		`{"type":"question.asked","request":{"tool_name":"plan_exit"}}`,
	} {
		agenttest.RequireProto(t, mimoPlanPromptOf(&leapmuxv1.PlanPrompt{}), agenttest.ControlPromptOf(mimoProvider{}, []byte(payload)))
	}
}

func TestReadControlRequest_RecognizesAQuestionAndNotAPlanApproval(t *testing.T) {
	question := map[string]any{
		"type": "question.asked",
		"properties": map[string]any{"id": "que_1", "questions": []any{
			map[string]any{"question": "Which database?", "header": "Database", "multiple": true,
				"options": []any{map[string]any{"label": "SQLite"}, map[string]any{"label": "Postgres"}}},
			map[string]any{"question": "Anything else?", "options": []any{}},
		}},
		"request": map[string]any{"tool_name": "question", "tool_use_id": "call-1"},
	}
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{
			{Question: "Which database?", Header: "Database", MultiSelect: true,
				Options: []*leapmuxv1.QuestionOption{{Label: "SQLite"}, {Label: "Postgres"}}},
			{Question: "Anything else?"},
		},
	}}}, agenttest.ControlPromptOf(mimoProvider{}, []byte(mustMiMoJSON(t, question))))

	// The plan approval is the tool name that the Worker recorded. A question
	// whose request states no tool name is still a question.
	withoutToolName := maps.Clone(question)
	withoutToolName["request"] = map[string]any{}
	assert.NotNil(t, agenttest.ControlPromptOf(mimoProvider{}, []byte(mustMiMoJSON(t, withoutToolName))).GetQuestion(), "a request header with no tool name")
	withoutRequest := maps.Clone(question)
	delete(withoutRequest, "request")
	assert.NotNil(t, agenttest.ControlPromptOf(mimoProvider{}, []byte(mustMiMoJSON(t, withoutRequest))).GetQuestion(), "no request header")
	plan := maps.Clone(question)
	plan["request"] = map[string]any{"tool_name": "plan_exit"}
	assert.NotNil(t, agenttest.ControlPromptOf(mimoProvider{}, []byte(mustMiMoJSON(t, plan))).GetPlan(), "a question.asked recorded under the plan tool is the plan approval")

	assert.Nil(t, agenttest.ControlPromptOf(mimoProvider{}, []byte(`{"type":"permission.asked"}`)).GetQuestion())
	// MiMo raises no other request, and the resolution refuses an answer to
	// one, so it reads as unrecognized.
	for _, payload := range []string{`{"type":"question.replied","properties":{"requestID":"que_1"}}`, `{}`, `not json`} {
		agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(mimoProvider{}, []byte(payload)), "%s reads as unrecognized", payload)
	}
}

// Only the one question that MiMo asks for an elicitation is one. A request
// that holds the key beside another question is an ordinary question.
func TestReadControlRequest_ATwoQuestionRequestWithTheKeyIsAQuestion(t *testing.T) {
	got := agenttest.ControlPromptOf(mimoProvider{}, []byte(`{"type":"question.asked","properties":{"id":"que_3","questions":[{"key":"mcp_elicitation","question":"A?"},{"question":"B?"}]},"request":{"tool_name":"question"}}`))
	require.NotNil(t, got.GetQuestion())
	assert.Len(t, got.GetQuestion().GetQuestions(), 2)
}

// MiMo asks an MCP server's confirmation as a question that takes no typed
// text, and reads every answer but Accept and Decline as Cancel. The form
// answers it instead of the question card.
func TestReadControlRequest_AnMCPElicitationIsAnEmptyForm(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: &leapmuxv1.ElicitationPrompt{
		Mode: "form", Server: "docs", Message: "Proceed with the lookup?\n\nThe server reads the public index.", SchemaJson: `{"type":"object","properties":{}}`,
	}}}, agenttest.ControlPromptOf(mimoProvider{}, []byte(mimoElicitationQuestion(t, nil))))

	got := agenttest.ControlPromptOf(mimoProvider{}, []byte(mimoElicitationQuestion(t, map[string]any{"question": "docs\n\nProceed?"})))
	assert.Nil(t, got.GetQuestion(), "the question card leaves an elicitation to the form")
	assert.Equal(t, "Proceed?", got.GetElicitation().GetMessage())
}

func TestMiMoElicitation_StatesTheMessageWithoutTheServerInFrontOfIt(t *testing.T) {
	elicitation := func(entry map[string]any) *leapmuxv1.ElicitationPrompt {
		return mimoElicitation(readStoredControlPayload([]byte(mimoElicitationQuestion(t, entry))))
	}
	for name, tc := range map[string]struct {
		entry           map[string]any
		server, message string
	}{
		// MiMo joins the parts it has, so a question that does not open with the
		// server is stated whole.
		"a question that does not open with the server": {map[string]any{"question": "Proceed?"}, "docs", "Proceed?"},
		"no server":                              {map[string]any{"header": "", "question": "Proceed?"}, "", "Proceed?"},
		"a question that holds the server alone": {map[string]any{"question": "docs"}, "docs", "docs"},
		// The server stands in front of the message only when a blank line
		// follows it.
		"a message that opens with the server name": {map[string]any{"question": "docs lookup: proceed?"}, "docs", "docs lookup: proceed?"},
		"only the server and its blank line":        {map[string]any{"question": "docs\n\n"}, "docs", ""},
		"no question text":                          {map[string]any{"question": nil}, "docs", ""},
		"a question text of the wrong type":         {map[string]any{"question": 7}, "docs", ""},
	} {
		got := elicitation(tc.entry)
		require.NotNil(t, got, name)
		assert.Equal(t, tc.server, got.GetServer(), name)
		assert.Equal(t, tc.message, got.GetMessage(), name)
		assert.Equal(t, contracts.MCPElicitationModeForm, got.GetMode(), name)
		assert.Equal(t, mimoElicitationSchema, got.GetSchemaJson(), name)
	}
}

func TestMiMoElicitation_ReadsNoOtherRequest(t *testing.T) {
	for name, payload := range map[string]string{
		"a question with another key":       mimoElicitationQuestion(t, map[string]any{"key": "plan_exit"}),
		"a question with no key":            mimoElicitationQuestion(t, map[string]any{"key": nil}),
		"a permission":                      `{"type":"permission.asked","properties":{"questions":[{"key":"mcp_elicitation"}]}}`,
		"two questions":                     `{"type":"question.asked","properties":{"questions":[{"key":"mcp_elicitation"},{"key":"mcp_elicitation"}]}}`,
		"a questions field that is no list": `{"type":"question.asked","properties":{"questions":{"key":"mcp_elicitation"}}}`,
		"a question that is no object":      `{"type":"question.asked","properties":{"questions":["mcp_elicitation"]}}`,
		"no properties":                     `{"type":"question.asked"}`,
	} {
		assert.Nil(t, mimoElicitation(readStoredControlPayload([]byte(payload))), name)
	}
}

// --- answers ---

// mimoNativeAnswer reads answer as the Worker does before it sends it to
// MiMo, and fails the test when ResolveControlResponse would withhold it.
func mimoNativeAnswer(t *testing.T, payload, requestID string, answer agent.ControlAnswer) mimoAnswer {
	t.Helper()
	resolved := agenttest.ResolveAnswer(mimoProvider{}, []byte(payload), requestID, answer)
	require.False(t, resolved.Withhold, "%v", resolved.Refusal())
	kind, ok := controlKindOfPayload([]byte(payload))
	require.True(t, ok)
	native, err := readControlAnswer(kind, []byte(payload), answer.Content)
	require.NoError(t, err)
	return native
}

func TestAnswerControlRequest_SendsTheChosenOptionAsMiMosReplyWord(t *testing.T) {
	payload := mimoPermissionRequest(t, map[string]any{"permission": "bash", "patterns": []any{"pwd"}, "always": []any{"pwd *"}, "metadata": map[string]any{}})
	for _, word := range []string{"once", "always", "reject"} {
		got, err := agenttest.AnswerControl(mimoProvider{}, []byte(payload), "mimo-permission:per_1", agenttest.PermissionDecision(word, ""))
		require.NoError(t, err, word)
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":"mimo-permission:per_1","result":{"outcome":{"outcome":"selected","optionId":"`+word+`"}}}`, string(got.Content), word)
		assert.Empty(t, got.FollowUp, word)
		assert.Equal(t, mimoPermissionReplyBody{Reply: word}, mimoNativeAnswer(t, payload, "mimo-permission:per_1", got).permission, word)
	}
}

// The composer sends a refusal with the typed words as the reason, which
// MiMo takes with its rejection.
func TestAnswerControlRequest_ARefusalWithFeedbackDeniesWithIt(t *testing.T) {
	payload := mimoPermissionRequest(t, map[string]any{"permission": "bash", "patterns": []any{"pwd"}})
	got, err := agenttest.AnswerControl(mimoProvider{}, []byte(payload), "mimo-permission:per_1", agenttest.PermissionDecision("reject", "not yet"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"mimo-permission:per_1","response":{"behavior":"deny","message":"not yet"}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp, "the answer carries the reason itself")
	assert.Equal(t, mimoPermissionReplyBody{Reply: "reject", Message: "not yet"}, mimoNativeAnswer(t, payload, "mimo-permission:per_1", got).permission)

	_, err = agenttest.AnswerControl(mimoProvider{}, []byte(payload), "mimo-permission:per_1", agenttest.PermissionDecision("once", "a note"))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "an approval carries no reason")
	_, err = agenttest.AnswerControl(mimoProvider{}, []byte(payload), "mimo-permission:per_1", agenttest.PermissionDecision("always", ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a request with no always pattern offers no always")
}

func TestAnswerControlRequest_ApprovesAPlanWithItsSettings(t *testing.T) {
	payload := `{"type":"question.asked","properties":{"id":"que_2","sessionID":"ses_1","questions":[{"key":"plan_exit","params":{"plan":"plan.md"},"question":"Approve?"}]},` +
		`"request":{"tool_name":"plan_exit","tool_use_id":"call-1"},"plan":"Do the thing."}`
	got, err := agenttest.AnswerControl(mimoProvider{}, []byte(payload), "mimo-question:que_2", agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, PermissionMode: "build", ClearContext: true}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"mimo-question:que_2","response":{"behavior":"allow","updatedInput":{}}}}`, string(got.Content))
	require.NotNil(t, got.PlanApproval)
	assert.Equal(t, "build", got.PlanApproval.GetPermissionMode())
	assert.True(t, got.PlanApproval.GetClearContext())
	assert.Equal(t, [][]string{{planAnswerYes}}, mimoNativeAnswer(t, payload, "mimo-question:que_2", got).answers)

	got, err = agenttest.AnswerControl(mimoProvider{}, []byte(payload), "mimo-question:que_2", agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true}))
	require.NoError(t, err)
	require.NotNil(t, got.PlanApproval, "an approval states its settings, empty ones too")
	assert.Empty(t, got.PlanApproval.GetPermissionMode())
}

// A plan the reader sends back keeps planning with the reader's words as its
// feedback.
func TestAnswerControlRequest_RefusesAPlanWithTheFeedback(t *testing.T) {
	payload := `{"type":"question.asked","properties":{"id":"que_2","questions":[{"key":"plan_exit"}]},"request":{"tool_name":"plan_exit"},"plan":"# Plan"}`
	got, err := agenttest.AnswerControl(mimoProvider{}, []byte(payload), "mimo-question:que_2", agenttest.PlanDecision(&leapmuxv1.PlanDecision{Feedback: "Add tests"}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"mimo-question:que_2","response":{"behavior":"deny","message":"Add tests"}}}`, string(got.Content))
	assert.Nil(t, got.PlanApproval)
	assert.Equal(t, [][]string{{"Add tests"}}, mimoNativeAnswer(t, payload, "mimo-question:que_2", got).answers)

	got, err = agenttest.AnswerControl(mimoProvider{}, []byte(payload), "mimo-question:que_2", agenttest.PlanDecision(&leapmuxv1.PlanDecision{}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"mimo-question:que_2","response":{"behavior":"deny","message":"Rejected by user."}}}`, string(got.Content),
		"an empty composer sends the deny with the placeholder reason")
	assert.Equal(t, [][]string{{planAnswerNo}}, mimoNativeAnswer(t, payload, "mimo-question:que_2", got).answers)

	_, err = agenttest.AnswerControl(mimoProvider{}, []byte(payload), "mimo-question:que_2", agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, ChoiceId: "auto"}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "MiMo's plan approval offers no choice")
}

// MiMo's question tool is OpenCode's, so the answers and the rejection are
// the OpenCode question protocol's own.
func TestAnswerControlRequest_AnswersAQuestion(t *testing.T) {
	payload := `{"type":"question.asked","properties":{"id":"que_1","questions":[
		{"question":"Which database?","header":"Database","options":[{"label":"SQLite"},{"label":"Postgres"}],"multiple":true},
		{"question":"Anything else?","options":[]}]},"request":{"tool_name":"question"}}`
	got, err := agenttest.AnswerControl(mimoProvider{}, []byte(payload), "mimo-question:que_1", agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"SQLite", "Postgres"}}, &leapmuxv1.QuestionAnswer{CustomText: "  more tests  "}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"mimo-question:que_1","result":{"answers":[["SQLite","Postgres"],["more tests"]]}}`, string(got.Content))
	assert.Equal(t, [][]string{{"SQLite", "Postgres"}, {"more tests"}}, mimoNativeAnswer(t, payload, "mimo-question:que_1", got).answers)

	// A multiple-choice answer of two options and typed text chose all three,
	// and the typed text is one more value that MiMo receives.
	got, err = agenttest.AnswerControl(mimoProvider{}, []byte(payload), "mimo-question:que_1", agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"SQLite", "Postgres"}, CustomText: "Oracle"}, &leapmuxv1.QuestionAnswer{CustomText: "none"}))
	require.NoError(t, err)
	assert.Equal(t, [][]string{{"SQLite", "Postgres", "Oracle"}, {"none"}}, mimoNativeAnswer(t, payload, "mimo-question:que_1", got).answers)

	// The browser sends an answer only when every question has one.
	_, err = agenttest.AnswerControl(mimoProvider{}, []byte(payload), "mimo-question:que_1", agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"SQLite"}}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)

	// MiMo's reject route takes no body, so the reader's words cannot reach it.
	got, err = agenttest.AnswerControl(mimoProvider{}, []byte(payload), "mimo-question:que_1", agenttest.CancelQuestions("not now"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"mimo-question:que_1","result":{"rejected":true}}`, string(got.Content))
	assert.True(t, mimoNativeAnswer(t, payload, "mimo-question:que_1", got).reject)
}

// The form's action becomes MiMo's own answer word, which the question
// offers.
func TestAnswerControlRequest_AnswersAnMCPElicitationWithItsAction(t *testing.T) {
	payload := mimoElicitationQuestion(t, map[string]any{"question": "docs\n\nProceed?"})
	for action, word := range map[leapmuxv1.ElicitationAction]string{
		leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT:  elicitationAnswerAccept,
		leapmuxv1.ElicitationAction_ELICITATION_ACTION_DECLINE: elicitationAnswerDecline,
		leapmuxv1.ElicitationAction_ELICITATION_ACTION_CANCEL:  elicitationAnswerCancel,
	} {
		got, err := agenttest.AnswerControl(mimoProvider{}, []byte(payload), "mimo-question:que_1", agenttest.ElicitationDecision(action, ""))
		require.NoError(t, err, action.String())
		native := mimoNativeAnswer(t, payload, "mimo-question:que_1", got)
		assert.True(t, native.elicitation, action.String())
		assert.Equal(t, [][]string{{word}}, native.answers, action.String())
	}

	got, err := agenttest.AnswerControl(mimoProvider{}, []byte(payload), "mimo-question:que_1", agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, `{}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"mimo-question:que_1","response":{"action":"accept","content":{}}}}`, string(got.Content))
}

// A request that MiMo does not state reads as unrecognized, so no decision
// answers it, and LeapMux's own Allow and Deny answer no permission of MiMo,
// whose prompt offers MiMo's reply words alone.
func TestAnswerControlRequest_AnswersNoRequestThatMiMoDoesNotState(t *testing.T) {
	for _, decision := range []*leapmuxv1.ControlDecision{
		agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""),
		agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, ""),
	} {
		_, err := agenttest.AnswerControl(mimoProvider{}, []byte(`{}`), "r", decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
		_, err = mimoProvider{}.AnswerControlRequest(agent.ControlAnswerInput{
			Request: agent.NewControlRequestInput([]byte(`{}`)), RequestID: "r",
			Prompt: agent.GenericPermissionPrompt(), Decision: decision,
		})
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
	}
}

func TestAnswerControlRequest_RefusesADecisionOfAnotherKind(t *testing.T) {
	permission := mimoPermissionRequest(t, map[string]any{"permission": "read"})
	question := `{"type":"question.asked","properties":{"questions":[{"question":"Which?"}]}}`
	for name, tc := range map[string]struct {
		payload  string
		decision *leapmuxv1.ControlDecision
	}{
		"a plan decision to a permission":       {permission, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true})},
		"a permission decision to a question":   {question, agenttest.PermissionDecision("once", "")},
		"a permission decision to a plan":       {`{"type":"question.asked","request":{"tool_name":"plan_exit"}}`, agenttest.PermissionDecision("once", "")},
		"a question decision to an elicitation": {mimoElicitationQuestion(t, nil), agenttest.QuestionDecision()},
		"an option the request does not offer":  {permission, agenttest.PermissionDecision("forever", "")},
		"no decision":                           {permission, nil},
	} {
		_, err := agenttest.AnswerControl(mimoProvider{}, []byte(tc.payload), "r", tc.decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}

	input := agent.NewControlRequestInput([]byte(`{}`))
	_, err := mimoProvider{}.AnswerControlRequest(agent.ControlAnswerInput{
		Request: input, RequestID: "r",
		Prompt:   &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Dialog{Dialog: &leapmuxv1.DialogPrompt{}}},
		Decision: agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_UNSPECIFIED, ""),
	})
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "MiMo states no dialog")
}

package grok

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// grokQuestionPayload is Grok's question dialog as the probe recorded it.
const grokQuestionPayload = `{"jsonrpc":"2.0","id":0,"method":"_x.ai/ask_user_question","params":{
	"sessionId":"s","toolCallId":"call_2_0","mode":"default",
	"questions":[
		{"question":"Which database?","multiSelect":null,"options":[
			{"label":"Postgres (Recommended)","description":"Relational","preview":"CREATE TABLE t ()"},
			{"label":"Redis","description":"In-memory"}]},
		{"question":"Which extras?","multiSelect":true,"options":[
			{"label":"Caching","description":"Add a cache"},
			{"label":"Metrics","description":"Add metrics"}]}
	]}}`

// grokPermissionPayload is a tool permission with one allow and one reject
// option, which every provider of the family reads alike.
const grokPermissionPayload = `{"jsonrpc":"2.0","id":5,"method":"session/request_permission","params":{"sessionId":"s",
	"toolCall":{"toolCallId":"c","kind":"execute","title":"Run","rawInput":{"command":"ls"}},
	"options":[{"optionId":"allow-1","name":"Allow","kind":"allow_once"},{"optionId":"reject-1","name":"Reject","kind":"reject_once"}]}}`

// grokFolderTrustPayload is Grok's folder-trust question with the native id 5.
const grokFolderTrustPayload = `{"jsonrpc":"2.0","id":5,"method":"_x.ai/folder_trust/request","params":{"sessionId":"s","cwd":"/w/sub","workspace":"/w","configKinds":["mcp","hooks"]}}`

// grokPlanPayload is Grok's plan approval with the native id 5.
const grokPlanPayload = `{"jsonrpc":"2.0","id":5,"method":"_x.ai/exit_plan_mode","params":{"sessionId":"s","toolCallId":"c","planContent":"Do the thing."}}`

// grokResultOf reads the JSON-RPC result of content, and checks that it
// answers requestID.
func grokResultOf(t *testing.T, content []byte, requestID string) string {
	t.Helper()
	var reply struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(content, &reply))
	assert.Equal(t, "2.0", reply.JSONRPC)
	assert.Equal(t, requestID, string(reply.ID))
	return string(reply.Result)
}

func TestReadControlRequest_GrokPlanApprovalCarriesThePlanThatGrokSent(t *testing.T) {
	plan := agenttest.ControlPromptOf(grokProvider{}, []byte(`{"method":"_x.ai/exit_plan_mode","params":{"toolCallId":"c","planContent":"# Plan\n\n1. Do X"}}`)).GetPlan()
	require.NotNil(t, plan)
	assert.Equal(t, "# Plan\n\n1. Do X", plan.GetText())

	// Grok sends null for an empty plan, and a plan of another type or none at
	// all states no text either.
	for _, payload := range []string{
		`{"method":"_x.ai/exit_plan_mode","params":{"toolCallId":"c","planContent":null}}`,
		`{"method":"_x.ai/exit_plan_mode","params":{"planContent":""}}`,
		`{"method":"_x.ai/exit_plan_mode","params":{"planContent":7}}`,
		`{"method":"_x.ai/exit_plan_mode"}`,
		`{"method":"_x.ai/exit_plan_mode","params":"plan"}`,
	} {
		plan := agenttest.ControlPromptOf(grokProvider{}, []byte(payload)).GetPlan()
		require.NotNil(t, plan, payload)
		assert.Empty(t, plan.GetText(), payload)
		assert.Empty(t, plan.GetChoices(), payload)
	}
}

func TestReadControlRequest_GrokFolderTrustIsAPermissionWithItsTwoAnswers(t *testing.T) {
	permission := agenttest.ControlPromptOf(grokProvider{}, []byte(grokFolderTrustPayload)).GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, "Trust the workspace /w", permission.GetTitle(), "the workspace wins over the working directory")
	assert.Equal(t, "The repository holds its own Grok Build configuration (mcp, hooks). Grok Build loads it only from a workspace you trust, and it keeps your answer for the workspace.", permission.GetReason())
	require.Len(t, permission.GetOptions(), 2)
	assert.Equal(t, &leapmuxv1.PermissionOption{OptionId: "trust", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Trust this workspace"}, permission.GetOptions()[0])
	assert.Equal(t, &leapmuxv1.PermissionOption{OptionId: "reject", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Do not trust"}, permission.GetOptions()[1])
	assert.Empty(t, permission.GetInputJson())

	permission = agenttest.ControlPromptOf(grokProvider{}, []byte(`{"method":"_x.ai/folder_trust/request","params":{"cwd":"/w","configKinds":[]}}`)).GetPermission()
	assert.Equal(t, "Trust the workspace /w", permission.GetTitle(), "the working directory titles a request with no workspace")
	assert.NotContains(t, permission.GetReason(), "(", "a request with no kind lists none")

	permission = agenttest.ControlPromptOf(grokProvider{}, []byte(`{"method":"_x.ai/folder_trust/request","params":{}}`)).GetPermission()
	assert.Equal(t, "Trust this workspace", permission.GetTitle())
	assert.Equal(t, []string{"trust", "reject"}, agenttest.PermissionOptionIDs(permission.GetOptions()))

	permission = agenttest.ControlPromptOf(grokProvider{}, []byte(`{"method":"_x.ai/folder_trust/request","params":{"workspace":"/w","configKinds":["mcp","",7,null,"hooks"]}}`)).GetPermission()
	assert.Contains(t, permission.GetReason(), "(mcp, hooks)", "only the kinds that are words")

	permission = agenttest.ControlPromptOf(grokProvider{}, []byte(`{"method":"_x.ai/folder_trust/request","params":{"workspace":7,"cwd":"/w","configKinds":"mcp"}}`)).GetPermission()
	assert.Equal(t, "Trust the workspace /w", permission.GetTitle(), "a workspace that is no string reads as absent")
	assert.NotContains(t, permission.GetReason(), "(", "a kind list that is no list states no kind")
}

func TestReadControlRequest_GrokToolPermissionReadsThroughTheBaseReader(t *testing.T) {
	permission := agenttest.ControlPromptOf(grokProvider{}, []byte(`{"method":"session/request_permission","params":{"toolCall":{"toolCallId":"c","kind":"execute","title":"Execute `+"`ls`"+`","rawInput":{"variant":"Bash","command":"ls"}},
		"options":[{"optionId":"allow-once","name":"Yes, proceed","kind":"allow_once"},{"optionId":"reject-once","name":"No","kind":"reject_once"}]}}`)).GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, "ls", permission.GetCommand())
	assert.Equal(t, []string{"allow-once", "reject-once"}, agenttest.PermissionOptionIDs(permission.GetOptions()))

	// Grok presents the normalized input with a `variant` tag, beside the name in
	// `_meta`. With no option list, only LeapMux's cancel answers it.
	const command = "npm test -- --runInBand"
	permission = agenttest.ControlPromptOf(grokProvider{}, []byte(`{"method":"session/request_permission","params":{"toolCall":{"toolCallId":"c","kind":"execute","title":"Execute",
		"rawInput":{"variant":"Bash","command":"`+command+`"},"_meta":{"x.ai/tool":{"name":"run_terminal_command"}}}}}`)).GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, command, permission.GetCommand())
	assert.Equal(t, "Execute", permission.GetTitle())
	assert.Equal(t, []string{acp.CancelOptionID}, agenttest.PermissionOptionIDs(permission.GetOptions()))

	// The arguments carry the whole call, the command included.
	permission = agenttest.ControlPromptOf(grokProvider{}, []byte(`{"method":"session/request_permission","params":{"toolCall":{"toolCallId":"command","kind":"execute","title":"Run command",
		"rawInput":{"command":"printf x","timeout":0,"quiet":false,"note":""}}}}`)).GetPermission()
	assert.Equal(t, "printf x", permission.GetCommand())
	assert.JSONEq(t, `{"command":"printf x","timeout":0,"quiet":false,"note":""}`, permission.GetInputJson())

	permission = agenttest.ControlPromptOf(grokProvider{}, []byte(`{"params":{"toolCall":{"toolCallId":"c","kind":"other","title":"Write"},
		"options":[{"optionId":"once","kind":"allow_once","name":"Allow once"},{"optionId":"reject","kind":"reject_once","name":"Reject"}]}}`)).GetPermission()
	assert.Equal(t, []string{"once", "reject"}, agenttest.PermissionOptionIDs(permission.GetOptions()), "a request that states its options keeps them")
}

func TestReadControlRequest_GrokReadsAnUnknownRequestAsUnrecognized(t *testing.T) {
	for _, payload := range []string{`{"method":"_x.ai/unknown"}`, `{}`, `not json`, `{"method":7,"params":{"planContent":"x"}}`} {
		agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(grokProvider{}, []byte(payload)), payload)
	}
}

func TestReadControlRequest_GrokReadsItsOwnElicitationMethod(t *testing.T) {
	elicitation := agenttest.ControlPromptOf(grokProvider{}, []byte(`{"method":"_x.ai/mcp/elicit","params":{"serverName":"auth","message":"Sign in","mode":"url","url":"https://example.com/login","elicitationId":"e"}}`)).GetElicitation()
	require.NotNil(t, elicitation)
	assert.Equal(t, &leapmuxv1.ElicitationPrompt{Mode: "url", Message: "Sign in", Server: "auth", Url: "https://example.com/login"}, elicitation,
		"Grok states no schema, title or description for a URL")

	elicitation = agenttest.ControlPromptOf(grokProvider{}, []byte(`{"method":"_x.ai/mcp/elicit","params":{"serverName":"docs","message":"Pick one","mode":"form","requestedSchema":{"type":"object"},"title":"T","server":"other"}}`)).GetElicitation()
	require.NotNil(t, elicitation)
	assert.Equal(t, "docs", elicitation.GetServer(), "the server is under serverName")
	assert.JSONEq(t, `{"type":"object"}`, elicitation.GetSchemaJson())
	assert.Empty(t, elicitation.GetTitle(), "Grok's own method states no title")

	for _, payload := range []string{`{"method":"_x.ai/mcp/elicit","params":{}}`, `{"method":"_x.ai/mcp/elicit"}`, `{"method":"_x.ai/mcp/elicit","params":{"mode":7}}`} {
		elicitation := agenttest.ControlPromptOf(grokProvider{}, []byte(payload)).GetElicitation()
		require.NotNil(t, elicitation, payload)
		assert.Equal(t, contracts.MCPElicitationModeForm, elicitation.GetMode(), payload)
		assert.Empty(t, elicitation.GetSchemaJson(), payload)
	}

	elicitation = agenttest.ControlPromptOf(grokProvider{}, []byte(`{"method":"elicitation/create","params":{"message":"Hi","server":"s"}}`)).GetElicitation()
	require.NotNil(t, elicitation, "the protocol's own elicitation still reads as a form")
	assert.Equal(t, "Hi", elicitation.GetMessage())

	assert.Nil(t, agenttest.ControlPromptOf(grokProvider{}, []byte(`{"method":"session/request_permission"}`)).GetElicitation())
}

func TestReadControlRequest_GrokQuestionDialog(t *testing.T) {
	prompt := agenttest.ControlPromptOf(grokProvider{}, []byte(grokQuestionPayload))
	question := prompt.GetQuestion()
	require.NotNil(t, question)
	assert.True(t, question.GetPreservesSelectionNotes(), "Grok's reply carries a note beside a chosen option")
	require.Len(t, question.GetQuestions(), 2)
	assert.False(t, question.GetQuestions()[0].GetMultiSelect(), "a null multiSelect is a single choice")
	assert.True(t, question.GetQuestions()[1].GetMultiSelect())
	assert.Equal(t, &leapmuxv1.QuestionOption{Label: "Postgres (Recommended)", Description: "Relational", Preview: "CREATE TABLE t ()"}, question.GetQuestions()[0].GetOptions()[0])
	assert.Equal(t, leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_UNSPECIFIED, prompt.GetEditorPurpose())

	for _, payload := range []string{`{"method":"_x.ai/ask_user_question"}`, `{"method":"_x.ai/ask_user_question","params":{"questions":"text"}}`} {
		question := agenttest.ControlPromptOf(grokProvider{}, []byte(payload)).GetQuestion()
		require.NotNil(t, question, payload)
		assert.Empty(t, question.GetQuestions(), payload)
	}

	assert.Nil(t, agenttest.ControlPromptOf(grokProvider{}, []byte(`{"method":"_x.ai/exit_plan_mode"}`)).GetQuestion(), "only the question method is a question")
}

func TestAnswerControlRequest_GrokQuestionStatesGrokReply(t *testing.T) {
	extras := agenttest.QuestionAnswer("", "Metrics")
	for _, tc := range []struct {
		name    string
		answers []*leapmuxv1.QuestionAnswer
		want    string
	}{
		{
			name:    "keys each answer by the question text, with the chosen labels",
			answers: []*leapmuxv1.QuestionAnswer{agenttest.QuestionAnswer("", "Redis"), agenttest.QuestionAnswer("", "Caching", "Metrics")},
			want:    `{"outcome":"accepted","answers":{"Which database?":["Redis"],"Which extras?":["Caching","Metrics"]}}`,
		},
		{
			name:    "sends the notes beside a chosen option, and the preview of a single choice",
			answers: []*leapmuxv1.QuestionAnswer{agenttest.QuestionAnswer(" Use version 16 ", "Postgres (Recommended)"), extras},
			want:    `{"outcome":"accepted","answers":{"Which database?":["Postgres (Recommended)"],"Which extras?":["Metrics"]},"annotations":{"Which database?":{"preview":"CREATE TABLE t ()","notes":"Use version 16"}}}`,
		},
		{
			name:    "answers a typed answer alone with the free-text label",
			answers: []*leapmuxv1.QuestionAnswer{agenttest.QuestionAnswer("SQLite"), extras},
			want:    `{"outcome":"accepted","answers":{"Which database?":["Other"],"Which extras?":["Metrics"]},"annotations":{"Which database?":{"notes":"SQLite"}}}`,
		},
		{
			name:    "sends no annotation for a single choice that carries no preview",
			answers: []*leapmuxv1.QuestionAnswer{agenttest.QuestionAnswer("", "Redis"), extras},
			want:    `{"outcome":"accepted","answers":{"Which database?":["Redis"],"Which extras?":["Metrics"]}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := agenttest.AnswerControl(grokProvider{}, []byte(grokQuestionPayload), "jsonrpc:0", agenttest.QuestionDecision(tc.answers...))
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, grokResultOf(t, got.Content, `"jsonrpc:0"`))
			assert.Empty(t, got.FollowUp)
			assert.Nil(t, got.PlanApproval)
		})
	}
}

// The browser sends an answer only when every question has one, and white
// space is no answer, so the Worker refuses an answer that leaves one open.
func TestAnswerControlRequest_GrokQuestionRefusesAQuestionLeftOpen(t *testing.T) {
	for name, answers := range map[string][]*leapmuxv1.QuestionAnswer{
		"an empty question":         {agenttest.QuestionAnswer(""), agenttest.QuestionAnswer("", "Metrics")},
		"white space alone":         {agenttest.QuestionAnswer("", "Redis"), agenttest.QuestionAnswer("   ")},
		"no answer to any question": nil,
	} {
		_, err := agenttest.AnswerControl(grokProvider{}, []byte(grokQuestionPayload), "jsonrpc:0", agenttest.QuestionDecision(answers...))
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

// A preview belongs to ONE choice, so a multi-select answer sends none, even
// with one option chosen.
func TestAnswerControlRequest_GrokQuestionSendsNoPreviewForAMultipleChoice(t *testing.T) {
	payload := `{"method":"_x.ai/ask_user_question","params":{"questions":[{"question":"Which?","multiSelect":true,"options":[{"label":"A","preview":"a()"},{"label":"B"}]}]}}`
	got, err := agenttest.AnswerControl(grokProvider{}, []byte(payload), "jsonrpc:0", agenttest.QuestionDecision(agenttest.QuestionAnswer("", "A")))
	require.NoError(t, err)
	assert.JSONEq(t, `{"outcome":"accepted","answers":{"Which?":["A"]}}`, grokResultOf(t, got.Content, `"jsonrpc:0"`))
}

// The keys are the model's own question text, so a text that spells a
// prototype key in the browser is an ordinary key.
func TestAnswerControlRequest_GrokQuestionKeepsAnAnswerToAQuestionWordedLikeAPrototypeKey(t *testing.T) {
	payload := `{"method":"_x.ai/ask_user_question","params":{"questions":[{"question":"__proto__","options":[{"label":"Yes"}]}]}}`
	got, err := agenttest.AnswerControl(grokProvider{}, []byte(payload), "jsonrpc:0", agenttest.QuestionDecision(agenttest.QuestionAnswer("", "Yes")))
	require.NoError(t, err)
	assert.JSONEq(t, `{"outcome":"accepted","answers":{"__proto__":["Yes"]}}`, grokResultOf(t, got.Content, `"jsonrpc:0"`))
}

// Two questions with the same text share one key: the later answer wins, and
// the earlier annotation stays when the later answer states none.
func TestAnswerControlRequest_GrokQuestionWithARepeatedTextKeepsTheLaterAnswer(t *testing.T) {
	payload := `{"method":"_x.ai/ask_user_question","params":{"questions":[
		{"question":"Pick","options":[{"label":"A","preview":"a()"},{"label":"B"}]},
		{"question":"Pick","options":[{"label":"C"}]}]}}`
	got, err := agenttest.AnswerControl(grokProvider{}, []byte(payload), "jsonrpc:0", agenttest.QuestionDecision(agenttest.QuestionAnswer("", "A"), agenttest.QuestionAnswer("", "C")))
	require.NoError(t, err)
	assert.JSONEq(t, `{"outcome":"accepted","answers":{"Pick":["C"]},"annotations":{"Pick":{"preview":"a()"}}}`, grokResultOf(t, got.Content, `"jsonrpc:0"`))
}

// An option's value is what the answer sends, and its preview is found by that
// value.
func TestAnswerControlRequest_GrokQuestionSendsTheValueOfAnOption(t *testing.T) {
	payload := `{"method":"_x.ai/ask_user_question","params":{"questions":[{"question":"Pick","options":[{"label":"Alpha","value":"a","preview":"p"}]}]}}`
	got, err := agenttest.AnswerControl(grokProvider{}, []byte(payload), "jsonrpc:0", agenttest.QuestionDecision(agenttest.QuestionAnswer("", "a")))
	require.NoError(t, err)
	assert.JSONEq(t, `{"outcome":"accepted","answers":{"Pick":["a"]},"annotations":{"Pick":{"preview":"p"}}}`, grokResultOf(t, got.Content, `"jsonrpc:0"`))
}

func TestAnswerControlRequest_GrokQuestionSendsTheWholeReplyUnderTheWorkerRequestID(t *testing.T) {
	got, err := agenttest.AnswerControl(grokProvider{}, []byte(grokQuestionPayload), "jsonrpc:0", agenttest.QuestionDecision(agenttest.QuestionAnswer("", "Redis"), agenttest.QuestionAnswer("", "Metrics")))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:0","result":{"outcome":"accepted","answers":{"Which database?":["Redis"],"Which extras?":["Metrics"]}}}`, string(got.Content))
}

// Grok's `cancelled` carries no reason: Grok tells the model that the reader
// declined to answer.
func TestAnswerControlRequest_GrokQuestionCancelSendsTheCancelledOutcome(t *testing.T) {
	for _, feedback := range []string{"", "User stopped"} {
		got, err := agenttest.AnswerControl(grokProvider{}, []byte(grokQuestionPayload), "jsonrpc:0", agenttest.CancelQuestions(feedback))
		require.NoError(t, err, feedback)
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:0","result":{"outcome":"cancelled"}}`, string(got.Content), feedback)
		assert.Empty(t, got.FollowUp, "the reason is not sent as a message either")
	}
}

func TestAnswerControlRequest_GrokQuestionRefusesAnAnswerThatThePromptDoesNotOffer(t *testing.T) {
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"an unknown value":           agenttest.QuestionDecision(agenttest.QuestionAnswer("", "MySQL")),
		"two values for one choice":  agenttest.QuestionDecision(agenttest.QuestionAnswer("", "Redis", "Postgres (Recommended)")),
		"an answer for no question":  agenttest.QuestionDecision(agenttest.QuestionAnswer(""), agenttest.QuestionAnswer(""), agenttest.QuestionAnswer("x")),
		"a decision of another kind": agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""),
	} {
		_, err := agenttest.AnswerControl(grokProvider{}, []byte(grokQuestionPayload), "jsonrpc:0", decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

func TestAnswerControlRequest_GrokPlanApprovalTakesTheNeutralEnvelope(t *testing.T) {
	got, err := agenttest.AnswerControl(grokProvider{}, []byte(grokPlanPayload), grokStoredRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, PermissionMode: contracts.GrokModeDefault, ClearContext: true}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:5","response":{"behavior":"allow","updatedInput":{}}}}`, string(got.Content))
	assert.Equal(t, contracts.GrokModeDefault, got.PlanApproval.GetPermissionMode())
	assert.True(t, got.PlanApproval.GetClearContext())
	resolved := agenttest.ResolveAnswer(grokProvider{}, []byte(grokPlanPayload), grokStoredRequestID, got)
	require.False(t, resolved.Withhold)
	assert.JSONEq(t, `{"outcome":"approved"}`, grokResultOf(t, resolved.Content, "5"), "Grok's own reply follows from the answer")
	assert.Equal(t, agent.PlanModeControlExit, resolved.PlanModeControl)

	// The composer's text refuses the plan, and Grok reads it as its feedback,
	// so it never follows as a message of its own.
	got, err = agenttest.AnswerControl(grokProvider{}, []byte(grokPlanPayload), grokStoredRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Feedback: "Split step 2."}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:5","response":{"behavior":"deny","message":"Split step 2."}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp)
	assert.Nil(t, got.PlanApproval, "a refusal carries no plan settings")
	resolved = agenttest.ResolveAnswer(grokProvider{}, []byte(grokPlanPayload), grokStoredRequestID, got)
	require.False(t, resolved.Withhold)
	assert.JSONEq(t, `{"outcome":"cancelled","feedback":"Split step 2."}`, grokResultOf(t, resolved.Content, "5"))

	got, err = agenttest.AnswerControl(grokProvider{}, []byte(grokPlanPayload), grokStoredRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"outcome":"cancelled"}`, grokResultOf(t, agenttest.ResolveAnswer(grokProvider{}, []byte(grokPlanPayload), grokStoredRequestID, got).Content, "5"), "a bare refusal states no feedback")
}

func TestAnswerControlRequest_GrokPlanApprovalRefusesWhatThePromptDoesNotOffer(t *testing.T) {
	_, err := agenttest.AnswerControl(grokProvider{}, []byte(grokPlanPayload), grokStoredRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, ChoiceId: "abandoned"}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "Grok's plan approval offers no choice")
	_, err = agenttest.AnswerControl(grokProvider{}, []byte(grokPlanPayload), grokStoredRequestID, agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}

func TestAnswerControlRequest_GrokFolderTrustSelectsAnOptionThatGrokReads(t *testing.T) {
	got, err := agenttest.AnswerControl(grokProvider{}, []byte(grokFolderTrustPayload), grokStoredRequestID, agenttest.PermissionDecision(contracts.GrokTrustOutcomeTrust, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"outcome":{"outcome":"selected","optionId":"trust"}}`, grokResultOf(t, got.Content, `"jsonrpc:5"`))
	resolved := agenttest.ResolveAnswer(grokProvider{}, []byte(grokFolderTrustPayload), grokStoredRequestID, got)
	require.False(t, resolved.Withhold)
	assert.JSONEq(t, `{"outcome":"trust"}`, grokResultOf(t, resolved.Content, "5"))

	got, err = agenttest.AnswerControl(grokProvider{}, []byte(grokFolderTrustPayload), grokStoredRequestID, agenttest.PermissionDecision(contracts.GrokTrustOutcomeReject, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"outcome":{"outcome":"selected","optionId":"reject"}}`, grokResultOf(t, got.Content, `"jsonrpc:5"`))
	assert.Empty(t, got.FollowUp)
	resolved = agenttest.ResolveAnswer(grokProvider{}, []byte(grokFolderTrustPayload), grokStoredRequestID, got)
	require.False(t, resolved.Withhold)
	assert.JSONEq(t, `{"outcome":"reject"}`, grokResultOf(t, resolved.Content, "5"))

	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"an answer that is no option":   agenttest.PermissionDecision("maybe", ""),
		"LeapMux's allow":               agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""),
		"a note beside the trust grant": agenttest.PermissionDecision(contracts.GrokTrustOutcomeTrust, "a note"),
		// resolveGrokFolderTrust sends the decision alone, so a reason would
		// reach nobody.
		"a reason beside the refusal": agenttest.PermissionDecision(contracts.GrokTrustOutcomeReject, "not this repository"),
	} {
		_, err := agenttest.AnswerControl(grokProvider{}, []byte(grokFolderTrustPayload), grokStoredRequestID, decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

// Every permission of the family answers with the agent's own option, and
// Grok's refusal carries its reason in Grok's own field.
func TestAnswerControlRequest_GrokPermissionSelectsTheAgentsOwnOption(t *testing.T) {
	got, err := agenttest.AnswerControl(grokProvider{}, []byte(grokPermissionPayload), "jsonrpc:3", agenttest.PermissionDecision("reject-1", "use the other file"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:3","result":{"outcome":{"outcome":"selected","optionId":"reject-1"},"_meta":{"followup_message":"use the other file"}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp, "the reason rides in the reply, so it is not sent twice")

	got, err = agenttest.AnswerControl(grokProvider{}, []byte(grokPermissionPayload), "jsonrpc:3", agenttest.PermissionDecision("allow-1", ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:3","result":{"outcome":{"outcome":"selected","optionId":"allow-1"}}}`, string(got.Content))

	got, err = agenttest.AnswerControl(grokProvider{}, []byte(grokPermissionPayload), "jsonrpc:3", agenttest.PermissionDecision("reject-1", ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:3","result":{"outcome":{"outcome":"selected","optionId":"reject-1"}}}`, string(got.Content), "a bare refusal states no reason")

	_, err = agenttest.AnswerControl(grokProvider{}, []byte(grokPermissionPayload), "jsonrpc:3", agenttest.PermissionDecision("allow-1", "a note"))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "an approval carries no reason")
}

func TestAnswerControlRequest_GrokPermissionWithOnlyARejectOptionCarriesTheReason(t *testing.T) {
	payload := `{"params":{"toolCall":{"toolCallId":"c"},"options":[{"optionId":"no","kind":"reject_once"}]}}`
	got, err := agenttest.AnswerControl(grokProvider{}, []byte(payload), "r", agenttest.PermissionDecision("no", "why"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"r","result":{"outcome":{"outcome":"selected","optionId":"no"},"_meta":{"followup_message":"why"}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp)
}

// A request with options and no reject option is refused by the protocol's
// cancelled outcome, and Grok still reads the reason beside it.
func TestAnswerControlRequest_GrokPermissionWithNoRejectOptionCancels(t *testing.T) {
	payload := `{"params":{"toolCall":{"toolCallId":"c"},"options":[{"optionId":"once","kind":"allow_once"}]}}`
	got, err := agenttest.AnswerControl(grokProvider{}, []byte(payload), "r", agenttest.PermissionDecision(acp.CancelOptionID, "why"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"r","result":{"outcome":{"outcome":"cancelled"},"_meta":{"followup_message":"why"}}}`, string(got.Content))
}

// A request with no options takes LeapMux's cancel, which reaches Grok as the
// protocol's cancelled outcome under Grok's own id, with the reason in Grok's
// own field.
func TestAnswerControlRequest_GrokPermissionWithNoOptionsCancels(t *testing.T) {
	payload := `{"jsonrpc":"2.0","id":3,"method":"session/request_permission","params":{"toolCall":{"toolCallId":"c","title":"Edit"}}}`
	got, err := agenttest.AnswerControl(grokProvider{}, []byte(payload), "jsonrpc:3", agenttest.PermissionDecision(acp.CancelOptionID, "why"))
	require.NoError(t, err)
	resolved := agenttest.ResolveAnswer(grokProvider{}, []byte(payload), "jsonrpc:3", got)
	require.NoError(t, resolved.Refusal())
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":3,"result":{"outcome":{"outcome":"cancelled"},"_meta":{"followup_message":"why"}}}`, string(resolved.Content))
	assert.Empty(t, got.FollowUp, "the reason rides in the reply, so it is not sent twice")

	_, err = agenttest.AnswerControl(grokProvider{}, []byte(payload), "jsonrpc:3", agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "Grok cannot read LeapMux's deny")
}

func TestAnswerControlRequest_GrokElicitationTakesTheNeutralEnvelopeThatGrokResolves(t *testing.T) {
	payload := `{"jsonrpc":"2.0","id":5,"method":"_x.ai/mcp/elicit","params":{"sessionId":"s","serverName":"docs","message":"Pick one","mode":"form","requestedSchema":{"type":"object"}}}`
	got, err := agenttest.AnswerControl(grokProvider{}, []byte(payload), grokStoredRequestID, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, `{"name":"x"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:5","response":{"action":"accept","content":{"name":"x"}}}}`, string(got.Content))
	resolved := agenttest.ResolveAnswer(grokProvider{}, []byte(payload), grokStoredRequestID, got)
	require.False(t, resolved.Withhold)
	assert.JSONEq(t, `{"outcome":"accept","content":{"name":"x"}}`, grokResultOf(t, resolved.Content, "5"))

	got, err = agenttest.AnswerControl(grokProvider{}, []byte(payload), grokStoredRequestID, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_DECLINE, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"outcome":"decline"}`, grokResultOf(t, agenttest.ResolveAnswer(grokProvider{}, []byte(payload), grokStoredRequestID, got).Content, "5"))
}

func TestGrokRejectReason(t *testing.T) {
	encode := func(value map[string]any) string {
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		return string(encoded)
	}
	result := map[string]any{
		"outcome": map[string]any{"outcome": "selected", "optionId": "reject-once"},
		"_meta":   map[string]any{"other": 1},
	}
	assert.JSONEq(t, `{"outcome":{"outcome":"selected","optionId":"reject-once"},"_meta":{"other":1,"followup_message":"Use pnpm"}}`,
		encode(grokRejectReason(result, "Use pnpm")), "adds the reason as the follow-up message and keeps the rest")
	assert.JSONEq(t, `{"outcome":{"outcome":"selected","optionId":"reject-once"},"_meta":{"other":1}}`, encode(result), "leaves the result it received unchanged")

	assert.JSONEq(t, `{"outcome":{"outcome":"cancelled"},"_meta":{"followup_message":"why"}}`,
		encode(grokRejectReason(map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, "why")), "adds a meta object to a result that has none")
	assert.JSONEq(t, `{"_meta":{"followup_message":"why"}}`, encode(grokRejectReason(map[string]any{"_meta": "x"}, "why")), "replaces a meta value that is no object")
	assert.JSONEq(t, `{"_meta":{"followup_message":"new"}}`, encode(grokRejectReason(map[string]any{"_meta": map[string]any{"followup_message": "old"}}, "new")), "replaces an earlier follow-up message")
}

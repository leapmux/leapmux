package kiro

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

// kiroAlwaysOptions are the four options of a Kiro permission whose rule can
// persist.
const kiroAlwaysOptions = `[
	{"optionId":"accept","name":"Allow","kind":"allow_once"},
	{"optionId":"always-accept","name":"Always allow","kind":"allow_always"},
	{"optionId":"reject","name":"Deny","kind":"reject_once"},
	{"optionId":"always-reject","name":"Always deny","kind":"reject_always"}]`

// kiroShellPayload is Kiro's permission request for a shell command, as the
// probe recorded it.
const kiroShellPayload = `{"jsonrpc":"2.0","id":5,"method":"session/request_permission","params":{"sessionId":"s",
	"toolCall":{"toolCallId":"run_command_t_sh","status":"pending","title":"echo v3-shell"},
	"options":` + kiroAlwaysOptions + `,
	"_meta":{"kiro":{"toolId":"run_command","command":"echo v3-shell","consentRound":1,
		"consent":{"capability":"shell","resource":"echo v3-shell","askType":"implicit","workspaceRoot":"/w"}}}}}`

// kiroTurnApprovalPayload is Kiro's review of a Supervised turn, as the probe
// recorded it, with the `_meta.kiro` that each case states.
func kiroTurnApprovalPayload(kiroMeta string) string {
	return `{"jsonrpc":"2.0","id":6,"method":"session/request_permission","params":{"sessionId":"s",
		"toolCall":{"toolCallId":"turn_approval_1","status":"pending","title":"Review changes"},
		"options":[{"optionId":"accept","name":"Accept changes","kind":"allow_once"},{"optionId":"reject","name":"Reject changes","kind":"reject_once"}],
		"_meta":{"kiro":` + kiroMeta + `}}}`
}

// kiroQuestionPayload is Kiro's question as the probe recorded it.
const kiroQuestionPayload = `{"jsonrpc":"2.0","id":4,"method":"_kiro/userInput","params":{"sessionId":"s","toolCallId":"t_q","question":"Which DB?","options":[
	{"title":"Postgres","description":"pg","recommended":true,"subOptionsLabel":"Extras","subOptions":[{"title":"PostGIS","description":"geo"},{"title":"pgvector"}]},
	{"title":"SQLite","recommended":false}]}}`

func kiroAnswer(custom string, selected ...string) *leapmuxv1.QuestionAnswer {
	return &leapmuxv1.QuestionAnswer{Selected: selected, CustomText: custom}
}

func kiroOptionIDs(options []*leapmuxv1.PermissionOption) []string {
	ids := make([]string, 0, len(options))
	for _, option := range options {
		ids = append(ids, option.GetOptionId())
	}
	return ids
}

func kiroValue(value string) *string { return &value }

func kiroOption(id string, kind leapmuxv1.PermissionOptionKind, name string, scope leapmuxv1.PermissionScope) *leapmuxv1.PermissionOption {
	return &leapmuxv1.PermissionOption{OptionId: id, Kind: kind, Name: name, Scope: scope}
}

const (
	kiroAllowOnce    = leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE
	kiroAllowAlways  = leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS
	kiroRejectOnce   = leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE
	kiroRejectAlways = leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ALWAYS
	kiroNoScope      = leapmuxv1.PermissionScope_PERMISSION_SCOPE_UNSPECIFIED
	kiroSession      = leapmuxv1.PermissionScope_PERMISSION_SCOPE_SESSION
	kiroWorkspace    = leapmuxv1.PermissionScope_PERMISSION_SCOPE_WORKSPACE
	kiroUser         = leapmuxv1.PermissionScope_PERMISSION_SCOPE_USER
)

// kiroPermissionWith is a permission request with options and `_meta.kiro`.
func kiroPermissionWith(options, kiroMeta string) string {
	return `{"method":"session/request_permission","params":{"toolCall":{"toolCallId":"c","title":"Run"},"options":` + options + `,"_meta":{"kiro":` + kiroMeta + `}}}`
}

func TestReadControlRequest_KiroAddsTheWiderScopesAfterEachOfItsOwnAlwaysOptions(t *testing.T) {
	permission := agenttest.ControlPromptOf(kiroProvider{}, []byte(kiroPermissionWith(kiroAlwaysOptions, `{"consent":{"workspaceRoot":"/w"}}`))).GetPermission()
	require.NotNil(t, permission)
	want := []*leapmuxv1.PermissionOption{
		kiroOption("accept", kiroAllowOnce, "Allow", kiroNoScope),
		kiroOption("always-accept", kiroAllowAlways, "Always allow for this session", kiroSession),
		kiroOption("always-accept-workspace", kiroAllowAlways, "Always allow in this workspace", kiroWorkspace),
		kiroOption("always-accept-user", kiroAllowAlways, "Always allow everywhere", kiroUser),
		kiroOption("reject", kiroRejectOnce, "Deny", kiroNoScope),
		kiroOption("always-reject", kiroRejectAlways, "Always deny for this session", kiroSession),
		kiroOption("always-reject-workspace", kiroRejectAlways, "Always deny in this workspace", kiroWorkspace),
		kiroOption("always-reject-user", kiroRejectAlways, "Always deny everywhere", kiroUser),
	}
	require.Len(t, permission.GetOptions(), len(want))
	for i := range want {
		agenttest.RequireProto(t, want[i], permission.GetOptions()[i])
	}
}

func TestReadControlRequest_KiroLeavesTheWorkspaceScopeOutWithNoWorkspaceRoot(t *testing.T) {
	for _, kiroMeta := range []string{`null`, `{}`, `{"consent":{}}`, `{"consent":{"workspaceRoot":"  "}}`, `{"consent":"/w"}`, `{"consent":{"workspaceRoot":7}}`} {
		permission := agenttest.ControlPromptOf(kiroProvider{}, []byte(kiroPermissionWith(kiroAlwaysOptions, kiroMeta))).GetPermission()
		assert.Equal(t, []string{"accept", "always-accept", "always-accept-user", "reject", "always-reject", "always-reject-user"}, kiroOptionIDs(permission.GetOptions()), kiroMeta)
	}
}

// The option id alone is not Kiro's always rule: the kind must say so too.
func TestReadControlRequest_KiroAddsNothingForItsIDUnderAnotherKind(t *testing.T) {
	permission := agenttest.ControlPromptOf(kiroProvider{}, []byte(kiroPermissionWith(`[{"optionId":"always-accept","name":"Allow","kind":"allow_once"},{"optionId":"always-reject","name":"Deny","kind":"reject_once"}]`, `{"consent":{"workspaceRoot":"/w"}}`))).GetPermission()
	agenttest.RequireProto(t, kiroOption("always-accept", kiroAllowOnce, "Allow", kiroNoScope), permission.GetOptions()[0])
	agenttest.RequireProto(t, kiroOption("always-reject", kiroRejectOnce, "Deny", kiroNoScope), permission.GetOptions()[1])
	assert.Len(t, permission.GetOptions(), 2)

	permission = agenttest.ControlPromptOf(kiroProvider{}, []byte(kiroPermissionWith(`[{"optionId":"accept","name":"Allow","kind":"allow_once"},{"optionId":"reject","name":"Deny","kind":"reject_once"}]`, `{"consent":{"workspaceRoot":"/w"}}`))).GetPermission()
	assert.Equal(t, []string{"accept", "reject"}, kiroOptionIDs(permission.GetOptions()), "a request with no always option keeps its options")
	assert.Empty(t, permission.GetOptions()[0].GetScope())
}

// Kiro offers no always-accept for an explicit ask, so no scope pill draws,
// and the always-deny is a button of its own that states its duration.
func TestReadControlRequest_KiroStatesTheSessionScopeOfAnAlwaysDenyAlone(t *testing.T) {
	permission := agenttest.ControlPromptOf(kiroProvider{}, []byte(kiroPermissionWith(`[{"optionId":"accept","name":"Allow","kind":"allow_once"},{"optionId":"reject","name":"Deny","kind":"reject_once"},{"optionId":"always-reject","name":"Always deny","kind":"reject_always"}]`, `{"consent":{"workspaceRoot":"/w"}}`))).GetPermission()
	require.Len(t, permission.GetOptions(), 3)
	agenttest.RequireProto(t, kiroOption("always-reject", kiroRejectAlways, "Always deny for this session", kiroSession), permission.GetOptions()[2])
}

// A request with Kiro's always-accept and no reject option keeps LeapMux's
// cancel, which the base reader adds, after the wider scopes.
func TestReadControlRequest_KiroKeepsTheCancelOfARequestWithNoRejectOption(t *testing.T) {
	permission := agenttest.ControlPromptOf(kiroProvider{}, []byte(kiroPermissionWith(`[{"optionId":"always-accept","kind":"allow_always"}]`, `{}`))).GetPermission()
	assert.Equal(t, []string{"always-accept", "always-accept-user", acp.CancelOptionID}, kiroOptionIDs(permission.GetOptions()))
}

func TestReadControlRequest_KiroShellPermissionStatesItsCommandAndEveryScope(t *testing.T) {
	permission := agenttest.ControlPromptOf(kiroProvider{}, []byte(kiroShellPayload)).GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, "echo v3-shell", permission.GetTitle())
	assert.Equal(t, "echo v3-shell", permission.GetCommand(), "Kiro states the command in its own metadata")
	assert.Contains(t, kiroOptionIDs(permission.GetOptions()), "always-accept-workspace")
	assert.Empty(t, permission.GetReason())

	// Kiro's request states the call as its title alone, and the command in its
	// own metadata.
	const command = "npm test -- --runInBand"
	permission = agenttest.ControlPromptOf(kiroProvider{}, []byte(`{"method":"session/request_permission","params":{"toolCall":{"toolCallId":"run_command_c","status":"pending","title":"`+command+`"},
		"_meta":{"kiro":{"toolId":"run_command","command":"`+command+`"}}}}`)).GetPermission()
	assert.Equal(t, command, permission.GetCommand())
	assert.Equal(t, command, permission.GetTitle())
	assert.Equal(t, []string{acp.CancelOptionID}, kiroOptionIDs(permission.GetOptions()),
		"the protocol answers a call with no options only with the cancelled outcome")
}

// The command that the base reader found is what the call runs, so Kiro's own
// copy only fills a permission that states none.
func TestReadControlRequest_KiroKeepsTheCommandThatTheBaseReaderFound(t *testing.T) {
	permission := agenttest.ControlPromptOf(kiroProvider{}, []byte(`{"method":"session/request_permission","params":{
		"toolCall":{"toolCallId":"c","status":"pending","kind":"execute","title":"List","rawInput":{"command":"ls -la"}},
		"options":`+kiroAlwaysOptions+`,"_meta":{"kiro":{"command":"echo v3-shell"}}}}`)).GetPermission()
	assert.Equal(t, "ls -la", permission.GetCommand())
	assert.JSONEq(t, `{"command":"ls -la"}`, permission.GetInputJson())

	permission = agenttest.ControlPromptOf(kiroProvider{}, []byte(`{"method":"session/request_permission","params":{"toolCall":{"toolCallId":"command","kind":"execute","title":"Run command",
		"rawInput":{"command":"printf x","timeout":0,"quiet":false,"note":""}}}}`)).GetPermission()
	assert.Equal(t, "printf x", permission.GetCommand())
	assert.JSONEq(t, `{"command":"printf x","timeout":0,"quiet":false,"note":""}`, permission.GetInputJson())
}

func TestReadControlRequest_KiroTurnReviewStatesTheFilesThatItHolds(t *testing.T) {
	permission := agenttest.ControlPromptOf(kiroProvider{}, []byte(kiroTurnApprovalPayload(`{"type":"turn_approval","executionId":"e","files":[{"path":"/w/supervised.txt","toolCallId":"w_sup"}]}`))).GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, "Review changes", permission.GetTitle())
	assert.Equal(t, "Kiro holds the file changes of this turn for your review. Allow applies them, and Deny restores each file:\n- /w/supervised.txt", permission.GetReason())
	assert.Equal(t, []string{"Accept changes", "Reject changes"}, []string{permission.GetOptions()[0].GetName(), permission.GetOptions()[1].GetName()})

	permission = agenttest.ControlPromptOf(kiroProvider{}, []byte(kiroTurnApprovalPayload(`{"type":"turn_approval","files":[{"path":""},"loose",null,{"path":"/w/kept.txt"},{"path":7}]}`))).GetPermission()
	assert.Equal(t, "Kiro holds the file changes of this turn for your review. Allow applies them, and Deny restores each file:\n- /w/kept.txt", permission.GetReason(), "only the files that state a path")

	const bare = "Kiro holds the file changes of this turn for your review. Allow applies them, and Deny restores each file."
	for _, kiroMeta := range []string{`{"type":"turn_approval"}`, `{"type":"turn_approval","files":[{"path":""}]}`, `{"type":"turn_approval","files":"x"}`} {
		assert.Equal(t, bare, agenttest.ControlPromptOf(kiroProvider{}, []byte(kiroTurnApprovalPayload(kiroMeta))).GetPermission().GetReason(), kiroMeta)
	}

	// A turn review states no command, even when its metadata names one.
	permission = agenttest.ControlPromptOf(kiroProvider{}, []byte(kiroTurnApprovalPayload(`{"type":"turn_approval","command":"rm -rf /"}`))).GetPermission()
	assert.Empty(t, permission.GetCommand())
}

func TestReadControlRequest_KiroReadsAnUnknownRequestAsUnrecognized(t *testing.T) {
	for _, payload := range []string{`{"method":"_kiro/unknown"}`, `{}`, `not json`, `{"method":7}`} {
		agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(kiroProvider{}, []byte(payload)), payload)
	}
	permission := agenttest.ControlPromptOf(kiroProvider{}, []byte(`{"params":{"toolCall":{"toolCallId":"c","kind":"other","title":"Write"},
		"options":[{"optionId":"once","kind":"allow_once","name":"Allow once"},{"optionId":"reject","kind":"reject_once","name":"Reject"}]}}`)).GetPermission()
	assert.Equal(t, []string{"once", "reject"}, kiroOptionIDs(permission.GetOptions()), "a request that states its options keeps them")
}

func TestReadControlRequest_KiroReadsItsOwnElicitationMethod(t *testing.T) {
	elicitation := agenttest.ControlPromptOf(kiroProvider{}, []byte(`{"method":"_kiro/mcp/elicitation","params":{"sessionId":"s","toolCallId":"m_ask","elicitation":{"mode":"form","message":"Pick a color","requestedSchema":{"type":"object"}}}}`)).GetElicitation()
	require.NotNil(t, elicitation)
	assert.Equal(t, "form", elicitation.GetMode())
	assert.Equal(t, "Pick a color", elicitation.GetMessage())
	assert.Empty(t, elicitation.GetServer())
	assert.JSONEq(t, `{"type":"object"}`, elicitation.GetSchemaJson())
	assert.Empty(t, elicitation.GetUrl())
	assert.Empty(t, elicitation.GetTitle())
	assert.Empty(t, elicitation.GetDescription())

	elicitation = agenttest.ControlPromptOf(kiroProvider{}, []byte(`{"method":"_kiro/mcp/elicitation","params":{"elicitation":{"mode":"url","message":"Sign in","url":"https://example.com/login","elicitationId":"e"}}}`)).GetElicitation()
	assert.Equal(t, &leapmuxv1.ElicitationPrompt{Mode: "url", Message: "Sign in", Url: "https://example.com/login"}, elicitation)

	for _, payload := range []string{`{"method":"_kiro/mcp/elicitation","params":{}}`, `{"method":"_kiro/mcp/elicitation"}`, `{"method":"_kiro/mcp/elicitation","params":{"elicitation":[]}}`} {
		elicitation := agenttest.ControlPromptOf(kiroProvider{}, []byte(payload)).GetElicitation()
		require.NotNil(t, elicitation, payload)
		assert.Equal(t, contracts.MCPElicitationModeForm, elicitation.GetMode(), payload)
	}

	elicitation = agenttest.ControlPromptOf(kiroProvider{}, []byte(`{"method":"elicitation/create","params":{"message":"Hi","server":"s"}}`)).GetElicitation()
	require.NotNil(t, elicitation, "the protocol's own elicitation still reads as a form")
	assert.Equal(t, "Hi", elicitation.GetMessage())
	assert.Nil(t, agenttest.ControlPromptOf(kiroProvider{}, []byte(`{"method":"session/request_permission"}`)).GetElicitation())
}

func TestReadControlRequest_KiroQuestionStatesAQuestionForEachSubOptionList(t *testing.T) {
	prompt := agenttest.ControlPromptOf(kiroProvider{}, []byte(kiroQuestionPayload))
	require.NotNil(t, prompt.GetQuestion())
	agenttest.RequireProto(t, &leapmuxv1.QuestionPrompt{Questions: []*leapmuxv1.ControlQuestion{
		{Question: "Which DB?", Options: []*leapmuxv1.QuestionOption{
			{Value: kiroValue("Postgres"), Label: "Postgres (recommended)", Description: "pg"},
			{Value: kiroValue("SQLite"), Label: "SQLite"},
		}},
		{Header: "Postgres", Question: "Extras for Postgres: select any to leave out", MultiSelect: true, AllowEmpty: true, Options: []*leapmuxv1.QuestionOption{
			{Value: kiroValue("PostGIS"), Label: "PostGIS", Description: "geo"},
			{Value: kiroValue("pgvector"), Label: "pgvector"},
		}},
	}}, prompt.GetQuestion())
	assert.False(t, prompt.GetQuestion().GetPreservesSelectionNotes(), "Kiro takes a typed answer or a choice, never both")

	assert.Nil(t, agenttest.ControlPromptOf(kiroProvider{}, []byte(`{"method":"_kiro/mcp/elicitation"}`)).GetQuestion(), "only the question method is a question")
}

func TestReadControlRequest_KiroQuestionReadsEachOptionDefensively(t *testing.T) {
	question := func(options string) *leapmuxv1.ControlQuestion {
		questions := agenttest.ControlPromptOf(kiroProvider{}, []byte(`{"method":"_kiro/userInput","params":{"question":"Q","options":`+options+`}}`)).GetQuestion().GetQuestions()
		require.NotEmpty(t, questions, options)
		return questions[0]
	}
	for _, options := range []string{`[]`, `null`, `"Yes"`} {
		agenttest.RequireProto(t, &leapmuxv1.ControlQuestion{Question: "Q"}, question(options))
	}
	agenttest.RequireProto(t, &leapmuxv1.ControlQuestion{Question: "Q", Options: []*leapmuxv1.QuestionOption{
		{Value: kiroValue("Yes"), Label: "Yes"}, {Value: kiroValue("No"), Label: "No"},
	}}, question(`["Yes","No"]`))
	agenttest.RequireProto(t, &leapmuxv1.ControlQuestion{Question: "Q", Options: []*leapmuxv1.QuestionOption{
		{Value: kiroValue("Kept"), Label: "Kept"},
	}}, question(`[null,7,"",{"description":"d"},{"title":7},{"title":"Kept"}]`))
	agenttest.RequireProto(t, &leapmuxv1.ControlQuestion{Question: "Q", Options: []*leapmuxv1.QuestionOption{
		{Value: kiroValue("A"), Label: "A"}, {Value: kiroValue("B"), Label: "B"},
	}}, question(`[{"title":"A","recommended":"yes"},{"title":"B","recommended":1}]`))

	questions := agenttest.ControlPromptOf(kiroProvider{}, []byte(`{"method":"_kiro/userInput","params":{"question":"Q","options":[{"title":"A","subOptions":[{"title":"a1"}]}]}}`)).GetQuestion().GetQuestions()
	require.Len(t, questions, 2)
	assert.Equal(t, "Choices for A: select any to leave out", questions[1].GetQuestion(), "a sub-option list that states no label")

	questions = agenttest.ControlPromptOf(kiroProvider{}, []byte(`{"method":"_kiro/userInput","params":{"question":7}}`)).GetQuestion().GetQuestions()
	agenttest.RequireProto(t, &leapmuxv1.ControlQuestion{}, questions[0])
}

func TestAnswerControlRequest_KiroQuestionStatesTheAnswerAsKiroWritesIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers []*leapmuxv1.QuestionAnswer
		want    string
	}{
		{name: "answers the title of a chosen option", answers: []*leapmuxv1.QuestionAnswer{kiroAnswer("", "SQLite")}, want: "SQLite"},
		{name: "states the sub-options that the reader kept", answers: []*leapmuxv1.QuestionAnswer{kiroAnswer("", "Postgres"), kiroAnswer("", "PostGIS")}, want: "Postgres [pgvector]"},
		{name: "states every sub-option of an unanswered list", answers: []*leapmuxv1.QuestionAnswer{kiroAnswer("", "Postgres")}, want: "Postgres [PostGIS, pgvector]"},
		{name: "states every sub-option of an empty list", answers: []*leapmuxv1.QuestionAnswer{kiroAnswer("", "Postgres"), kiroAnswer("")}, want: "Postgres [PostGIS, pgvector]"},
		{name: "states no sub-option when every one is left out", answers: []*leapmuxv1.QuestionAnswer{kiroAnswer("", "Postgres"), kiroAnswer("", "PostGIS", "pgvector")}, want: "Postgres []"},
		{name: "reads no sub-option list of an option that is not chosen", answers: []*leapmuxv1.QuestionAnswer{kiroAnswer("", "SQLite"), kiroAnswer("", "PostGIS")}, want: "SQLite"},
		{name: "answers the typed text of the main question", answers: []*leapmuxv1.QuestionAnswer{kiroAnswer(" MySQL ")}, want: "MySQL"},
		// A sub-option question is a multiple choice, which takes a selection
		// AND typed text: the text is one more kept entry.
		{name: "keeps two sub-options and the typed text", answers: []*leapmuxv1.QuestionAnswer{kiroAnswer("", "Postgres"), kiroAnswer(" TimescaleDB ")}, want: "Postgres [PostGIS, pgvector, TimescaleDB]"},
		{name: "keeps the typed text beside a left-out sub-option", answers: []*leapmuxv1.QuestionAnswer{kiroAnswer("", "Postgres"), kiroAnswer("TimescaleDB", "PostGIS")}, want: "Postgres [pgvector, TimescaleDB]"},
		{name: "keeps the typed text when every sub-option is left out", answers: []*leapmuxv1.QuestionAnswer{kiroAnswer("", "Postgres"), kiroAnswer("TimescaleDB", "PostGIS", "pgvector")}, want: "Postgres [TimescaleDB]"},
		// A client saves the text to the question that shows, which can belong to
		// an option that the reader did not choose; Kiro takes one answer.
		{name: "lets typed text on another option's question win over the chosen option", answers: []*leapmuxv1.QuestionAnswer{kiroAnswer("", "SQLite"), kiroAnswer("Use MySQL instead")}, want: "Use MySQL instead"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := agenttest.AnswerControl(kiroProvider{}, []byte(kiroQuestionPayload), "jsonrpc:4", agenttest.QuestionDecision(tc.answers...))
			require.NoError(t, err)
			want, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "jsonrpc:4", "result": map[string]any{"action": "answered", "answer": tc.want}})
			require.NoError(t, err)
			assert.JSONEq(t, string(want), string(got.Content))
			assert.Empty(t, got.FollowUp)
		})
	}
}

// Kiro's `dismissed` carries no reason. Kiro's native tool reports it to the
// model as the user's readiness to move on. The browser sends an answer only
// when the main question has one, so the Worker refuses an answer with nothing
// chosen or typed.
func TestAnswerControlRequest_KiroQuestionDismissesAQuestionWithNoAnswer(t *testing.T) {
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"a cancellation":            agenttest.CancelQuestions(""),
		"a cancellation and reason": agenttest.CancelQuestions("User stopped"),
	} {
		got, err := agenttest.AnswerControl(kiroProvider{}, []byte(kiroQuestionPayload), "jsonrpc:4", decision)
		require.NoError(t, err, name)
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:4","result":{"action":"dismissed"}}`, string(got.Content), name)
		assert.Empty(t, got.FollowUp, name)
	}
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"nothing chosen or typed": agenttest.QuestionDecision(),
		"only whitespace typed":   agenttest.QuestionDecision(kiroAnswer("  ")),
		"typed on a later page":   agenttest.QuestionDecision(kiroAnswer("  "), kiroAnswer("Later page")),
	} {
		_, err := agenttest.AnswerControl(kiroProvider{}, []byte(kiroQuestionPayload), "jsonrpc:4", decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}

	// A prompt that asks nothing takes a cancel alone, which dismisses it.
	input := agent.NewControlRequestInput([]byte(kiroQuestionPayload))
	nothing := func(decision *leapmuxv1.ControlDecision) (agent.ControlAnswer, error) {
		return kiroProvider{}.AnswerControlRequest(agent.ControlAnswerInput{
			Request: input, RequestID: "jsonrpc:4",
			Prompt:   &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{}}},
			Decision: decision,
		})
	}
	got, err := nothing(agenttest.CancelQuestions(""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:4","result":{"action":"dismissed"}}`, string(got.Content))
	_, err = nothing(agenttest.QuestionDecision())
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}

func TestAnswerControlRequest_KiroQuestionRefusesAnAnswerThatThePromptDoesNotOffer(t *testing.T) {
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		// The browser ignored a left-out value that the list does not hold; the
		// neutral decision refuses it, as it refuses every unknown value.
		"a left-out value that the list does not hold": agenttest.QuestionDecision(kiroAnswer("", "Postgres"), kiroAnswer("", "Not a sub-option")),
		"two options for the one question":             agenttest.QuestionDecision(kiroAnswer("", "Postgres", "SQLite")),
		// Kiro takes one answer, so the main question cannot be an option and
		// typed text at once.
		"an option and typed text for the one question": agenttest.QuestionDecision(kiroAnswer("MySQL", "SQLite")),
		"the label rather than the value":               agenttest.QuestionDecision(kiroAnswer("", "Postgres (recommended)")),
		"an answer for a question it does not ask":      agenttest.QuestionDecision(kiroAnswer(""), kiroAnswer(""), kiroAnswer("Unused")),
		"a decision of another kind":                    agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""),
	} {
		_, err := agenttest.AnswerControl(kiroProvider{}, []byte(kiroQuestionPayload), "jsonrpc:4", decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

// Kiro reads the reason of a refusal from its own field, so the reason never
// follows as a message of its own.
func TestAnswerControlRequest_KiroPermissionCarriesTheReasonInKirosOwnField(t *testing.T) {
	payload := `{"method":"session/request_permission","params":{"toolCall":{"toolCallId":"c"},"options":[{"optionId":"accept","kind":"allow_once"},{"optionId":"reject","kind":"reject_once"}]}}`
	got, err := agenttest.AnswerControl(kiroProvider{}, []byte(payload), "jsonrpc:1", agenttest.PermissionDecision("reject", "Use rg"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:1","result":{"outcome":{"outcome":"selected","optionId":"reject"},"_meta":{"kiro":{"rejectionReason":"Use rg"}}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp)

	got, err = agenttest.AnswerControl(kiroProvider{}, []byte(payload), "jsonrpc:1", agenttest.PermissionDecision("reject", ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:1","result":{"outcome":{"outcome":"selected","optionId":"reject"}}}`, string(got.Content), "a bare refusal states no reason")

	got, err = agenttest.AnswerControl(kiroProvider{}, []byte(payload), "jsonrpc:1", agenttest.PermissionDecision("accept", ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:1","result":{"outcome":{"outcome":"selected","optionId":"accept"}}}`, string(got.Content))

	_, err = agenttest.AnswerControl(kiroProvider{}, []byte(payload), "jsonrpc:1", agenttest.PermissionDecision("accept", "a note"))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "an approval carries no reason")
}

// A request with no options takes LeapMux's cancel, which passes Kiro's
// resolution as the protocol's cancelled outcome under Kiro's own id, with the
// reason in Kiro's own field.
func TestAnswerControlRequest_KiroPermissionWithNoOptionsCancels(t *testing.T) {
	payload := `{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{"toolCall":{"toolCallId":"c","title":"ls"}}}`
	got, err := agenttest.AnswerControl(kiroProvider{}, []byte(payload), "jsonrpc:1", agenttest.PermissionDecision(acp.CancelOptionID, "Use rg"))
	require.NoError(t, err)
	resolved := agenttest.ResolveAnswer(kiroProvider{}, []byte(payload), "jsonrpc:1", got)
	require.NoError(t, resolved.Refusal())
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":1,"result":{"outcome":{"outcome":"cancelled"},"_meta":{"kiro":{"rejectionReason":"Use rg"}}}}`, string(resolved.Content))
	assert.Empty(t, got.FollowUp, "the reason rides in the reply, so it is not sent twice")

	_, err = agenttest.AnswerControl(kiroProvider{}, []byte(payload), "jsonrpc:1", agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "Kiro cannot read LeapMux's allow")
}

// A wider scope travels as LeapMux's own option, and Kiro's resolution turns it
// back into Kiro's always option with the consent scope, beside the reason.
func TestAnswerControlRequest_KiroScopedOptionResolvesToKirosOwnOption(t *testing.T) {
	for _, tc := range []struct {
		optionID, feedback, want string
	}{
		{optionID: "always-accept-workspace", want: `{"outcome":{"outcome":"selected","optionId":"always-accept"},"_meta":{"kiro":{"consent":{"scope":"workspace"}}}}`},
		{optionID: "always-accept-user", want: `{"outcome":{"outcome":"selected","optionId":"always-accept"},"_meta":{"kiro":{"consent":{"scope":"user"}}}}`},
		{optionID: "always-reject-workspace", feedback: "Use rg", want: `{"outcome":{"outcome":"selected","optionId":"always-reject"},"_meta":{"kiro":{"rejectionReason":"Use rg","consent":{"scope":"workspace"}}}}`},
		{optionID: "always-reject-user", want: `{"outcome":{"outcome":"selected","optionId":"always-reject"},"_meta":{"kiro":{"consent":{"scope":"user"}}}}`},
		{optionID: "always-reject", feedback: "Use rg", want: `{"outcome":{"outcome":"selected","optionId":"always-reject"},"_meta":{"kiro":{"rejectionReason":"Use rg"}}}`},
	} {
		got, err := agenttest.AnswerControl(kiroProvider{}, []byte(kiroShellPayload), kiroStoredRequestID, agenttest.PermissionDecision(tc.optionID, tc.feedback))
		require.NoError(t, err, tc.optionID)
		resolved := resolveKiro(t, json.RawMessage(kiroShellPayload), got.Content)
		require.False(t, resolved.Withhold, tc.optionID)
		var reply struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		require.NoError(t, json.Unmarshal(resolved.Content, &reply), tc.optionID)
		assert.Equal(t, "5", string(reply.ID), tc.optionID)
		assert.JSONEq(t, tc.want, string(reply.Result), tc.optionID)
	}
}

// A request with no workspace root offers no workspace scope, so a decision
// for it is refused before it reaches Kiro.
func TestAnswerControlRequest_KiroRefusesAScopeThatTheRequestDoesNotOffer(t *testing.T) {
	payload := kiroPermissionWith(kiroAlwaysOptions, `{}`)
	_, err := agenttest.AnswerControl(kiroProvider{}, []byte(payload), kiroStoredRequestID, agenttest.PermissionDecision("always-accept-workspace", ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}

func TestAnswerControlRequest_KiroElicitationTakesTheNeutralEnvelope(t *testing.T) {
	payload := `{"jsonrpc":"2.0","id":5,"method":"_kiro/mcp/elicitation","params":{"elicitation":{"mode":"form","message":"Choose","requestedSchema":{"type":"object"}}}}`
	got, err := agenttest.AnswerControl(kiroProvider{}, []byte(payload), kiroStoredRequestID, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, `{"size":"Large"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:5","response":{"action":"accept","content":{"size":"Large"}}}}`, string(got.Content))
	resolved := resolveKiro(t, json.RawMessage(payload), got.Content)
	require.False(t, resolved.Withhold)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":5,"result":{"action":"accept","content":{"size":"Large"}}}`, string(resolved.Content))
}

func TestKiroRejectReason(t *testing.T) {
	encode := func(value map[string]any) string {
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		return string(encoded)
	}
	result := map[string]any{
		"outcome": map[string]any{"outcome": "selected", "optionId": "reject"},
		"_meta":   map[string]any{"other": 1, "kiro": map[string]any{"editedCommand": "ls"}},
	}
	assert.JSONEq(t, `{"outcome":{"outcome":"selected","optionId":"reject"},"_meta":{"other":1,"kiro":{"editedCommand":"ls","rejectionReason":"Use rg"}}}`,
		encode(kiroRejectReason(result, "Use rg")), "adds the reason under Kiro's metadata and keeps the rest")
	assert.JSONEq(t, `{"outcome":{"outcome":"selected","optionId":"reject"},"_meta":{"other":1,"kiro":{"editedCommand":"ls"}}}`, encode(result), "leaves the result it received unchanged")

	assert.JSONEq(t, `{"outcome":{"outcome":"cancelled"},"_meta":{"kiro":{"rejectionReason":"why"}}}`,
		encode(kiroRejectReason(map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, "why")), "adds a meta object to a result that has none")
	assert.JSONEq(t, `{"_meta":{"kiro":{"rejectionReason":"why"}}}`, encode(kiroRejectReason(map[string]any{"_meta": "x"}, "why")), "replaces a meta value that is no object")
	assert.JSONEq(t, `{"_meta":{"other":1,"kiro":{"rejectionReason":"why"}}}`, encode(kiroRejectReason(map[string]any{"_meta": map[string]any{"other": 1, "kiro": "x"}}, "why")), "replaces a kiro value that is no object")
}

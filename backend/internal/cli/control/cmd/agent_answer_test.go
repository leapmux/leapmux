package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
)

// requireDecision asserts that got is want, and prints both and why on a
// mismatch.
func requireDecision(t *testing.T, want, got *leapmuxv1.ControlDecision, why ...string) {
	t.Helper()
	require.True(t, proto.Equal(want, got), "%s\nwant %s\ngot  %s", strings.Join(why, " "), protojson.Format(want), protojson.Format(got))
}

// requireUsage asserts that err is a usage error whose message holds text.
func requireUsage(t *testing.T, err error, text string) {
	t.Helper()
	require.Error(t, err)
	e := cli.FromError(err)
	assert.Equal(t, cli.CodeUsage, e.Code, e.Message)
	assert.Contains(t, e.Message, text)
}

func TestApproveDecision(t *testing.T) {
	got, err := approveDecision(permissionPrompt(), planSettings{})
	require.NoError(t, err)
	requireDecision(t, decisionOf(&leapmuxv1.PermissionDecision{OptionId: "once"}), got)

	onlyAlways := permissionPrompt()
	onlyAlways.GetPermission().Options = onlyAlways.GetPermission().GetOptions()[1:]
	_, err = approveDecision(onlyAlways, planSettings{})
	requireUsage(t, err, "no one-time approval")

	got, err = approveDecision(planPrompt(), planSettings{permissionMode: "acceptEdits", clearContext: true, set: true})
	require.NoError(t, err)
	requireDecision(t, decisionOf(&leapmuxv1.PlanDecision{Approve: true, PermissionMode: "acceptEdits", ClearContext: true}), got)

	_, err = approveDecision(permissionPrompt(), planSettings{clearContext: true, set: true})
	requireUsage(t, err, "apply to a plan")

	for _, variant := range []leapmuxv1.DialogVariant{leapmuxv1.DialogVariant_DIALOG_VARIANT_CONFIRM, leapmuxv1.DialogVariant_DIALOG_VARIANT_UNSPECIFIED} {
		got, err = approveDecision(dialogPrompt(variant), planSettings{})
		require.NoError(t, err)
		requireDecision(t, decisionOf(&leapmuxv1.DialogDecision{Action: leapmuxv1.DialogAction_DIALOG_ACTION_CONFIRM}), got)
	}
	_, err = approveDecision(dialogPrompt(leapmuxv1.DialogVariant_DIALOG_VARIANT_INPUT), planSettings{})
	requireUsage(t, err, "asks for text")

	_, err = approveDecision(questionPrompt(databaseQuestion()), planSettings{})
	requireUsage(t, err, "takes an answer")

	_, err = approveDecision(elicitationPrompt(repositorySchema), planSettings{})
	requireUsage(t, err, "requires name")
	got, err = approveDecision(elicitationPrompt(`{"properties":{"private":{"type":"boolean","default":false}}}`), planSettings{})
	require.NoError(t, err)
	requireDecision(t, decisionOf(&leapmuxv1.ElicitationDecision{Action: leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, ContentJson: `{"private":false}`}), got)

	_, err = approveDecision(&leapmuxv1.ControlPrompt{}, planSettings{})
	requireUsage(t, err, "takes no approval")
}

func TestDenyDecision(t *testing.T) {
	got, err := denyDecision(permissionPrompt(), "Run the unit tests only.")
	require.NoError(t, err)
	requireDecision(t, decisionOf(&leapmuxv1.PermissionDecision{OptionId: "deny", Feedback: "Run the unit tests only."}), got)

	noRefusal := permissionPrompt()
	noRefusal.GetPermission().Options = noRefusal.GetPermission().GetOptions()[:2]
	_, err = denyDecision(noRefusal, "")
	requireUsage(t, err, "no one-time refusal")

	got, err = denyDecision(planPrompt(), "Backfill in batches.")
	require.NoError(t, err)
	requireDecision(t, decisionOf(&leapmuxv1.PlanDecision{Feedback: "Backfill in batches."}), got)

	got, err = denyDecision(questionPrompt(databaseQuestion()), "Not now.")
	require.NoError(t, err)
	requireDecision(t, decisionOf(&leapmuxv1.QuestionDecision{Cancelled: true, Feedback: "Not now."}), got)

	got, err = denyDecision(elicitationPrompt(repositorySchema), "")
	require.NoError(t, err)
	requireDecision(t, decisionOf(&leapmuxv1.ElicitationDecision{Action: leapmuxv1.ElicitationAction_ELICITATION_ACTION_DECLINE}), got)
	_, err = denyDecision(elicitationPrompt(repositorySchema), "no")
	requireUsage(t, err, "takes no reason")

	got, err = denyDecision(dialogPrompt(leapmuxv1.DialogVariant_DIALOG_VARIANT_CONFIRM), "")
	require.NoError(t, err)
	requireDecision(t, decisionOf(&leapmuxv1.DialogDecision{Action: leapmuxv1.DialogAction_DIALOG_ACTION_REFUSE}), got)
	got, err = denyDecision(dialogPrompt(leapmuxv1.DialogVariant_DIALOG_VARIANT_EDITOR), "")
	require.NoError(t, err)
	requireDecision(t, decisionOf(&leapmuxv1.DialogDecision{Action: leapmuxv1.DialogAction_DIALOG_ACTION_CANCEL}), got)

	_, err = denyDecision(&leapmuxv1.ControlPrompt{}, "")
	requireUsage(t, err, "takes no refusal")
}

func TestAnswerDecision_Permission(t *testing.T) {
	got, err := answerDecision(permissionPrompt(), answerSpec{options: []string{"2"}})
	require.NoError(t, err)
	requireDecision(t, decisionOf(&leapmuxv1.PermissionDecision{OptionId: "session"}), got)

	got, err = answerDecision(permissionPrompt(), answerSpec{options: []string{" 3 "}, message: "Not that file."})
	require.NoError(t, err)
	requireDecision(t, decisionOf(&leapmuxv1.PermissionDecision{OptionId: "deny", Feedback: "Not that file."}), got)

	for name, tc := range map[string]struct {
		spec answerSpec
		want string
	}{
		"no option":      {answerSpec{message: "x"}, "give one option"},
		"two options":    {answerSpec{options: []string{"1", "2"}}, "give one option"},
		"zero":           {answerSpec{options: []string{"0"}}, "from 1 to 4"},
		"past the last":  {answerSpec{options: []string{"5"}}, "from 1 to 4"},
		"negative":       {answerSpec{options: []string{"-1"}}, "from 1 to 4"},
		"not a number":   {answerSpec{options: []string{"once"}}, "from 1 to 4"},
		"a text":         {answerSpec{texts: []string{"yes"}}, "takes no text"},
		"a field":        {answerSpec{fields: []string{"a=b"}}, "--field fills an MCP form"},
		"plan settings":  {answerSpec{options: []string{"1"}, settings: planSettings{set: true}}, "apply to a plan"},
		"a huge number":  {answerSpec{options: []string{"99999999999999999999"}}, "from 1 to 4"},
		"an empty value": {answerSpec{options: []string{""}}, "from 1 to 4"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := answerDecision(permissionPrompt(), tc.spec)
			requireUsage(t, err, tc.want)
		})
	}
}

func TestAnswerDecision_Plan(t *testing.T) {
	got, err := answerDecision(planPrompt(), answerSpec{options: []string{"1"}, settings: planSettings{permissionMode: "acceptEdits", set: true}})
	require.NoError(t, err)
	requireDecision(t, decisionOf(&leapmuxv1.PlanDecision{Approve: true, ChoiceId: "auto", PermissionMode: "acceptEdits"}), got)

	got, err = answerDecision(planPrompt(), answerSpec{options: []string{"2"}, message: "Add a rollback step."})
	require.NoError(t, err)
	requireDecision(t, decisionOf(&leapmuxv1.PlanDecision{ChoiceId: "keep", Feedback: "Add a rollback step."}), got,
		"a refusing choice refuses, and the Worker checks the settings")

	_, err = answerDecision(planPrompt(), answerSpec{message: "x"})
	requireUsage(t, err, "--option <n>")
	noChoices := planPrompt()
	noChoices.GetPlan().Choices = nil
	_, err = answerDecision(noChoices, answerSpec{options: []string{"1"}})
	requireUsage(t, err, "offers no choice")
}

func TestAnswerDecision_Questions(t *testing.T) {
	one := questionPrompt(extrasQuestion())
	got, err := answerDecision(one, answerSpec{options: []string{"1", "2"}, texts: []string{"Metrics"}})
	require.NoError(t, err)
	requireDecision(t, decisionOf(&leapmuxv1.QuestionDecision{Answers: []*leapmuxv1.QuestionAnswer{{Selected: []string{"Cache", "logs"}, CustomText: "Metrics"}}}), got,
		"a multiple choice sends the options and the text together, each option by its value")

	got, err = answerDecision(one, answerSpec{options: []string{"1:3"}, texts: []string{"2:Metrics"}})
	require.NoError(t, err)
	requireDecision(t, decisionOf(&leapmuxv1.QuestionDecision{Answers: []*leapmuxv1.QuestionAnswer{{Selected: []string{"Traces"}, CustomText: "2:Metrics"}}}), got,
		"with one question an option may name it, and a text is all text")

	optional := extrasQuestion()
	optional.AllowEmpty = true
	got, err = answerDecision(questionPrompt(optional, optional), answerSpec{})
	require.NoError(t, err)
	requireDecision(t, decisionOf(&leapmuxv1.QuestionDecision{Answers: []*leapmuxv1.QuestionAnswer{{}, {}}}), got,
		"questions that each allow no answer take an empty answer for each")

	defaulted := extrasQuestion()
	defaulted.InitialSelections = &leapmuxv1.QuestionSelections{Values: []string{"logs"}}
	got, err = answerDecision(questionPrompt(databaseQuestion(), defaulted), answerSpec{options: []string{"1:2"}})
	require.NoError(t, err)
	requireDecision(t, decisionOf(&leapmuxv1.QuestionDecision{Answers: []*leapmuxv1.QuestionAnswer{{Selected: []string{"SQLite"}}, {Selected: []string{"logs"}}}}), got,
		"a question that the command line leaves unanswered keeps the agent's selection")
	got, err = answerDecision(questionPrompt(defaulted), answerSpec{options: []string{"3"}})
	require.NoError(t, err)
	requireDecision(t, decisionOf(&leapmuxv1.QuestionDecision{Answers: []*leapmuxv1.QuestionAnswer{{Selected: []string{"Traces"}}}}), got,
		"an option replaces the agent's selection")
	got, err = answerDecision(questionPrompt(defaulted), answerSpec{texts: []string{"Metrics"}})
	require.NoError(t, err)
	requireDecision(t, decisionOf(&leapmuxv1.QuestionDecision{Answers: []*leapmuxv1.QuestionAnswer{{Selected: []string{"logs"}, CustomText: "Metrics"}}}), got,
		"a text joins the agent's selection, as in the browser")
	got, err = answerDecision(questionPrompt(defaulted), answerSpec{})
	require.NoError(t, err)
	requireDecision(t, decisionOf(&leapmuxv1.QuestionDecision{Answers: []*leapmuxv1.QuestionAnswer{{Selected: []string{"logs"}}}}), got,
		"no value at all sends the agent's selection")
	emptyDefault := extrasQuestion()
	emptyDefault.AllowEmpty = true
	emptyDefault.InitialSelections = &leapmuxv1.QuestionSelections{}
	got, err = answerDecision(questionPrompt(emptyDefault), answerSpec{})
	require.NoError(t, err)
	requireDecision(t, decisionOf(&leapmuxv1.QuestionDecision{Answers: []*leapmuxv1.QuestionAnswer{{}}}), got)
	_, err = answerDecision(questionPrompt(defaulted, databaseQuestion()), answerSpec{})
	requireUsage(t, err, "give an answer")

	two := questionPrompt(databaseQuestion(), extrasQuestion())
	got, err = answerDecision(two, answerSpec{options: []string{"1:2", "2:1", "2:3"}, texts: []string{"2: Metrics: all of them"}})
	require.NoError(t, err)
	requireDecision(t, decisionOf(&leapmuxv1.QuestionDecision{Answers: []*leapmuxv1.QuestionAnswer{
		{Selected: []string{"SQLite"}},
		{Selected: []string{"Cache", "Traces"}, CustomText: " Metrics: all of them"},
	}}), got, "the text after the question number keeps its own colons")

	for name, tc := range map[string]struct {
		prompt *leapmuxv1.ControlPrompt
		spec   answerSpec
		want   string
	}{
		"an option without its question":   {two, answerSpec{options: []string{"1"}}, "must say which one"},
		"a text without its question":      {two, answerSpec{texts: []string{"SQLite"}}, "must say which one"},
		"a question past the last":         {two, answerSpec{options: []string{"3:1"}}, "questions 1 to 2"},
		"a question zero":                  {two, answerSpec{texts: []string{"0:x"}}, "questions 1 to 2"},
		"an option past the last":          {two, answerSpec{options: []string{"1:3"}}, "question 1 option number from 1 to 2"},
		"the same option twice":            {one, answerSpec{options: []string{"2", "1:2"}}, "twice"},
		"two texts for one question":       {two, answerSpec{texts: []string{"1:a", "1:b"}}, "two texts"},
		"two texts for the only question":  {one, answerSpec{texts: []string{"a", "b"}}, "two texts"},
		"an empty text":                    {one, answerSpec{texts: []string{"  "}}, "is empty"},
		"a reason":                         {one, answerSpec{options: []string{"1"}, message: "x"}, "--message"},
		"no question":                      {questionPrompt(), answerSpec{options: []string{"1"}}, "asks no question"},
		"no answer":                        {one, answerSpec{}, "give an answer"},
		"no answer to a required question": {questionPrompt(optional, databaseQuestion()), answerSpec{}, "give an answer"},
		"no answer and no question":        {questionPrompt(), answerSpec{}, "asks no question"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := answerDecision(tc.prompt, tc.spec)
			requireUsage(t, err, tc.want)
		})
	}
}

func TestAnswerDecision_Elicitation(t *testing.T) {
	got, err := answerDecision(elicitationPrompt(repositorySchema), answerSpec{
		fields:  []string{"name=leapmux", "private=false", "stars=42", "license=mit", "topics=go", "topics=cli"},
		options: []string{"2"},
	})
	require.NoError(t, err)
	decision := got.GetElicitation()
	require.NotNil(t, decision)
	assert.Equal(t, leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, decision.GetAction())
	assert.Equal(t, "session", decision.GetAcceptChoiceKey())
	assert.JSONEq(t, `{"name":"leapmux","private":false,"stars":42,"license":"mit","topics":["go","cli"]}`, decision.GetContentJson())

	got, err = answerDecision(elicitationPrompt(repositorySchema), answerSpec{fields: []string{"name=a=b"}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"a=b","private":true}`, got.GetElicitation().GetContentJson(), "a default fills an unset field, and a value keeps its own =")

	url := &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: &leapmuxv1.ElicitationPrompt{Mode: "url", Url: "https://example.com/auth"}}}
	got, err = approveDecision(url, planSettings{})
	require.NoError(t, err)
	requireDecision(t, decisionOf(&leapmuxv1.ElicitationDecision{Action: leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT}), got, "a URL mode accept carries no content")
	_, err = answerDecision(url, answerSpec{fields: []string{"a=b"}})
	requireUsage(t, err, "has no form")

	for name, tc := range map[string]struct {
		spec answerSpec
		want string
	}{
		"no equals sign":          {answerSpec{fields: []string{"name"}}, "<name>=<value>"},
		"no name":                 {answerSpec{fields: []string{"=x"}}, "<name>=<value>"},
		"an unknown field":        {answerSpec{fields: []string{"name=a", "color=red"}}, `no field "color"`},
		"a field twice":           {answerSpec{fields: []string{"name=a", "name=b"}}, "twice"},
		"a word for a boolean":    {answerSpec{fields: []string{"name=a", "private=maybe"}}, "true or false"},
		"a fraction for integer":  {answerSpec{fields: []string{"name=a", "stars=1.5"}}, "an integer"},
		"a value outside an enum": {answerSpec{fields: []string{"name=a", "license=gpl"}}, "takes one of mit, apache-2.0"},
		"a required field unset":  {answerSpec{fields: []string{"stars=1"}}, "requires name"},
		"a text":                  {answerSpec{texts: []string{"x"}}, "takes no text"},
		"an accept choice past":   {answerSpec{fields: []string{"name=a"}, options: []string{"3"}}, "accept choice number from 1 to 2"},
		"a reason":                {answerSpec{fields: []string{"name=a"}, message: "x"}, "--message"},
		"no field and no choice":  {answerSpec{}, "fill the form with --field"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := answerDecision(elicitationPrompt(repositorySchema), tc.spec)
			requireUsage(t, err, tc.want)
		})
	}

	t.Run("field rules", func(t *testing.T) {
		ruled := func(rules ...*leapmuxv1.ElicitationFieldRule) *leapmuxv1.ControlPrompt {
			prompt := elicitationPrompt(`{"properties":{` +
				`"choice":{"type":"string"},` +
				`"tags":{"type":"array","items":{"type":"string"}},` +
				`"details":{"type":"string"},` +
				`"count":{"type":"integer","default":0},` +
				`"replicas":{"type":"integer"},` +
				`"zone":{"type":"string"}` +
				`},"required":["details","zone"]}`)
			prompt.GetElicitation().FieldRules = rules
			return prompt
		}
		equal := leapmuxv1.ElicitationFieldComparison_ELICITATION_FIELD_COMPARISON_EQUAL
		notEqual := leapmuxv1.ElicitationFieldComparison_ELICITATION_FIELD_COMPARISON_NOT_EQUAL
		when := func(field string, comparison leapmuxv1.ElicitationFieldComparison, value string) []*leapmuxv1.ElicitationFieldCondition {
			return []*leapmuxv1.ElicitationFieldCondition{{Field: field, Comparison: comparison, ValueJson: value}}
		}
		rules := []*leapmuxv1.ElicitationFieldRule{
			{Key: "details", Conditions: when("choice", equal, `"yes"`)},
			{Key: "count", Hidden: true},
			{Key: "zone", Conditions: when("replicas", notEqual, `1`)},
		}
		content := func(t *testing.T, fields ...string) map[string]any {
			t.Helper()
			got, err := answerDecision(ruled(rules...), answerSpec{fields: fields})
			require.NoError(t, err)
			var decoded map[string]any
			require.NoError(t, json.Unmarshal([]byte(got.GetElicitation().GetContentJson()), &decoded))
			return decoded
		}

		assert.Equal(t, map[string]any{"choice": "no", "count": 0.0, "replicas": 1.0}, content(t, "choice=no", "replicas=1"),
			"an inactive field is neither sent nor required, and a hidden field sends its default")
		assert.Equal(t, map[string]any{"choice": "yes", "details": "d", "count": 0.0, "replicas": 1.0}, content(t, "choice=yes", "details=d", "replicas=1"),
			"a field whose condition holds is asked")
		_, err := answerDecision(ruled(rules...), answerSpec{fields: []string{"choice=yes", "replicas=1"}})
		requireUsage(t, err, "requires details")
		_, err = answerDecision(ruled(rules...), answerSpec{fields: []string{"choice=no", "details=stale", "replicas=1"}})
		requireUsage(t, err, `asks field "details" only when choice = "yes"`)
		_, err = answerDecision(ruled(rules...), answerSpec{fields: []string{"count=3"}})
		requireUsage(t, err, `keeps field "count" hidden`)
		_, err = answerDecision(ruled(rules...), answerSpec{fields: []string{"choice=no", "replicas=2"}})
		requireUsage(t, err, "requires zone")
		assert.Equal(t, map[string]any{"choice": "no", "count": 0.0, "replicas": 1.0}, content(t, "choice=no", "replicas=1"),
			"an integer answer matches the number of a condition")

		listRules := []*leapmuxv1.ElicitationFieldRule{{Key: "details", Conditions: when("tags", equal, `"b"`)}}
		got, err := answerDecision(ruled(listRules...), answerSpec{fields: []string{"tags=a", "tags=b", "details=d", "zone=z"}})
		require.NoError(t, err)
		assert.JSONEq(t, `{"tags":["a","b"],"details":"d","count":0,"zone":"z"}`, got.GetElicitation().GetContentJson(), "a list answer meets equal when it holds the value")
		_, err = answerDecision(ruled(listRules...), answerSpec{fields: []string{"tags=a", "zone=z"}})
		require.NoError(t, err, "a list answer without the value leaves the field inactive")

		noValue := []*leapmuxv1.ElicitationFieldRule{{Key: "details", Conditions: when("choice", notEqual, `"yes"`)}}
		got, err = answerDecision(ruled(noValue...), answerSpec{fields: []string{"zone=z"}})
		require.NoError(t, err)
		assert.JSONEq(t, `{"count":0,"zone":"z"}`, got.GetElicitation().GetContentJson(), "a condition on a field with no value holds for no comparison")

		got, err = approveDecision(ruled(rules...), planSettings{})
		require.NoError(t, err)
		assert.JSONEq(t, `{"count":0}`, got.GetElicitation().GetContentJson(),
			"an approval sends the active defaults, and requires no inactive field")
		_, err = approveDecision(ruled(&leapmuxv1.ElicitationFieldRule{Key: "count", Hidden: true}), planSettings{})
		requireUsage(t, err, "requires details, zone")
	})

	numbers := elicitationPrompt(`{"properties":{"ratio":{"type":"number"}}}`)
	got, err = answerDecision(numbers, answerSpec{fields: []string{"ratio=1e3"}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"ratio":1e3}`, got.GetElicitation().GetContentJson())
	_, err = answerDecision(numbers, answerSpec{fields: []string{"ratio=many"}})
	requireUsage(t, err, "takes a number")
}

func TestAnswerDecision_Dialog(t *testing.T) {
	got, err := answerDecision(dialogPrompt(leapmuxv1.DialogVariant_DIALOG_VARIANT_INPUT), answerSpec{texts: []string{"feature/cli"}})
	require.NoError(t, err)
	requireDecision(t, decisionOf(&leapmuxv1.DialogDecision{Action: leapmuxv1.DialogAction_DIALOG_ACTION_SUBMIT, Value: "feature/cli"}), got)

	_, err = answerDecision(dialogPrompt(leapmuxv1.DialogVariant_DIALOG_VARIANT_CONFIRM), answerSpec{texts: []string{"yes"}})
	requireUsage(t, err, "takes no text")
	_, err = answerDecision(dialogPrompt(leapmuxv1.DialogVariant_DIALOG_VARIANT_EDITOR), answerSpec{texts: []string{"a", "b"}})
	requireUsage(t, err, "one argument")
	_, err = answerDecision(dialogPrompt(leapmuxv1.DialogVariant_DIALOG_VARIANT_EDITOR), answerSpec{options: []string{"1"}})
	requireUsage(t, err, "no options")
}

func TestFindControlRequest(t *testing.T) {
	requests := []*leapmuxv1.AgentControlRequest{
		pendingRequest("abc123", permissionPrompt()),
		pendingRequest("abd456", planPrompt()),
		pendingRequest("abc", questionPrompt(databaseQuestion())),
	}
	got, err := findControlRequest("ag-1", requests, "abc")
	require.NoError(t, err)
	assert.Equal(t, "abc", got.GetClaimToken(), "an exact handle wins over the prefix of a longer one")
	got, err = findControlRequest("ag-1", requests, " abd ")
	require.NoError(t, err)
	assert.Equal(t, "abd456", got.GetClaimToken())

	_, err = findControlRequest("ag-1", requests, "ab")
	e := cli.FromError(err)
	assert.Equal(t, cli.CodeAmbiguousTarget, e.Code)
	assert.Len(t, e.Candidates, 3)

	_, err = findControlRequest("ag-1", requests, "zz")
	e = cli.FromError(err)
	assert.Equal(t, cli.CodeNotFound, e.Code)
	assert.Contains(t, e.Hints[0], "agent requests ag-1")

	_, err = findControlRequest("ag-1", requests, " ")
	requireUsage(t, err, "empty")
	_, err = findControlRequest("ag-1", nil, "abc")
	assert.Equal(t, cli.CodeNotFound, cli.FromError(err).Code)
}

func TestRequireAnswerable(t *testing.T) {
	require.NoError(t, requireAnswerable("ag-1", pendingRequest("c1", permissionPrompt())))

	answered := pendingRequest("c1", permissionPrompt())
	answered.ResponseState = leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_DELIVERED
	e := cli.FromError(requireAnswerable("ag-1", answered))
	assert.Equal(t, cli.CodeConflict, e.Code)
	assert.Contains(t, e.Message, "already has an answer (delivered)")

	unreadable := pendingRequest("c1", &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Unreadable{Unreadable: leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_MALFORMED}})
	e = cli.FromError(requireAnswerable("ag-1", unreadable))
	assert.Equal(t, cli.CodeConflict, e.Code)
	assert.Contains(t, e.Hints[0], "agent interrupt ag-1")

	unrecognized := pendingRequest("c1", &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Unreadable{Unreadable: leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_UNRECOGNIZED}})
	e = cli.FromError(requireAnswerable("ag-1", unrecognized))
	assert.Equal(t, cli.CodeConflict, e.Code)
	assert.Equal(t, "LeapMux cannot read request c1 (unrecognized), so it takes no answer", e.Message)

	e = cli.FromError(requireAnswerable("ag-1", pendingRequest("c1", &leapmuxv1.ControlPrompt{})))
	assert.Equal(t, cli.CodeVersionMismatch, e.Code)
}

func TestControlAnswerResultOf(t *testing.T) {
	result, err := controlAnswerResultOf("ag-1", "c1", &leapmuxv1.AnswerControlResponse{State: leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED})
	require.NoError(t, err)
	assert.Equal(t, controlAnswerResult{ID: "c1", AgentID: "ag-1", State: "completed"}, result)
	for _, state := range []leapmuxv1.ControlResponseState{leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_DELIVERED, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_PENDING} {
		_, err = controlAnswerResultOf("ag-1", "c1", &leapmuxv1.AnswerControlResponse{State: state})
		assert.NoError(t, err, "the Worker holds the answer and delivers it: %v", state)
	}

	// A canceled instance is a request that the agent replaced or withdrew, or
	// that another client answered. The Worker states a replacement with no
	// error text, and a withdrawal with its reason. Neither left an answer
	// with the Worker, and a replacement waits under a new handle.
	for _, errText := range []string{"", "the control request is no longer pending"} {
		_, err = controlAnswerResultOf("ag-1", "c1", &leapmuxv1.AnswerControlResponse{State: leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_CANCELED, Error: errText})
		e := cli.FromError(err)
		assert.Equal(t, cli.CodeConflict, e.Code, "error text %q", errText)
		assert.Equal(t, "request c1 is no longer pending: the agent replaced or withdrew it, or another client answered it", e.Message)
		require.Len(t, e.Hints, 1)
		assert.Contains(t, e.Hints[0], `"leapmux control agent requests ag-1"`)
		assert.Contains(t, e.Hints[0], "new handle")
		assert.False(t, e.Retryable(), "the same answer to the same handle cannot succeed")
	}

	_, err = controlAnswerResultOf("ag-1", "c1", &leapmuxv1.AnswerControlResponse{State: leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY, Error: "the agent's process ended"})
	e := cli.FromError(err)
	assert.Equal(t, cli.CodeUnavailable, e.Code, "an answer that was not sent can be sent again")
	assert.True(t, e.Retryable())

	_, err = controlAnswerResultOf("ag-1", "c1", &leapmuxv1.AnswerControlResponse{State: leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_UNCERTAIN, Error: "no receipt"})
	e = cli.FromError(err)
	assert.Equal(t, cli.CodeInternal, e.Code)
	assert.False(t, e.Retryable(), "the Worker never sends a second answer")
	assert.Contains(t, e.Message, "uncertain")
}

func TestAgentApproveOverIPC_SendsTheOneTimeApproval(t *testing.T) {
	id := fullTestID("ag-2")
	w := &controlWorker{requests: map[string][]*leapmuxv1.AgentControlRequest{id: {pendingRequest("claim-1", permissionPrompt())}}}
	startControlWorker(t, w, id)

	data := runControl(t, "agent", "approve", id, "claim").Data(t)
	assert.Equal(t, map[string]any{"id": "claim-1", "agent_id": id, "state": "completed"}, data)
	answers := w.recordedAnswers()
	require.Len(t, answers, 1)
	assert.Equal(t, id, answers[0].GetAgentId())
	assert.Equal(t, "req-claim-1", answers[0].GetRequestId())
	assert.Equal(t, "claim-1", answers[0].GetClaimToken(), "the answer names the instance that the caller read")
	assert.Equal(t, "once", answers[0].GetDecision().GetPermission().GetOptionId())
}

func TestAgentDenyOverIPC_SendsTheReason(t *testing.T) {
	id := fullTestID("ag-2")
	w := &controlWorker{requests: map[string][]*leapmuxv1.AgentControlRequest{id: {pendingRequest("claim-1", planPrompt())}}}
	startControlWorker(t, w, id)

	runControl(t, "agent", "deny", id, "claim-1", "--message", "Backfill in batches.").Data(t)
	answers := w.recordedAnswers()
	require.Len(t, answers, 1)
	assert.Equal(t, "Backfill in batches.", answers[0].GetDecision().GetPlan().GetFeedback())
	assert.False(t, answers[0].GetDecision().GetPlan().GetApprove())
}

func TestAgentAnswerOverIPC_SendsOptionsAndText(t *testing.T) {
	id := fullTestID("ag-2")
	w := &controlWorker{requests: map[string][]*leapmuxv1.AgentControlRequest{id: {pendingRequest("claim-1", questionPrompt(databaseQuestion(), extrasQuestion()))}}}
	startControlWorker(t, w, id)

	runControl(t, "agent", "answer", id, "claim-1", "--option", "1:1", "--option", "2:2", "2:Metrics").Data(t)
	answers := w.recordedAnswers()
	require.Len(t, answers, 1)
	requireDecision(t, decisionOf(&leapmuxv1.QuestionDecision{Answers: []*leapmuxv1.QuestionAnswer{
		{Selected: []string{"PostgreSQL"}},
		{Selected: []string{"logs"}, CustomText: "Metrics"},
	}}), answers[0].GetDecision())
}

// A question that allows no answer takes `agent answer` with no answer at
// all, as the browser sends it, and refuses a form that would accept its
// defaults by accident.
func TestAgentAnswerOverIPC_SendsNoAnswerWhereTheQuestionAllowsIt(t *testing.T) {
	id := fullTestID("ag-2")
	optional := extrasQuestion()
	optional.AllowEmpty = true
	w := &controlWorker{requests: map[string][]*leapmuxv1.AgentControlRequest{id: {
		pendingRequest("claim-1", questionPrompt(optional)),
		pendingRequest("form-1", elicitationPrompt(`{"properties":{"private":{"type":"boolean","default":false}}}`)),
	}}}
	startControlWorker(t, w, id)

	f := runControl(t, "agent", "answer", id, "form-1").requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "fill the form with --field")
	assert.Empty(t, w.recordedAnswers(), "an empty answer never accepts a form")

	runControl(t, "agent", "answer", id, "claim-1").Data(t)
	answers := w.recordedAnswers()
	require.Len(t, answers, 1)
	requireDecision(t, decisionOf(&leapmuxv1.QuestionDecision{Answers: []*leapmuxv1.QuestionAnswer{{}}}), answers[0].GetDecision())
}

// An empty --option or --field, such as an unset variable in a script, is a
// usage error. It never vanishes, which would send the agent's default
// selection or the form's defaults in its place.
func TestAgentAnswerOverIPC_RefusesAnEmptyOptionOrField(t *testing.T) {
	id := fullTestID("ag-2")
	defaulted := extrasQuestion()
	defaulted.InitialSelections = &leapmuxv1.QuestionSelections{Values: []string{"logs"}}
	w := &controlWorker{requests: map[string][]*leapmuxv1.AgentControlRequest{id: {
		pendingRequest("claim-1", questionPrompt(defaulted)),
		pendingRequest("form-1", elicitationPrompt(`{"properties":{"private":{"type":"boolean","default":false}}}`)),
	}}}
	startControlWorker(t, w, id)

	f := runControl(t, "agent", "answer", id, "claim-1", "--option", "").requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, `--option ""`)
	f = runControl(t, "agent", "answer", id, "form-1", "--field", "").requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, `--field ""`)
	assert.Empty(t, w.recordedAnswers(), "an empty value sends no answer")
}

func TestAgentAnswerOverIPC_ReadsTheTextFromStdin(t *testing.T) {
	id := fullTestID("ag-2")
	w := &controlWorker{requests: map[string][]*leapmuxv1.AgentControlRequest{id: {pendingRequest("claim-1", dialogPrompt(leapmuxv1.DialogVariant_DIALOG_VARIANT_EDITOR))}}}
	startControlWorker(t, w, id)

	o := runControlIn(t, "line one\nline two\n", "agent", "answer", id, "claim-1", "-")
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	answers := w.recordedAnswers()
	require.Len(t, answers, 1)
	assert.Equal(t, "line one\nline two\n", answers[0].GetDecision().GetDialog().GetValue())

	f := runControl(t, "agent", "answer", id, "claim-1", "-", "-").requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, `only one text`)
}

func TestAgentAnswerOverIPC_Refusals(t *testing.T) {
	id := fullTestID("ag-2")
	answered := pendingRequest("claim-2", permissionPrompt())
	answered.ResponseState = leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_DELIVERED
	w := &controlWorker{requests: map[string][]*leapmuxv1.AgentControlRequest{id: {pendingRequest("claim-1", permissionPrompt()), answered}}}
	startControlWorker(t, w, id)

	t.Run("an unknown handle", func(t *testing.T) {
		f := runControl(t, "agent", "approve", id, "nope").requireFailure(t, cli.CodeNotFound)
		assert.Contains(t, f.Message, "no pending request nope")
	})
	t.Run("an ambiguous handle", func(t *testing.T) {
		f := runControl(t, "agent", "approve", id, "claim-").requireFailure(t, cli.CodeAmbiguousTarget)
		assert.Len(t, f.Candidates, 2)
	})
	t.Run("a request with an answer", func(t *testing.T) {
		runControl(t, "agent", "approve", id, "claim-2").requireFailure(t, cli.CodeConflict)
	})
	t.Run("no answer at all", func(t *testing.T) {
		runControl(t, "agent", "answer", id, "claim-1").requireFailure(t, cli.CodeUsage)
	})
	t.Run("the calling agent", func(t *testing.T) {
		t.Setenv("LEAPMUX_CONTROL_TAB_ID", id)
		f := runControl(t, "agent", "approve", id, "claim-1").requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "cannot answer its own requests")
	})
	assert.Empty(t, w.recordedAnswers(), "no refused command reached AnswerControl")

	t.Run("a decision that the Worker refuses", func(t *testing.T) {
		w.mu.Lock()
		w.refuseAnswer = "the decision does not answer the control request: the prompt offers no option"
		w.mu.Unlock()
		f := runControl(t, "agent", "approve", id, "claim-1").requireFailure(t, cli.CodeInvalidArgument)
		assert.Contains(t, f.Message, "offers no option")
	})
	t.Run("a request that the agent replaced, or that another client answered first", func(t *testing.T) {
		w.mu.Lock()
		w.refuseAnswer = ""
		w.answerResponse = &leapmuxv1.AnswerControlResponse{State: leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_CANCELED}
		w.mu.Unlock()
		f := runControl(t, "agent", "approve", id, "claim-1").requireFailure(t, cli.CodeConflict)
		assert.Contains(t, f.Message, "no longer pending: the agent replaced or withdrew it")
	})
	for _, answer := range w.recordedAnswers() {
		assert.Equal(t, "claim-1", answer.GetClaimToken(), "an answered request is never answered again")
	}
}

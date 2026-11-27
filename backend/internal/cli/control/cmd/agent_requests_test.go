package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

func int32Ptr(v int32) *int32 { return &v }
func int64Ptr(v int64) *int64 { return &v }

// longCommand is longer than a table cell, so a test can see that the text
// form prints it whole.
const longCommand = "rm -rf ./build ./dist ./coverage ./node_modules/.cache && npm ci && npm run build -- --production"

func permissionPrompt() *leapmuxv1.ControlPrompt {
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
		Title:            "Bash",
		Reason:           "The command writes outside the workspace.",
		Command:          longCommand,
		WorkingDirectory: "/w",
		InputJson:        `{"command":"npm test","timeout":120}`,
		Options: []*leapmuxv1.PermissionOption{
			{OptionId: "once", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Allow"},
			{OptionId: "session", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, Name: "Allow for the session", Scope: leapmuxv1.PermissionScope_PERMISSION_SCOPE_SESSION},
			{OptionId: "deny", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Deny"},
			{OptionId: "abort", Name: "Abort the turn"},
		},
	}}}
}

func planPrompt() *leapmuxv1.ControlPrompt {
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{
		Text:        "1. Add the column.\n2. Backfill it.",
		Permissions: []*leapmuxv1.PlanPermission{{Tool: "Bash", Prompt: "run the migration"}},
		Details:     []string{"Mode: plan"},
		Choices: []*leapmuxv1.PlanChoice{
			{Id: "auto", Label: "Yes, and accept edits", Effect: leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_APPROVE},
			{Id: "keep", Label: "No, keep planning", Description: "Stay in plan mode.", Effect: leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_REFUSE},
			{Id: "later", Label: "Save for later"},
		},
	}}}
}

func questionPrompt(questions ...*leapmuxv1.ControlQuestion) *leapmuxv1.ControlPrompt {
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{Questions: questions}}}
}

func extrasQuestion() *leapmuxv1.ControlQuestion {
	return &leapmuxv1.ControlQuestion{
		Header: "Extras", Question: "Which extras?", MultiSelect: true,
		Options: []*leapmuxv1.QuestionOption{{Label: "Cache"}, {Label: "Logs", Value: proto.String("logs")}, {Label: "Traces"}},
	}
}

func databaseQuestion() *leapmuxv1.ControlQuestion {
	return &leapmuxv1.ControlQuestion{
		Question: "Which database?",
		Options:  []*leapmuxv1.QuestionOption{{Label: "PostgreSQL", Description: "The default."}, {Label: "SQLite"}},
	}
}

func elicitationPrompt(schema string) *leapmuxv1.ControlPrompt {
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: &leapmuxv1.ElicitationPrompt{
		Mode: "form", Server: "github", Message: "Create the repository?", SchemaJson: schema,
		AcceptChoices: []*leapmuxv1.ElicitationAcceptChoice{{Key: "once", Label: "Allow once"}, {Key: "session", Label: "Allow for the session"}},
	}}}
}

const repositorySchema = `{"type":"object","properties":{"name":{"type":"string","description":"The repository name."},"private":{"type":"boolean","default":true},"stars":{"type":"integer"},"license":{"type":"string","enum":["mit","apache-2.0"]},"topics":{"type":"array","items":{"type":"string"}}},"required":["name"]}`

func dialogPrompt(variant leapmuxv1.DialogVariant) *leapmuxv1.ControlPrompt {
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Dialog{Dialog: &leapmuxv1.DialogPrompt{
		Title: "Name the branch", Placeholder: "feature/…", Variant: variant, TimeoutMs: int64Ptr(30000),
	}}}
}

func pendingRequest(claim string, prompt *leapmuxv1.ControlPrompt) *leapmuxv1.AgentControlRequest {
	return &leapmuxv1.AgentControlRequest{
		RequestId: "req-" + claim, ClaimToken: claim, Prompt: prompt,
		ResponseState: leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY,
	}
}

func TestControlRequestRowOf_ProjectsEachKind(t *testing.T) {
	t.Run("permission", func(t *testing.T) {
		row := controlRequestRowOf("ag-1", pendingRequest("c1", permissionPrompt()))
		assert.Equal(t, "c1", row.ID, "the handle is the claim token")
		assert.Equal(t, "req-c1", row.RequestID)
		assert.Equal(t, controlKindPermission, row.Kind)
		assert.True(t, row.Blocking)
		assert.Equal(t, "ready", row.State)
		require.NotNil(t, row.Permission)
		assert.JSONEq(t, `{"command":"npm test","timeout":120}`, string(row.Permission.Input))
		assert.Equal(t, []permissionOptionView{
			{N: 1, ID: "once", Kind: "allow_once", Name: "Allow"},
			{N: 2, ID: "session", Kind: "allow_always", Name: "Allow for the session", Scope: "session"},
			{N: 3, ID: "deny", Kind: "reject_once", Name: "Deny"},
			{N: 4, ID: "abort", Kind: "other", Name: "Abort the turn"},
		}, row.Permission.Options, "an option of no stated kind is other")
		assert.Nil(t, row.Plan)
		assert.Empty(t, row.Questions)
	})
	t.Run("plan", func(t *testing.T) {
		row := controlRequestRowOf("ag-1", pendingRequest("c1", planPrompt()))
		require.NotNil(t, row.Plan)
		assert.Equal(t, []planChoiceView{
			{N: 1, ID: "auto", Label: "Yes, and accept edits", Effect: "approve"},
			{N: 2, ID: "keep", Label: "No, keep planning", Description: "Stay in plan mode.", Effect: "refuse"},
			{N: 3, ID: "later", Label: "Save for later", Effect: "other"},
		}, row.Plan.Choices, "a choice of no stated effect is other")
		assert.Equal(t, []planPermissionView{{Tool: "Bash", Prompt: "run the migration"}}, row.Plan.Permissions)
	})
	t.Run("question", func(t *testing.T) {
		notes := questionPrompt(databaseQuestion(), extrasQuestion(), &leapmuxv1.ControlQuestion{
			Question: "Options only?", AllowCustomText: proto.Bool(false), AllowEmpty: true, MultiSelect: true,
			MinimumSelections: int32Ptr(1), MaximumSelections: int32Ptr(2), Options: []*leapmuxv1.QuestionOption{{Label: "A"}},
		})
		row := controlRequestRowOf("ag-1", pendingRequest("c1", notes))
		require.Len(t, row.Questions, 3)
		assert.False(t, row.Questions[0].TypedWithOptions, "one choice takes an option or a text")
		assert.True(t, row.Questions[0].TypedAnswer)
		assert.True(t, row.Questions[1].TypedWithOptions, "a multiple choice takes options and a text together")
		assert.Equal(t, "logs", row.Questions[1].Options[1].Value, "an option's value is what the answer sends")
		assert.Equal(t, "Cache", row.Questions[1].Options[0].Value, "an option without a value sends its label")
		assert.False(t, row.Questions[2].TypedAnswer)
		assert.False(t, row.Questions[2].TypedWithOptions)
		assert.Equal(t, int32Ptr(2), row.Questions[2].MaximumSelections)

		notes.GetQuestion().PreservesSelectionNotes = true
		row = controlRequestRowOf("ag-1", pendingRequest("c1", notes))
		assert.True(t, row.Questions[0].TypedWithOptions, "a prompt that keeps notes takes both on one choice")
	})
	t.Run("elicitation", func(t *testing.T) {
		prompt := elicitationPrompt(repositorySchema)
		prompt.GetElicitation().ArgumentsJson = "not json"
		row := controlRequestRowOf("ag-1", pendingRequest("c1", prompt))
		require.NotNil(t, row.Elicitation)
		assert.Equal(t, "form", row.Elicitation.Purpose)
		assert.JSONEq(t, repositorySchema, string(row.Elicitation.Schema))
		assert.Equal(t, `"not json"`, string(row.Elicitation.Arguments), "text that is not JSON stays a JSON string")
		assert.Equal(t, []acceptChoiceView{{N: 1, Key: "once", Label: "Allow once"}, {N: 2, Key: "session", Label: "Allow for the session"}}, row.Elicitation.AcceptChoices)

		prompt.GetElicitation().Purpose = leapmuxv1.ElicitationPurpose_ELICITATION_PURPOSE_PERMISSION
		assert.Equal(t, "permission", controlRequestRowOf("ag-1", pendingRequest("c1", prompt)).Elicitation.Purpose)
	})
	t.Run("dialog", func(t *testing.T) {
		row := controlRequestRowOf("ag-1", pendingRequest("c1", dialogPrompt(leapmuxv1.DialogVariant_DIALOG_VARIANT_UNSPECIFIED)))
		require.NotNil(t, row.Dialog)
		assert.Equal(t, "confirm", row.Dialog.Variant, "the browser draws a dialog of no variant as a confirm")
		assert.Equal(t, int64Ptr(30000), row.Dialog.TimeoutMS)
	})
	t.Run("unreadable", func(t *testing.T) {
		row := controlRequestRowOf("ag-1", pendingRequest("c1", &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Unreadable{
			Unreadable: leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_NOT_AN_OBJECT,
		}}))
		assert.Equal(t, controlKindUnreadable, row.Kind)
		assert.Equal(t, "not_an_object", row.Fault)
	})
	t.Run("unrecognized", func(t *testing.T) {
		row := controlRequestRowOf("ag-1", pendingRequest("c1", &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Unreadable{
			Unreadable: leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_UNRECOGNIZED,
		}}))
		assert.Equal(t, controlKindUnreadable, row.Kind)
		assert.Equal(t, "unrecognized", row.Fault)
	})
	t.Run("a request that does not block, with an answer", func(t *testing.T) {
		r := pendingRequest("c1", permissionPrompt())
		r.NonBlocking = true
		r.ResponseState = leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_DELIVERED
		row := controlRequestRowOf("ag-1", r)
		assert.False(t, row.Blocking)
		assert.Equal(t, "delivered", row.State)
		r.ResponseState = leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_UNSPECIFIED
		assert.Equal(t, "unknown", controlRequestRowOf("ag-1", r).State, "an unstated state never prints empty")
	})
	t.Run("a kind that this CLI does not know", func(t *testing.T) {
		assert.Equal(t, controlKindUnknown, controlRequestRowOf("ag-1", pendingRequest("c1", &leapmuxv1.ControlPrompt{})).Kind)
	})
}

func renderRequests(t *testing.T, requests ...*leapmuxv1.AgentControlRequest) string {
	t.Helper()
	list := controlRequestList{}
	for _, r := range requests {
		list.Requests = append(list.Requests, controlRequestRowOf("ag-1", r))
	}
	var out bytes.Buffer
	require.NoError(t, list.RenderText(&out))
	return out.String()
}

func TestControlRequestList_RenderText(t *testing.T) {
	t.Run("permission", func(t *testing.T) {
		assert.Equal(t, `request c1 of agent ag-1: permission
  title:             Bash
  reason:            The command writes outside the workspace.
  command:           `+longCommand+`
  working directory: /w
  input:             {"command":"npm test","timeout":120}
  options:
    1  allow_once              Allow
    2  allow_always (session)  Allow for the session
    3  reject_once             Deny
    4  other                   Abort the turn
`, renderRequests(t, pendingRequest("c1", permissionPrompt())), "the command prints whole, never cut to a cell")
	})
	t.Run("plan", func(t *testing.T) {
		assert.Equal(t, `request c1 of agent ag-1: plan
  plan:
    1. Add the column.
    2. Backfill it.
  permissions:
    Bash  run the migration
  Mode: plan
  choices:
    1  approve  Yes, and accept edits
    2  refuse   No, keep planning      Stay in plan mode.
    3  other    Save for later
`, renderRequests(t, pendingRequest("c1", planPrompt())))
	})
	t.Run("questions", func(t *testing.T) {
		assert.Equal(t, `request c1 of agent ag-1: question
  question 1: Which database?
    answer: one option or a typed answer
    1  PostgreSQL  The default.
    2  SQLite
  question 2: Extras: Which extras?
    answer: any options, a typed answer, or both
    1  Cache
    2  Logs
    3  Traces
`, renderRequests(t, pendingRequest("c1", questionPrompt(databaseQuestion(), extrasQuestion()))))
	})
	t.Run("elicitation", func(t *testing.T) {
		assert.Equal(t, `request c1 of agent ag-1: elicitation
  purpose: form
  server:  github
  message: Create the repository?
  fields:
    name     string                    required  The repository name.
    private  boolean
    stars    integer
    license  string (mit, apache-2.0)
    topics   array
  accept choices:
    1  Allow once
    2  Allow for the session
`, renderRequests(t, pendingRequest("c1", elicitationPrompt(repositorySchema))), "the fields keep the schema's order")
	})
	t.Run("dialog", func(t *testing.T) {
		assert.Equal(t, `request c1 of agent ag-1: dialog
  variant:              input
  title:                Name the branch
  placeholder:          feature/…
  answers itself after: 30s
`, renderRequests(t, pendingRequest("c1", dialogPrompt(leapmuxv1.DialogVariant_DIALOG_VARIANT_INPUT))))
	})
	t.Run("unreadable, answered and not blocking", func(t *testing.T) {
		unreadable := pendingRequest("c1", &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Unreadable{Unreadable: leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_MALFORMED}})
		answered := pendingRequest("c2", &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Dialog{Dialog: &leapmuxv1.DialogPrompt{Title: "Go on?", Message: "Line one.\nLine two."}}})
		answered.NonBlocking = true
		answered.ResponseState = leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_PENDING
		assert.Equal(t, `request c1 of agent ag-1: unreadable
  LeapMux cannot read this request (malformed), so it takes no answer.
  Stop the agent's turn with "leapmux agent interrupt ag-1".

request c2 of agent ag-1: dialog (the agent does not wait for it; answered, pending)
  variant: confirm
  title:   Go on?
  message: Line one.
           Line two.
`, renderRequests(t, unreadable, answered), "a value of several lines continues under its first line")
	})
	t.Run("unrecognized", func(t *testing.T) {
		unrecognized := pendingRequest("c1", &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Unreadable{Unreadable: leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_UNRECOGNIZED}})
		assert.Equal(t, `request c1 of agent ag-1: unreadable
  LeapMux cannot read this request (unrecognized), so it takes no answer.
  Stop the agent's turn with "leapmux agent interrupt ag-1".
`, renderRequests(t, unrecognized))
	})
	t.Run("a refusal that ends the turn", func(t *testing.T) {
		cancel := &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
			Title: "Bash",
			Options: []*leapmuxv1.PermissionOption{
				{OptionId: "accept", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Allow"},
				{OptionId: "cancel", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Cancel", EndsTurn: true},
			},
		}}}
		assert.Equal(t, `request c1 of agent ag-1: permission
  title: Bash
  options:
    1  allow_once                   Allow
    2  reject_once (ends the turn)  Cancel
`, renderRequests(t, pendingRequest("c1", cancel)))
		row := controlRequestRowOf("ag-1", pendingRequest("c1", cancel))
		encoded, err := json.Marshal(row.Permission.Options)
		require.NoError(t, err)
		assert.JSONEq(t, `[{"n":1,"id":"accept","kind":"allow_once","name":"Allow"},{"n":2,"id":"cancel","kind":"reject_once","name":"Cancel","ends_turn":true}]`, string(encoded))
	})
	// Codex keeps a command rule and a host rule in the user scope, so the scope
	// alone cannot tell a script which option keeps which rule. The scope label
	// does, beside the scope.
	t.Run("the scope labels of remembered answers", func(t *testing.T) {
		rules := &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
			Title: "Command Execution",
			Options: []*leapmuxv1.PermissionOption{
				{OptionId: "accept", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Allow"},
				{OptionId: "acceptForSession", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, Name: "Allow for Session", Scope: leapmuxv1.PermissionScope_PERMISSION_SCOPE_SESSION},
				{
					OptionId: "host", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, Name: "Allow Host & Remember: example.com",
					Scope: leapmuxv1.PermissionScope_PERMISSION_SCOPE_USER, ScopeLabel: "Host rule",
				},
				{
					OptionId: "block", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ALWAYS, Name: "Block Host & Remember: example.com",
					Scope: leapmuxv1.PermissionScope_PERMISSION_SCOPE_USER, ScopeLabel: "Host rule",
				},
				{OptionId: "labeled", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, Name: "Keep it", ScopeLabel: "Folder rule"},
			},
		}}}
		assert.Equal(t, `request c1 of agent ag-1: permission
  title: Command Execution
  options:
    1  allow_once                       Allow
    2  allow_always (session)           Allow for Session
    3  allow_always (user: Host rule)   Allow Host & Remember: example.com
    4  reject_always (user: Host rule)  Block Host & Remember: example.com
    5  allow_always (Folder rule)       Keep it
`, renderRequests(t, pendingRequest("c1", rules)), "a label without a scope prints alone")
		row := controlRequestRowOf("ag-1", pendingRequest("c1", rules))
		encoded, err := json.Marshal(row.Permission.Options)
		require.NoError(t, err)
		assert.JSONEq(t, `[
			{"n":1,"id":"accept","kind":"allow_once","name":"Allow"},
			{"n":2,"id":"acceptForSession","kind":"allow_always","name":"Allow for Session","scope":"session"},
			{"n":3,"id":"host","kind":"allow_always","name":"Allow Host & Remember: example.com","scope":"user","scope_label":"Host rule"},
			{"n":4,"id":"block","kind":"reject_always","name":"Block Host & Remember: example.com","scope":"user","scope_label":"Host rule"},
			{"n":5,"id":"labeled","kind":"allow_always","name":"Keep it","scope_label":"Folder rule"}
		]`, string(encoded))
	})
	t.Run("question context and default", func(t *testing.T) {
		extras := extrasQuestion()
		extras.Context = "The build runs nightly.\nIt keeps a week of logs."
		extras.InitialSelections = &leapmuxv1.QuestionSelections{Values: []string{"logs", "Traces"}}
		database := databaseQuestion()
		database.InitialSelections = &leapmuxv1.QuestionSelections{}
		assert.Equal(t, `request c1 of agent ag-1: question
  question 1: Extras: Which extras?
    context: The build runs nightly.
             It keeps a week of logs.
    answer:  any options, a typed answer, or both
    default: options 2, 3
    1  Cache
    2  Logs
    3  Traces
  question 2: Which database?
    answer:  one option or a typed answer
    default: no option
    1  PostgreSQL  The default.
    2  SQLite
`, renderRequests(t, pendingRequest("c1", questionPrompt(extras, database))), "a default lists options by number, and an empty one says so")
	})
	t.Run("form field rules", func(t *testing.T) {
		prompt := elicitationPrompt(`{"properties":{"env":{"type":"string"},"region":{"type":"string","description":"Where it runs."},"token":{"type":"string","default":"t"}},"required":["region"]}`)
		prompt.GetElicitation().AcceptChoices = nil
		prompt.GetElicitation().FieldRules = []*leapmuxv1.ElicitationFieldRule{
			{Key: "region", Conditions: []*leapmuxv1.ElicitationFieldCondition{
				{Field: "env", Comparison: leapmuxv1.ElicitationFieldComparison_ELICITATION_FIELD_COMPARISON_EQUAL, ValueJson: `"prod"`},
				{Field: "replicas", Comparison: leapmuxv1.ElicitationFieldComparison_ELICITATION_FIELD_COMPARISON_NOT_EQUAL, ValueJson: `0`},
			}},
			{Key: "token", Hidden: true},
		}
		assert.Equal(t, `request c1 of agent ag-1: elicitation
  purpose: form
  server:  github
  message: Create the repository?
  fields:
    env     string
    region  string  required  when env = "prod" and replicas != 0  Where it runs.
`, renderRequests(t, pendingRequest("c1", prompt)), "a hidden field is not listed, and a conditional field states its conditions")
	})
	t.Run("nothing", func(t *testing.T) {
		assert.Empty(t, renderRequests(t))
	})
}

func TestQuestionViewsOf_ContextAndDefaults(t *testing.T) {
	with := databaseQuestion()
	with.Context = "Pick one."
	with.InitialSelections = &leapmuxv1.QuestionSelections{Values: []string{"SQLite"}}
	empty := databaseQuestion()
	empty.InitialSelections = &leapmuxv1.QuestionSelections{}
	views := questionViewsOf(&leapmuxv1.QuestionPrompt{Questions: []*leapmuxv1.ControlQuestion{with, empty, databaseQuestion()}})
	encoded, err := json.Marshal(views)
	require.NoError(t, err)
	var got []map[string]any
	require.NoError(t, json.Unmarshal(encoded, &got))
	assert.Equal(t, "Pick one.", got[0]["context"])
	assert.Equal(t, []any{"SQLite"}, got[0]["initial_selections"])
	assert.Equal(t, []any{}, got[1]["initial_selections"], "an empty default is an answer, so it prints as an empty list")
	assert.NotContains(t, got[2], "initial_selections", "a question with no default prints none")
	assert.NotContains(t, got[2], "context")
}

func TestFieldRuleViewsOf(t *testing.T) {
	condition := func(comparison leapmuxv1.ElicitationFieldComparison, value string) *leapmuxv1.ElicitationFieldCondition {
		return &leapmuxv1.ElicitationFieldCondition{Field: "env", Comparison: comparison, ValueJson: value}
	}
	equal := leapmuxv1.ElicitationFieldComparison_ELICITATION_FIELD_COMPARISON_EQUAL
	views := fieldRuleViewsOf([]*leapmuxv1.ElicitationFieldRule{
		{Key: "token", Hidden: true},
		{Key: "region", Conditions: []*leapmuxv1.ElicitationFieldCondition{
			condition(equal, ` "prod" `),
			condition(leapmuxv1.ElicitationFieldComparison_ELICITATION_FIELD_COMPARISON_NOT_EQUAL, `1.5`),
			condition(equal, `true`),
			condition(leapmuxv1.ElicitationFieldComparison_ELICITATION_FIELD_COMPARISON_UNSPECIFIED, `"dev"`),
			condition(equal, `["prod"]`),
			condition(equal, `{"a":1}`),
			condition(equal, `null`),
			condition(equal, `prod`),
			condition(equal, ``),
		}},
	})
	assert.Equal(t, []fieldRuleView{
		{Key: "token", Hidden: true},
		{Key: "region", Conditions: []fieldConditionView{
			{Field: "env", Comparison: "equal", Value: json.RawMessage(`"prod"`)},
			{Field: "env", Comparison: "not_equal", Value: json.RawMessage(`1.5`)},
			{Field: "env", Comparison: "equal", Value: json.RawMessage(`true`)},
		}},
	}, views, "a condition of no known comparison or of a value that is not a scalar is dropped")
	assert.Empty(t, fieldRuleViewsOf(nil))
}

func TestParseConditionScalar(t *testing.T) {
	v, ok := parseConditionScalar([]byte(`"prod"`))
	assert.True(t, ok)
	assert.Equal(t, "prod", v)

	v, ok = parseConditionScalar([]byte(`42`))
	assert.True(t, ok)
	assert.Equal(t, 42.0, v)

	v, ok = parseConditionScalar([]byte(`true`))
	assert.True(t, ok)
	assert.Equal(t, true, v)

	for _, raw := range []string{`null`, `[]`, `{}`, `bad`, ``} {
		v, ok := parseConditionScalar([]byte(raw))
		assert.False(t, ok, raw)
		assert.Nil(t, v, raw)
	}
}

func TestWriteRows_StripsWhitespaceOnlyTrailingCells(t *testing.T) {
	var buf bytes.Buffer
	writeRows(&buf, "  ", [][]string{
		{"1", "alpha", "Description"},
		{"2", "beta", "  \n\t  "},
		{"3", "gamma", ""},
	})
	assert.Equal(t, "  1  alpha  Description\n  2  beta\n  3  gamma\n", buf.String())

	buf.Reset()
	writeRows(&buf, "  ", nil)
	assert.Empty(t, buf.String(), "nil rows write nothing")

	buf.Reset()
	writeRows(&buf, "  ", [][]string{})
	assert.Empty(t, buf.String(), "empty rows slice writes nothing")

	buf.Reset()
	writeRows(&buf, "  ", [][]string{{"   ", "\t\n"}})
	assert.Equal(t, "  \n", buf.String(), "all-whitespace row trims every cell to indent plus newline")

	buf.Reset()
	writeRows(&buf, "  ", [][]string{
		{"1", "  \t ", "desc"},
		{"2", "\n", "more"},
	})
	assert.Equal(t, "  1  desc\n  2  more\n", buf.String(), "all-whitespace interior column takes no horizontal space")
}

func TestQuestionView_AnswerRule(t *testing.T) {
	for want, q := range map[string]questionView{
		"one option or a typed answer":                     {Options: make([]questionOptionView, 1), TypedAnswer: true},
		"one option, a typed answer, or both":              {Options: make([]questionOptionView, 1), TypedAnswer: true, TypedWithOptions: true},
		"one option":                                       {Options: make([]questionOptionView, 1)},
		"one option, or nothing":                           {Options: make([]questionOptionView, 1), AllowEmpty: true},
		"any options, a typed answer, or both":             {Options: make([]questionOptionView, 1), MultiSelect: true, TypedAnswer: true, TypedWithOptions: true},
		"2 options":                                        {Options: make([]questionOptionView, 1), MultiSelect: true, MinimumSelections: int32Ptr(2), MaximumSelections: int32Ptr(2)},
		"1 to 3 options":                                   {Options: make([]questionOptionView, 1), MultiSelect: true, MinimumSelections: int32Ptr(1), MaximumSelections: int32Ptr(3)},
		"at least 2 options":                               {Options: make([]questionOptionView, 1), MultiSelect: true, MinimumSelections: int32Ptr(2)},
		"up to 3 options, a typed answer, or both":         {Options: make([]questionOptionView, 1), MultiSelect: true, MaximumSelections: int32Ptr(3), TypedAnswer: true, TypedWithOptions: true},
		"a typed answer":                                   {TypedAnswer: true},
		"a typed answer, or nothing":                       {TypedAnswer: true, AllowEmpty: true},
		"or nothing":                                       {AllowEmpty: true},
		"nothing that this command can send":               {},
		"any options, a typed answer, or both, or nothing": {Options: make([]questionOptionView, 1), MultiSelect: true, TypedAnswer: true, TypedWithOptions: true, AllowEmpty: true},
	} {
		assert.Equal(t, want, q.answerRule())
	}
}

func TestElicitationFields(t *testing.T) {
	fields := elicitationFields(json.RawMessage(`{"properties":{"z":{"type":"string","title":"Zed"},"a":{"type":"string","oneOf":[{"const":"x","title":"X"},{"const":"y"}]},"tags":{"type":"array","items":{"type":"string","anyOf":[{"const":"p"}]}}},"required":["a"]}`), nil)
	require.Len(t, fields, 3)
	assert.Equal(t, "z", fields[0].Name, "the fields keep the schema's order")
	assert.Equal(t, "Zed", fields[0].Description, "a title stands in for a missing description")
	assert.Equal(t, []string{"x", "y"}, fields[1].Enum, "a oneOf of constants is an enum")
	assert.True(t, fields[1].Required)
	assert.Equal(t, []string{"p"}, fields[2].Enum, "an array field takes the enum of its items")
	assert.Equal(t, "string", fields[2].ItemType)

	for _, schema := range []string{``, `not json`, `{"properties":[1]}`, `{}`} {
		assert.Empty(t, elicitationFields(json.RawMessage(schema), nil), schema)
	}

	largeNums := elicitationFields(json.RawMessage(`{"properties":{`+
		`"limit":{"type":"integer","enum":[1000000,2000000,9007199254740993]},`+
		`"budget":{"type":"integer","oneOf":[{"const":9007199254740993,"title":"Above 2^53"},{"const":null}]},`+
		`"sizes":{"type":"array","items":{"type":"integer","anyOf":[{"const":9007199254740993}]}}`+
		`}}`), nil)
	require.Len(t, largeNums, 3)
	assert.Equal(t, []string{"1000000", "2000000", "9007199254740993"}, largeNums[0].Enum, "large integers keep exact decimal spelling beyond 2^53")
	assert.Equal(t, []string{"9007199254740993"}, largeNums[1].Enum)
	assert.Equal(t, []string{"9007199254740993"}, largeNums[2].Enum)
}

// fakeWorkerChannels answers listControlRequests' calls from a table, and
// records each agent that it was asked about.
type fakeWorkerChannels struct {
	mu        sync.Mutex
	requests  map[string][]*leapmuxv1.AgentControlRequest
	failAgent map[string]error
	failOpen  map[string]error
	asked     []string
}

func (f *fakeWorkerChannels) open(workerID string, body func(call workerCaller) error) error {
	if err := f.failOpen[workerID]; err != nil {
		return err
	}
	return body(func(method string, in, out proto.Message) error {
		if method != leapmuxv1connect.WorkerAgentServiceListControlRequestsProcedure {
			return errors.New("unexpected method " + method)
		}
		agentID := in.(*leapmuxv1.ListControlRequestsRequest).GetAgentId()
		f.mu.Lock()
		f.asked = append(f.asked, agentID)
		f.mu.Unlock()
		if err := f.failAgent[agentID]; err != nil {
			return err
		}
		out.(*leapmuxv1.ListControlRequestsResponse).Requests = f.requests[agentID]
		return nil
	})
}

func TestListControlRequests_KeepsEachAgentsResultApart(t *testing.T) {
	fake := &fakeWorkerChannels{
		requests: map[string][]*leapmuxv1.AgentControlRequest{
			"a1": {pendingRequest("c1", permissionPrompt()), pendingRequest("c2", planPrompt())},
			"a3": {pendingRequest("c3", permissionPrompt())},
		},
		failAgent: map[string]error{"a2": cli.Newf(cli.CodeNotFound, "no agent a2")},
		failOpen:  map[string]error{"w2": cli.Newf(cli.CodeWorkerOffline, "worker w2 is offline")},
	}
	groups := []workerAgents{{workerID: "w1", agentIDs: []string{"a1", "a2", "a3"}}, {workerID: "w2", agentIDs: []string{"a4"}}}
	got := listControlRequests(context.Background(), fake.open, groups)

	require.Len(t, got, 4)
	require.NoError(t, got["a1"].err)
	assert.Equal(t, []string{"c1", "c2"}, []string{got["a1"].rows[0].ID, got["a1"].rows[1].ID}, "an agent's requests keep the Worker's order")
	assert.Equal(t, "a1", got["a1"].rows[0].AgentID)
	assert.ErrorContains(t, got["a2"].err, "list the requests of agent a2")
	assert.Len(t, got["a3"].rows, 1, "a failure of one agent leaves the others")
	assert.Equal(t, cli.CodeWorkerOffline, cli.FromError(got["a4"].err).Code, "a channel that does not open fails each agent of its Worker with its own cause")
	assert.ElementsMatch(t, []string{"a1", "a2", "a3"}, fake.asked)
}

func TestReportControlRequests(t *testing.T) {
	result := controlRequestList{Requests: []controlRequestRow{controlRequestRowOf("a1", pendingRequest("c1", permissionPrompt()))}}
	offline := cli.Newf(cli.CodeWorkerOffline, "list the requests of agent a2: worker w2 is offline")

	t.Run("every agent answered", func(t *testing.T) {
		var stderr bytes.Buffer
		p := &cli.Printer{Out: &bytes.Buffer{}, Err: &stderr}
		got, err := reportControlRequests(p, result, nil, 1)
		require.NoError(t, err)
		assert.Equal(t, result, got)
		assert.Empty(t, stderr.String())

		got, err = reportControlRequests(p, controlRequestList{Requests: []controlRequestRow{}}, nil, 1)
		require.NoError(t, err)
		assert.Empty(t, got.(controlRequestList).Requests)
		assert.Contains(t, stderr.String(), "no agent waits for an answer")
	})
	t.Run("some agents failed", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		p := &cli.Printer{Out: &stdout, Err: &stderr}
		got, err := reportControlRequests(p, result, []error{offline}, 2)
		assert.Nil(t, got, "the result already printed")
		var status *cli.ExitStatus
		require.ErrorAs(t, err, &status)
		assert.Equal(t, cli.ExitUnavailable, status.Code, "a partial list never exits 0")
		assert.Contains(t, stdout.String(), "request c1 of agent a1")
		assert.Contains(t, stderr.String(), "worker w2 is offline")
	})
	t.Run("every agent failed", func(t *testing.T) {
		got, err := reportControlRequests(&cli.Printer{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}, controlRequestList{}, []error{offline}, 1)
		assert.Nil(t, got)
		assert.Equal(t, offline, err, "the failure is the result")
	})
}

// controlWorker is a fake Worker that holds pending control requests and
// records each answer. mu guards every field.
type controlWorker struct {
	mu        sync.Mutex
	requests  map[string][]*leapmuxv1.AgentControlRequest
	agents    []*leapmuxv1.AgentInfo
	answers   []*leapmuxv1.AnswerControlRequest
	listCalls int
	// answerResponse is what AnswerControl returns; nil answers COMPLETED.
	answerResponse *leapmuxv1.AnswerControlResponse
	// refuseAnswer, when not empty, is the InvalidArgument message that
	// AnswerControl sends instead.
	refuseAnswer string
}

func (w *controlWorker) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
		writeProtoResponse(rw, &leapmuxv1.ListAgentsResponse{Agents: w.agents})
	case leapmuxv1connect.WorkerAgentServiceListControlRequestsProcedure:
		w.listCalls++
		var in leapmuxv1.ListControlRequestsRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		writeProtoResponse(rw, &leapmuxv1.ListControlRequestsResponse{Requests: w.requests[in.GetAgentId()]})
	case leapmuxv1connect.WorkerAgentServiceAnswerControlProcedure:
		var in leapmuxv1.AnswerControlRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		w.answers = append(w.answers, &in)
		if w.refuseAnswer != "" {
			_ = rw.SendError(int32(codes.InvalidArgument), w.refuseAnswer)
			return
		}
		response := w.answerResponse
		if response == nil {
			response = &leapmuxv1.AnswerControlResponse{State: leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED}
		}
		writeProtoResponse(rw, response)
	default:
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
	}
}

func (w *controlWorker) recordedAnswers() []*leapmuxv1.AnswerControlRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]*leapmuxv1.AnswerControlRequest(nil), w.answers...)
}

func (w *controlWorker) listCallCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.listCalls
}

func TestAgentRequestsOverIPC_ListsTheRequestsOfAnAgent(t *testing.T) {
	id := fullTestID("ag-2")
	w := &controlWorker{requests: map[string][]*leapmuxv1.AgentControlRequest{id: {pendingRequest("claim-1", permissionPrompt())}}}
	startAgentWorker(t, id, w)

	data := runControl(t, "agent", "requests", id).Data(t)
	requests := data["requests"].([]any)
	require.Len(t, requests, 1)
	row := requests[0].(map[string]any)
	assert.Equal(t, "claim-1", row["id"])
	assert.Equal(t, id, row["agent_id"])
	assert.Equal(t, "permission", row["kind"])
	assert.Equal(t, true, row["blocking"])
	assert.Equal(t, "ready", row["state"])
	assert.Equal(t, longCommand, row["permission"].(map[string]any)["command"])

	o := runControlText(t, "agent", "requests", id)
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Contains(t, o.Stdout, "request claim-1 of agent "+id+": permission")
	assert.Contains(t, o.Stdout, longCommand)
}

func TestAgentRequestsOverIPC_ListsEveryAgentWithoutAnArgument(t *testing.T) {
	id := fullTestID("ag-2")
	w := &controlWorker{requests: map[string][]*leapmuxv1.AgentControlRequest{id: {pendingRequest("claim-1", planPrompt())}}}
	startAgentWorker(t, id, w)

	data := runControl(t, "agent", "requests").Data(t)
	requests := data["requests"].([]any)
	require.Len(t, requests, 1)
	assert.Equal(t, "plan", requests[0].(map[string]any)["kind"])

	// --workspace scopes agent lookup when combined with explicit agent args.
	scoped := runControl(t, "agent", "requests", id, "--workspace", "ws-1").Data(t)
	require.Len(t, scoped["requests"].([]any), 1)
}

func TestAgentRequestsOverIPC_SaysWhenNoAgentWaits(t *testing.T) {
	id := fullTestID("ag-2")
	startAgentWorker(t, id, &controlWorker{})

	o := runControlText(t, "agent", "requests", id)
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Empty(t, strings.TrimSpace(o.Stdout))
	assert.Contains(t, o.Stderr, "no agent waits for an answer")
}

func TestAgentRequestsOverIPC_ChildrenDeduplicationAndQuietFormat(t *testing.T) {
	parentID := fullTestID("ag-parent")
	child1 := fullTestID("ag-child1")
	child2 := fullTestID("ag-child2")
	grandchild := fullTestID("ag-grand1")

	hub := &recordingHub{
		workers: workerRows("worker-A"),
		locateTab: &leapmuxv1.WorkspaceTab{
			Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: child1},
			TileId:      "root-1",
			WorkerId:    "worker-A",
			WorkspaceId: "ws-1",
		},
		tabs: []*leapmuxv1.WorkspaceTab{
			{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: parentID}, TileId: "root-1", WorkerId: "worker-A", WorkspaceId: "ws-1"},
			{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: child1}, TileId: "root-1", WorkerId: "worker-A", WorkspaceId: "ws-1"},
			{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: child2}, TileId: "root-1", WorkerId: "worker-A", WorkspaceId: "ws-1"},
			{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: grandchild}, TileId: "root-1", WorkerId: "worker-A", WorkspaceId: "ws-1"},
		},
	}
	w := &controlWorker{
		agents: []*leapmuxv1.AgentInfo{
			{Id: parentID},
			{Id: child1, ParentAgentId: parentID},
			{Id: child2, ParentAgentId: parentID},
			{Id: grandchild, ParentAgentId: child1},
		},
		requests: map[string][]*leapmuxv1.AgentControlRequest{
			parentID:   {pendingRequest("claim-parent", permissionPrompt())},
			child1:     {pendingRequest("claim-c1", permissionPrompt())},
			child2:     {pendingRequest("claim-c2", planPrompt())},
			grandchild: {pendingRequest("claim-grand", permissionPrompt())},
		},
	}
	startSpawnIPC(t, hub, w)
	t.Setenv(controlenv.TabIDVariable, parentID)
	t.Setenv(controlenv.TabTypeVariable, "agent")
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")

	// Passing child1 explicitly alongside "children" deduplicates child1,
	// preserves first-seen order, and excludes parent and grandchild.
	data := runControl(t, "agent", "requests", child1, "children").Data(t)
	rows := data["requests"].([]any)
	require.Len(t, rows, 2)
	assert.Equal(t, "claim-c1", rows[0].(map[string]any)["id"])
	assert.Equal(t, child1, rows[0].(map[string]any)["agent_id"])
	assert.Equal(t, "claim-c2", rows[1].(map[string]any)["id"])
	assert.Equal(t, child2, rows[1].(map[string]any)["agent_id"])
	assert.Equal(t, 2, w.listCallCount(), "deduplicated target must be queried once")

	// "children" counts as multi-target in -q even when only one child has a request.
	wSingle := &controlWorker{
		agents: []*leapmuxv1.AgentInfo{
			{Id: parentID},
			{Id: child1, ParentAgentId: parentID},
		},
		requests: map[string][]*leapmuxv1.AgentControlRequest{
			child1: {pendingRequest("claim-c1", permissionPrompt())},
		},
	}
	startSpawnIPC(t, hub, wSingle)
	t.Setenv(controlenv.TabIDVariable, parentID)
	t.Setenv(controlenv.TabTypeVariable, "agent")
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")

	o := runControlText(t, "agent", "requests", "children", "-q")
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, child1+"\tclaim-c1\n", o.Stdout, "children selector keeps <agent_id>\\t<handle> quiet format")

	o = runControlText(t, "agent", "requests", child1, "-q")
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, "claim-c1\n", o.Stdout, "single explicit agent target prints bare handle")
}

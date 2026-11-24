package providerkit

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func requireClaudeShapedPrompt(t *testing.T, want, got *leapmuxv1.ControlPrompt) {
	t.Helper()
	require.Truef(t, proto.Equal(want, got), "prompt mismatch:\nwant %s\ngot  %s", protojson.Format(want), protojson.Format(got))
}

func TestClaudeShapedQuestionPrompt_ReadsTheQuestionsOfTheToolInput(t *testing.T) {
	got, ok := ClaudeShapedQuestionPrompt([]byte(`{"request":{"tool_name":"AskUserQuestion","input":{"questions":[{"question":"Which?","header":"Pick","options":[{"label":"A"}]}]}}}`))
	require.True(t, ok)
	requireClaudeShapedPrompt(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{{Question: "Which?", Header: "Pick", Options: []*leapmuxv1.QuestionOption{{Label: "A"}}}},
	}}}, got)
}

func TestClaudeShapedQuestionPrompt_ReadsAnInputWithNoListAsNoQuestion(t *testing.T) {
	for _, payload := range []string{
		`{"request":{"tool_name":"AskUserQuestion"}}`,
		`{"request":{"tool_name":"AskUserQuestion","input":{"questions":"nope"}}}`,
		`{"request":{"tool_name":"AskUserQuestion","input":[1]}}`,
	} {
		got, ok := ClaudeShapedQuestionPrompt([]byte(payload))
		require.True(t, ok, payload)
		require.NotNil(t, got.GetQuestion(), payload)
		assert.Empty(t, got.GetQuestion().GetQuestions(), payload)
	}
}

func TestClaudeShapedQuestionPrompt_RefusesAnotherTool(t *testing.T) {
	for _, payload := range []string{`{"request":{"tool_name":"Bash","input":{}}}`, `{}`, `not json`} {
		_, ok := ClaudeShapedQuestionPrompt([]byte(payload))
		assert.False(t, ok, payload)
	}
}

func TestClaudeShapedToolPrompt_ExitPlanModeIsAPlanThatStatesItsKindAlone(t *testing.T) {
	got := ClaudeShapedToolPrompt([]byte(`{"request":{"tool_name":"ExitPlanMode","input":{"plan":"1. Edit.","allowedPrompts":[{"tool":"Bash","prompt":"run the tests"}]}}}`), agent.AllowDenyOptions())
	requireClaudeShapedPrompt(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{}}}, got)
}

func TestClaudeShapedToolPrompt_AnotherToolIsAPermissionWithTheGivenOptions(t *testing.T) {
	options := []*leapmuxv1.PermissionOption{{OptionId: "x", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE}}
	got := ClaudeShapedToolPrompt([]byte(`{"request":{"tool_name":"Edit","input":{"file_path":"/w/a.go","command":"not a shell"}}}`), options)
	permission := got.GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, "Edit", permission.GetTitle())
	assert.JSONEq(t, `{"file_path":"/w/a.go","command":"not a shell"}`, permission.GetInputJson())
	assert.Empty(t, permission.GetCommand(), "a `command` of a tool that is no shell is one of its arguments")
	require.Len(t, permission.GetOptions(), 1)
	assert.True(t, proto.Equal(options[0], permission.GetOptions()[0]), "the prompt offers the options that the provider gives")
}

func TestClaudeShapedToolPrompt_ABashCallStatesItsCommand(t *testing.T) {
	got := ClaudeShapedToolPrompt([]byte(`{"request":{"tool_name":"Bash","input":{"command":"ls -la"}}}`), nil)
	assert.Equal(t, "ls -la", got.GetPermission().GetCommand())

	got = ClaudeShapedToolPrompt([]byte(`{"request":{"tool_name":"Bash","input":{"command":7}}}`), nil)
	assert.Empty(t, got.GetPermission().GetCommand(), "a command that is no text states none")

	got = ClaudeShapedToolPrompt([]byte(`{"request":{"tool_name":"PowerShell","input":{"command":"dir"}}}`), nil)
	assert.Empty(t, got.GetPermission().GetCommand(), "only Bash is the shell tool of this shape")
}

func TestClaudeShapedToolPrompt_APayloadWithNoToolIsAnUntitledPermission(t *testing.T) {
	for _, payload := range []string{`{}`, `{"request":{"input":null}}`, `{"request":"text"}`} {
		got := ClaudeShapedToolPrompt([]byte(payload), agent.AllowDenyOptions())
		permission := got.GetPermission()
		require.NotNil(t, permission, payload)
		assert.Empty(t, permission.GetTitle(), payload)
		assert.Equal(t, "{}", permission.GetInputJson(), "the input that the answer sends back: %s", payload)
	}
}

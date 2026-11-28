package providerkit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
)

func TestReadWorkspaceTrustPrompt_StatesTheFolderTheAgentAndWhatTheTrustControls(t *testing.T) {
	got, ok := providerkit.ReadWorkspaceTrustPrompt(providerkittest.WorkspaceTrustQuestionPayload(t, "/work/project", []any{"MCP servers"}), leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT)
	require.True(t, ok)
	want := &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
		Title:  "Trust the folder /work/project",
		Reason: "GitHub Copilot loads the MCP servers of a project only from a folder that you trust, and it keeps your answer for the folder.",
		Options: []*leapmuxv1.PermissionOption{
			{OptionId: "workspace-trust", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Trust this folder"},
			{OptionId: "workspace-reject", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Do not trust"},
		},
	}}, EditorPurpose: leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE}
	agenttest.RequireProto(t, want, got)
}

func TestReadWorkspaceTrustPrompt_JoinsTheConfigurationWords(t *testing.T) {
	for _, tc := range []struct {
		configuration any
		phrase        string
	}{
		{[]any{}, "the configuration"},
		{[]any{"MCP servers", "hooks"}, "the MCP servers and hooks"},
		{[]any{"MCP servers", "hooks", "agents"}, "the MCP servers, hooks and agents"},
		// An empty word and a word that is no string are dropped.
		{[]any{"MCP servers", "", 7, nil}, "the MCP servers"},
		// A value that is no list states no words.
		{"MCP servers", "the configuration"},
		{nil, "the configuration"},
	} {
		got, ok := providerkit.ReadWorkspaceTrustPrompt(providerkittest.WorkspaceTrustQuestionPayload(t, "/work/project", tc.configuration), leapmuxv1.AgentProvider_AGENT_PROVIDER_KIMI_CODE)
		require.True(t, ok)
		assert.Equal(t, "Kimi Code loads "+tc.phrase+" of a project only from a folder that you trust, and it keeps your answer for the folder.",
			got.GetPermission().GetReason(), "%v", tc.configuration)
	}
}

func TestReadWorkspaceTrustPrompt_TitlesAQuestionWithoutAFolder(t *testing.T) {
	got, ok := providerkit.ReadWorkspaceTrustPrompt(providerkittest.WorkspaceTrustQuestionPayload(t, "", []any{}), leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE)
	require.True(t, ok)
	assert.Equal(t, "Trust this folder", got.GetPermission().GetTitle())
	assert.Contains(t, got.GetPermission().GetReason(), "ZCode loads ")
}

func TestReadWorkspaceTrustPrompt_FallsBackToAGeneralSubjectWithoutADisplayName(t *testing.T) {
	got, ok := providerkit.ReadWorkspaceTrustPrompt(providerkittest.WorkspaceTrustQuestionPayload(t, "/w", []any{}), leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED)
	require.True(t, ok)
	assert.Equal(t, "The agent loads the configuration of a project only from a folder that you trust, and it keeps your answer for the folder.", got.GetPermission().GetReason())
}

func TestReadWorkspaceTrustPrompt_ReadsNoQuestionFromAnotherPayload(t *testing.T) {
	for _, payload := range []string{
		`{"type":"permission"}`,
		`{"method":"session.event"}`,
		`{"type":7}`,
		`[]`,
		`not json`,
		``,
	} {
		_, ok := providerkit.ReadWorkspaceTrustPrompt([]byte(payload), leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT)
		assert.False(t, ok, payload)
		assert.False(t, providerkit.IsWorkspaceTrustQuestion([]byte(payload)), payload)
	}
}

// answerWorkspaceTrust answers the question payload with a permission decision.
func answerWorkspaceTrust(t *testing.T, optionID, feedback string) (agent.ControlAnswer, error) {
	t.Helper()
	payload := providerkittest.WorkspaceTrustQuestionPayload(t, "/work/project", []any{"hooks"})
	prompt, ok := providerkit.ReadWorkspaceTrustPrompt(payload, leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE)
	require.True(t, ok)
	return providerkit.AnswerWorkspaceTrust(agent.ControlAnswerInput{
		Request:   agent.NewControlRequestInput(payload),
		RequestID: "workspace-trust:session-1",
		Prompt:    prompt,
		Decision:  agenttest.PermissionDecision(optionID, feedback),
	})
}

func TestAnswerWorkspaceTrust_SendsTrustAsAnAllowDecision(t *testing.T) {
	got, err := answerWorkspaceTrust(t, providerkit.WorkspaceTrustOptionTrust, "")
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"workspace-trust:session-1","response":{"behavior":"allow"}}}`, string(got.Content))
	assert.Nil(t, got.PlanApproval)
	assert.Empty(t, got.FollowUp)
}

func TestAnswerWorkspaceTrust_SendsDoNotTrustAsADenyDecision(t *testing.T) {
	got, err := answerWorkspaceTrust(t, providerkit.WorkspaceTrustOptionReject, "")
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"workspace-trust:session-1","response":{"behavior":"deny"}}}`, string(got.Content),
		"the button sends a deny with no message")
}

// ResolveWorkspaceTrustResponse sends the runtime the decision alone, so a
// reason would reach nobody: the answer refuses one.
func TestAnswerWorkspaceTrust_RefusesAReason(t *testing.T) {
	for _, option := range []string{providerkit.WorkspaceTrustOptionReject, providerkit.WorkspaceTrustOptionTrust} {
		_, err := answerWorkspaceTrust(t, option, "Not this folder.")
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, option)
	}
}

// The answer must resolve to the saved decision through the resolver that
// the providers already run, so the two halves cannot drift.
func TestAnswerWorkspaceTrust_ResolvesToTheSavedDecision(t *testing.T) {
	payload := providerkittest.WorkspaceTrustQuestionPayload(t, "/work/project", []any{"hooks"})
	for option, decision := range map[string]string{
		providerkit.WorkspaceTrustOptionTrust:  contracts.WorkspaceTrustDecisionTrust,
		providerkit.WorkspaceTrustOptionReject: contracts.WorkspaceTrustDecisionReject,
	} {
		got, err := answerWorkspaceTrust(t, option, "")
		require.NoError(t, err)
		resolved, ok := providerkit.ResolveWorkspaceTrustResponse(agenttest.ControlResponseContextOf(payload, "workspace-trust:session-1", got))
		require.True(t, ok)
		assert.False(t, resolved.Withhold, option)
		assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"workspace-trust:session-1","response":{"decision":"`+decision+`"}}}`, string(resolved.Content), option)
	}
}

func TestAnswerWorkspaceTrust_RefusesADecisionThatDoesNotAnswerTheQuestion(t *testing.T) {
	_, err := answerWorkspaceTrust(t, providerkit.WorkspaceTrustOptionTrust, "Why?")
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "feedback goes with a refusal")
	_, err = answerWorkspaceTrust(t, "allow", "")
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "the question offers its own two options only")

	payload := providerkittest.WorkspaceTrustQuestionPayload(t, "/w", []any{})
	prompt, _ := providerkit.ReadWorkspaceTrustPrompt(payload, leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE)
	_, err = providerkit.AnswerWorkspaceTrust(agent.ControlAnswerInput{
		Request: agent.NewControlRequestInput(payload), RequestID: "r", Prompt: prompt,
		Decision: agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true}),
	})
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "the question takes a permission decision")
}

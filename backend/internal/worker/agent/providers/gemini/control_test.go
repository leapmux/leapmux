package gemini

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPermissionBypassExcludesEveryPlanIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, params string
		allow        bool
	}{
		{"ordinary", `{"toolCall":{"toolCallId":"run_shell_command__one"}}`, true},
		{"plan", `{"toolCall":{"toolCallId":"exit_plan_mode"}}`, false},
		{"plan with invalid path", `{"toolCall":{"toolCallId":"exit_plan_mode__one","title":"invalid path"}}`, false},
		{"plan without suffix", `{"toolCall":{"toolCallId":"exit_plan_mode__"}}`, false},
		{"another identifier", `{"toolCall":{"toolCallId":"exit_plan_mode_other"}}`, true},
		{"empty", `{}`, false},
		{"wrong identifier type", `{"toolCall":{"toolCallId":0}}`, false},
		{"malformed", `[`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.allow, permissionAllowsBypass(&providerkit.ParsedLine{Params: []byte(tc.params)}))
		})
	}
}

func geminiPlanRequest(t *testing.T, path string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 4, "method": "session/request_permission", "params": map[string]any{
		"sessionId": "root", "toolCall": map[string]any{"toolCallId": "exit_plan_mode__call", "title": geminiPlanTitlePrefix + path, "kind": "other", "status": "pending", "content": []any{}},
		"options": []map[string]string{{"optionId": "proceed_once", "name": "Allow", "kind": "allow_once"}, {"optionId": "cancel", "name": "Reject", "kind": "reject_once"}},
	}})
	require.NoError(t, err)
	return data
}

// An approval carries its plan settings and a refusal carries none, as the
// plan answer of AnswerControlRequest states them.
func TestGeminiPlanResponseKeepsTheNativeIdentityAndOptions(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		decision *leapmuxv1.PlanDecision
		option   string
		kind     agent.PlanModeControlKind
		feedback string
	}{
		{&leapmuxv1.PlanDecision{Approve: true, PermissionMode: contracts.GeminiModeYolo}, "proceed_once", agent.PlanModeControlExit, ""},
		{&leapmuxv1.PlanDecision{Feedback: "Split the second step."}, "cancel", agent.PlanModeControlNone, "Split the second step."},
	} {
		payload := geminiPlanRequest(t, "/private/native/plan.md")
		answer, err := agenttest.AnswerControl(geminiProvider{}, payload, "jsonrpc:4", agenttest.PlanDecision(test.decision))
		require.NoError(t, err)
		result := agenttest.ResolveAnswer(geminiProvider{}, payload, "jsonrpc:4", answer)
		require.False(t, result.Withhold)
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":4,"result":{"outcome":{"outcome":"selected","optionId":"`+test.option+`"}}}`, string(result.Content))
		assert.Equal(t, test.kind, result.PlanModeControl)
		assert.Equal(t, agent.FeedbackFollowUp(test.feedback), result.FollowUp)
	}
}

// The Worker resolves only an answer that addresses the stored request, so the
// answers here differ from a valid one in their decision or in the request.
func TestGeminiPlanResponseRejectsAnInvalidDecisionOrUnadvertisedOption(t *testing.T) {
	t.Parallel()
	valid := `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:4","response":{"behavior":"allow"}}}`
	request := geminiPlanRequest(t, "/private/native/plan.md")
	for _, response := range []string{strings.ReplaceAll(valid, `"allow"`, `"unknown"`), strings.ReplaceAll(valid, `"behavior":"allow"`, `"decision":"allow"`)} {
		result := agenttest.ResolveAnswer(geminiProvider{}, request, "jsonrpc:4", agent.ControlAnswer{Content: []byte(response)})
		assert.True(t, result.Withhold, response)
		assert.EqualError(t, result.Refusal(), agent.RefusalNoDecision, response)
	}
	result := agenttest.ResolveAnswer(geminiProvider{}, []byte(strings.ReplaceAll(string(request), "proceed_once", "different-option")), "jsonrpc:4", agent.ControlAnswer{Content: []byte(valid)})
	assert.True(t, result.Withhold)

	// A stored request that states no id gives the reply no id to answer.
	noID := strings.Replace(string(request), `"id":4,`, "", 1)
	require.NotEqual(t, string(request), noID)
	result = agenttest.ResolveAnswer(geminiProvider{}, []byte(noID), "jsonrpc:4", agent.ControlAnswer{Content: []byte(valid)})
	assert.True(t, result.Withhold)
	assert.EqualError(t, result.Refusal(), agent.RefusalUnreadableRequest)
}

type geminiPlanCapture struct {
	agent.ProviderServices
	requests []agent.ControlRequest
}

func (capture *geminiPlanCapture) PublishControlRequest(request agent.ControlRequest) error {
	capture.requests = append(capture.requests, request)
	return nil
}

func TestGeminiPlanPublicationPreservesNativeBytesAndLinksItsCompleteSource(t *testing.T) {
	t.Parallel()
	query, directory, _ := geminiStoreFixture(t)
	path := filepath.Join(filepath.Dir(directory), "root", "plans", "plan.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	plan := []byte("# Native plan\n\n1. Inspect the source.\n")
	require.NoError(t, os.WriteFile(path, plan, 0o600))
	sink := &agenttest.Sink{}
	capture := &geminiPlanCapture{ProviderServices: agent.NewProviderServices(sink)}
	services := &geminiControlServices{ProviderServices: capture, query: query, currentSession: func() string { return "root" }}
	transcript := newGeminiToolTranscript(t.Context(), services, query, nil)
	services.transcript = transcript
	request := geminiPlanRequest(t, path)
	var envelope struct {
		Params struct {
			ToolCall map[string]any `json:"toolCall"`
		} `json:"params"`
	}
	require.NoError(t, json.Unmarshal(request, &envelope))
	envelope.Params.ToolCall["sessionUpdate"] = "tool_call"
	original, err := json.Marshal(envelope.Params.ToolCall)
	require.NoError(t, err)
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: original}, agent.SpanInfo{SpanID: "exit_plan_mode__call", SpanType: "other"}))
	require.NoError(t, services.PublishControlRequest(agent.ControlRequest{RequestID: "jsonrpc:4", Payload: request, AgentSessionID: "root"}))
	require.Len(t, capture.requests, 1)
	assert.Equal(t, request, capture.requests[0].Payload)
	source, err := sink.ReadToolRequest("exit_plan_mode__call")
	require.NoError(t, err)
	require.NotNil(t, source)
	assert.Equal(t, original, source.Content.Original)
	assert.Equal(t, source.Seq, capture.requests[0].SourceSeq)
	assert.True(t, geminiPlanSupplementMatches(source.Content.Supplemental, path, plan))
}

func TestGeminiPlanReaderRejectsForeignPathsSymlinksAndInvalidContent(t *testing.T) {
	t.Parallel()
	query, directory, _ := geminiStoreFixture(t)
	for _, test := range []struct {
		name    string
		content []byte
	}{
		{"empty", nil}, {"invalid", []byte{0xff}}, {"oversize", make([]byte, geminiPlanReadLimit+1)},
	} {
		path := filepath.Join(query.WorkingDir, test.name+".md")
		require.NoError(t, os.WriteFile(path, test.content, 0o600))
		_, err := readGeminiPlan(query, "root", path)
		assert.Error(t, err, test.name)
	}
	foreign := filepath.Join(filepath.Dir(directory), "other", "plans", "plan.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(foreign), 0o700))
	require.NoError(t, os.WriteFile(foreign, []byte("foreign plan"), 0o600))
	_, err := readGeminiPlan(query, "root", foreign)
	assert.Error(t, err)
	link := filepath.Join(query.WorkingDir, "plan-link.md")
	require.NoError(t, os.Symlink(foreign, link))
	_, err = readGeminiPlan(query, "root", link)
	assert.Error(t, err)
	for _, path := range []string{"", "relative.md", filepath.Join(t.TempDir(), "foreign.md")} {
		_, err := readGeminiPlan(query, "root", path)
		assert.Error(t, err)
	}
}

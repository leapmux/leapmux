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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func geminiPlanRequest(t *testing.T, path string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 4, "method": "session/request_permission", "params": map[string]any{
		"sessionId": "root", "toolCall": map[string]any{"toolCallId": "exit_plan_mode__call", "title": geminiPlanTitlePrefix + path, "kind": "other", "status": "pending", "content": []any{}},
		"options": []map[string]string{{"optionId": "proceed_once", "name": "Allow", "kind": "allow_once"}, {"optionId": "cancel", "name": "Reject", "kind": "reject_once"}},
	}})
	require.NoError(t, err)
	return data
}

func TestGeminiPlanResponseKeepsTheNativeIdentityAndOptions(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		behavior string
		option   string
		kind     agent.PlanModeControlKind
		feedback string
	}{
		{agent.ControlBehaviorAllow, "proceed_once", agent.PlanModeControlExit, ""},
		{agent.ControlBehaviorDeny, "cancel", agent.PlanModeControlNone, "Split the second step."},
	} {
		response, err := json.Marshal(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": "jsonrpc:4", "response": map[string]string{"behavior": test.behavior, "message": test.feedback}}})
		require.NoError(t, err)
		result := geminiProvider{}.ResolveControlResponse(agent.ControlResponseContext{RequestID: "jsonrpc:4", RequestPayload: geminiPlanRequest(t, "/private/native/plan.md"), ResponseContent: response, PlanApproval: &leapmuxv1.PlanApprovalSettings{PermissionMode: contracts.GeminiModeYolo}})
		require.False(t, result.Withhold)
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":4,"result":{"outcome":{"outcome":"selected","optionId":"`+test.option+`"}}}`, string(result.Content))
		assert.Equal(t, test.kind, result.PlanModeControl)
		assert.Equal(t, test.feedback, result.Feedback)
	}
}

func TestGeminiPlanResponseRejectsAnInvalidDecisionOrUnadvertisedOption(t *testing.T) {
	t.Parallel()
	valid := `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:4","response":{"behavior":"allow"}}}`
	request := geminiPlanRequest(t, "/private/native/plan.md")
	for _, response := range []string{"null", "{", `{}`, strings.ReplaceAll(valid, "jsonrpc:4", "jsonrpc:other"), strings.ReplaceAll(valid, `"allow"`, `"unknown"`)} {
		result := geminiProvider{}.ResolveControlResponse(agent.ControlResponseContext{RequestID: "jsonrpc:4", RequestPayload: request, ResponseContent: []byte(response)})
		assert.True(t, result.Withhold)
	}
	result := geminiProvider{}.ResolveControlResponse(agent.ControlResponseContext{RequestID: "jsonrpc:4", RequestPayload: []byte(strings.ReplaceAll(string(request), "proceed_once", "different-option")), ResponseContent: []byte(valid)})
	assert.True(t, result.Withhold)
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

package codex

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveControlResponse_CodexApprovalPreservesTheResponse(t *testing.T) {
	t.Parallel()

	// A native command approval keeps its result. The answer addresses the
	// Worker's key of the request, and Codex receives its own id back.
	content := agent.JSONRPCResult("jsonrpc:7", map[string]any{"decision": "accept"})
	res := agenttest.ResolveAnswer(codexProvider{}, []byte(`{"jsonrpc":"2.0","id":7,"method":"item/commandExecution/requestApproval","params":{}}`), "jsonrpc:7",
		agent.ControlAnswer{Content: content})

	require.NoError(t, res.Refusal())
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{"decision":"accept"}}`, string(res.Content))
	assert.Equal(t, agent.PlanModeControlNone, res.PlanModeControl)
}

func TestResolveControlResponse_CodexPlanModePrompt(t *testing.T) {
	t.Parallel()

	// The synthesized plan-mode prompt frame carries no top-level method; its request.tool_name is
	// the pruned context, and the neutral allow/deny envelope is forwarded verbatim.
	content := []byte(`{"response":{"request_id":"plan-1","response":{"behavior":"allow"}}}`)
	res := agenttest.ResolveAnswer(codexProvider{}, []byte(`{"type":"control_request","request_id":"plan-1","request":{"tool_name":"`+contracts.CodexControlToolPlanModePrompt+`","input":{}}}`), "plan-1",
		agent.ControlAnswer{Content: content})

	assert.Equal(t, content, res.Content)
	assert.Equal(t, agent.PlanModeControlPrompt, res.PlanModeControl)
	assert.True(t, res.LeapMuxOwned, "the plan-mode prompt is one of LeapMux's own control requests")
}

func TestCodexResolveControlResponse_PreservesButWithholdsTheResponseForAMalformedRequest(t *testing.T) {
	t.Parallel()
	agenttest.AssertWithholdsTheResponseForAMalformedRequest(t, codexProvider{})
}

func TestCodexResolveControlResponse_PreservesTheResponseWithoutARequest(t *testing.T) {
	t.Parallel()
	agenttest.AssertPreservesTheResponseWithoutARequest(t, codexProvider{})
}

// TestResolveControlResponse_CodexAsyncQuestion reads the neutral answer to one
// LeapMux-owned async question. An allow with an answer queues the answer as a
// question answer. A deny skips the question: Codex keeps no pending question
// state, so a skip sends nothing. Each answer addresses the stored question, as
// every answer that the Worker resolves does.
func TestResolveControlResponse_CodexAsyncQuestion(t *testing.T) {
	t.Parallel()

	request := json.RawMessage(`{"type":"control_request","request_id":"codex-async-question-call_abc-0","request":{"tool_name":"CodexAsyncQuestion","input":{"question_item_id":"[\"request_user_input_async\",\"call_abc\",0]","title":"Which color?"}}}`)
	for _, test := range []struct {
		name        string
		content     string
		wantKind    leapmuxv1.AgentInputKind
		wantText    string
		wantRefusal string
	}{
		{name: "an allow with an answer", content: `{"response":{"request_id":"codex-async-question-call_abc-0","response":{"behavior":"allow","answer":"Red"}}}`, wantKind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER, wantText: "Red"},
		{name: "an allow with a blank answer", content: `{"response":{"request_id":"codex-async-question-call_abc-0","response":{"behavior":"allow","answer":"  "}}}`, wantRefusal: agent.RefusalUnreadableAnswer},
		{name: "a deny", content: `{"response":{"request_id":"codex-async-question-call_abc-0","response":{"behavior":"deny"}}}`},
		{name: "a behavior that is neither allow nor deny", content: `{"response":{"request_id":"codex-async-question-call_abc-0","response":{"behavior":"maybe","answer":"Red"}}}`, wantRefusal: agent.RefusalUnreadableAnswer},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			res := agenttest.ResolveAnswer(codexProvider{}, request, "codex-async-question-call_abc-0", agent.ControlAnswer{Content: []byte(test.content)})
			if test.wantRefusal != "" {
				assert.True(t, res.Withhold)
				assert.ErrorContains(t, res.Refusal(), test.wantRefusal)
				assert.False(t, res.LeapMuxOwned)
				assert.Equal(t, agent.ControlFollowUp{}, res.FollowUp)
				return
			}
			require.NoError(t, res.Refusal())
			assert.True(t, res.LeapMuxOwned, "the answer stays in LeapMux's own pipeline")
			assert.Equal(t, test.wantKind, res.FollowUp.Kind)
			assert.Equal(t, test.wantText, res.FollowUp.Text)
		})
	}
}

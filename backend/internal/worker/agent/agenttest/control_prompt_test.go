package agenttest

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// resolutionRecorder records the context of each resolution. ResolveAnswer
// calls only ResolveControlResponse, so the embedded Provider stays nil.
type resolutionRecorder struct {
	agent.Provider
	contexts   []agent.ControlResponseContext
	resolution agent.ControlResponseResolution
}

func (r *resolutionRecorder) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	r.contexts = append(r.contexts, ctx)
	return r.resolution
}

// The Worker states the tool name of the stored request and the plan
// settings of the answer, because a provider's resolution reads both.
// ResolveAnswer must state them as the Worker does.
func TestResolveAnswerStatesTheContextThatTheWorkerStates(t *testing.T) {
	t.Parallel()
	recorder := &resolutionRecorder{resolution: agent.ControlResponseResolution{Content: []byte(`{"native":true}`)}}
	payload := []byte(`{"type":"control_request","request_id":"req-1","request":{"tool_name":"ExitPlanMode","input":{"plan":"1. Edit."}}}`)
	settings := &leapmuxv1.PlanApprovalSettings{PermissionMode: "acceptEdits", ClearContext: true}
	answer := agent.ControlAnswer{Content: []byte(`{"type":"control_response"}`), PlanApproval: settings, FollowUp: "a note"}

	got := ResolveAnswer(recorder, payload, "req-1", answer)

	assert.Equal(t, recorder.resolution, got, "ResolveAnswer returns the provider's resolution")
	require.Len(t, recorder.contexts, 1)
	ctx := recorder.contexts[0]
	assert.Equal(t, "req-1", ctx.RequestID)
	assert.Equal(t, json.RawMessage(payload), ctx.RequestPayload)
	assert.Equal(t, answer.Content, ctx.ResponseContent)
	assert.Equal(t, "ExitPlanMode", ctx.ToolName, "the tool name comes from request.tool_name")
	assert.Same(t, settings, ctx.PlanApproval, "the plan settings come from the answer")
}

// A request that states no tool name, such as a JSON-RPC request, and an
// answer that applies no plan settings leave both fields empty.
func TestResolveAnswerStatesNoToolNameOrSettingsThatTheRequestAndTheAnswerLack(t *testing.T) {
	t.Parallel()
	recorder := &resolutionRecorder{}
	payload := []byte(`{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{"tool_name":"Bash"}}`)

	ResolveAnswer(recorder, payload, "jsonrpc:7", agent.ControlAnswer{Content: []byte(`{"jsonrpc":"2.0"}`)})

	require.Len(t, recorder.contexts, 1)
	ctx := recorder.contexts[0]
	assert.Equal(t, "jsonrpc:7", ctx.RequestID)
	assert.Equal(t, json.RawMessage(payload), ctx.RequestPayload)
	assert.Equal(t, []byte(`{"jsonrpc":"2.0"}`), ctx.ResponseContent)
	assert.Empty(t, ctx.ToolName, "a tool name outside request.tool_name is not the request's")
	assert.Nil(t, ctx.PlanApproval)
}

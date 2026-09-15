package agent

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// copilotAnswer builds the neutral envelope the browser's control surface sends.
func copilotAnswer(requestID string, response map[string]any) []byte {
	raw, _ := json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype": "success", "request_id": requestID, "response": response,
		},
	})
	return raw
}

// copilotNativeResponse reads back the value that reaches the runtime.
func copilotNativeResponse(t *testing.T, resolution ControlResponseResolution) map[string]any {
	t.Helper()
	require.False(t, resolution.Withhold, "the resolution withheld the forward")
	var envelope struct {
		Response struct {
			RequestID string         `json:"request_id"`
			Response  map[string]any `json:"response"`
		} `json:"response"`
	}
	require.NoError(t, json.Unmarshal(resolution.Content, &envelope))
	return envelope.Response.Response
}

func copilotResolve(t *testing.T, eventType string, data map[string]any, response map[string]any) ControlResponseResolution {
	t.Helper()
	return copilotPlugin(t).ResolveControlResponse(ControlResponseContext{
		RequestID:       "copilot-1",
		RequestPayload:  copilotStoredFrame(t, eventType, data),
		ResponseContent: copilotAnswer("copilot-1", response),
	})
}

// A single approval is the decision CP-007 verified. The runtime refuses the
// schema's own `approved` value, so the word LeapMux sends is `approve-once`.
func TestCopilotPermissionApprovalSendsTheVerifiedDecision(t *testing.T) {
	t.Parallel()
	resolution := copilotResolve(t, contracts.CopilotEventPermissionRequested,
		map[string]any{"requestId": "native-1", "permissionRequest": map[string]any{"kind": "read", "path": "/project/main.go"}},
		map[string]any{"behavior": "allow"})
	assert.Equal(t, map[string]any{"kind": "approve-once"}, copilotNativeResponse(t, resolution))
}

// A bare `approve-for-session` did not retain the read approval in CP-007, so the
// rule travels with every wider scope.
func TestCopilotSessionApprovalCarriesItsRule(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		request map[string]any
		want    map[string]any
	}{
		{
			name:    "read",
			request: map[string]any{"kind": "read", "path": "/project/main.go"},
			want:    map[string]any{"kind": "read"},
		},
		{
			name:    "write",
			request: map[string]any{"kind": "write", "fileName": "main.go", "canOfferSessionApproval": true},
			want:    map[string]any{"kind": "write"},
		},
		{
			name: "shell",
			request: map[string]any{
				"kind": "shell", "fullCommandText": "ls -l", "canOfferSessionApproval": true,
				"commands": []any{map[string]any{"identifier": "ls", "readOnly": true}},
			},
			want: map[string]any{"kind": "commands", "commandIdentifiers": []any{"ls"}},
		},
		{
			name:    "mcp",
			request: map[string]any{"kind": "mcp", "serverName": "docs", "toolName": "lookup"},
			want:    map[string]any{"kind": "mcp", "serverName": "docs", "toolName": "lookup"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			for scope, kind := range map[string]string{
				contracts.CopilotApprovalScopeSession: "approve-for-session",
				contracts.CopilotApprovalScopeProject: "approve-for-location",
			} {
				resolution := copilotResolve(t, contracts.CopilotEventPermissionRequested,
					map[string]any{"requestId": "native-1", "permissionRequest": test.request},
					map[string]any{"behavior": "allow", "scope": scope})
				answer := copilotNativeResponse(t, resolution)
				assert.Equal(t, kind, answer["kind"], scope)
				assert.Equal(t, test.want, answer["approval"], scope)
				// The location key is absent on purpose: only the runtime can resolve
				// a working directory into one, and this resolution is pure.
				assert.NotContains(t, answer, "locationKey", scope)
			}
		})
	}
}

// A request whose rule this build cannot construct withholds the forward. Sending a
// scope the runtime would not apply claims an approval that never happened.
func TestCopilotSessionApprovalWithholdsAnUnexpressibleRule(t *testing.T) {
	t.Parallel()
	for _, request := range []map[string]any{
		{"kind": "write", "canOfferSessionApproval": false},
		{"kind": "shell", "canOfferSessionApproval": true, "commands": []any{}},
		{"kind": "shell", "canOfferSessionApproval": true, "commands": []any{map[string]any{"identifier": ""}}},
		{"kind": "mcp"},
		{"kind": "future_kind"},
	} {
		for _, scope := range []string{contracts.CopilotApprovalScopeSession, contracts.CopilotApprovalScopeProject} {
			resolution := copilotResolve(t, contracts.CopilotEventPermissionRequested,
				map[string]any{"requestId": "native-1", "permissionRequest": request},
				map[string]any{"behavior": "allow", "scope": scope})
			assert.True(t, resolution.Withhold, request)
		}
	}
}

func TestCopilotPermissionRejectionCarriesItsFeedback(t *testing.T) {
	t.Parallel()
	resolution := copilotResolve(t, contracts.CopilotEventPermissionRequested,
		map[string]any{"requestId": "native-1", "permissionRequest": map[string]any{"kind": "read"}},
		map[string]any{"behavior": "deny", "message": "Read another file."})
	assert.Equal(t, map[string]any{"kind": "reject", "feedback": "Read another file."}, copilotNativeResponse(t, resolution))
}

// A refusal with no typed reason stays untyped: the placeholder the control surface
// sends collapses to nothing rather than reaching the runtime as the user's words.
func TestCopilotBarePermissionRejectionCarriesNoFeedback(t *testing.T) {
	t.Parallel()
	resolution := copilotResolve(t, contracts.CopilotEventPermissionRequested,
		map[string]any{"requestId": "native-1", "permissionRequest": map[string]any{"kind": "read"}},
		map[string]any{"behavior": "deny", "message": ControlRejectedByUserMessage})
	assert.Equal(t, map[string]any{"kind": "reject"}, copilotNativeResponse(t, resolution))
}

// The runtime accepts an explicit empty answer, and it distinguishes a typed answer
// from a selected choice. Both facts travel exactly as the user gave them.
func TestCopilotQuestionAnswerPreservesEmptyAndFreeformValues(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		response     map[string]any
		wantAnswer   string
		wantFreeform bool
	}{
		{"typed", map[string]any{"behavior": "allow", "answer": "Use the second option.", "wasFreeform": true}, "Use the second option.", true},
		{"selected", map[string]any{"behavior": "allow", "answer": "Option B", "wasFreeform": false}, "Option B", false},
		{"explicit empty", map[string]any{"behavior": "allow", "answer": "", "wasFreeform": true}, "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			resolution := copilotResolve(t, contracts.CopilotEventUserInputRequested,
				map[string]any{"requestId": "native-1", "question": "Which one?"}, test.response)
			answer := copilotNativeResponse(t, resolution)
			assert.Equal(t, test.wantAnswer, answer["answer"])
			assert.Equal(t, test.wantFreeform, answer["wasFreeform"])
		})
	}
}

// The runtime offers no decline for a question, so a refusal is the text the user
// typed instead of an answer.
func TestCopilotQuestionRejectionSendsTheTypedText(t *testing.T) {
	t.Parallel()
	resolution := copilotResolve(t, contracts.CopilotEventUserInputRequested,
		map[string]any{"requestId": "native-1", "question": "Which one?"},
		map[string]any{"behavior": "deny", "message": "Neither. Stop here."})
	assert.Equal(t, map[string]any{"answer": "Neither. Stop here.", "wasFreeform": true}, copilotNativeResponse(t, resolution))
}

func TestCopilotPlanDecisionCarriesTheRuntimeAction(t *testing.T) {
	t.Parallel()
	approved := copilotResolve(t, contracts.CopilotEventExitPlanModeRequested,
		map[string]any{"requestId": "native-1", "summary": "The plan", "recommendedAction": "interactive"},
		map[string]any{"behavior": "allow"})
	assert.Equal(t, map[string]any{"approved": true, "selectedAction": "interactive"}, copilotNativeResponse(t, approved))
	assert.Equal(t, PlanModeControlExit, approved.PlanModeControl)

	rejected := copilotResolve(t, contracts.CopilotEventExitPlanModeRequested,
		map[string]any{"requestId": "native-1", "summary": "The plan"},
		map[string]any{"behavior": "deny", "message": "Split the second step."})
	assert.Equal(t, map[string]any{"approved": false, "feedback": "Split the second step."}, copilotNativeResponse(t, rejected))
	assert.Equal(t, PlanModeControlExit, rejected.PlanModeControl)
}

// The form's own answer reaches the runtime whole, including a valid zero, false and
// empty value.
func TestCopilotElicitationPreservesTheFormAnswer(t *testing.T) {
	t.Parallel()
	content := map[string]any{"count": float64(0), "enabled": false, "text": ""}
	accepted := copilotResolve(t, contracts.CopilotEventElicitationRequested,
		map[string]any{"requestId": "native-1", "message": "Fill the form."},
		map[string]any{"action": contracts.MCPElicitationActionAccept, "content": content})
	answer := copilotNativeResponse(t, accepted)
	assert.Equal(t, contracts.MCPElicitationActionAccept, answer["action"])
	assert.Equal(t, content, answer["content"])

	for _, action := range []string{contracts.MCPElicitationActionDecline, contracts.MCPElicitationActionCancel} {
		resolution := copilotResolve(t, contracts.CopilotEventElicitationRequested,
			map[string]any{"requestId": "native-1", "message": "Fill the form."},
			map[string]any{"action": action})
		assert.Equal(t, map[string]any{"action": action}, copilotNativeResponse(t, resolution), action)
	}
}

// A decision this build cannot read withholds the forward. The runtime then keeps
// waiting and the request stays answerable, which a frame it cannot parse would not.
func TestCopilotWithholdsAnUnreadableDecision(t *testing.T) {
	t.Parallel()
	plugin := copilotPlugin(t)
	for name, response := range map[string][]byte{
		"no behavior":     copilotAnswer("copilot-1", map[string]any{"decision": "approve"}),
		"unknown action":  copilotAnswer("copilot-1", map[string]any{"action": "maybe"}),
		"another request": copilotAnswer("copilot-2", map[string]any{"behavior": "allow"}),
		"not json":        []byte("not json"),
	} {
		resolution := plugin.ResolveControlResponse(ControlResponseContext{
			RequestID:       "copilot-1",
			RequestPayload:  copilotStoredFrame(t, contracts.CopilotEventPermissionRequested, map[string]any{"requestId": "native-1", "permissionRequest": map[string]any{"kind": "read"}}),
			ResponseContent: response,
		})
		assert.True(t, resolution.Withhold, name)
	}
}

// A response for a request that is not Copilot's own keeps the shared default, so a
// row this plugin does not own is never rewritten.
func TestCopilotLeavesAnotherProvidersResponseAlone(t *testing.T) {
	t.Parallel()
	response := copilotAnswer("copilot-1", map[string]any{"behavior": "allow"})
	resolution := copilotPlugin(t).ResolveControlResponse(ControlResponseContext{
		RequestID:       "copilot-1",
		RequestPayload:  json.RawMessage(`{"method":"session/request_permission","params":{}}`),
		ResponseContent: response,
	})
	assert.False(t, resolution.Withhold)
	assert.Equal(t, PlanModeControlNone, resolution.PlanModeControl)
	assert.JSONEq(t, string(response), string(resolution.Content))
}

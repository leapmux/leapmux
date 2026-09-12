package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestResolveControlResponse_NoopKeepsRawResponse(t *testing.T) {
	t.Parallel()

	content := []byte(`{"id":1,"result":{"ok":true}}`)
	res := noopProvider{}.ResolveControlResponse(ControlResponseContext{ResponseContent: content})

	assert.Equal(t, content, res.Content)
	assert.False(t, res.SelfDisplayed)
	assert.Equal(t, PlanModeControlNone, res.PlanModeControl)
}

func TestControlResponsePreservesNativeAnswerBytes(t *testing.T) {
	t.Parallel()
	type nativeAnswerCase struct {
		name     string
		provider Provider
		request  string
		response string
	}
	cases := []nativeAnswerCase{
		{"codex questions", codexProvider{}, `{"id":7,"method":"item/tool/requestUserInput","params":{"questions":[{"id":"task","header":"Task"}]}}`, " {\"id\":7,\"result\":{\"answers\":{\"task\":{\"answers\":[\"Inspect\"]}}},\"unknown\":9007199254740993}\n"},
		{"cursor questions", acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR}, `{"id":7,"method":"cursor/ask_question","params":{"questions":[{"id":"color","prompt":"Choose","options":[{"id":"red","label":"Red"}]}]}}`, `{"id":7,"result":{"outcome":{"outcome":"answered","answers":[{"questionId":"color","selectedOptionIds":["red"]}]}}}`},
		{"opencode questions", acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE}, `{"type":"question.asked","properties":{"questions":[{"header":"Task"}]}}`, `{"id":7,"result":{"answers":[["Inspect"]]}}`},
		{"kilo questions", acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO}, `{"type":"question.asked","properties":{"questions":[{"header":"Task"}]}}`, `{"id":7,"result":{"answers":[["Inspect"]]}}`},
	}
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
	} {
		for _, options := range []string{`[]`, `[{"optionId":"once","name":"Allow once","kind":"allow_once"}]`} {
			cases = append(cases, nativeAnswerCase{provider.String() + options, acpProvider{provider: provider}, `{"id":7,"method":"session/request_permission","params":{"options":` + options + `}}`, `{"id":7,"result":{"outcome":{"optionId":"once"}}}`})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			response := []byte(tc.response)
			resolved := tc.provider.ResolveControlResponse(ControlResponseContext{RequestPayload: []byte(tc.request), ResponseContent: response})
			assert.Equal(t, response, resolved.Content)
			assert.False(t, resolved.Withhold)
			assert.Equal(t, PlanModeControlNone, resolved.PlanModeControl)
		})
	}
}

func TestResolveControlResponse_CodexApprovalPreservesTheResponse(t *testing.T) {
	t.Parallel()

	// Native command approval preserves the response bytes.
	content := []byte(`{"jsonrpc":"2.0","id":7,"result":{"decision":"accept"}}`)
	res := codexProvider{}.ResolveControlResponse(ControlResponseContext{
		RequestPayload:  []byte(`{"jsonrpc":"2.0","id":7,"method":"item/commandExecution/requestApproval","params":{}}`),
		ResponseContent: content,
	})

	assert.Equal(t, content, res.Content)
	assert.Equal(t, PlanModeControlNone, res.PlanModeControl)
}

func TestResolveControlResponse_CodexPlanModePrompt(t *testing.T) {
	t.Parallel()

	// The synthesized plan-mode prompt frame carries no top-level method; its request.tool_name is
	// the pruned context, and the neutral allow/deny envelope is forwarded verbatim.
	content := []byte(`{"response":{"request_id":"plan-1","response":{"behavior":"allow"}}}`)
	res := codexProvider{}.ResolveControlResponse(ControlResponseContext{
		RequestPayload:  []byte(`{"request":{"tool_name":"CodexPlanModePrompt"}}`),
		ResponseContent: content,
		ToolName:        ToolNameCodexPlanModePrompt,
	})

	assert.Equal(t, content, res.Content)
	assert.Equal(t, PlanModeControlPrompt, res.PlanModeControl)
}

func TestResolveControlResponse_ClaudeSelfDisplayAndPlanMode(t *testing.T) {
	t.Parallel()

	res := claudeProvider{}.ResolveControlResponse(ControlResponseContext{
		ResponseContent: []byte(`{"type":"control_response","response":{"request_id":"req-1","response":{"behavior":"allow"}}}`),
		ToolName:        ToolNameExitPlanMode,
	})

	assert.True(t, res.SelfDisplayed)
	assert.Equal(t, PlanModeControlExit, res.PlanModeControl)
	// The tool name is all the frontend needs to render Claude's Approved / Rejected / feedback.
}

func TestResolveControlResponse_CursorCreatePlanTransformsResponse(t *testing.T) {
	t.Parallel()

	res := acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR}.ResolveControlResponse(ControlResponseContext{
		RequestPayload: []byte(`{
			"jsonrpc":"2.0",
			"id":7,
			"method":"cursor/create_plan",
			"params":{}
		}`),
		ResponseContent: []byte(`{
			"response":{
				"request_id":"7",
				"response":{"behavior":"deny","message":"Needs tests."}
			}
		}`),
	})

	// The plan decision renders from the transformed outcome alone, so the pruned context is
	// method-only.
	var normalized struct {
		ID     int `json:"id"`
		Result struct {
			Outcome struct {
				Outcome string `json:"outcome"`
				Reason  string `json:"reason"`
			} `json:"outcome"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(res.Content, &normalized))
	assert.Equal(t, 7, normalized.ID)
	assert.Equal(t, "rejected", normalized.Result.Outcome.Outcome)
	assert.Equal(t, "Needs tests.", normalized.Result.Outcome.Reason)
}

func TestResolveControlResponse_CursorCreatePlanAcceptsResponse(t *testing.T) {
	t.Parallel()

	res := acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR}.ResolveControlResponse(ControlResponseContext{
		RequestPayload: []byte(`{
			"jsonrpc":"2.0",
			"id":"plan-7",
			"method":"cursor/create_plan",
			"params":{}
		}`),
		ResponseContent: []byte(`{
			"response":{
				"request_id":"plan-7",
				"response":{"behavior":"allow"}
			}
		}`),
	})

	var normalized struct {
		ID     string `json:"id"`
		Result struct {
			Outcome struct {
				Outcome string `json:"outcome"`
				Reason  string `json:"reason"`
			} `json:"outcome"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(res.Content, &normalized))
	assert.Equal(t, "plan-7", normalized.ID)
	assert.Equal(t, "accepted", normalized.Result.Outcome.Outcome)
	assert.Empty(t, normalized.Result.Outcome.Reason)
}

func TestResolveControlResponse_CursorCreatePlanRejectsDefaultMessageAsReject(t *testing.T) {
	t.Parallel()

	res := acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR}.ResolveControlResponse(ControlResponseContext{
		RequestPayload: []byte(`{
			"jsonrpc":"2.0",
			"id":"plan-7",
			"method":"cursor/create_plan",
			"params":{}
		}`),
		ResponseContent: []byte(`{
			"response":{
				"request_id":"plan-7",
				"response":{"behavior":"deny","message":"Rejected by user."}
			}
		}`),
	})

	var normalized struct {
		Result struct {
			Outcome struct {
				Outcome string `json:"outcome"`
				Reason  string `json:"reason"`
			} `json:"outcome"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(res.Content, &normalized))
	assert.Equal(t, "rejected", normalized.Result.Outcome.Outcome)
	assert.Empty(t, normalized.Result.Outcome.Reason)
}

func TestResolveControlResponse_CursorCreatePlanIgnoresMalformedEnvelope(t *testing.T) {
	t.Parallel()

	// The response isn't the neutral envelope, so the transform bails and the create-plan request
	// falls through to the ACP permission context -- which has no options, so it degrades to
	// method-only. The raw response is forwarded unchanged.
	content := []byte(`{"jsonrpc":"2.0","id":7,"result":{"outcome":{"outcome":"rejected","reason":"No"}}}`)
	res := acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR}.ResolveControlResponse(ControlResponseContext{
		RequestPayload: []byte(`{
			"jsonrpc":"2.0",
			"id":7,
			"method":"cursor/create_plan",
			"params":{}
		}`),
		ResponseContent: content,
	})

	assert.Equal(t, content, res.Content)
}

func TestResolveControlResponse_PiPreservesTheResponse(t *testing.T) {
	t.Parallel()

	confirmed := true
	response, err := json.Marshal(map[string]interface{}{"confirmed": confirmed})
	require.NoError(t, err)

	res := piProvider{}.ResolveControlResponse(ControlResponseContext{
		RequestPayload:  []byte(`{"method":"confirm"}`),
		ResponseContent: response,
	})

	assert.Equal(t, response, res.Content)
}

func TestResolveControlResponse_PreservesTheResponseWithoutARequest(t *testing.T) {
	t.Parallel()

	// An absent request must not change the response bytes.
	content := []byte(`{"jsonrpc":"2.0","id":7,"result":{"decision":"accept"}}`)
	cases := map[string]Provider{
		"codex":  codexProvider{},
		"pi":     piProvider{},
		"acp":    acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE},
		"claude": claudeProvider{}, // Claude keys context off ToolName, empty here
	}
	for name, provider := range cases {
		t.Run(name, func(t *testing.T) {
			res := provider.ResolveControlResponse(ControlResponseContext{ResponseContent: content})
			assert.Equal(t, content, res.Content)
		})
	}
}

func TestResolveControlResponse_PreservesButWithholdsTheResponseForAMalformedRequest(t *testing.T) {
	t.Parallel()

	// A corrupt request prevents native ID validation. Keep the response bytes for recovery.
	content := []byte(`{"jsonrpc":"2.0","id":7,"result":{"decision":"accept"}}`)
	for name, provider := range map[string]Provider{
		"codex": codexProvider{},
		"pi":    piProvider{},
		"acp":   acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE},
	} {
		t.Run(name, func(t *testing.T) {
			res := provider.ResolveControlResponse(ControlResponseContext{
				RequestPayload:  []byte(`not json`),
				ResponseContent: content,
			})
			assert.Equal(t, content, res.Content)
			assert.True(t, res.Withhold)
		})
	}
}

func TestControlResponseRequestID(t *testing.T) {
	t.Parallel()

	// Both wire shapes are cross-provider, so every provider must extract the same id from the same
	// bytes -- run each case over the provider map to pin "identical across providers" as a property,
	// not an accident of one provider's resolver.
	providers := map[string]Provider{
		"noop":   noopProvider{},
		"claude": claudeProvider{},
		"codex":  codexProvider{},
		"pi":     piProvider{},
		"acp":    acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE},
	}
	cases := []struct {
		name    string
		content string
		want    string
	}{
		// Neutral approve/reject envelope: response.request_id.
		{"envelope", `{"response":{"request_id":"req-1","response":{"behavior":"allow"}}}`, "req-1"},
		// Mixed envelopes still belong to the nested control response. A top-level JSON-RPC id can be
		// present for provider plumbing, but the pending control_request row is keyed by
		// response.request_id, so the nested id wins.
		{"mixed nested wins", `{"id":"jsonrpc-req","response":{"request_id":"req-1","response":{"behavior":"allow"}}}`, "req-1"},
		// JSON-RPC numeric id (ACP family).
		{"jsonrpc numeric", `{"jsonrpc":"2.0","id":5,"result":{"outcome":{"outcome":"selected","optionId":"once"}}}`, "5"},
		// JSON-RPC string id.
		{"jsonrpc string", `{"jsonrpc":"2.0","id":"abc-123","result":{"outcome":{"outcome":"selected","optionId":"reject"}}}`, "abc-123"},
		{"no id", `{"type":"unknown"}`, ""},
		{"null id", `{"id":null}`, ""},
		{"invalid json", `not json`, ""},
	}
	for providerName, provider := range providers {
		for _, tc := range cases {
			t.Run(providerName+"/"+tc.name, func(t *testing.T) {
				assert.Equal(t, tc.want, provider.ControlResponseRequestID([]byte(tc.content)))
			})
		}
	}
}

func TestWarnUnmarshal(t *testing.T) {
	t.Parallel()

	var ok struct {
		Method string `json:"method"`
	}
	assert.True(t, warnUnmarshal([]byte(`{"method":"m"}`), &ok, "test"), "valid JSON decodes and returns true")
	assert.Equal(t, "m", ok.Method)

	var bad struct{}
	assert.False(t, warnUnmarshal([]byte(`not json`), &bad, "test"), "malformed JSON returns false")
}

func TestDecodeControlBehavior(t *testing.T) {
	t.Parallel()

	// A real deny with a typed reason: request id + behavior + message all surface, trimmed.
	id, behavior, message, ok := DecodeControlBehavior([]byte(
		`{"response":{"request_id":" req-1 ","response":{"behavior":" deny ","message":" not this way "}}}`))
	assert.True(t, ok)
	assert.Equal(t, "req-1", id)
	assert.Equal(t, ControlBehaviorDeny, behavior)
	assert.Equal(t, "not this way", message)

	// The ControlRejectedByUserMessage placeholder is collapsed to "" -- a bare rejection carries
	// no reason, so both the Codex feedback path and the Cursor transform treat it as empty.
	_, _, message, ok = DecodeControlBehavior([]byte(
		`{"response":{"request_id":"req-2","response":{"behavior":"deny","message":"Rejected by user."}}}`))
	assert.True(t, ok)
	assert.Empty(t, message, "the ControlRejectedByUserMessage placeholder collapses to \"\"")

	// An allow with no message.
	_, behavior, message, ok = DecodeControlBehavior([]byte(`{"response":{"request_id":"req-3","response":{"behavior":"allow"}}}`))
	assert.True(t, ok)
	assert.Equal(t, ControlBehaviorAllow, behavior)
	assert.Empty(t, message)

	// Malformed JSON: ok is false and every field is empty.
	id, behavior, message, ok = DecodeControlBehavior([]byte(`not json`))
	assert.False(t, ok)
	assert.Empty(t, id)
	assert.Empty(t, behavior)
	assert.Empty(t, message)
}

func TestNormalizeRejectionMessage(t *testing.T) {
	t.Parallel()

	// A typed reason surfaces trimmed.
	assert.Equal(t, "not this way", NormalizeRejectionMessage("  not this way  "))
	// The auto-filled placeholder collapses to "" (no real feedback), including when padded.
	assert.Empty(t, NormalizeRejectionMessage(ControlRejectedByUserMessage))
	assert.Empty(t, NormalizeRejectionMessage("  "+ControlRejectedByUserMessage+"  "))
	// A genuinely empty / whitespace reason is "".
	assert.Empty(t, NormalizeRejectionMessage(""))
	assert.Empty(t, NormalizeRejectionMessage("   \n "))
	// DecodeControlBehavior and NormalizeRejectionMessage apply the SAME rule -- the sentinel
	// collapse must not drift between the raw-bytes decoder and the shared helper.
	_, _, decoded, ok := DecodeControlBehavior([]byte(
		`{"response":{"request_id":"r","response":{"behavior":"deny","message":"  ` + ControlRejectedByUserMessage + `  "}}}`))
	assert.True(t, ok)
	assert.Equal(t, NormalizeRejectionMessage("  "+ControlRejectedByUserMessage+"  "), decoded)
}

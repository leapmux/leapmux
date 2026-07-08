package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestResolveControlResponse_NoopKeepsRawResponse(t *testing.T) {
	content := []byte(`{"id":1,"result":{"ok":true}}`)
	res := noopProvider{}.ResolveControlResponse(ControlResponseContext{ResponseContent: content})

	assert.Equal(t, content, res.Content)
	assert.Nil(t, res.RequestContext)
	assert.False(t, res.SelfDisplayed)
	assert.Equal(t, PlanModeControlNone, res.PlanModeControl)
}

func TestResolveControlResponse_CodexApprovalRequestContext(t *testing.T) {
	// A Codex command-approval decision: the native response is forwarded verbatim, and the pruned
	// request context is just the method -- the frontend maps result.decision to a label itself.
	content := []byte(`{"jsonrpc":"2.0","id":7,"result":{"decision":"accept"}}`)
	res := codexProvider{}.ResolveControlResponse(ControlResponseContext{
		RequestPayload:  []byte(`{"jsonrpc":"2.0","id":7,"method":"item/commandExecution/requestApproval","params":{}}`),
		ResponseContent: content,
	})

	assert.Equal(t, content, res.Content)
	assert.JSONEq(t, `{"method":"item/commandExecution/requestApproval"}`, string(res.RequestContext))
	assert.Equal(t, PlanModeControlNone, res.PlanModeControl)
}

func TestResolveControlResponse_CodexUserInputRequestContext(t *testing.T) {
	// requestUserInput keeps the question id + header the frontend labels its answer values with.
	res := codexProvider{}.ResolveControlResponse(ControlResponseContext{
		RequestPayload: []byte(`{
			"jsonrpc":"2.0",
			"id":7,
			"method":"item/tool/requestUserInput",
			"params":{"questions":[{"header":"Task","id":"task"},{"header":"Reason","id":"reason"}]}
		}`),
		ResponseContent: []byte(`{
			"jsonrpc":"2.0",
			"id":7,
			"result":{"answers":{"task":{"answers":["Inspect"]},"reason":{"answers":["Parity"]}}}
		}`),
	})

	assert.JSONEq(t, `{
		"method":"item/tool/requestUserInput",
		"params":{"questions":[{"id":"task","header":"Task"},{"id":"reason","header":"Reason"}]}
	}`, string(res.RequestContext))
	assert.Equal(t, PlanModeControlNone, res.PlanModeControl)
}

func TestResolveControlResponse_CodexPlanModePrompt(t *testing.T) {
	// The synthesized plan-mode prompt frame carries no top-level method; its request.tool_name is
	// the pruned context, and the neutral allow/deny envelope is forwarded verbatim.
	content := []byte(`{"response":{"request_id":"plan-1","response":{"behavior":"allow"}}}`)
	res := codexProvider{}.ResolveControlResponse(ControlResponseContext{
		RequestPayload:  []byte(`{"request":{"tool_name":"CodexPlanModePrompt"}}`),
		ResponseContent: content,
		ToolName:        ToolNameCodexPlanModePrompt,
	})

	assert.Equal(t, content, res.Content)
	assert.JSONEq(t, `{"request":{"tool_name":"CodexPlanModePrompt"}}`, string(res.RequestContext))
	assert.Equal(t, PlanModeControlPrompt, res.PlanModeControl)
}

func TestResolveControlResponse_ClaudeSelfDisplayAndPlanMode(t *testing.T) {
	res := claudeProvider{}.ResolveControlResponse(ControlResponseContext{
		ResponseContent: []byte(`{"type":"control_response","response":{"request_id":"req-1","response":{"behavior":"allow"}}}`),
		ToolName:        ToolNameExitPlanMode,
	})

	assert.True(t, res.SelfDisplayed)
	assert.Equal(t, PlanModeControlExit, res.PlanModeControl)
	// The tool name is all the frontend needs to render Claude's Approved / Rejected / feedback.
	assert.JSONEq(t, `{"request":{"tool_name":"ExitPlanMode"}}`, string(res.RequestContext))
}

func TestResolveControlResponse_CursorCreatePlanTransformsResponse(t *testing.T) {
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
	assert.JSONEq(t, `{"method":"cursor/create_plan"}`, string(res.RequestContext))
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

	assert.JSONEq(t, `{"method":"cursor/create_plan"}`, string(res.RequestContext))
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

	assert.JSONEq(t, `{"method":"cursor/create_plan"}`, string(res.RequestContext))
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
	assert.JSONEq(t, `{"method":"cursor/create_plan"}`, string(res.RequestContext))
}

func TestResolveControlResponse_CursorQuestionRequestContext(t *testing.T) {
	// Cursor AskQuestion keeps the question prompts and the option id->label map the frontend needs
	// to render selected option ids as their labels.
	res := acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR}.ResolveControlResponse(ControlResponseContext{
		RequestPayload: []byte(`{
			"jsonrpc":"2.0","id":7,"method":"` + CursorMethodAskQuestion + `",
			"params":{"questions":[
				{"id":"q1","prompt":"Pick a color","options":[{"id":"o1","label":"Red"},{"id":"o2","label":"Blue"}]},
				{"id":"q2","prompt":"Pick a size","options":[{"id":"s1","label":"Large"}]}
			]}
		}`),
		ResponseContent: []byte(`{
			"jsonrpc":"2.0","id":7,
			"result":{"outcome":{"outcome":"answered","answers":[
				{"questionId":"q1","selectedOptionIds":["o1","o2"]},
				{"questionId":"q2","selectedOptionIds":["s1"]}
			]}}
		}`),
	})

	assert.JSONEq(t, `{
		"method":"cursor/ask_question",
		"params":{"questions":[
			{"id":"q1","prompt":"Pick a color","options":[{"id":"o1","label":"Red"},{"id":"o2","label":"Blue"}]},
			{"id":"q2","prompt":"Pick a size","options":[{"id":"s1","label":"Large"}]}
		]}
	}`, string(res.RequestContext))
}

func TestResolveControlResponse_OpenCodeQuestionRequestContext(t *testing.T) {
	// The OpenCode/Kilo question context keeps the question headers the frontend labels its answer
	// values with. Resolve through ProviderFor (not an inline literal) so the test exercises the
	// real registration: the question hook is set only for OpenCode/Kilo in init().
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
	} {
		t.Run(provider.String(), func(t *testing.T) {
			res := ProviderFor(provider).ResolveControlResponse(ControlResponseContext{
				RequestPayload: []byte(`{
					"type":"question.asked",
					"properties":{"questions":[{"header":"Task"},{"header":"Env"}]}
				}`),
				ResponseContent: []byte(`{"jsonrpc":"2.0","id":"q1","result":{"answers":[["Build"],["Dev"]]}}`),
			})

			assert.JSONEq(t, `{
				"type":"question.asked",
				"properties":{"questions":[{"header":"Task"},{"header":"Env"}]}
			}`, string(res.RequestContext))
		})
	}
}

func TestResolveControlResponse_OpenCodeQuestionDispatchIsRegistrationDriven(t *testing.T) {
	// The OpenCode-protocol `question.asked` request context must dispatch through the
	// registration-time questionRequestContext hook (set only for OpenCode and Kilo in init()), NOT a
	// provider-enum allowlist in ResolveControlResponse. This is the backend mirror of the frontend's
	// registerOpenCodeProtocolProvider membership: keeping "who speaks the question protocol" at the
	// single registration site stops a second source of truth from drifting.
	//
	// The fixture deliberately carries BOTH wire shapes at once -- an OpenCode `question.asked`
	// question AND an ACP permission method/options -- so the two dispatch paths yield DISTINCT
	// request context. A provider WITH the hook prunes the question context; a provider WITHOUT it
	// falls through to the permission context. That divergence is what makes case (b) a real
	// regression guard: it fails if someone re-adds a non-question provider to an enum allowlist, or
	// wires the hook onto a provider that shouldn't have it.
	requestPayload := []byte(`{
		"type":"question.asked",
		"properties":{"questions":[{"header":"Task"}]},
		"method":"session/request_permission",
		"params":{"options":[{"optionId":"proceed_once","name":"Allow once"}]}
	}`)
	responseContent := []byte(`{"result":{"answers":[["Build"]],"outcome":{"optionId":"proceed_once"}}}`)

	questionContext := `{"type":"question.asked","properties":{"questions":[{"header":"Task"}]}}`
	permissionContext := `{"method":"session/request_permission","params":{"options":[{"optionId":"proceed_once","name":"Allow once"}]}}`

	// (a) Both providers registered WITH the hook prune the OpenCode question context.
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
	} {
		t.Run(provider.String()+"_uses_question_hook", func(t *testing.T) {
			res := ProviderFor(provider).ResolveControlResponse(ControlResponseContext{
				RequestPayload:  requestPayload,
				ResponseContent: responseContent,
			})
			assert.JSONEq(t, questionContext, string(res.RequestContext))
		})
	}

	// (b) An ACP provider registered WITHOUT the hook falls through to the ACP permission context for
	// the very same `question.asked` payload -- it never invokes opencodeQuestionRequestContext.
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
	} {
		t.Run(provider.String()+"_falls_through_to_permission", func(t *testing.T) {
			res := ProviderFor(provider).ResolveControlResponse(ControlResponseContext{
				RequestPayload:  requestPayload,
				ResponseContent: responseContent,
			})
			assert.JSONEq(t, permissionContext, string(res.RequestContext))
		})
	}
}

func TestResolveControlResponse_ACPPermissionRequestContext(t *testing.T) {
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
	} {
		t.Run(provider.String(), func(t *testing.T) {
			res := acpProvider{provider: provider}.ResolveControlResponse(ControlResponseContext{
				RequestPayload: []byte(`{
					"jsonrpc":"2.0",
					"id":7,
					"method":"session/request_permission",
					"params":{"options":[
						{"optionId":"proceed_once","name":"Allow once","kind":"allow_once"},
						{"optionId":"reject","name":"Reject","kind":"reject_once"}
					]}
				}`),
				ResponseContent: []byte(`{"jsonrpc":"2.0","id":7,"result":{"outcome":{"optionId":"proceed_once"}}}`),
			})

			// Options keep optionId + name -- the minimal context the frontend matches the selected
			// id against to render its label; the selected optionId itself lives in the native
			// response. The request's `kind` is intentionally pruned away (the frontend never reads it).
			assert.JSONEq(t, `{
				"method":"session/request_permission",
				"params":{"options":[
					{"optionId":"proceed_once","name":"Allow once"},
					{"optionId":"reject","name":"Reject"}
				]}
			}`, string(res.RequestContext))
		})
	}
}

func TestResolveControlResponse_ACPPermissionRequestContextWithoutOptions(t *testing.T) {
	// A permission request that carries no options (e.g. a Cursor create-plan request that fell
	// through the transform) degrades to method-only context rather than an empty options list.
	res := acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE}.ResolveControlResponse(ControlResponseContext{
		RequestPayload:  []byte(`{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{}}`),
		ResponseContent: []byte(`{"jsonrpc":"2.0","id":7,"result":{"outcome":{"optionId":"proceed_once"}}}`),
	})

	assert.JSONEq(t, `{"method":"session/request_permission"}`, string(res.RequestContext))
}

func TestResolveControlResponse_PiRequestContext(t *testing.T) {
	confirmed := true
	response, err := json.Marshal(map[string]interface{}{"confirmed": confirmed})
	require.NoError(t, err)

	res := piProvider{}.ResolveControlResponse(ControlResponseContext{
		RequestPayload:  []byte(`{"method":"confirm"}`),
		ResponseContent: response,
	})

	assert.Equal(t, response, res.Content)
	assert.JSONEq(t, `{"method":"confirm"}`, string(res.RequestContext))
}

func TestResolveControlResponse_EmptyRequestPayloadYieldsNilContext(t *testing.T) {
	// Every resolver returns nil RequestContext when it has no stored request to prune (the request
	// row was already deleted, or never captured) -- the row then persists with `request` omitted.
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
			assert.Nil(t, res.RequestContext)
			assert.Equal(t, content, res.Content)
		})
	}
}

func TestResolveControlResponse_MalformedRequestPayloadYieldsNilContext(t *testing.T) {
	// A stored request that doesn't parse as JSON leaves RequestContext nil (warnUnmarshal fails)
	// without dropping the forwarded response.
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
			assert.Nil(t, res.RequestContext)
			assert.Equal(t, content, res.Content)
		})
	}
}

func TestWarnUnmarshal(t *testing.T) {
	var ok struct {
		Method string `json:"method"`
	}
	assert.True(t, warnUnmarshal([]byte(`{"method":"m"}`), &ok, "test"), "valid JSON decodes and returns true")
	assert.Equal(t, "m", ok.Method)

	var bad struct{}
	assert.False(t, warnUnmarshal([]byte(`not json`), &bad, "test"), "malformed JSON returns false")
}

func TestDecodeControlBehavior(t *testing.T) {
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

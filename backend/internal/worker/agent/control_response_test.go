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
	assert.Empty(t, res.DisplayText)
	assert.False(t, res.SelfDisplayed)
	assert.Equal(t, PlanModeControlNone, res.PlanModeControl)
}

func TestResolveControlResponse_CodexUserInputAnswer(t *testing.T) {
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

	assert.Equal(t, "Task: Inspect\nReason: Parity", res.DisplayText)
	assert.Equal(t, PlanModeControlNone, res.PlanModeControl)
}

func TestResolveControlResponse_CodexUserInputAnswerStableExtraOrdering(t *testing.T) {
	// The response carries answer keys NOT present in the request's questions ("alpha", "mango",
	// "zebra"). Those are absent from the request-derived order, so they must render in a STABLE
	// (sorted) order rather than Go's randomized map-iteration order -- otherwise the same control
	// response would render its trailing lines differently across renders.
	res := codexProvider{}.ResolveControlResponse(ControlResponseContext{
		RequestPayload: []byte(`{
			"jsonrpc":"2.0","id":7,"method":"item/tool/requestUserInput",
			"params":{"questions":[{"header":"Task","id":"task"}]}
		}`),
		ResponseContent: []byte(`{
			"jsonrpc":"2.0","id":7,
			"result":{"answers":{
				"task":{"answers":["Inspect"]},
				"zebra":{"answers":["Z"]},
				"alpha":{"answers":["A"]},
				"mango":{"answers":["M"]}
			}}
		}`),
	})

	// The request-ordered question first, then the extra keys alphabetically.
	assert.Equal(t, "Task: Inspect\nalpha: A\nmango: M\nzebra: Z", res.DisplayText)
}

func TestResolveControlResponse_CodexPlanModePrompt(t *testing.T) {
	content := []byte(`{"response":{"request_id":"plan-1","response":{"behavior":"allow"}}}`)
	res := codexProvider{}.ResolveControlResponse(ControlResponseContext{
		RequestPayload:  []byte(`{"request":{"tool_name":"CodexPlanModePrompt"}}`),
		ResponseContent: content,
		ToolName:        ToolNameCodexPlanModePrompt,
	})

	assert.Equal(t, content, res.Content)
	assert.Empty(t, res.DisplayText)
	assert.Equal(t, PlanModeControlPrompt, res.PlanModeControl)
}

func TestResolveControlResponse_CodexFeedbackMessage(t *testing.T) {
	content := []byte(`{"response":{"response":{"behavior":"deny","message":"Add tests first."}}}`)
	res := codexProvider{}.ResolveControlResponse(ControlResponseContext{
		RequestPayload:  []byte(`{"method":"item/commandExecution/requestApproval"}`),
		ResponseContent: content,
	})

	assert.Equal(t, content, res.Content)
	assert.Equal(t, "Add tests first.", res.DisplayText)
	assert.Equal(t, PlanModeControlNone, res.PlanModeControl)
}

func TestResolveControlResponse_CodexDecisionDisplayText(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		want    string
	}{
		{
			name:    "accept",
			content: []byte(`{"jsonrpc":"2.0","id":7,"result":{"decision":"accept"}}`),
			want:    "Allow",
		},
		{
			name:    "decline",
			content: []byte(`{"jsonrpc":"2.0","id":7,"result":{"decision":"decline"}}`),
			want:    "Reject",
		},
		{
			name:    "exec policy amendment",
			content: []byte(`{"jsonrpc":"2.0","id":7,"result":{"decision":{"acceptWithExecpolicyAmendment":{"match":"npm test"}}}}`),
			want:    "Allow & Remember",
		},
		{
			name:    "network policy amendment",
			content: []byte(`{"jsonrpc":"2.0","id":7,"result":{"decision":{"applyNetworkPolicyAmendment":true}}}`),
			want:    "Apply Network Policy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := codexProvider{}.ResolveControlResponse(ControlResponseContext{
				RequestPayload:  []byte(`{"method":"item/commandExecution/requestApproval"}`),
				ResponseContent: tt.content,
			})

			assert.Equal(t, tt.want, res.DisplayText)
		})
	}
}

func TestResolveControlResponse_ClaudeSelfDisplayAndPlanMode(t *testing.T) {
	res := claudeProvider{}.ResolveControlResponse(ControlResponseContext{
		ResponseContent: []byte(`{"type":"control_response","response":{"request_id":"req-1","response":{"behavior":"allow"}}}`),
		ToolName:        ToolNameExitPlanMode,
	})

	assert.True(t, res.SelfDisplayed)
	assert.Equal(t, PlanModeControlExit, res.PlanModeControl)
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

	require.Equal(t, "Needs tests.", res.DisplayText)
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

	require.Equal(t, "Accept", res.DisplayText)
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

	require.Equal(t, "Reject", res.DisplayText)
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
	assert.Empty(t, res.DisplayText)
}

func TestResolveControlResponse_CursorQuestionAnsweredMapsOptionLabels(t *testing.T) {
	// Cursor AskQuestion "answered": each selected optionId maps to its option LABEL, formatted as
	// "prompt: label1, label2" per question in request order. This exercises cursorQuestionAnswersText's
	// answered path (option-label mapping + labeled-line formatting), which had no direct coverage.
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

	assert.Equal(t, "Pick a color: Red, Blue\nPick a size: Large", res.DisplayText)
}

func TestResolveControlResponse_OpenCodeStyleQuestionAnswer(t *testing.T) {
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
	} {
		t.Run(provider.String(), func(t *testing.T) {
			// Resolve through ProviderFor (not an inline acpProvider literal) so the test exercises
			// the real registration: the OpenCode question summary now dispatches through the
			// questionAnswersText hook that init() sets only for OpenCode/Kilo.
			res := ProviderFor(provider).ResolveControlResponse(ControlResponseContext{
				RequestPayload: []byte(`{
					"type":"question.asked",
					"properties":{"questions":[{"header":"Task"},{"header":"Env"}]}
				}`),
				ResponseContent: []byte(`{"jsonrpc":"2.0","id":"q1","result":{"answers":[["Build"],["Dev"]]}}`),
			})

			assert.Equal(t, "Task: Build\nEnv: Dev", res.DisplayText)
		})
	}
}

func TestResolveControlResponse_OpenCodeStyleQuestionReject(t *testing.T) {
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
	} {
		t.Run(provider.String(), func(t *testing.T) {
			res := ProviderFor(provider).ResolveControlResponse(ControlResponseContext{
				RequestPayload: []byte(`{
					"type":"question.asked",
					"properties":{"questions":[{"header":"Task"}]}
				}`),
				ResponseContent: []byte(`{"jsonrpc":"2.0","id":"q1","result":{"rejected":true}}`),
			})

			assert.Equal(t, "Reject", res.DisplayText)
		})
	}
}

func TestResolveControlResponse_OpenCodeDropsEmptyAnswerValues(t *testing.T) {
	// Edge case for the shared labeledAnswerLine formatting: an answer whose values are all
	// empty/whitespace produces NO line (never a dangling "Env: "), and the question is omitted
	// entirely -- while a sibling question with a real value still renders.
	res := ProviderFor(leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE).ResolveControlResponse(ControlResponseContext{
		RequestPayload:  []byte(`{"type":"question.asked","properties":{"questions":[{"header":"Task"},{"header":"Env"}]}}`),
		ResponseContent: []byte(`{"result":{"answers":[["Build"],["  ",""]]}}`),
	})

	assert.Equal(t, "Task: Build", res.DisplayText)
}

func TestResolveControlResponse_OpenCodeQuestionDispatchIsRegistrationDriven(t *testing.T) {
	// The OpenCode-protocol `question.asked` answer summary must dispatch through the
	// registration-time questionAnswersText hook (set only for OpenCode and Kilo in init()), NOT a
	// provider-enum allowlist in ResolveControlResponse. This is the backend mirror of the frontend's
	// registerOpenCodeProtocolProvider membership: keeping "who speaks the question protocol" at the
	// single registration site stops a second source of truth from drifting.
	//
	// The fixture deliberately carries BOTH wire shapes at once -- an OpenCode `question.asked`
	// question/answer AND an ACP permission option/outcome -- so the two dispatch paths yield DISTINCT
	// display text. A provider WITH the hook renders the question summary ("Task: Build"); a provider
	// WITHOUT it falls through to the permission summary ("Allow once"). That divergence is what makes
	// case (b) a real regression guard: it fails if someone re-adds a non-question provider to an enum
	// allowlist, or wires the hook onto a provider that shouldn't have it -- either way the wrong
	// provider would render "Task: Build" instead of "Allow once".
	requestPayload := []byte(`{
		"type":"question.asked",
		"properties":{"questions":[{"header":"Task"}]},
		"params":{"options":[{"optionId":"proceed_once","name":"Allow once"}]}
	}`)
	responseContent := []byte(`{"result":{"answers":[["Build"]],"outcome":{"optionId":"proceed_once"}}}`)

	// (a) Both providers registered WITH the hook render the OpenCode question summary.
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
	} {
		t.Run(provider.String()+"_uses_question_hook", func(t *testing.T) {
			res := ProviderFor(provider).ResolveControlResponse(ControlResponseContext{
				RequestPayload:  requestPayload,
				ResponseContent: responseContent,
			})
			assert.Equal(t, "Task: Build", res.DisplayText)
		})
	}

	// (b) An ACP provider registered WITHOUT the hook falls through to the ACP permission summary for
	// the very same `question.asked` payload -- it never invokes opencodeQuestionAnswersText.
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
	} {
		t.Run(provider.String()+"_falls_through_to_permission", func(t *testing.T) {
			res := ProviderFor(provider).ResolveControlResponse(ControlResponseContext{
				RequestPayload:  requestPayload,
				ResponseContent: responseContent,
			})
			assert.Equal(t, "Allow once", res.DisplayText)
			assert.NotEqual(t, "Task: Build", res.DisplayText,
				"a provider without the questionAnswersText hook must not render the OpenCode question summary")
		})
	}
}

func TestResolveControlResponse_ACPPermissionLabels(t *testing.T) {
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
					"params":{"options":[{"optionId":"proceed_once","name":"Allow once"}]}
				}`),
				ResponseContent: []byte(`{"jsonrpc":"2.0","id":7,"result":{"outcome":{"optionId":"proceed_once"}}}`),
			})

			assert.Equal(t, "Allow once", res.DisplayText)
		})
	}
}

func TestResolveControlResponse_PiExtensionUIResponse(t *testing.T) {
	confirmed := true
	response, err := json.Marshal(map[string]interface{}{"confirmed": confirmed})
	require.NoError(t, err)

	res := piProvider{}.ResolveControlResponse(ControlResponseContext{
		RequestPayload:  []byte(`{"method":"confirm"}`),
		ResponseContent: response,
	})

	assert.Equal(t, "Approve", res.DisplayText)
}

func TestFirstNonEmpty(t *testing.T) {
	// Returns the first argument non-empty AFTER trimming, and trims the chosen value --
	// including a fallback passed with surrounding whitespace (the display-label tidy the
	// answer summaries rely on).
	assert.Equal(t, "x", firstNonEmpty("", "  ", "x"))
	assert.Equal(t, "a", firstNonEmpty("  a  ", "b"), "trims the chosen value")
	assert.Equal(t, "fallback", firstNonEmpty("   ", "  fallback  "), "trims a whitespace-padded fallback")
	assert.Equal(t, "", firstNonEmpty(), "no args -> empty")
	assert.Equal(t, "", firstNonEmpty("", "\t\n "), "all blank -> empty")
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

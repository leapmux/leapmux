package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestNewControlRequestInput_FindsNoRow(t *testing.T) {
	input := NewControlRequestInput([]byte(`{"a":1}`))
	assert.JSONEq(t, `{"a":1}`, string(input.Payload))
	_, ok := input.Source()
	assert.False(t, ok)
	_, ok = input.SpanRequest("span-1")
	assert.False(t, ok)
}

func TestGenericPermissionPrompt_OffersAllowAndDenyAndNoArguments(t *testing.T) {
	permission := GenericPermissionPrompt().GetPermission()
	require.NotNil(t, permission)
	assert.Empty(t, permission.GetInputJson(), "the generic prompt draws no part of the envelope as arguments")
	require.Len(t, permission.GetOptions(), 2)
	assert.Equal(t, contracts.ControlAnswerOptionAllow, permission.GetOptions()[0].GetOptionId())
	assert.True(t, IsAllowOption(permission.GetOptions()[0]))
	assert.Equal(t, contracts.ControlAnswerOptionDeny, permission.GetOptions()[1].GetOptionId())
	assert.False(t, IsAllowOption(permission.GetOptions()[1]))
}

// promptReader reads every payload as prompt, and counts its calls.
type promptReader struct {
	Provider
	prompt *leapmuxv1.ControlPrompt
	reads  int
}

func (r *promptReader) ReadControlRequest(ControlRequestInput) *leapmuxv1.ControlPrompt {
	r.reads++
	return r.prompt
}

func TestReadControlPrompt(t *testing.T) {
	question := func(purpose leapmuxv1.ControlEditorPurpose) *leapmuxv1.ControlPrompt {
		return &leapmuxv1.ControlPrompt{
			Kind:          &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{Questions: []*leapmuxv1.ControlQuestion{{Question: "Which?"}}}},
			EditorPurpose: purpose,
		}
	}

	t.Run("a payload that is no object reaches no provider", func(t *testing.T) {
		reader := &promptReader{prompt: question(leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_UNSPECIFIED)}
		got := ReadControlPrompt(reader, NewControlRequestInput([]byte(`[1]`)))
		assert.Equal(t, leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_NOT_AN_OBJECT, got.GetUnreadable())
		assert.Equal(t, leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE, got.GetEditorPurpose())
		assert.Zero(t, reader.reads)
	})

	t.Run("the provider's prompt takes the editor purpose of its kind", func(t *testing.T) {
		reader := &promptReader{prompt: question(leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_UNSPECIFIED)}
		got := ReadControlPrompt(reader, NewControlRequestInput([]byte(`{}`)))
		assert.Equal(t, "Which?", got.GetQuestion().GetQuestions()[0].GetQuestion())
		assert.Equal(t, leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_ANSWER, got.GetEditorPurpose())
		assert.Equal(t, 1, reader.reads)
	})

	t.Run("a purpose that the provider states stays", func(t *testing.T) {
		reader := &promptReader{prompt: question(leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE)}
		got := ReadControlPrompt(reader, NewControlRequestInput([]byte(`{}`)))
		assert.Equal(t, leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE, got.GetEditorPurpose())
	})

	t.Run("a provider that reads no prompt states the generic one", func(t *testing.T) {
		for name, prompt := range map[string]*leapmuxv1.ControlPrompt{
			"nil":     nil,
			"no kind": {},
		} {
			t.Run(name, func(t *testing.T) {
				got := ReadControlPrompt(&promptReader{prompt: prompt}, NewControlRequestInput([]byte(`{}`)))
				require.NotNil(t, got.GetPermission())
				assert.Len(t, got.GetPermission().GetOptions(), 2)
				assert.Equal(t, leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_FEEDBACK, got.GetEditorPurpose())
			})
		}
	})
}

// The Worker keeps a request whose payload is not a JSON object, and its
// prompt states the fault. The browser reads the fault from the prompt.
func TestUnreadableControlPrompt(t *testing.T) {
	malformed := leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_MALFORMED
	notAnObject := leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_NOT_AN_OBJECT
	for payload, want := range map[string]leapmuxv1.ControlPayloadFault{
		``:             malformed,
		`{"a":`:        malformed,
		`not json`:     malformed,
		`[1,2]`:        notAnObject,
		`"text"`:       notAnObject,
		`7`:            notAnObject,
		`null`:         notAnObject,
		`true`:         notAnObject,
		` {"a":1} `:    leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_UNSPECIFIED,
		`{}`:           leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_UNSPECIFIED,
		`{"a":[null]}`: leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_UNSPECIFIED,
	} {
		prompt, unreadable := UnreadableControlPrompt([]byte(payload))
		if want == leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_UNSPECIFIED {
			assert.False(t, unreadable, payload)
			assert.Nil(t, prompt, payload)
			continue
		}
		require.True(t, unreadable, payload)
		assert.Equal(t, want, prompt.GetUnreadable(), payload)
		assert.Equal(t, leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE, prompt.GetEditorPurpose(),
			"the composer offers no answer to a request that takes none: %s", payload)
	}
}

func TestIsAllowOption(t *testing.T) {
	for kind, want := range map[leapmuxv1.PermissionOptionKind]bool{
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE:    true,
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS:  true,
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE:   false,
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ALWAYS: false,
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_OTHER:         false,
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_UNSPECIFIED:   false,
	} {
		assert.Equal(t, want, IsAllowOption(&leapmuxv1.PermissionOption{Kind: kind}), kind.String())
	}
}

func defaultAnswer(t *testing.T, payload string, decision *leapmuxv1.ControlDecision) (ControlAnswer, error) {
	t.Helper()
	input := NewControlRequestInput([]byte(payload))
	return ProviderDefaults{}.AnswerControlRequest(ControlAnswerInput{
		Request: input, RequestID: "req-1", Prompt: ProviderDefaults{}.ReadControlRequest(input), Decision: decision,
	})
}

func permission(optionID, feedback string) *leapmuxv1.ControlDecision {
	return &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Permission{Permission: &leapmuxv1.PermissionDecision{OptionId: optionID, Feedback: feedback}}}
}

func TestProviderDefaults_AnswersTheGenericPromptWithTheNeutralEnvelope(t *testing.T) {
	payload := `{"request":{"tool_name":"Edit","input":{"file_path":"/w/a.go"}}}`
	got, err := defaultAnswer(t, payload, permission(contracts.ControlAnswerOptionAllow, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow","updatedInput":{"file_path":"/w/a.go"}}}}`, string(got.Content),
		"the allow sends the request's own input back")

	got, err = defaultAnswer(t, payload, permission(contracts.ControlAnswerOptionDeny, "Not now."))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"deny","message":"Not now."}}}`, string(got.Content))

	got, err = defaultAnswer(t, `{}`, permission(contracts.ControlAnswerOptionAllow, ""))
	require.NoError(t, err)
	assert.Contains(t, string(got.Content), `"updatedInput":{}`, "a request with no input sends an empty object")

	_, err = defaultAnswer(t, payload, permission("always", ""))
	assert.ErrorIs(t, err, ErrInvalidControlDecision)
	_, err = defaultAnswer(t, payload, nil)
	assert.ErrorIs(t, err, ErrInvalidControlDecision, "an absent decision answers nothing")
}

func TestIsRefusalOption(t *testing.T) {
	for kind, want := range map[leapmuxv1.PermissionOptionKind]bool{
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE:   true,
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ALWAYS: true,
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE:    false,
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS:  false,
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_OTHER:         false,
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_UNSPECIFIED:   false,
	} {
		assert.Equal(t, want, IsRefusalOption(&leapmuxv1.PermissionOption{Kind: kind}), kind.String())
	}
	assert.False(t, IsRefusalOption(nil))
}

func TestWithRefusal(t *testing.T) {
	refusal := &leapmuxv1.PermissionOption{OptionId: "leapmux:cancel", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE}
	assert.Equal(t, []string{contracts.ControlAnswerOptionAllow, contracts.ControlAnswerOptionDeny}, optionIDs(WithRefusal(nil, refusal)), "no options become LeapMux's allow and deny")

	allowOnly := []*leapmuxv1.PermissionOption{
		{OptionId: "once", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE},
		{OptionId: "later", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_OTHER},
	}
	assert.Equal(t, []string{"once", "later", "leapmux:cancel"}, optionIDs(WithRefusal(allowOnly, refusal)))

	withRefusal := []*leapmuxv1.PermissionOption{
		{OptionId: "once", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE},
		{OptionId: "never", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ALWAYS},
	}
	assert.Equal(t, []string{"once", "never"}, optionIDs(WithRefusal(withRefusal, refusal)), "a list that can refuse stays as it is")
}

func optionIDs(options []*leapmuxv1.PermissionOption) []string {
	ids := make([]string, 0, len(options))
	for _, option := range options {
		ids = append(ids, option.GetOptionId())
	}
	return ids
}

// Feedback is the reason of a refusal: no runtime has a place for it beside
// an approval, or beside an option that neither allows nor refuses.
func TestPermissionDecisionOf(t *testing.T) {
	prompt := &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{Options: []*leapmuxv1.PermissionOption{
		{OptionId: "once", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE},
		{OptionId: "later", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_OTHER},
		{OptionId: "never", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ALWAYS},
	}}}}
	decide := func(optionID, feedback string) (*leapmuxv1.PermissionDecision, *leapmuxv1.PermissionOption, error) {
		return PermissionDecisionOf(ControlAnswerInput{Prompt: prompt, Decision: &leapmuxv1.ControlDecision{
			Kind: &leapmuxv1.ControlDecision_Permission{Permission: &leapmuxv1.PermissionDecision{OptionId: optionID, Feedback: feedback}},
		}})
	}
	decision, option, err := decide("never", "Too broad.")
	require.NoError(t, err)
	assert.Equal(t, "Too broad.", decision.GetFeedback())
	assert.Equal(t, "never", option.GetOptionId())
	for _, optionID := range []string{"once", "later"} {
		_, option, err := decide(optionID, "")
		require.NoError(t, err, optionID)
		assert.Equal(t, optionID, option.GetOptionId())
		_, _, err = decide(optionID, "a note")
		assert.ErrorIs(t, err, ErrInvalidControlDecision, "feedback beside %s", optionID)
	}
	_, _, err = decide("always", "")
	assert.ErrorIs(t, err, ErrInvalidControlDecision, "an option that the prompt does not offer")
	_, _, err = PermissionDecisionOf(ControlAnswerInput{Prompt: prompt, Decision: &leapmuxv1.ControlDecision{}})
	assert.ErrorIs(t, err, ErrInvalidControlDecision, "a decision of no kind")
}

func TestAnswerAllowDeny(t *testing.T) {
	answer := func(payload, optionID, feedback string) (ControlAnswer, error) {
		return AnswerAllowDeny(ControlAnswerInput{
			Request: NewControlRequestInput([]byte(payload)), RequestID: "req-1", Prompt: GenericPermissionPrompt(),
			Decision: &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Permission{Permission: &leapmuxv1.PermissionDecision{OptionId: optionID, Feedback: feedback}}},
		})
	}
	got, err := answer(`{"request":{"tool_name":"Edit","input":{"file_path":"/w/a.go"}}}`, contracts.ControlAnswerOptionAllow, "")
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow","updatedInput":{"file_path":"/w/a.go"}}}}`, string(got.Content))
	assert.Nil(t, got.PlanApproval)
	assert.Empty(t, got.FollowUp)

	got, err = answer(`{}`, contracts.ControlAnswerOptionDeny, "Use the other file.")
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"deny","message":"Use the other file."}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp, "the deny carries the feedback itself")

	got, err = answer(`{}`, contracts.ControlAnswerOptionDeny, "")
	require.NoError(t, err)
	assert.Contains(t, string(got.Content), contracts.ControlAnswerMessageRejectedByUser)

	_, err = answer(`{}`, contracts.ControlAnswerOptionAllow, "Why not.")
	assert.ErrorIs(t, err, ErrInvalidControlDecision, "an allow has no field for feedback")
	_, err = answer(`{}`, "allow", "")
	assert.ErrorIs(t, err, ErrInvalidControlDecision, "LeapMux's ids carry their prefix")
}

func TestControlToolNameAndInput(t *testing.T) {
	assert.Equal(t, "Bash", ControlToolName([]byte(`{"request":{"tool_name":"Bash"}}`)))
	assert.Equal(t, "Bash", ControlToolName([]byte(`{"request":{"tool_name":"Bash","input":5}}`)), "a sibling of the wrong type never hides the name")
	assert.JSONEq(t, `{"a":1}`, string(ControlToolInput([]byte(`{"request":{"tool_name":5,"input":{"a":1}}}`))), "a sibling of the wrong type never hides the input")
	assert.Empty(t, ControlToolName([]byte(`{"request":"text"}`)))
	assert.Empty(t, ControlToolName([]byte(`not json`)))
	assert.Empty(t, ControlToolName([]byte(`{"request":{"tool_name":7}}`)), "a name that is no text reads as none")

	assert.JSONEq(t, `{"command":"ls"}`, string(ControlToolInput([]byte(`{"request":{"input":{"command":"ls"}}}`))))
	for _, payload := range []string{`not json`, `{}`, `{"request":{}}`, `{"request":{"input":null}}`, `{"request":{"input":"text"}}`, `{"request":{"input":[1]}}`} {
		assert.JSONEq(t, `{}`, string(ControlToolInput([]byte(payload))), payload)
	}
}

func TestControlToolUseID(t *testing.T) {
	assert.Equal(t, "toolu-1", ControlToolUseID([]byte(`{"request":{"tool_use_id":"toolu-1"}}`)))
	assert.Equal(t, "toolu-1", ControlToolUseID([]byte(`{"request":{"tool_name":7,"tool_use_id":"toolu-1"}}`)), "a sibling of the wrong type never hides the id")
	for _, payload := range []string{`not json`, `{}`, `{"request":"text"}`, `{"request":{}}`, `{"request":{"tool_use_id":7}}`, `{"tool_use_id":"toolu-1"}`} {
		assert.Empty(t, ControlToolUseID([]byte(payload)), payload)
	}
}

func TestDenyResponse_NeverStatesAnEmptyMessage(t *testing.T) {
	var envelope ControlBehaviorEnvelope
	require.NoError(t, json.Unmarshal(DenyResponse("req-1", ""), &envelope))
	assert.Equal(t, contracts.ControlAnswerMessageRejectedByUser, envelope.Response.Response.Message)
	assert.Equal(t, "req-1", envelope.Response.RequestID)
}

func TestWithControlChoice(t *testing.T) {
	got := WithControlChoice(AllowResponse("req-1", json.RawMessage(`{"a":1}`)), "revise")
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow","updatedInput":{"a":1},"choice":"revise"}}}`, string(got))
	assert.Equal(t, "revise", DecodeControlChoice(got))
	assert.Panics(t, func() { WithControlChoice([]byte(`{"jsonrpc":"2.0"}`), "x") }, "a choice rides only the neutral envelope")
}

func TestJSONRPCResultKeepsTheWorkerKeyAsTheID(t *testing.T) {
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"decision":"accept"}}`, string(JSONRPCResult("jsonrpc:7", map[string]any{"decision": "accept"})))
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"selected","optionId":"allow-once"}}}`, string(SelectedOptionResult("jsonrpc:7", "allow-once")))
}

func TestWithEditorPurpose(t *testing.T) {
	answer := leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_ANSWER
	none := leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE
	feedback := leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_FEEDBACK
	for name, tc := range map[string]struct {
		prompt *leapmuxv1.ControlPrompt
		want   leapmuxv1.ControlEditorPurpose
	}{
		"a question":                            {&leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{}}}, answer},
		"a form":                                {&leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: &leapmuxv1.ElicitationPrompt{}}}, none},
		"a permission":                          {GenericPermissionPrompt(), feedback},
		"a plan":                                {&leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{}}}, feedback},
		"a dialog":                              {&leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Dialog{Dialog: &leapmuxv1.DialogPrompt{}}}, none},
		"unreadable":                            {&leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Unreadable{Unreadable: leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_MALFORMED}}, none},
		"a provider's own purpose stays":        {&leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Dialog{Dialog: &leapmuxv1.DialogPrompt{}}, EditorPurpose: answer}, answer},
		"a provider's none on a question stays": {&leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{}}, EditorPurpose: none}, none},
	} {
		assert.Equal(t, tc.want, WithEditorPurpose(tc.prompt).GetEditorPurpose(), name)
	}
}

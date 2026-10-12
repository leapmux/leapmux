package muse

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testApprovalKey(session, approval string, index int64) string {
	raw, _ := json.Marshal([]any{session, approval, index})
	return "muse:approval:" + string(raw)
}
func approvalRequest(session string, index int64) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": contracts.MuseMethodApprovalRequested, "params": map[string]any{"sessionId": session, "approvalId": "approval", "currentRequirementId": map[string]any{"approvalId": "approval", "sourceIndex": index}, "availableChoices": []map[string]any{{"choiceId": "allow", "decision": "approved", "label": "Allow once", "scope": "once"}}}})
	return raw
}
func approvalAnswer(id string) []byte {
	raw, _ := json.Marshal(map[string]any{"response": map[string]any{"request_id": id, "response": map[string]any{"behavior": "allow"}}})
	return raw
}
func TestMuseApprovalStageZeroKeepsItsExactNativeGuard(t *testing.T) {
	t.Parallel()
	id := testApprovalKey("session", "approval", 0)
	result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: approvalRequest("session", 0), ResponseContent: approvalAnswer(id)})
	require.False(t, result.Withhold)
	var frame struct {
		Params struct {
			SessionID   string `json:"sessionId"`
			Requirement struct {
				ID    string `json:"approvalId"`
				Index int64  `json:"sourceIndex"`
			} `json:"requirementId"`
		} `json:"params"`
	}
	require.NoError(t, json.Unmarshal(result.Content, &frame))
	assert.Equal(t, "session", frame.Params.SessionID)
	assert.Equal(t, "approval", frame.Params.Requirement.ID)
	assert.Zero(t, frame.Params.Requirement.Index)
}
func TestMuseApprovalRejectsAKeyFromAnotherStageOrSession(t *testing.T) {
	t.Parallel()
	for label, request := range map[string]json.RawMessage{"later stage": approvalRequest("session", 1), "replaced session": approvalRequest("replacement", 0)} {
		t.Run(label, func(t *testing.T) {
			id := testApprovalKey("session", "approval", 0)
			result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: approvalAnswer(id)})
			require.True(t, result.Withhold)
			require.Error(t, result.Refusal())
		})
	}
}

func questionRequest(t *testing.T, questions any) (string, []byte) {
	t.Helper()
	params := controlParams{SessionID: "session", UserInputID: "question"}
	id, err := controlID(params)
	require.NoError(t, err)
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": contracts.MuseMethodUserInputRequested, "params": map[string]any{"sessionId": "session", "userInputId": "question", "questions": questions}})
	require.NoError(t, err)
	return id, raw
}

func TestMuseQuestionAnswersValidateTheExactNativeForms(t *testing.T) {
	questions := []any{map[string]any{"id": "single", "header": "", "question": "Choose", "options": []any{map[string]any{"label": "a"}, map[string]any{"label": "b"}}, "selection": map[string]any{"mode": "single"}}}
	id, request := questionRequest(t, questions)
	for _, tc := range []struct {
		label  string
		answer any
		valid  bool
	}{
		{"selected", map[string]any{"questionId": "single", "selectedLabel": "a"}, true},
		{"empty text", map[string]any{"questionId": "single", "freeText": ""}, false},
		{"whitespace", map[string]any{"questionId": "single", "freeText": " "}, false},
		{"text maximum", map[string]any{"questionId": "single", "freeText": strings.Repeat("界", 500)}, true},
		{"wrong identity", map[string]any{"questionId": "other", "selectedLabel": "a"}, false},
		{"unknown choice", map[string]any{"questionId": "single", "selectedLabel": "other"}, false},
		{"wrong form", map[string]any{"questionId": "single", "selectedLabels": []string{"a"}}, false},
		{"two forms", map[string]any{"questionId": "single", "selectedLabel": "a", "freeText": "x"}, false},
		{"absent form", map[string]any{"questionId": "single"}, false},
		{"text too long", map[string]any{"questionId": "single", "freeText": strings.Repeat("x", 501)}, false},
		{"note too long", map[string]any{"questionId": "single", "selectedLabel": "a", "note": strings.Repeat("x", 501)}, false},
		{"null text", map[string]any{"questionId": "single", "freeText": nil}, false},
	} {
		t.Run(tc.label, func(t *testing.T) {
			answer, err := json.Marshal(map[string]any{"id": id, "result": map[string]any{"answers": []any{tc.answer}}})
			require.NoError(t, err)
			result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: answer})
			assert.Equal(t, !tc.valid, result.Withhold)
		})
	}
}

func TestMuseMultipleAnswersValidateCountsAndUniqueChoices(t *testing.T) {
	questions := []any{map[string]any{"id": "multi", "header": "", "question": "Choose", "options": []any{map[string]any{"label": "a"}, map[string]any{"label": "b"}, map[string]any{"label": "c"}}, "selection": map[string]any{"mode": "multiple", "minSelections": 1, "maxSelections": 2}}}
	id, request := questionRequest(t, questions)
	for _, tc := range []struct {
		labels []string
		valid  bool
	}{
		{[]string{"a"}, true}, {[]string{"a", "b"}, true}, {[]string{}, false}, {[]string{"a", "a"}, false}, {[]string{"a", "b", "c"}, false}, {[]string{"foreign"}, false},
	} {
		answer, err := json.Marshal(map[string]any{"id": id, "result": map[string]any{"answers": []any{map[string]any{"questionId": "multi", "selectedLabels": tc.labels}}}})
		require.NoError(t, err)
		result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: answer})
		assert.Equal(t, !tc.valid, result.Withhold, "%v", tc.labels)
	}
}

func TestMuseApprovalRejectsAnInvalidNativeChoiceBeforeItBuildsAReply(t *testing.T) {
	for _, field := range []string{"choiceId", "label", "decision", "scope", "acceptsFeedback"} {
		values := []any{nil, 0, []any{}, map[string]any{}}
		if field != "acceptsFeedback" {
			values = append(values, false)
		}
		for _, value := range values {
			t.Run(field+"/"+string(mustMuseChoiceJSON(t, value)), func(t *testing.T) {
				choices := []any{
					map[string]any{"choiceId": "allow", "label": "Allow once", "decision": "approved", "scope": "once"},
					map[string]any{"choiceId": "deny", "label": "Deny once", "decision": "denied", "scope": "once"},
				}
				choices[1].(map[string]any)[field] = value
				id := testApprovalKey("session", "approval", 0)
				request := mustMuseChoiceJSON(t, map[string]any{"method": "approval/request", "params": map[string]any{
					"sessionId": "session", "approvalId": "approval", "currentRequirementId": map[string]any{"approvalId": "approval", "sourceIndex": 0},
					"availableChoices": choices,
				}})
				answer := approvalAnswer(id)
				result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: answer})
				require.True(t, result.Withhold)
				assert.Error(t, result.Refusal())
			})
		}
	}
}

func TestMuseApprovalRejectsDuplicateNativeChoiceIdentities(t *testing.T) {
	id := testApprovalKey("session", "approval", 0)
	request := mustMuseChoiceJSON(t, map[string]any{"method": "approval/request", "params": map[string]any{
		"sessionId": "session", "approvalId": "approval", "currentRequirementId": map[string]any{"approvalId": "approval", "sourceIndex": 0},
		"availableChoices": []any{
			map[string]any{"choiceId": "allow", "label": "Allow once", "decision": "approved", "scope": "once"},
			map[string]any{"choiceId": "allow", "label": "Deny once", "decision": "denied", "scope": "once"},
		},
	}})
	answer := approvalAnswer(id)
	result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: answer})
	require.True(t, result.Withhold)
	assert.Error(t, result.Refusal())
}

func mustMuseChoiceJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return raw
}

func museApprovalChoice(id, decision, scope string) map[string]any {
	return map[string]any{"choiceId": id, "label": "Native choice", "decision": decision, "scope": scope}
}

func museApprovalRequest(t *testing.T, choices any) (string, json.RawMessage) {
	t.Helper()
	id := testApprovalKey("session", "approval", 0)
	return id, mustMuseChoiceJSON(t, map[string]any{"method": contracts.MuseMethodApprovalRequested, "params": map[string]any{
		"sessionId": "session", "approvalId": "approval", "currentRequirementId": map[string]any{"approvalId": "approval", "sourceIndex": 0}, "availableChoices": choices,
	}})
}

func museApprovalReply(t *testing.T, id, choice, feedback string) json.RawMessage {
	t.Helper()
	result := map[string]any{"choiceId": choice}
	if feedback != "" {
		result["feedback"] = feedback
	}
	return mustMuseChoiceJSON(t, map[string]any{"id": id, "result": result})
}

func TestMuseApprovalRejectsMissingEmptyAndBlankNativeChoiceFields(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"choiceId", "label", "decision", "scope"} {
		for _, value := range []struct {
			label   string
			present bool
			value   string
		}{
			{label: "absent"}, {label: "empty", present: true}, {label: "blank", present: true, value: " \t\n"},
		} {
			t.Run(field+"/"+value.label, func(t *testing.T) {
				t.Parallel()
				invalid := museApprovalChoice("deny", contracts.MuseApprovalDecisionDenied, contracts.MuseChoiceScopeOnce)
				if value.present {
					invalid[field] = value.value
				} else {
					delete(invalid, field)
				}
				id, request := museApprovalRequest(t, []any{museApprovalChoice("allow", contracts.MuseApprovalDecisionApproved, contracts.MuseChoiceScopeOnce), invalid})
				original := slices.Clone(request)
				answer := approvalAnswer(id)
				result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: answer})
				require.True(t, result.Withhold)
				assert.Error(t, result.Refusal())
				assert.Equal(t, original, request)
				assert.Equal(t, []byte(answer), result.Content)
			})
		}
	}
}

func TestMuseApprovalRejectsUnreadableListsAndUnknownNativeVocabulary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		label   string
		choices any
	}{
		{label: "absent list"}, {label: "null list"}, {label: "empty list", choices: []any{}},
		{label: "numeric list", choices: 0}, {label: "object list", choices: map[string]any{}},
		{label: "nonobject choice", choices: []any{"choice"}},
		{label: "unknown decision", choices: []any{museApprovalChoice("allow", contracts.MuseApprovalDecisionApproved, contracts.MuseChoiceScopeOnce), museApprovalChoice("future", "futureDecision", contracts.MuseChoiceScopeOnce)}},
		{label: "unknown scope", choices: []any{museApprovalChoice("allow", contracts.MuseApprovalDecisionApproved, contracts.MuseChoiceScopeOnce), museApprovalChoice("future", contracts.MuseApprovalDecisionDenied, "futureScope")}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			id, request := museApprovalRequest(t, tc.choices)
			if tc.label == "absent list" {
				var envelope map[string]any
				require.NoError(t, json.Unmarshal(request, &envelope))
				delete(envelope["params"].(map[string]any), "availableChoices")
				request = mustMuseChoiceJSON(t, envelope)
			}
			original := slices.Clone(request)
			answer := approvalAnswer(id)
			result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: answer})
			if tc.label == "unknown decision" || tc.label == "unknown scope" {
				require.False(t, result.Withhold)
				var frame struct {
					Params struct {
						Choice string `json:"choiceId"`
					} `json:"params"`
				}
				require.NoError(t, json.Unmarshal(result.Content, &frame))
				assert.Equal(t, "allow", frame.Params.Choice)
			} else {
				require.True(t, result.Withhold)
				assert.Error(t, result.Refusal())
				assert.Equal(t, []byte(answer), result.Content)
			}
			assert.Equal(t, original, request)
		})
	}
}

func TestMuseApprovalKeepsEveryExplicitNativeDecisionAndScope(t *testing.T) {
	t.Parallel()
	decisions := []string{
		contracts.MuseApprovalDecisionApproved, contracts.MuseApprovalDecisionApprovedForSession,
		contracts.MuseApprovalDecisionApprovedPolicyAmendment, contracts.MuseApprovalDecisionDenied,
		contracts.MuseApprovalDecisionDeniedPolicyAmendment, contracts.MuseApprovalDecisionTimedOut, contracts.MuseApprovalDecisionAbort,
	}
	for _, decision := range decisions {
		for _, scope := range []string{contracts.MuseChoiceScopeOnce, contracts.MuseChoiceScopeSession, contracts.MuseChoiceScopeLocalPersistent} {
			t.Run(decision+"/"+scope, func(t *testing.T) {
				t.Parallel()
				id, request := museApprovalRequest(t, []any{museApprovalChoice("native-explicit-choice", decision, scope)})
				original := slices.Clone(request)
				result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: museApprovalReply(t, id, "native-explicit-choice", "")})
				require.False(t, result.Withhold)
				var frame struct {
					Method string `json:"method"`
					Params struct {
						ChoiceID string          `json:"choiceId"`
						Stage    json.RawMessage `json:"requirementId"`
					} `json:"params"`
				}
				require.NoError(t, json.Unmarshal(result.Content, &frame))
				assert.Equal(t, methodApprovalDecide, frame.Method)
				assert.Equal(t, "native-explicit-choice", frame.Params.ChoiceID)
				assert.JSONEq(t, `{"approvalId":"approval","sourceIndex":0}`, string(frame.Params.Stage))
				assert.Equal(t, original, request)
			})
		}
	}
}

func TestMuseApprovalKeepsNativeFeedbackPermission(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		label   string
		present bool
		allowed bool
	}{
		{label: "omitted"}, {label: "refused", present: true}, {label: "accepted", present: true, allowed: true},
	} {
		for _, feedback := range []string{"", "  Native denial feedback\n"} {
			t.Run(tc.label+"/"+feedback, func(t *testing.T) {
				t.Parallel()
				choice := museApprovalChoice("deny", contracts.MuseApprovalDecisionDenied, contracts.MuseChoiceScopeOnce)
				if tc.present {
					choice["acceptsFeedback"] = tc.allowed
				}
				id, request := museApprovalRequest(t, []any{choice})
				result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: museApprovalReply(t, id, "deny", feedback)})
				if feedback != "" && !tc.allowed {
					require.True(t, result.Withhold)
					assert.ErrorContains(t, result.Refusal(), "does not accept feedback")
					return
				}
				require.False(t, result.Withhold)
				var frame struct {
					Params struct {
						Feedback string `json:"feedback"`
					} `json:"params"`
				}
				require.NoError(t, json.Unmarshal(result.Content, &frame))
				assert.Equal(t, feedback, frame.Params.Feedback)
			})
		}
	}
}

func TestMuseApprovalRequiresAnExplicitChoiceForNativePersistence(t *testing.T) {
	t.Parallel()
	for _, behavior := range []string{"allow", "deny"} {
		for _, scope := range []string{contracts.MuseChoiceScopeOnce, contracts.MuseChoiceScopeSession, contracts.MuseChoiceScopeLocalPersistent} {
			t.Run(behavior+"/"+scope, func(t *testing.T) {
				t.Parallel()
				decision := contracts.MuseApprovalDecisionApproved
				if behavior == "deny" {
					decision = contracts.MuseApprovalDecisionDenied
				}
				id, request := museApprovalRequest(t, []any{museApprovalChoice("native-choice", decision, scope)})
				answer := mustMuseChoiceJSON(t, map[string]any{"response": map[string]any{"request_id": id, "response": map[string]any{"behavior": behavior}}})
				result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: answer})
				assert.Equal(t, scope != contracts.MuseChoiceScopeOnce, result.Withhold)
				if scope != contracts.MuseChoiceScopeOnce {
					assert.Error(t, result.Refusal())
					assert.Equal(t, []byte(answer), result.Content)
				}
			})
		}
	}
}

func TestMuseApprovalRejectsARequestWithAnotherNativeMethod(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"", "future/request", contracts.MuseMethodUserInputRequest, methodApprovalDecide} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			id, request := museApprovalRequest(t, []any{museApprovalChoice("allow", contracts.MuseApprovalDecisionApproved, contracts.MuseChoiceScopeOnce)})
			var envelope map[string]any
			require.NoError(t, json.Unmarshal(request, &envelope))
			envelope["method"] = method
			request = mustMuseChoiceJSON(t, envelope)
			answer := approvalAnswer(id)
			result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: answer})
			require.True(t, result.Withhold)
			assert.Error(t, result.Refusal())
			assert.Equal(t, []byte(answer), result.Content)
		})
	}
}

func TestMuseApprovalKeepsEachCurrentNativeRequestMethod(t *testing.T) {
	t.Parallel()
	for _, method := range []string{contracts.MuseMethodApprovalRequest, contracts.MuseMethodApprovalRequested, contracts.MuseMethodApprovalUpdated} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			id, request := museApprovalRequest(t, []any{museApprovalChoice("allow", contracts.MuseApprovalDecisionApproved, contracts.MuseChoiceScopeOnce)})
			var envelope map[string]any
			require.NoError(t, json.Unmarshal(request, &envelope))
			envelope["method"] = method
			request = mustMuseChoiceJSON(t, envelope)
			result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: museApprovalReply(t, id, "allow", "")})
			require.False(t, result.Withhold)
		})
	}
}

func TestMuseApprovalRejectsUnreadableAndMixedReplyEnvelopes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		label string
		build func(string) any
	}{
		{label: "absent result", build: func(id string) any { return map[string]any{"id": id} }},
		{label: "null result", build: func(id string) any { return map[string]any{"id": id, "result": nil} }},
		{label: "numeric result", build: func(id string) any { return map[string]any{"id": id, "result": 0} }},
		{label: "empty choice", build: func(id string) any { return map[string]any{"id": id, "result": map[string]any{"choiceId": ""}} }},
		{label: "null choice", build: func(id string) any { return map[string]any{"id": id, "result": map[string]any{"choiceId": nil}} }},
		{label: "numeric choice", build: func(id string) any { return map[string]any{"id": id, "result": map[string]any{"choiceId": 0}} }},
		{label: "wrong request", build: func(string) any {
			return map[string]any{"id": "foreign", "result": map[string]any{"choiceId": "allow"}}
		}},
		{label: "numeric request", build: func(string) any { return map[string]any{"id": 0, "result": map[string]any{"choiceId": "allow"}} }},
		{label: "numeric feedback", build: func(id string) any {
			return map[string]any{"id": id, "result": map[string]any{"choiceId": "allow", "feedback": 0}}
		}},
		{label: "mixed forms", build: func(id string) any {
			return map[string]any{"id": id, "result": map[string]any{"choiceId": "allow"}, "response": map[string]any{"request_id": id, "response": map[string]any{"behavior": "deny"}}}
		}},
		{label: "null native form with neutral form", build: func(id string) any {
			return map[string]any{"result": nil, "response": map[string]any{"request_id": id, "response": map[string]any{"behavior": "allow"}}}
		}},
		{label: "absent neutral behavior", build: func(id string) any {
			return map[string]any{"response": map[string]any{"request_id": id, "response": map[string]any{}}}
		}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			id, request := museApprovalRequest(t, []any{museApprovalChoice("allow", contracts.MuseApprovalDecisionApproved, contracts.MuseChoiceScopeOnce), museApprovalChoice("deny", contracts.MuseApprovalDecisionDenied, contracts.MuseChoiceScopeOnce)})
			answer := mustMuseChoiceJSON(t, tc.build(id))
			original := slices.Clone(answer)
			result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: answer})
			require.True(t, result.Withhold)
			assert.Error(t, result.Refusal())
			assert.Equal(t, original, answer)
			assert.Equal(t, []byte(original), result.Content)
		})
	}
}

func TestMuseApprovalRejectsAnInvalidNativeRequirementIdentity(t *testing.T) {
	t.Parallel()
	for _, requirement := range []any{
		nil, map[string]any{}, map[string]any{"approvalId": "approval"},
		map[string]any{"approvalId": "approval", "sourceIndex": nil},
		map[string]any{"approvalId": "approval", "sourceIndex": -1},
		map[string]any{"approvalId": "approval", "sourceIndex": 0.5},
		map[string]any{"approvalId": "foreign", "sourceIndex": 0},
		map[string]any{"approvalId": "approval", "sourceIndex": "0"},
	} {
		t.Run(string(mustMuseChoiceJSON(t, requirement)), func(t *testing.T) {
			t.Parallel()
			id := testApprovalKey("session", "approval", 0)
			request := mustMuseChoiceJSON(t, map[string]any{"method": contracts.MuseMethodApprovalRequested, "params": map[string]any{
				"sessionId": "session", "approvalId": "approval", "currentRequirementId": requirement,
				"availableChoices": []any{museApprovalChoice("allow", contracts.MuseApprovalDecisionApproved, contracts.MuseChoiceScopeOnce)},
			}})
			result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: museApprovalReply(t, id, "allow", "")})
			require.True(t, result.Withhold)
			assert.Error(t, result.Refusal())
		})
	}
}

func TestMuseApprovalKeepsLargeNativeStageIndicesAndExactChoiceIDs(t *testing.T) {
	t.Parallel()
	const stage int64 = 1<<63 - 1
	id := testApprovalKey("session", "approval", stage)
	request := mustMuseChoiceJSON(t, map[string]any{"method": contracts.MuseMethodApprovalRequested, "params": map[string]any{
		"sessionId": "session", "approvalId": "approval", "currentRequirementId": map[string]any{"approvalId": "approval", "sourceIndex": stage},
		"availableChoices": []any{museApprovalChoice(" native choice 界 ", contracts.MuseApprovalDecisionApproved, contracts.MuseChoiceScopeOnce)},
	}})
	result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: museApprovalReply(t, id, " native choice 界 ", "")})
	require.False(t, result.Withhold)
	var frame struct {
		Params struct {
			Choice string `json:"choiceId"`
			Stage  struct {
				Index int64 `json:"sourceIndex"`
			} `json:"requirementId"`
		} `json:"params"`
	}
	require.NoError(t, json.Unmarshal(result.Content, &frame))
	assert.Equal(t, stage, frame.Params.Stage.Index)
	assert.Equal(t, " native choice 界 ", frame.Params.Choice)
}

func TestMuseApprovalReadsALargeImmutableNativeChoiceListConcurrently(t *testing.T) {
	t.Parallel()
	choices := make([]any, 1024)
	for index := range choices {
		choices[index] = museApprovalChoice(fmt.Sprintf("native-choice-%d", index), contracts.MuseApprovalDecisionApproved, contracts.MuseChoiceScopeOnce)
	}
	choices[len(choices)-1].(map[string]any)["futureField"] = map[string]any{"native": "界\n\u0000"}
	id, request := museApprovalRequest(t, choices)
	answer := museApprovalReply(t, id, "native-choice-1023", "")
	originalRequest, originalAnswer := slices.Clone(request), slices.Clone(answer)
	results := make(chan agent.ControlResponseResolution, 16)
	var callers sync.WaitGroup
	for range 16 {
		callers.Go(func() {
			results <- (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: answer})
		})
	}
	callers.Wait()
	close(results)
	for result := range results {
		require.False(t, result.Withhold)
		var frame struct {
			Params struct {
				ChoiceID string `json:"choiceId"`
			} `json:"params"`
		}
		require.NoError(t, json.Unmarshal(result.Content, &frame))
		assert.Equal(t, "native-choice-1023", frame.Params.ChoiceID)
	}
	assert.Equal(t, originalRequest, request)
	assert.Equal(t, originalAnswer, answer)
}

type museControlPublicationSink struct {
	*agenttest.Sink
	requests             []agent.ControlRequest
	canceled             []string
	attempts             int
	err                  error
	onPublish            func(agent.ControlRequest)
	onCancel             func(string)
	pending              map[string]agent.ControlRequest
	notificationErr      error
	notificationAttempts [][]byte
}

func (sink *museControlPublicationSink) PublishControlRequest(request agent.ControlRequest) error {
	sink.attempts++
	if sink.err != nil {
		return sink.err
	}
	request.Payload = slices.Clone(request.Payload)
	sink.requests = append(sink.requests, request)
	if sink.pending == nil {
		sink.pending = make(map[string]agent.ControlRequest)
	}
	sink.pending[request.RequestID] = request
	if sink.onPublish != nil {
		callbackRequest := request
		callbackRequest.Payload = slices.Clone(request.Payload)
		sink.onPublish(callbackRequest)
	}
	return nil
}

func (sink *museControlPublicationSink) CancelControlRequest(id string) {
	sink.canceled = append(sink.canceled, id)
	delete(sink.pending, id)
	if sink.onCancel != nil {
		sink.onCancel(id)
	}
}

func museControlPublicationAgent(t *testing.T) (*Agent, *museControlPublicationSink) {
	t.Helper()
	a, base := testAgent(t)
	sink := &museControlPublicationSink{Sink: base}
	a.sink = agent.NewProviderServices(sink)
	a.sessions["session"].sink = a.sink
	return a, sink
}

func TestMuseApprovalRetainsPendingNativeBytesForUnreadableChoices(t *testing.T) {
	for _, tc := range []struct {
		label   string
		choices any
	}{
		{label: "wrong choice ID type", choices: []any{map[string]any{"choiceId": []any{}, "label": "Native choice", "decision": "approved", "scope": "once"}}},
		{label: "wrong feedback type", choices: []any{map[string]any{"choiceId": "choice", "label": "Native choice", "decision": "approved", "scope": "once", "acceptsFeedback": "true"}}},
		{label: "unknown decision", choices: []any{museApprovalChoice("choice", "futureDecision", contracts.MuseChoiceScopeOnce)}},
		{label: "unknown scope", choices: []any{museApprovalChoice("choice", contracts.MuseApprovalDecisionApproved, "futureScope")}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			a, sink := museControlPublicationAgent(t)
			id, request := museApprovalRequest(t, tc.choices)
			original := slices.Clone(request)
			a.HandleOutput(request)
			require.Len(t, sink.requests, 1)
			assert.Equal(t, id, sink.requests[0].RequestID)
			assert.Equal(t, "session", sink.requests[0].AgentSessionID)
			assert.True(t, bytes.Equal(original, sink.requests[0].Payload))
			assert.Equal(t, id, a.sessions["session"].controls["approval"].key)
			a.HandleOutput(request)
			assert.Len(t, sink.requests, 1)
			assert.Equal(t, original, request)
			result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: museApprovalReply(t, id, "choice", "")})
			if tc.label == "unknown decision" || tc.label == "unknown scope" {
				require.False(t, result.Withhold)
				var frame struct {
					Params struct {
						Choice string `json:"choiceId"`
					} `json:"params"`
				}
				require.NoError(t, json.Unmarshal(result.Content, &frame))
				assert.Equal(t, "choice", frame.Params.Choice)
			} else {
				assert.True(t, result.Withhold)
				assert.Error(t, result.Refusal())
				assert.Equal(t, []byte(museApprovalReply(t, id, "choice", "")), result.Content)
			}
			assert.Equal(t, id, a.sessions["session"].controls["approval"].key)
		})
	}
}

func TestMuseApprovalRetriesFailedPublicationBeforeItReplacesAStage(t *testing.T) {
	a, sink := museControlPublicationAgent(t)
	firstID, first := museApprovalRequest(t, []any{museApprovalChoice("allow", contracts.MuseApprovalDecisionApproved, contracts.MuseChoiceScopeOnce)})
	sink.err = errors.New("the control store is unavailable")
	a.HandleOutput(first)
	assert.Empty(t, sink.requests)
	assert.Empty(t, a.sessions["session"].controls)
	sink.err = nil
	a.HandleOutput(first)
	require.Len(t, sink.requests, 1)
	assert.Equal(t, firstID, sink.requests[0].RequestID)
	var second map[string]any
	require.NoError(t, json.Unmarshal(first, &second))
	second["method"] = contracts.MuseMethodApprovalUpdated
	second["params"].(map[string]any)["currentRequirementId"] = map[string]any{"approvalId": "approval", "sourceIndex": 1}
	secondRaw := mustMuseChoiceJSON(t, second)
	sink.err = errors.New("the replacement control store is unavailable")
	a.HandleOutput(secondRaw)
	assert.Empty(t, sink.canceled)
	assert.Equal(t, firstID, a.sessions["session"].controls["approval"].key)
	sink.err = nil
	a.HandleOutput(secondRaw)
	require.Len(t, sink.requests, 2)
	assert.Equal(t, testApprovalKey("session", "approval", 1), sink.requests[1].RequestID)
	assert.Equal(t, []byte(secondRaw), []byte(sink.requests[1].Payload))
	assert.Equal(t, []string{firstID}, sink.canceled)
	a.HandleOutput(secondRaw)
	assert.Equal(t, 4, sink.attempts)
	assert.Len(t, sink.requests, 2)
	assert.Equal(t, []string{firstID}, sink.canceled)
}

func TestMuseApprovalRejectsPublicationFromAForeignOrRetiredSession(t *testing.T) {
	for _, retired := range []bool{false, true} {
		t.Run(map[bool]string{false: "foreign", true: "retired"}[retired], func(t *testing.T) {
			a, sink := museControlPublicationAgent(t)
			_, request := museApprovalRequest(t, []any{museApprovalChoice("allow", contracts.MuseApprovalDecisionApproved, contracts.MuseChoiceScopeOnce)})
			if retired {
				a.sessions["session"].retired = true
			} else {
				a.sessionID = "replacement"
			}
			a.HandleOutput(request)
			assert.Empty(t, sink.requests)
			assert.Empty(t, a.sessions["session"].controls)
		})
	}
}

func museNativeQuestion(id, mode string) map[string]any {
	return map[string]any{
		"header": "", "id": id, "question": "Choose the native answer",
		"options":   []any{map[string]any{"label": "one"}, map[string]any{"label": "two"}},
		"selection": map[string]any{"mode": mode},
	}
}

func museQuestionReply(t *testing.T, id string, cancelled bool, answers any) json.RawMessage {
	t.Helper()
	result := map[string]any{"answers": answers}
	if cancelled {
		result = map[string]any{"cancelled": true}
	}
	return mustMuseChoiceJSON(t, map[string]any{"id": id, "result": result})
}

func TestMuseQuestionRetainsPendingBytesForAnUnreadableNativeList(t *testing.T) {
	for _, questions := range []any{nil, 0, false, "questions", map[string]any{}, []any{nil}} {
		t.Run(string(mustMuseChoiceJSON(t, questions)), func(t *testing.T) {
			a, sink := museControlPublicationAgent(t)
			id, request := questionRequest(t, questions)
			var envelope map[string]any
			require.NoError(t, json.Unmarshal(request, &envelope))
			envelope["method"] = contracts.MuseMethodUserInputRequested
			request = mustMuseChoiceJSON(t, envelope)
			original := slices.Clone(request)
			a.HandleOutput(request)
			require.Len(t, sink.requests, 1)
			assert.Equal(t, id, sink.requests[0].RequestID)
			assert.Equal(t, "session", sink.requests[0].AgentSessionID)
			assert.Equal(t, original, []byte(sink.requests[0].Payload))
			assert.Equal(t, id, a.sessions["session"].controls["question"].key)
			a.HandleOutput(request)
			assert.Len(t, sink.requests, 1)
			result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: museQuestionReply(t, id, true, nil)})
			require.False(t, result.Withhold)
			assert.Equal(t, id, a.sessions["session"].controls["question"].key)
			assert.Equal(t, original, request)
		})
	}
}

func TestMuseQuestionSeparatesUnreadableAnswersFromNativeCancellation(t *testing.T) {
	t.Parallel()
	type questionCase struct {
		label     string
		questions any
	}
	cases := []questionCase{
		{label: "null list"}, {label: "empty list", questions: []any{}},
		{label: "numeric list", questions: 0}, {label: "object list", questions: map[string]any{}},
		{label: "null question", questions: []any{nil}}, {label: "numeric question", questions: []any{0}},
	}
	for _, field := range []string{"header", "id", "question", "options", "selection"} {
		for _, value := range []struct {
			label   string
			present bool
			value   any
		}{
			{label: "absent"}, {label: "null", present: true}, {label: "numeric", present: true, value: 0},
		} {
			invalid := museNativeQuestion("native", "single")
			if value.present {
				invalid[field] = value.value
			} else {
				delete(invalid, field)
			}
			cases = append(cases, questionCase{label: field + "/" + value.label, questions: []any{invalid}})
		}
	}
	for _, field := range []string{"id", "question"} {
		for _, value := range []string{"", " \t\n"} {
			invalid := museNativeQuestion("native", "single")
			invalid[field] = value
			cases = append(cases, questionCase{label: field + "/" + value, questions: []any{invalid}})
		}
	}
	for _, selection := range []any{
		map[string]any{}, map[string]any{"mode": nil}, map[string]any{"mode": 0}, map[string]any{"mode": "futureMode"},
		map[string]any{"mode": "multiple", "minSelections": -1},
		map[string]any{"mode": "multiple", "maxSelections": -1},
		map[string]any{"mode": "multiple", "minSelections": 2, "maxSelections": 1},
		map[string]any{"mode": "multiple", "maxSelections": 3},
		map[string]any{"mode": "multiple", "minSelections": nil},
		map[string]any{"mode": "multiple", "maxSelections": nil},
		map[string]any{"mode": "multiple", "minSelections": "1"},
		map[string]any{"mode": "multiple", "maxSelections": 0.5},
	} {
		invalid := museNativeQuestion("native", "multiple")
		invalid["selection"] = selection
		cases = append(cases, questionCase{label: "selection/" + string(mustMuseChoiceJSON(t, selection)), questions: []any{invalid}})
	}
	for _, options := range []any{
		[]any{nil}, []any{0}, []any{map[string]any{}}, []any{map[string]any{"label": nil}},
		[]any{map[string]any{"label": 0}}, []any{map[string]any{"label": ""}}, []any{map[string]any{"label": " \t"}},
		[]any{map[string]any{"label": "one"}, map[string]any{"label": "one"}},
	} {
		invalid := museNativeQuestion("native", "single")
		invalid["options"] = options
		cases = append(cases, questionCase{label: "options/" + string(mustMuseChoiceJSON(t, options)), questions: []any{invalid}})
	}
	cases = append(cases,
		questionCase{label: "malformed later sibling", questions: []any{museNativeQuestion("native", "single"), nil}},
		questionCase{label: "duplicate IDs", questions: []any{museNativeQuestion("native", "single"), museNativeQuestion("native", "multiple")}},
	)
	for _, tc := range cases {
		for _, cancelled := range []bool{false, true} {
			t.Run(tc.label+map[bool]string{false: "/answer", true: "/cancel"}[cancelled], func(t *testing.T) {
				t.Parallel()
				id, request := questionRequest(t, tc.questions)
				answer := museQuestionReply(t, id, cancelled, []any{map[string]any{"questionId": "native", "freeText": ""}})
				original := slices.Clone(request)
				result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: answer})
				if cancelled {
					require.False(t, result.Withhold)
				} else {
					require.True(t, result.Withhold)
					assert.Error(t, result.Refusal())
					assert.Equal(t, []byte(answer), result.Content)
				}
				assert.Equal(t, original, request)
			})
		}
	}
}

func TestMuseQuestionRejectsAnUnreadableOrForeignCancellation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		label  string
		method string
		build  func(string) any
	}{
		{label: "wrong response ID", method: contracts.MuseMethodUserInputRequested, build: func(string) any { return map[string]any{"id": "foreign", "result": map[string]any{"cancelled": true}} }},
		{label: "unknown request method", method: "future/request", build: func(id string) any { return map[string]any{"id": id, "result": map[string]any{"cancelled": true}} }},
		{label: "approval request method", method: contracts.MuseMethodApprovalRequested, build: func(id string) any { return map[string]any{"id": id, "result": map[string]any{"cancelled": true}} }},
		{label: "mixed envelopes", method: contracts.MuseMethodUserInputRequested, build: func(id string) any {
			return map[string]any{"id": id, "result": map[string]any{"cancelled": true}, "response": map[string]any{"request_id": id, "response": map[string]any{"behavior": "deny"}}}
		}},
		{label: "null cancellation flag", method: contracts.MuseMethodUserInputRequested, build: func(id string) any {
			return map[string]any{"id": id, "result": map[string]any{"cancelled": nil, "answers": []any{map[string]any{"questionId": "native", "freeText": ""}}}}
		}},
		{label: "numeric cancellation flag", method: contracts.MuseMethodUserInputRequested, build: func(id string) any {
			return map[string]any{"id": id, "result": map[string]any{"cancelled": 1, "answers": []any{map[string]any{"questionId": "native", "freeText": ""}}}}
		}},
		{label: "string cancellation flag", method: contracts.MuseMethodUserInputRequested, build: func(id string) any {
			return map[string]any{"id": id, "result": map[string]any{"cancelled": "true", "answers": []any{map[string]any{"questionId": "native", "freeText": ""}}}}
		}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			id, request := questionRequest(t, []any{museNativeQuestion("native", "single")})
			var envelope map[string]any
			require.NoError(t, json.Unmarshal(request, &envelope))
			envelope["method"] = tc.method
			request = mustMuseChoiceJSON(t, envelope)
			answer := mustMuseChoiceJSON(t, tc.build(id))
			result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: answer})
			require.True(t, result.Withhold)
			assert.Error(t, result.Refusal())
			assert.Equal(t, []byte(answer), result.Content)
		})
	}
}

func TestMuseQuestionKeepsAnEmptyHeaderAndExplicitNativeCancellation(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"single", "multiple"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			question := museNativeQuestion(" native 界 ", mode)
			question["options"] = []any{}
			question["futureField"] = map[string]any{"bytes": "\u0000\n界"}
			id, request := questionRequest(t, []any{question})
			original := slices.Clone(request)
			result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: museQuestionReply(t, id, true, nil)})
			require.False(t, result.Withhold)
			var frame struct {
				Method string `json:"method"`
				Params struct {
					SessionID string `json:"sessionId"`
					InputID   string `json:"userInputId"`
				} `json:"params"`
			}
			require.NoError(t, json.Unmarshal(result.Content, &frame))
			assert.Equal(t, methodUserInputCancel, frame.Method)
			assert.Equal(t, "session", frame.Params.SessionID)
			assert.Equal(t, "question", frame.Params.InputID)
			assert.Equal(t, original, request)
			answer := museQuestionReply(t, id, false, []any{map[string]any{"questionId": " native 界 ", "freeText": ""}})
			answered := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: answer})
			require.True(t, answered.Withhold)
			assert.Error(t, answered.Refusal())
			assert.Equal(t, []byte(answer), answered.Content)
			answer = museQuestionReply(t, id, false, []any{map[string]any{"questionId": " native 界 ", "freeText": "Native typed answer"}})
			answered = (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: answer})
			require.False(t, answered.Withhold)
			require.NoError(t, json.Unmarshal(answered.Content, &frame))
			assert.Equal(t, methodUserInputAnswer, frame.Method)
		})
	}
}

func TestMuseQuestionRetriesFailedPublicationAndRejectsRetiredOwnership(t *testing.T) {
	for _, ownership := range []string{"current", "foreign", "retired"} {
		t.Run(ownership, func(t *testing.T) {
			a, sink := museControlPublicationAgent(t)
			id, raw := questionRequest(t, []any{museNativeQuestion("native", "single")})
			var request map[string]any
			require.NoError(t, json.Unmarshal(raw, &request))
			request["method"] = contracts.MuseMethodUserInputRequested
			raw = mustMuseChoiceJSON(t, request)
			switch ownership {
			case "foreign":
				a.sessionID = "replacement"
			case "retired":
				a.sessions["session"].retired = true
			}
			sink.err = errors.New("the question store is unavailable")
			a.HandleOutput(raw)
			assert.Empty(t, a.sessions["session"].controls)
			assert.Empty(t, sink.requests)
			sink.err = nil
			a.HandleOutput(raw)
			if ownership != "current" {
				assert.Empty(t, sink.requests)
				assert.Empty(t, a.sessions["session"].controls)
				return
			}
			require.Len(t, sink.requests, 1)
			assert.Equal(t, id, sink.requests[0].RequestID)
			assert.Equal(t, raw, []byte(sink.requests[0].Payload))
			a.HandleOutput(raw)
			assert.Equal(t, 2, sink.attempts)
			assert.Len(t, sink.requests, 1)
		})
	}
}

func TestMuseQuestionReadsALargeImmutableFormConcurrently(t *testing.T) {
	t.Parallel()
	questions, answers := make([]any, 256), make([]any, 256)
	for index := range questions {
		id := fmt.Sprintf("native-question-%d", index)
		questions[index] = museNativeQuestion(id, "single")
		answers[index] = map[string]any{"questionId": id, "freeText": "  Native text 界\n"}
	}
	id, request := questionRequest(t, questions)
	answer := museQuestionReply(t, id, false, answers)
	originalRequest, originalAnswer := slices.Clone(request), slices.Clone(answer)
	results := make(chan agent.ControlResponseResolution, 16)
	var callers sync.WaitGroup
	for range 16 {
		callers.Go(func() {
			results <- (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: answer})
		})
	}
	callers.Wait()
	close(results)
	for result := range results {
		require.False(t, result.Withhold)
		var reply struct {
			Params struct {
				Answers []json.RawMessage `json:"answers"`
			} `json:"params"`
		}
		require.NoError(t, json.Unmarshal(result.Content, &reply))
		require.Len(t, reply.Params.Answers, len(answers))
		for index, raw := range reply.Params.Answers {
			assert.JSONEq(t, string(mustMuseChoiceJSON(t, answers[index])), string(raw))
		}
	}
	assert.Equal(t, originalRequest, request)
	assert.Equal(t, originalAnswer, answer)
}

func (sink *museControlPublicationSink) PersistNotification(source leapmuxv1.MessageSource, content agent.MessageContent) (bool, error) {
	sink.notificationAttempts = append(sink.notificationAttempts, content.Clone().Original)
	if sink.notificationErr != nil {
		return false, sink.notificationErr
	}
	return sink.Sink.PersistNotification(source, content)
}

func TestMuseApprovalNeutralDenialUsesTheOfferedOnceAbort(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		label, decision, scope, feedback string
		acceptsFeedback, valid           bool
	}{
		{label: "actual once abort", decision: contracts.MuseApprovalDecisionAbort, scope: contracts.MuseChoiceScopeOnce, acceptsFeedback: true, valid: true},
		{label: "actual once abort feedback", decision: contracts.MuseApprovalDecisionAbort, scope: contracts.MuseChoiceScopeOnce, feedback: "  Native refusal 界\n", acceptsFeedback: true, valid: true},
		{label: "once denied", decision: contracts.MuseApprovalDecisionDenied, scope: contracts.MuseChoiceScopeOnce, valid: true},
		{label: "abort refuses feedback", decision: contracts.MuseApprovalDecisionAbort, scope: contracts.MuseChoiceScopeOnce, feedback: "Keep this feedback"},
		{label: "session abort", decision: contracts.MuseApprovalDecisionAbort, scope: contracts.MuseChoiceScopeSession},
		{label: "persistent abort", decision: contracts.MuseApprovalDecisionAbort, scope: contracts.MuseChoiceScopeLocalPersistent},
		{label: "future decision", decision: "futureDecision", scope: contracts.MuseChoiceScopeOnce},
		{label: "future scope", decision: contracts.MuseApprovalDecisionAbort, scope: "futureScope"},
		{label: "timeout is not a denial choice", decision: contracts.MuseApprovalDecisionTimedOut, scope: contracts.MuseChoiceScopeOnce},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			choice := museApprovalChoice("actual-native-reject", tc.decision, tc.scope)
			choice["acceptsFeedback"] = tc.acceptsFeedback
			id, request := museApprovalRequest(t, []any{museApprovalChoice("allow_once", contracts.MuseApprovalDecisionApproved, contracts.MuseChoiceScopeOnce), choice})
			answer := mustMuseChoiceJSON(t, map[string]any{"response": map[string]any{"request_id": id, "response": map[string]any{"behavior": agent.ControlBehaviorDeny, "message": tc.feedback}}})
			originalRequest, originalAnswer := slices.Clone(request), slices.Clone(answer)
			result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: answer})
			assert.Equal(t, !tc.valid, result.Withhold)
			if tc.valid {
				require.NoError(t, result.Refusal())
				var frame struct {
					Method string `json:"method"`
					Params struct {
						Session     string          `json:"sessionId"`
						Approval    string          `json:"approvalId"`
						Choice      string          `json:"choiceId"`
						Requirement json.RawMessage `json:"requirementId"`
						Feedback    string          `json:"feedback"`
					} `json:"params"`
				}
				require.NoError(t, json.Unmarshal(result.Content, &frame))
				assert.Equal(t, methodApprovalDecide, frame.Method)
				assert.Equal(t, "session", frame.Params.Session)
				assert.Equal(t, "approval", frame.Params.Approval)
				assert.Equal(t, "actual-native-reject", frame.Params.Choice)
				assert.JSONEq(t, `{"approvalId":"approval","sourceIndex":0}`, string(frame.Params.Requirement))
				assert.Equal(t, tc.feedback, frame.Params.Feedback)
			} else {
				require.Error(t, result.Refusal())
				assert.Equal(t, []byte(answer), result.Content)
			}
			assert.Equal(t, originalRequest, request)
			assert.Equal(t, originalAnswer, answer)
		})
	}
}

func TestMuseQuestionCancellationAlwaysSuppliesTheNativeReason(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		label, reason string
		present       bool
	}{
		{label: "omitted browser reason"},
		{label: "explicit empty reason", present: true},
		{label: "whitespace reason", reason: " \t\n", present: true},
		{label: "nonempty reason", reason: "The user declined this question.", present: true},
		{label: "Unicode reason", reason: "  Native reason 界\n", present: true},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			id, request := questionRequest(t, []any{museNativeQuestion("native", "single")})
			reply := map[string]any{"cancelled": true}
			if tc.present {
				reply["reason"] = tc.reason
			}
			answer := mustMuseChoiceJSON(t, map[string]any{"id": id, "result": reply})
			originalRequest, originalAnswer := slices.Clone(request), slices.Clone(answer)
			result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: answer})
			require.False(t, result.Withhold)
			var frame struct {
				Method string                     `json:"method"`
				Params map[string]json.RawMessage `json:"params"`
			}
			require.NoError(t, json.Unmarshal(result.Content, &frame))
			assert.Equal(t, methodUserInputCancel, frame.Method)
			assert.JSONEq(t, `"session"`, string(frame.Params["sessionId"]))
			assert.JSONEq(t, `"question"`, string(frame.Params["userInputId"]))
			require.Contains(t, frame.Params, "reason", "the installed native host refuses an omitted cancellation reason")
			var reason string
			require.NoError(t, json.Unmarshal(frame.Params["reason"], &reason))
			assert.Equal(t, tc.reason, reason)
			assert.NotContains(t, frame.Params, "answers")
			assert.Equal(t, originalRequest, request)
			assert.Equal(t, originalAnswer, answer)
		})
	}
}

func TestMuseQuestionCancellationRejectsAnUnreadableReason(t *testing.T) {
	t.Parallel()
	for _, reason := range []any{nil, false, 0, []any{}, map[string]any{}} {
		t.Run(string(mustMuseChoiceJSON(t, reason)), func(t *testing.T) {
			t.Parallel()
			id, request := questionRequest(t, []any{museNativeQuestion("native", "single")})
			answer := mustMuseChoiceJSON(t, map[string]any{"id": id, "result": map[string]any{"cancelled": true, "reason": reason}})
			originalRequest, originalAnswer := slices.Clone(request), slices.Clone(answer)
			result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: answer})
			require.True(t, result.Withhold)
			assert.Error(t, result.Refusal())
			assert.Equal(t, []byte(answer), result.Content)
			assert.Equal(t, originalRequest, request)
			assert.Equal(t, originalAnswer, answer)
		})
	}
}

func TestMuseQuestionTypedAnswersKeepNativeValidationSeparateFromNotes(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"single", "multiple"} {
		for _, tc := range []struct {
			label           string
			text            string
			selected, valid bool
		}{
			{label: "nonempty text", text: "  Native typed answer 界\n", valid: true},
			{label: "empty text"},
			{label: "whitespace text", text: " \t\n"},
			{label: "empty selected note", selected: true, valid: true},
			{label: "whitespace selected note", selected: true, text: " \t\n", valid: true},
			{label: "nonempty selected note", selected: true, text: "  Native note 界\n", valid: true},
		} {
			t.Run(mode+"/"+tc.label, func(t *testing.T) {
				t.Parallel()
				question := museNativeQuestion("native", mode)
				if mode == "multiple" {
					question["selection"] = map[string]any{"mode": mode, "minSelections": 1, "maxSelections": 2}
				}
				id, request := questionRequest(t, []any{question})
				fields := map[string]any{"questionId": "native", "freeText": tc.text}
				if tc.selected {
					delete(fields, "freeText")
					fields["note"] = tc.text
					if mode == "single" {
						fields["selectedLabel"] = "one"
					} else {
						fields["selectedLabels"] = []string{"one", "two"}
					}
				}
				answer := museQuestionReply(t, id, false, []any{fields})
				originalRequest, originalAnswer := slices.Clone(request), slices.Clone(answer)
				result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: answer})
				assert.Equal(t, !tc.valid, result.Withhold)
				if tc.valid {
					var frame struct {
						Method string `json:"method"`
						Params struct {
							Answers []json.RawMessage `json:"answers"`
						} `json:"params"`
					}
					require.NoError(t, result.Refusal())
					require.NoError(t, json.Unmarshal(result.Content, &frame))
					assert.Equal(t, methodUserInputAnswer, frame.Method)
					require.Len(t, frame.Params.Answers, 1)
					assert.JSONEq(t, string(mustMuseChoiceJSON(t, fields)), string(frame.Params.Answers[0]))
				} else {
					assert.Error(t, result.Refusal())
					assert.Equal(t, []byte(answer), result.Content)
				}
				assert.Equal(t, originalRequest, request)
				assert.Equal(t, originalAnswer, answer)
			})
		}
	}
}

func TestMuseApprovalForwardsFutureNativeVocabularyForAnExactChoice(t *testing.T) {
	t.Parallel()
	for _, vocabulary := range [][2]string{{"futureDecision", contracts.MuseChoiceScopeOnce}, {contracts.MuseApprovalDecisionApproved, "futureScope"}, {"futureDecision", "futureScope"}} {
		t.Run(vocabulary[0]+"/"+vocabulary[1], func(t *testing.T) {
			t.Parallel()
			id, request := museApprovalRequest(t, []any{museApprovalChoice("exact-future-choice", vocabulary[0], vocabulary[1])})
			original := slices.Clone(request)
			result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: museApprovalReply(t, id, "exact-future-choice", "")})
			require.False(t, result.Withhold)
			var frame struct {
				Method string `json:"method"`
				Params struct {
					ChoiceID    string          `json:"choiceId"`
					Requirement json.RawMessage `json:"requirementId"`
				} `json:"params"`
			}
			require.NoError(t, json.Unmarshal(result.Content, &frame))
			assert.Equal(t, methodApprovalDecide, frame.Method)
			assert.Equal(t, "exact-future-choice", frame.Params.ChoiceID)
			assert.JSONEq(t, `{"approvalId":"approval","sourceIndex":0}`, string(frame.Params.Requirement))
			assert.Equal(t, original, request)
		})
	}
}

func TestMuseApprovalNeutralFallbackDoesNotGuessFutureVocabulary(t *testing.T) {
	t.Parallel()
	for _, behavior := range []string{agent.ControlBehaviorAllow, agent.ControlBehaviorDeny} {
		for _, knownChoice := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/known=%t", behavior, knownChoice), func(t *testing.T) {
				t.Parallel()
				decision := contracts.MuseApprovalDecisionApproved
				if behavior == agent.ControlBehaviorDeny {
					decision = contracts.MuseApprovalDecisionDenied
				}
				choices := []any{museApprovalChoice("future-decision", "futureDecision", contracts.MuseChoiceScopeOnce), museApprovalChoice("future-scope", decision, "futureScope")}
				if knownChoice {
					choices = append(choices, museApprovalChoice("known-once-choice", decision, contracts.MuseChoiceScopeOnce))
				}
				id, request := museApprovalRequest(t, choices)
				original := slices.Clone(request)
				answer := mustMuseChoiceJSON(t, map[string]any{"response": map[string]any{"request_id": id, "response": map[string]any{"behavior": behavior}}})
				result := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: id, RequestPayload: request, ResponseContent: answer})
				assert.Equal(t, !knownChoice, result.Withhold)
				if knownChoice {
					var frame struct {
						Params struct {
							ChoiceID string `json:"choiceId"`
						} `json:"params"`
					}
					require.NoError(t, json.Unmarshal(result.Content, &frame))
					assert.Equal(t, "known-once-choice", frame.Params.ChoiceID)
				} else {
					assert.Error(t, result.Refusal())
					assert.Equal(t, []byte(answer), result.Content)
				}
				assert.Equal(t, original, request)
			})
		}
	}
}

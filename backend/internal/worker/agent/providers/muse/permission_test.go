package muse

import (
	"bytes"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func nativePermissionRequest(t *testing.T, id string, index int64) []byte {
	t.Helper()
	return []byte(mustMuseChoiceJSON(t, map[string]any{
		"jsonrpc": "2.0", "method": contracts.MuseMethodApprovalRequested,
		"params": map[string]any{
			"sessionId": "session", "approvalId": id,
			"currentRequirementId": map[string]any{"approvalId": id, "sourceIndex": index},
			"availableChoices":     []any{map[string]any{"choiceId": "once", "decision": contracts.MuseApprovalDecisionApproved, "label": "Allow once", "scope": contracts.MuseChoiceScopeOnce}},
		},
	}))
}

func TestBypassApprovesPendingAndLaterRequirementsButKeepsQuestions(t *testing.T) {
	for _, effective := range []string{contracts.MuseApprovalModeAllowAll, contracts.MuseApprovalModeDenyUnmatched} {
		t.Run(effective, func(t *testing.T) {
			a, _ := testAgent(t)
			sink := &agenttest.ControlSink{}
			a.sink = agent.NewProviderServices(sink)
			a.sessions["session"].sink = a.sink
			a.settings[agent.OptionIDPermissionMode] = contracts.MuseApprovalModePromptUnmatched
			var mu sync.Mutex
			var decisions []string
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
				var params struct {
					CommandID   string          `json:"commandId"`
					ApprovalID  string          `json:"approvalId"`
					Requirement json.RawMessage `json:"requirementId"`
				}
				if err := json.Unmarshal(raw, &params); err != nil {
					t.Error(err)
					return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"invalid parameters"}`)}
				}
				result := map[string]any{"commandId": params.CommandID, "status": "accepted"}
				switch method {
				case methodSetApprovalMode:
					result["effectiveMode"] = map[string]string{"mode": effective}
				case methodApprovalDecide:
					mu.Lock()
					decisions = append(decisions, params.ApprovalID)
					mu.Unlock()
					feed(t, a, contracts.MuseMethodApprovalResolved, map[string]any{
						"sessionId": "session", "approvalId": params.ApprovalID, "decision": contracts.MuseApprovalDecisionApproved,
						"stageEvidence": []any{map[string]any{"requirementId": params.Requirement, "position": 0, "totalStages": 1, "argv": []any{}, "resolution": map[string]any{"kind": "approved"}}},
					})
				default:
					t.Errorf("unexpected native method: %s", method)
				}
				encoded, _ := json.Marshal(result)
				return agenttest.RPCReply{Result: encoded}
			}})
			a.HandleOutput(nativePermissionRequest(t, "first", 0))
			a.HandleOutput(nativePermissionRequest(t, "second", 0))
			_, question := questionRequest(t, []any{map[string]any{"id": "q", "question": "Which option?", "options": []any{}, "selection": map[string]any{"mode": "single"}}})
			a.HandleOutput(question)
			a.UpdateSettings(map[string]string{agent.OptionIDPermissionMode: contracts.MuseApprovalModeAllowAll})
			if effective == contracts.MuseApprovalModeAllowAll {
				require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(decisions) == 2 }, 30*time.Second, time.Millisecond)
				require.Eventually(t, func() bool { return len(sink.CanceledControls()) == 2 }, 30*time.Second, time.Millisecond)
			}
			mu.Lock()
			received := append([]string(nil), decisions...)
			mu.Unlock()
			if effective != contracts.MuseApprovalModeAllowAll {
				assert.Empty(t, received, "a pinned native restriction cannot authorize Bypass")
				assert.Empty(t, sink.CanceledControls())
				return
			}
			require.ElementsMatch(t, []string{"first", "second"}, received)
			assert.ElementsMatch(t, []string{testApprovalKey("session", "first", 0), testApprovalKey("session", "second", 0)}, sink.CanceledControls())
			a.HandleOutput(nativePermissionRequest(t, "later", 0))
			require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(decisions) == 3 }, 30*time.Second, time.Millisecond)
			assert.Equal(t, 3, sink.PublishedControlCount(), "the later requirement needs no manual card")
		})
	}
}

func TestRawControlAnswerKeepsTheExactLargeRequirementIndex(t *testing.T) {
	a, _ := testAgent(t)
	const index int64 = 9007199254740993
	request := nativePermissionRequest(t, "approval", index)
	a.HandleOutput(request)
	var received int64
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(_ string, raw json.RawMessage) agenttest.RPCReply {
		var params struct {
			CommandID   string `json:"commandId"`
			Requirement struct {
				Index int64 `json:"sourceIndex"`
			} `json:"requirementId"`
		}
		if err := json.Unmarshal(raw, &params); err != nil {
			t.Error(err)
		}
		received = params.Requirement.Index
		encoded, _ := json.Marshal(map[string]any{"commandId": params.CommandID, "status": "accepted"})
		return agenttest.RPCReply{Result: encoded}
	}})
	key := testApprovalKey("session", "approval", index)
	answer := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: key, RequestPayload: request, ResponseContent: approvalAnswer(key)})
	require.False(t, answer.Withhold)
	require.NoError(t, a.SendRawInput(answer.Content, agent.StopContext{}))
	assert.Equal(t, index, received)
}

func TestNoPermissionWorkStartsWithoutAnOutstandingControl(t *testing.T) {
	a, _ := testAgent(t)
	a.sendMu.Lock()
	a.queuePermissionApprovals()
	assert.True(t, a.approvalWork.Idle(), "a settings change with no controls needs no background task")
	a.sendMu.Unlock()
	require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
}

func TestRawControlMethodMustMatchItsNativeIdentity(t *testing.T) {
	for _, method := range []string{methodUserInputAnswer, methodUserInputCancel, methodApprovalDecide} {
		t.Run(method, func(t *testing.T) {
			a, _ := testAgent(t)
			a.HandleOutput(approvalRequest("session", 0))
			questionKey, question := questionRequest(t, []any{})
			a.HandleOutput(question)
			calls := 0
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(_ string, raw json.RawMessage) agenttest.RPCReply {
				calls++
				var params struct {
					CommandID string `json:"commandId"`
				}
				if err := json.Unmarshal(raw, &params); err != nil {
					t.Error(err)
				}
				result, _ := json.Marshal(map[string]string{"commandId": params.CommandID, "status": "accepted"})
				return agenttest.RPCReply{Result: result}
			}})
			key := testApprovalKey("session", "approval", 0)
			params := map[string]any{"sessionId": "session", "userInputId": "question", "approvalId": "approval", "currentRequirementId": map[string]any{"approvalId": "approval", "sourceIndex": 0}}
			if method == methodApprovalDecide {
				key = questionKey
				delete(params, "approvalId")
			}
			raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": key, "method": method, "params": params})
			assert.Error(t, a.SendRawInput(raw, agent.StopContext{}))
			assert.Zero(t, calls, "a mismatched method must reach no native control")
		})
	}
}

func TestBypassUsesOnlyOneUnambiguousOnceChoice(t *testing.T) {
	for _, tc := range []struct {
		name    string
		choices string
		allowed bool
	}{
		{"once", `[{"choiceId":"once","label":"Allow","decision":"approved","scope":"once"}]`, true},
		{"session", `[{"choiceId":"session","label":"Allow","decision":"approved","scope":"session"}]`, false},
		{"denial", `[{"choiceId":"deny","label":"Deny","decision":"denied","scope":"once"}]`, false},
		{"ambiguous", `[{"choiceId":"first","label":"Allow","decision":"approved","scope":"once"},{"choiceId":"second","label":"Allow","decision":"approved","scope":"once"}]`, false},
		{"empty", `[]`, false},
		{"malformed", `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := controlParams{SessionID: "session", ApprovalID: "approval", Requirement: json.RawMessage(`{"approvalId":"approval","sourceIndex":0}`), Choices: json.RawMessage(tc.choices)}
			reply := onceApprovalReply(params)
			if !tc.allowed {
				assert.Nil(t, reply)
				return
			}
			require.NotNil(t, reply)
			assert.Equal(t, "once", reply["choiceId"])
			assert.JSONEq(t, string(params.Requirement), string(reply["requirementId"].(json.RawMessage)))
		})
	}
}

func TestRootBypassKeepsChildRequirementsManual(t *testing.T) {
	a, _ := testAgent(t)
	sink := &agenttest.ControlSink{}
	a.sink = agent.NewProviderServices(sink)
	a.sessions["child"] = &sessionState{childID: "child-agent", sink: a.sink}
	a.settings[agent.OptionIDPermissionMode] = contracts.MuseApprovalModeAllowAll
	var request map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(nativePermissionRequest(t, "child-approval", 0), &request))
	var params map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(request["params"], &params))
	params["sessionId"] = json.RawMessage(`"child"`)
	encoded, err := json.Marshal(params)
	require.NoError(t, err)
	request["params"] = encoded
	encoded, err = json.Marshal(request)
	require.NoError(t, err)
	a.HandleOutput(encoded)
	assert.Equal(t, 1, sink.PublishedControlCount())
	assert.True(t, a.approvalWork.Idle())
	assert.Empty(t, sink.CanceledControls())
}

func TestFailedAutomaticMuseAnswerKeepsOneManualFallback(t *testing.T) {
	a, _ := testAgent(t)
	sink := &agenttest.ControlSink{}
	a.sink = agent.NewProviderServices(sink)
	a.sessions["session"].sink = a.sink
	a.settings[agent.OptionIDPermissionMode] = contracts.MuseApprovalModeAllowAll
	var mu sync.Mutex
	calls := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(_ string, _ json.RawMessage) agenttest.RPCReply {
		mu.Lock()
		calls++
		mu.Unlock()
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"choice refused"}`)}
	}})
	a.HandleOutput(nativePermissionRequest(t, "approval", 0))
	require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
	require.Equal(t, 1, sink.PublishedControlCount())
	a.queuePermissionApprovals()
	require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
	mu.Lock()
	assert.Equal(t, 1, calls)
	mu.Unlock()
	assert.Empty(t, sink.CanceledControls())
	key := testApprovalKey("session", "approval", 0)
	request := nativePermissionRequest(t, "approval", 0)
	answer := (museProvider{}).ResolveControlResponse(agent.ControlResponseContext{RequestID: key, RequestPayload: request, ResponseContent: approvalAnswer(key)})
	require.False(t, answer.Withhold)
	assert.Error(t, a.SendRawInput(answer.Content, agent.StopContext{}))
	mu.Lock()
	assert.Equal(t, 2, calls, "the reader can retry a definite refusal")
	mu.Unlock()
}

func TestMuseQuestionMethodCannotAuthorizeATool(t *testing.T) {
	a, _ := testAgent(t)
	sink := &agenttest.ControlSink{}
	a.sink = agent.NewProviderServices(sink)
	a.sessions["session"].sink = a.sink
	a.settings[agent.OptionIDPermissionMode] = contracts.MuseApprovalModeAllowAll
	var calls atomic.Int32
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(_ string, raw json.RawMessage) agenttest.RPCReply {
		calls.Add(1)
		var params struct {
			CommandID string `json:"commandId"`
		}
		if err := json.Unmarshal(raw, &params); err != nil {
			t.Error(err)
		}
		encoded, _ := json.Marshal(map[string]string{"commandId": params.CommandID, "status": "accepted"})
		return agenttest.RPCReply{Result: encoded}
	}})
	raw := bytes.ReplaceAll(nativePermissionRequest(t, "approval", 0), []byte(contracts.MuseMethodApprovalRequested), []byte(contracts.MuseMethodUserInputRequested))
	a.HandleOutput(raw)
	require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
	assert.Zero(t, calls.Load())
}

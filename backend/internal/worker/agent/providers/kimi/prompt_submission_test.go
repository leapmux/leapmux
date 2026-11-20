package kimi

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newKimiPublicPromptRig(t *testing.T) (*kimiTestRig, *kimiOutputRig) {
	t.Helper()
	rig := newKimiTestRig(t, agent.Options{})
	main := rig.agent.run(kimiMainAgentID)
	waitFor(t, func() bool { return main.conversationRecovery.Load() == nil }, "the prompt test starts after the initial page")
	return rig, &kimiOutputRig{agent: rig.agent, sink: rig.sink}
}

func kimiPublicPromptItem(t *testing.T, itemID, placement string, promptIDs ...string) string {
	t.Helper()
	if promptIDs == nil {
		promptIDs = []string{}
	}
	encoded, err := json.Marshal(promptIDs)
	require.NoError(t, err)
	return fmt.Sprintf(`{"type":"user_message","item_id":%q,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","prompt_ids":%s,"placement":%q,"origin":{"kind":"user"},"text":"Native prompt","attachments":[],"skills":[]}`, itemID, encoded, placement)
}

func kimiPromptObserved(a *Agent, promptID string) bool {
	a.dispatchMu.Lock()
	defer a.finishDispatch()
	submission := a.promptSubmissions[promptID]
	return submission != nil && submission.observed
}

func TestKimiPromptExecutionProofUsesItsSessionAndReceivingAgent(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name     string
		observed bool
	}{
		{"live opener", true},
		{"snapshot opener", true},
		{"coalesced steer", true},
		{"missing prompt id", false},
		{"unrelated prompt id", false},
		{"wrong receiving agent", false},
		{"wrong session", false},
		{"missing session", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			rig, native := newKimiPublicPromptRig(t)
			receiver := kimiMainAgentID
			if scenario.name == "wrong receiving agent" {
				receiver = "agent-0"
			}
			reply, err := rig.agent.submitPrompt(rig.sessionID(), receiver, []map[string]any{{"type": "text", "text": "Run the prompt."}})
			require.NoError(t, err)
			requests := rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/prompts"))
			require.Len(t, requests, 1)
			assert.Equal(t, decodePrompt(t, requests[0]).PromptID, reply.PromptID)
			require.Len(t, reply.PromptID, 48)
			native.startPublicTurn(t, kimiMainAgentID, "turn", 0)
			assert.False(t, kimiPromptObserved(rig.agent, reply.PromptID), "a turn header has no prompt execution proof")

			item := kimiPublicPromptItem(t, reply.PromptID, "opener", reply.PromptID)
			switch scenario.name {
			case "missing prompt id":
				item = kimiPublicPromptItem(t, "external", "opener")
			case "unrelated prompt id":
				item = kimiPublicPromptItem(t, "external", "opener", "external")
			case "coalesced steer":
				native.publicOpener(t, kimiMainAgentID, "turn", `{"kind":"user"}`)
				item = kimiPublicPromptItem(t, "steered", "steer", "external", reply.PromptID)
			}
			frame := `{"type":"item.ended","turn_id":"turn","item":` + item + `}`
			switch scenario.name {
			case "snapshot opener":
				native.conversationFrame(t, `{"type":"turns","has_more":false,"turns":[{"turn_id":"turn","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[`+item+`]}]}`)
			case "wrong session":
				rig.agent.HandleOutput(kimiConversationTestFrame(t, "foreign-session", kimiMainAgentID, frame))
			case "missing session":
				rig.agent.HandleOutput(kimiConversationTestFrame(t, "", kimiMainAgentID, frame))
			default:
				native.conversationFrame(t, frame)
			}
			assert.Equal(t, scenario.observed, kimiPromptObserved(rig.agent, reply.PromptID))
			assert.Empty(t, rig.sink.Messages(), "the native proof cannot duplicate input that LeapMux already owns")
		})
	}
}

func TestKimiPromptSubmissionRetainsExecutionBeforeTheReceipt(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"lost receipt", "late refusal", "different receipt id", "invalid status", "definite refusal"} {
		for _, tracked := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s tracked=%t", scenario, tracked), func(t *testing.T) {
				t.Parallel()
				rig, native := newKimiPublicPromptRig(t)
				prove := func(promptID string) {
					t.Helper()
					native.startPublicTurn(t, kimiMainAgentID, "turn", 0)
					native.conversationFrame(t, `{"type":"item.ended","turn_id":"turn","item":`+
						kimiPublicPromptItem(t, promptID, "opener", promptID)+`}`)
				}
				result := fakeKapReply{DataFor: func(request fakeKapRequest) any {
					promptID := decodePrompt(t, request).PromptID
					if scenario != "definite refusal" {
						prove(promptID)
					}
					returnedID, status := promptID, kimiPromptRunning
					if scenario == "different receipt id" {
						returnedID = "foreign-receipt"
					}
					if scenario == "invalid status" {
						status = "invalid"
					}
					return map[string]any{"prompt_id": returnedID, "status": status}
				}}
				path := "POST " + kimiSessionPath(rig.sessionID(), "/prompts")
				if scenario == "lost receipt" {
					result.Drop = true
					result.BeforeReply = func() {
						requests := rig.fake.requestsTo(path)
						require.Len(t, requests, 1)
						prove(decodePrompt(t, requests[0]).PromptID)
					}
				}
				if scenario == "late refusal" || scenario == "definite refusal" {
					result.Code, result.Msg = 40001, "prompt refused"
				}
				rig.fake.reply(path, result)
				var receipt agent.NativeInputReceipt
				var err error
				if tracked {
					receipt, err = rig.agent.SendInputWithReceipt("Run the prompt.", nil)
				} else {
					err = rig.agent.SendInput("Run the prompt.", nil)
				}
				requests := rig.fake.requestsTo(path)
				require.Len(t, requests, 1)
				promptID := decodePrompt(t, requests[0]).PromptID
				if tracked && scenario != "definite refusal" {
					require.NoError(t, err)
					assert.Equal(t, agent.NativeInputReceipt{SessionID: rig.sessionID(), InputID: promptID}, receipt)
				} else {
					require.Error(t, err)
					assert.Empty(t, receipt)
					if scenario != "late refusal" && scenario != "definite refusal" {
						assert.ErrorIs(t, err, agent.ErrDeliveryUncertain)
					}
				}
				assert.Equal(t, scenario != "definite refusal", kimiPromptObserved(rig.agent, promptID))
				prove(promptID)
				assert.Equal(t, scenario != "definite refusal", kimiPromptObserved(rig.agent, promptID),
					"a late native item cannot resurrect a definitively refused submission")
			})
		}
	}
}

func TestKimiOlderPromptItemCannotRelabelTheCurrentTurn(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "old", 0)
	rig.endPublicTurn(t, kimiMainAgentID, "old", 0, "completed")
	rig.startPublicTurn(t, kimiMainAgentID, "current", 1)
	rig.publicOpener(t, kimiMainAgentID, "current", `{"kind":"system"}`)
	require.Equal(t, agent.TurnState{Active: true}, rig.agent.PublishTurnActive())
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"old","item":`+
		kimiPublicPromptItem(t, "old-prompt", "opener", "old-prompt")+`}`)
	assert.Equal(t, agent.TurnState{Active: true}, rig.agent.PublishTurnActive(),
		"the older user opener cannot allow steering in the current autonomous turn")
	rig.endPublicTurn(t, kimiMainAgentID, "current", 1, "completed")
	assert.Equal(t, agent.TurnState{}, rig.agent.PublishTurnActive())
}

func TestKimiUnjoinedChildActivityStaysIndependentFromLaterParentInvocation(t *testing.T) {
	t.Parallel()
	for _, submitBefore := range []bool{false, true} {
		t.Run(fmt.Sprintf("submission_before_unknown_turn=%t", submitBefore), func(t *testing.T) {
			t.Parallel()
			rig, native := newKimiChildPromptRig(t)
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
			native.endPublicTurn(t, kimiMainAgentID, "t0", 0, "completed")
			submit := func() string {
				t.Helper()
				reply, err := rig.agent.submitPrompt(rig.sessionID(), kimiMainAgentID, []map[string]any{{"type": "text", "text": "Resume again."}})
				require.NoError(t, err)
				return reply.PromptID
			}
			var promptID string
			if submitBefore {
				promptID = submit()
			}
			native.startPublicTurn(t, "agent-0", "unknown", 1)
			native.endPublicTurn(t, "agent-0", "unknown", 1, "completed")
			row, _ := childOf(t, native.sink, "session_1/agent-0")
			assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, row.Status)
			if !submitBefore {
				promptID = submit()
			}
			native.startPublicTurn(t, kimiMainAgentID, "t1", 1)
			native.conversationFrame(t, `{"type":"item.ended","turn_id":"t1","item":`+
				kimiPublicPromptItem(t, promptID, "opener", promptID)+`}`)
			native.conversationFrame(t, `{"type":"item.started","turn_id":"t1","item":{"type":"tool_call","item_id":"resume","step_index":1,"status":"running","stage":"executing","created_at":"2026-11-18T00:00:01Z","started_at":"2026-11-18T00:00:01Z","name":"Agent","input":{"resume":"agent-0","prompt":"Review again."},"agent_refs":[{"agent_id":"agent-0","role":"child"}]}}`)
			row, _ = childOf(t, native.sink, "session_1/agent-0")
			assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, row.Status, "a root invocation does not revive unjoined child activity")
			assert.Equal(t, bgtask.StatusSucceeded, nativeTaskOf(t, native.sink, "old-task").Status)
			current := invocationOutcomeOf(t, native.sink, kimiInvocationIdentity{sessionID: "session_1", agentID: "main", turnID: "t1", toolCallID: "resume"}, "agent-0")
			assert.Equal(t, bgtask.StatusRunning, current.Status)
			assert.True(t, kimiPromptObserved(rig.agent, promptID))
		})
	}
}

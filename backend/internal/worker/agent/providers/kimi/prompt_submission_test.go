package kimi

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func newKimiPromptSubmissionRig(t *testing.T, userRunning bool) (*kimiTestRig, *kimiOutputRig) {
	t.Helper()
	rig := newKimiTestRig(t, agent.Options{})
	native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
	seedKimiChildInvocation(t, native)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
	native.transcriptAgentOps(t, "agent-0", 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"user","state":"running","origin":{"kind":"user"}}}`)
	if !userRunning {
		native.transcriptAgentOps(t, "agent-0", 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"user","state":"completed","origin":{"kind":"user"}}}`)
	}
	native.transcriptOps(t, 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"completed","origin":{"kind":"user"}}}`)
	return rig, native
}

func kimiPromptResumeFrame() string {
	return `{"kind":"tool","frameId":"resume-frame","toolCallId":"resume","name":"Agent","state":"running","input":{"resume":"agent-0","prompt":"Review again."},"agentRefs":[{"agentId":"agent-0","role":"child"}]}`
}

func TestKimiPromptExecutionProofUsesItsSessionAndReceivingAgent(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"owned trigger", "missing trigger", "foreign trigger", "submission before idle", "newer user", "foreign receiver", "foreign session", "reset trigger"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			rig, native := newKimiPromptSubmissionRig(t, scenario == "submission before idle")
			receiver := kimiMainAgentID
			if scenario == "foreign receiver" {
				receiver = "agent-0"
			}
			reply, err := rig.agent.submitPrompt(rig.sessionID(), receiver, []map[string]any{{"type": "text", "text": "Resume the child."}})
			require.NoError(t, err)
			requests := rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/prompts"))
			require.Len(t, requests, 1)
			assert.Equal(t, decodePrompt(t, requests[0]).PromptID, reply.PromptID)
			assert.Len(t, reply.PromptID, 48)
			if scenario == "submission before idle" {
				native.transcriptAgentOps(t, "agent-0", 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"user","state":"completed","origin":{"kind":"user"}}}`)
			}
			if scenario == "newer user" {
				native.transcriptAgentOps(t, "agent-0", 3,
					`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"user-two","state":"running","origin":{"kind":"user"}}}`,
					`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"user-two","state":"completed","origin":{"kind":"user"}}}`)
			}
			promptID := reply.PromptID
			switch scenario {
			case "missing trigger":
				promptID = ""
			case "foreign trigger":
				promptID = "foreign-prompt"
			}
			turn := fmt.Sprintf(`{"kind":"turn","turnId":"t1","ordinal":1,"state":"running","triggerPromptId":%q,"origin":{"kind":"user"}}`, promptID)
			switch scenario {
			case "reset trigger":
				var value map[string]any
				require.NoError(t, json.Unmarshal([]byte(turn), &value))
				value["steps"] = []any{map[string]any{"kind": "step", "stepId": "t1.1", "ordinal": 1, "state": "running", "frames": []json.RawMessage{json.RawMessage(kimiPromptResumeFrame())}}}
				rig.agent.HandleOutput(nativeKimiRecoveryReset(t, rig.sessionID(), "main", 3, value))
			case "foreign session":
				var value map[string]any
				require.NoError(t, json.Unmarshal([]byte(turn), &value))
				rig.agent.HandleOutput(nativeKimiRecoveryReset(t, "foreign-session", "main", 3, value))
			default:
				native.transcriptOps(t, 3,
					`{"op":"turn.upsert","turn":`+turn+`}`,
					`{"op":"step.upsert","turnId":"t1","step":{"kind":"step","stepId":"t1.1","ordinal":1,"state":"running"}}`,
					`{"op":"frame.upsert","turnId":"t1","stepId":"t1.1","frame":`+kimiPromptResumeFrame()+`}`)
			}
			rig.agent.dispatchMu.Lock()
			submission := rig.agent.promptSubmissions[reply.PromptID]
			observed := submission != nil && submission.observed
			rig.agent.finishDispatch()
			expected := scenario != "missing trigger" && scenario != "foreign trigger" && scenario != "foreign receiver" && scenario != "foreign session"
			assert.Equal(t, expected, observed)
			row, _ := childOf(t, native.sink, "session_1/agent-0")
			assert.Equal(t, bgtask.StatusSucceeded, row.Status, "root prompt proof does not own the child's next turn")
			assert.Equal(t, bgtask.StatusSucceeded, nativeTaskOf(t, native.sink, "old-task").Status)
		})
	}
}

func TestKimiConsumedPromptKeepsItsNativeTurnAndStep(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name, consumedOrdinal, toolOrdinal string
	}{
		{"later step", `-2`, `-1`},
		{"same step", `1`, `1`},
		{"earlier step", `2`, `1`},
		{"missing consumed ordinal", `null`, `2`},
		{"missing invocation ordinal", `1`, `null`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			rig, native := newKimiPromptSubmissionRig(t, false)
			native.transcriptOps(t, 3, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t1","ordinal":1,"state":"running","triggerPromptId":"older-unowned-prompt","origin":{"kind":"user"}}}`)
			rig.fake.setBusy(rig.sessionID(), true)
			require.NoError(t, rig.agent.SteerInput("Resume the child.", nil))
			requests := rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/prompts"))
			require.Len(t, requests, 1)
			promptID := decodePrompt(t, requests[0]).PromptID
			native.transcriptOps(t, 4,
				fmt.Sprintf(`{"op":"step.upsert","turnId":"t1","step":{"kind":"step","stepId":"consumed","ordinal":%s,"state":"running"}}`, scenario.consumedOrdinal),
				fmt.Sprintf(`{"op":"frame.upsert","turnId":"t1","stepId":"consumed","frame":{"kind":"text","frameId":"steer","role":"user","text":"Resume the child.","promptIds":[%q]}}`, promptID),
				fmt.Sprintf(`{"op":"step.upsert","turnId":"t1","step":{"kind":"step","stepId":"tools","ordinal":%s,"state":"running"}}`, scenario.toolOrdinal),
				`{"op":"frame.upsert","turnId":"t1","stepId":"tools","frame":`+kimiPromptResumeFrame()+`}`)
			row, _ := childOf(t, native.sink, "session_1/agent-0")
			assert.Equal(t, bgtask.StatusSucceeded, row.Status)
			rig.agent.dispatchMu.Lock()
			submission := rig.agent.promptSubmissions[promptID]
			observed := submission != nil && submission.observed
			_, inStep := rig.agent.run("main").transcript.turn("t1").consumedPrompts["consumed"][promptID]
			rig.agent.finishDispatch()
			assert.True(t, observed, "the native consumed frame proves root execution independently from a later child operation")
			assert.True(t, inStep)
		})
	}
}

func TestKimiPromptSubmissionRetainsExecutionBeforeTheReceipt(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"lost receipt", "late refusal", "different receipt id", "invalid status", "definite refusal"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			rig, native := newKimiPromptSubmissionRig(t, false)
			var promptID string
			reply := fakeKapReply{DataFor: func(request fakeKapRequest) any {
				promptID = decodePrompt(t, request).PromptID
				if scenario != "definite refusal" {
					native.transcriptOps(t, 3, fmt.Sprintf(`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t1","ordinal":1,"state":"running","triggerPromptId":%q,"origin":{"kind":"user"}}}`, promptID))
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
			if scenario == "lost receipt" {
				reply.Drop = true
				reply.BeforeReply = func() {
					requests := rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/prompts"))
					promptID = decodePrompt(t, requests[len(requests)-1]).PromptID
					native.transcriptOps(t, 3, fmt.Sprintf(`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t1","ordinal":1,"state":"running","triggerPromptId":%q,"origin":{"kind":"user"}}}`, promptID))
				}
			}
			if scenario == "late refusal" || scenario == "definite refusal" {
				reply.Code, reply.Msg = 40001, "prompt refused"
			}
			rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), reply)
			err := rig.agent.SendInput("Resume the child.", nil)
			require.Error(t, err)
			if scenario != "late refusal" && scenario != "definite refusal" {
				assert.ErrorIs(t, err, agent.ErrDeliveryUncertain)
			}
			native.transcriptOps(t, 4,
				fmt.Sprintf(`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t1","ordinal":1,"state":"running","triggerPromptId":%q,"origin":{"kind":"user"}}}`, promptID),
				`{"op":"step.upsert","turnId":"t1","step":{"kind":"step","stepId":"t1.1","ordinal":1,"state":"running"}}`,
				`{"op":"frame.upsert","turnId":"t1","stepId":"t1.1","frame":`+kimiPromptResumeFrame()+`}`)
			row, _ := childOf(t, native.sink, "session_1/agent-0")
			assert.Equal(t, bgtask.StatusSucceeded, row.Status)
			rig.agent.dispatchMu.Lock()
			submission := rig.agent.promptSubmissions[promptID]
			observed := submission != nil && submission.observed
			rig.agent.finishDispatch()
			assert.Equal(t, scenario != "definite refusal", observed, "only actual native execution survives a failed receipt")
			assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), "/prompts")), 1)
		})
	}
}

func TestKimiPromptSubmissionCannotRelabelAnOlderTurn(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"finished header", "running header", "reset header", "empty session header"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			rig, native := newKimiPromptSubmissionRig(t, false)
			native.transcriptOps(t, 3, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"old","ordinal":1,"state":"running","triggerPromptId":"old-unowned","origin":{"kind":"user"}}}`, `{"op":"step.upsert","turnId":"old","step":{"kind":"step","stepId":"old.1","ordinal":1,"state":"running"}}`)
			if scenario == "finished header" {
				native.transcriptOps(t, 4, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"old","ordinal":1,"state":"completed","triggerPromptId":"old-unowned","origin":{"kind":"user"}}}`)
			}
			_, err := rig.agent.submitPrompt(rig.sessionID(), kimiMainAgentID, []map[string]any{{"type": "text", "text": "Resume the child."}})
			require.NoError(t, err)
			requests := rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/prompts"))
			promptID := decodePrompt(t, requests[0]).PromptID
			// Confirm P on its genuine new turn, then try to assign P to the older t0.
			native.transcriptOps(t, 5, fmt.Sprintf(`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t1","ordinal":2,"state":"running","triggerPromptId":%q,"origin":{"kind":"user"}}}`, promptID))
			state := "completed"
			if scenario == "running header" {
				state = "running"
			}
			turn := fmt.Sprintf(`{"kind":"turn","turnId":"old","ordinal":1,"state":%q,"triggerPromptId":%q,"origin":{"kind":"user"}}`, state, promptID)
			switch scenario {
			case "reset header":
				var value map[string]any
				require.NoError(t, json.Unmarshal([]byte(turn), &value))
				value["steps"] = []any{map[string]any{"kind": "step", "stepId": "old.1", "ordinal": 2, "state": "running", "frames": []json.RawMessage{json.RawMessage(kimiPromptResumeFrame())}}}
				rig.agent.HandleOutput(nativeKimiRecoveryReset(t, rig.sessionID(), "main", 6, value))
			case "empty session header":
				payload := json.RawMessage(`{"type":"transcript.ops","agent_id":"main","seq":6,"ops":[{"op":"turn.upsert","turn":` + turn + `}]}`)
				raw, err := json.Marshal(kimiFrame{Type: "transcript.ops", Payload: payload})
				require.NoError(t, err)
				rig.agent.HandleOutput(raw)
			default:
				native.transcriptOps(t, 6, `{"op":"turn.upsert","turn":`+turn+`}`)
			}
			native.transcriptOps(t, 7,
				`{"op":"step.upsert","turnId":"old","step":{"kind":"step","stepId":"old.1","ordinal":2,"state":"running"}}`,
				`{"op":"frame.upsert","turnId":"old","stepId":"old.1","frame":`+kimiPromptResumeFrame()+`}`)
			row, _ := childOf(t, native.sink, "session_1/agent-0")
			assert.Equal(t, bgtask.StatusSucceeded, row.Status)
			rig.agent.dispatchMu.Lock()
			oldTrigger := rig.agent.run("main").transcript.turn("old").triggerPromptID
			currentTrigger := rig.agent.run("main").transcript.turn("t1").triggerPromptID
			rig.agent.finishDispatch()
			assert.Equal(t, "old-unowned", oldTrigger, "an established native trigger is immutable")
			assert.Equal(t, promptID, currentTrigger)
		})
	}
}

func TestKimiPromptSubmissionBackfillsAnUnknownTrigger(t *testing.T) {
	t.Parallel()
	for _, reset := range []bool{false, true} {
		t.Run(fmt.Sprintf("reset=%t", reset), func(t *testing.T) {
			t.Parallel()
			rig, native := newKimiPromptSubmissionRig(t, false)
			require.NoError(t, rig.agent.SendInput("Resume the child.", nil))
			requests := rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/prompts"))
			promptID := decodePrompt(t, requests[0]).PromptID
			native.transcriptOps(t, 3, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t1","ordinal":1,"state":"running","origin":{"kind":"user"}}}`)
			turn := fmt.Sprintf(`{"kind":"turn","turnId":"t1","ordinal":1,"state":"running","triggerPromptId":%q,"origin":{"kind":"user"}}`, promptID)
			if reset {
				var value map[string]any
				require.NoError(t, json.Unmarshal([]byte(turn), &value))
				value["steps"] = []any{map[string]any{"kind": "step", "stepId": "t1.1", "ordinal": 1, "state": "running", "frames": []json.RawMessage{json.RawMessage(kimiPromptResumeFrame())}}}
				rig.agent.HandleOutput(nativeKimiRecoveryReset(t, rig.sessionID(), "main", 4, value))
			} else {
				native.transcriptOps(t, 4,
					`{"op":"turn.upsert","turn":`+turn+`}`,
					`{"op":"step.upsert","turnId":"t1","step":{"kind":"step","stepId":"t1.1","ordinal":1,"state":"running"}}`,
					`{"op":"frame.upsert","turnId":"t1","stepId":"t1.1","frame":`+kimiPromptResumeFrame()+`}`)
			}
			row, _ := childOf(t, native.sink, "session_1/agent-0")
			assert.Equal(t, bgtask.StatusSucceeded, row.Status)
			rig.agent.dispatchMu.Lock()
			trigger := rig.agent.run("main").transcript.turn("t1").triggerPromptID
			submission := rig.agent.promptSubmissions[promptID]
			observed := submission != nil && submission.observed
			rig.agent.finishDispatch()
			assert.Equal(t, promptID, trigger)
			assert.True(t, observed)
		})
	}
}

func TestKimiUnjoinedChildActivityStaysIndependentFromLaterParentInvocation(t *testing.T) {
	t.Parallel()
	for _, submitBefore := range []bool{false, true} {
		t.Run(fmt.Sprintf("submission_before_unknown_turn=%t", submitBefore), func(t *testing.T) {
			t.Parallel()
			rig, native := newKimiPromptSubmissionRig(t, false)
			submit := func() string {
				t.Helper()
				reply, err := rig.agent.submitPrompt(rig.sessionID(), "main", []map[string]any{{"type": "text", "text": "Resume again."}})
				require.NoError(t, err)
				return reply.PromptID
			}
			promptID := ""
			if submitBefore {
				promptID = submit()
			}
			native.transcriptAgentOps(t, "agent-0", 3,
				`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"unknown","state":"running","origin":{"kind":"other"}}}`,
				`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"unknown","state":"completed","origin":{"kind":"other"}}}`)
			row, _ := childOf(t, native.sink, "session_1/agent-0")
			assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, row.Status, "a finished unjoined turn cannot stay running or inherit an earlier outcome")
			if !submitBefore {
				promptID = submit()
			}
			native.transcriptOps(t, 3,
				fmt.Sprintf(`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t1","ordinal":1,"state":"running","triggerPromptId":%q,"origin":{"kind":"user"}}}`, promptID),
				`{"op":"step.upsert","turnId":"t1","step":{"kind":"step","stepId":"t1.1","ordinal":1,"state":"running"}}`,
				`{"op":"frame.upsert","turnId":"t1","stepId":"t1.1","frame":`+kimiPromptResumeFrame()+`}`)
			row, _ = childOf(t, native.sink, "session_1/agent-0")
			assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, row.Status, "a root invocation does not revive unjoined child activity")
			assert.Equal(t, bgtask.StatusSucceeded, nativeTaskOf(t, native.sink, "old-task").Status)
			current := invocationOutcomeOf(t, native.sink, kimiTranscriptToolIdentity{sessionID: "session_1", agentID: "main", turnID: "t1", stepID: "t1.1", frameID: "resume-frame", toolCallID: "resume"}, "agent-0")
			assert.Equal(t, bgtask.StatusRunning, current.Status)
		})
	}
}

package kimi

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestKimiInteractionFramesPublishAndWithdrawNativeControls(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kind    string
		request map[string]any
	}{
		{kind: "approval", request: map[string]any{
			"tool_name": "Bash", "action": "Run pwd", "tool_input_display": map[string]any{"kind": "command", "command": "pwd"},
		}},
		{kind: "question", request: map[string]any{
			"questions": []map[string]any{{"id": "q_0", "question": "Which color?", "options": []map[string]any{{"id": "red", "label": "Red"}}}},
		}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			frame := map[string]any{
				"type": "interaction", "session_id": rig.sessionID(), "agent_id": "main",
				"interaction_id": "interaction_1", "kind": tc.kind, "status": "pending",
				"tool_call_id": "call_1", "event_created_at": "2026-10-30T00:00:00Z", "request": tc.request,
			}
			raw, err := json.Marshal(frame)
			require.NoError(t, err)
			rig.agent.HandleOutput(raw)
			controls := rig.sink.PublishedControls()
			require.Len(t, controls, 1, "the current native interaction frame must publish its control")
			assert.Equal(t, "interaction_1", controls[0].RequestID)
			assert.Equal(t, rig.sessionID(), controls[0].AgentSessionID)
			assert.JSONEq(t, string(raw), string(controls[0].Payload), "the stored control must preserve the native frame")
			agenttest.AssertPublishedControlKeys(t, kimiProvider{}, rig.sink)
			rig.agent.HandleOutput(raw)
			assert.Equal(t, 1, rig.sink.PublishedControlCount(), "replayed pending interactions publish once")
			frame["status"], frame["outcome"] = "ended", "cancelled"
			frame["session_id"] = "previous_session"
			raw, err = json.Marshal(frame)
			require.NoError(t, err)
			rig.agent.HandleOutput(raw)
			assert.Empty(t, rig.sink.CanceledControls(), "another session cannot withdraw this request")
			frame["session_id"] = rig.sessionID()
			raw, err = json.Marshal(frame)
			require.NoError(t, err)
			rig.agent.HandleOutput(raw)
			assert.Equal(t, []string{"interaction_1"}, rig.sink.CanceledControls())
		})
	}
}

func TestKimiReplayedEventsStillReconcileUnsequencedInteractions(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	rig.feed(t, approvalEvent("approval_ended_while_disconnected", map[string]any{"kind": "command", "command": "pwd"}))
	require.Equal(t, 1, rig.sink.PublishedControlCount())
	replayed := approvalEvent("approval_raised_while_disconnected", map[string]any{"kind": "command", "command": "pwd"})
	replayed["session_id"] = rig.sessionID()
	finished := beginNativeKimiReconnect(t, rig, replayed)
	waitFor(t, finished, "the native replay delivers the new pending interaction")
	waitFor(t, func() bool { return len(rig.sink.CanceledControls()) == 1 }, "the confirmed control replay retires the resolved interaction")
	assert.Equal(t, []string{"approval_ended_while_disconnected"}, rig.sink.CanceledControls())
	assert.Equal(t, 2, rig.sink.PublishedControlCount())
	assert.Equal(t, "approval_raised_while_disconnected", rig.sink.LastPublishedControl().RequestID)
	assert.Empty(t, rig.fake.requestsTo("GET "+kimiSessionPath(rig.sessionID(), "/snapshot")))
}

func TestKimiInteractionFramesRejectInvalidControls(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		label string
		field string
		value any
	}{
		{label: "empty ID", field: "interaction_id", value: ""},
		{label: "empty session", field: "session_id", value: ""},
		{label: "another session", field: "session_id", value: "previous_session"},
		{label: "unknown kind", field: "kind", value: "unsupported"},
		{label: "unknown status", field: "status", value: "unsupported"},
		{label: "numeric status", field: "status", value: 0},
		{label: "absent request", field: "request", value: nil},
		{label: "scalar request", field: "request", value: "invalid"},
		{label: "array request", field: "request", value: []any{}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			frame := approvalEvent("approval_invalid", map[string]any{"kind": "command", "command": "pwd"})
			frame["session_id"] = rig.sessionID()
			frame[tc.field] = tc.value
			raw, err := json.Marshal(frame)
			require.NoError(t, err)
			rig.agent.HandleOutput(raw)
			assert.Zero(t, rig.sink.PublishedControlCount())
			assert.Empty(t, rig.sink.CanceledControls())
		})
	}
}

func TestKimiControlAnswersUseUnifiedInteractionRoute(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		label    string
		request  map[string]any
		behavior string
		fields   map[string]any
		want     string
	}{
		{label: "approval", request: approvalEvent("interaction_1", map[string]any{"kind": "command", "command": "pwd"}), behavior: "allow", want: `{"kind":"approval","decision":"approved"}`},
		{label: "question", request: questionEvent("interaction_1"), behavior: "allow", fields: map[string]any{"answers": map[string]any{"q_0": map[string]any{"kind": "single", "option_id": "opt_0_0"}}}, want: `{"kind":"question","answers":{"q_0":{"kind":"single","option_id":"opt_0_0"}},"method":"click"}`},
		{label: "dismissal", request: questionEvent("interaction_1"), behavior: "deny", want: `{"kind":"question","dismissed":true}`},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			rig.feed(t, tc.request)
			require.NoError(t, rig.agent.SendRawInput(agent.RawInput{Data: answerFor(t, rig, "interaction_1", tc.behavior, tc.fields, nil)}))
			posts := rig.fake.requestsTo("POST " + kimiItemPath(rig.sessionID(), "interactions", "interaction_1", ""))
			require.Len(t, posts, 1, "the current server accepts control answers through its interaction route")
			assert.JSONEq(t, tc.want, string(posts[0].Body))
		})
	}
}

func TestKimiMissingInteractionIsGone(t *testing.T) {
	t.Parallel()
	assert.ErrorIs(t, classifyKimiControlError(&kimiAPIError{Code: 40404}), errKimiControlGone)
}

func TestKimiMissingSessionRetiresItsInteractions(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	rig.feed(t, approvalEvent("approval_missing_session", map[string]any{"kind": "command", "command": "pwd"}))
	route := kimiItemPath(rig.sessionID(), "interactions", "approval_missing_session", "")
	rig.fake.reply("POST "+route, fakeKapReply{Code: 40401, Msg: "the session does not exist"})
	err := rig.agent.SendRawInput(agent.RawInput{Data: answerFor(t, rig, "approval_missing_session", "allow", nil, nil)})
	assert.ErrorIs(t, err, errKimiControlGone)
	assert.Equal(t, []string{"approval_missing_session"}, rig.sink.CanceledControls(),
		"a control whose native session disappeared must leave the user's banner")
}

func TestKimiNativeResolutionRetiresControlsBeforeChildAdoption(t *testing.T) {
	t.Parallel()
	for _, agentID := range []string{"main", "agent-0"} {
		t.Run(agentID, func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			publishNativeKimiApproval(t, rig, agentID, "approval_1")
			require.Len(t, rig.sink.PublishedControls(), 1)
			raw := fmt.Sprintf(`{"type":"interaction","session_id":"session_1","agent_id":%q,"interaction_id":"approval_1","tool_call_id":"call","kind":"approval","status":"ended","outcome":"cancelled"}`, agentID)
			rig.agent.HandleOutput([]byte(raw))
			assert.Equal(t, []string{"approval_1"}, rig.sink.CanceledControls())
			assert.Empty(t, rig.sink.ChildAgentIDs(), "control resolution needs no adopted transcript")
		})
	}
}

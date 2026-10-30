package kimi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
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
	rig.fake.update(rig.sessionID(), func(session *fakeKapSession) {
		session.PendingApprovals = []map[string]any{{
			"approval_id": "approval_raised_while_disconnected", "tool_name": "Bash",
			"tool_input_display": map[string]any{"kind": "command", "command": "pwd"},
		}}
	})
	rig.agent.resyncSession(t.Context(), rig.sessionID(), true)
	assert.Equal(t, []string{"approval_ended_while_disconnected"}, rig.sink.CanceledControls(),
		"durable replay cannot recover the resolution of an unsequenced interaction")
	assert.Equal(t, 2, rig.sink.PublishedControlCount())
	assert.Equal(t, "approval_raised_while_disconnected", rig.sink.LastPublishedControl().RequestID)
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
			require.NoError(t, rig.agent.SendRawInput(answerFor(t, rig, "interaction_1", tc.behavior, tc.fields, nil), agent.StopContext{}))
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
	err := rig.agent.SendRawInput(answerFor(t, rig, "approval_missing_session", "allow", nil, nil), agent.StopContext{})
	assert.ErrorIs(t, err, errKimiControlGone)
	assert.Equal(t, []string{"approval_missing_session"}, rig.sink.CanceledControls(),
		"a control whose native session disappeared must leave the user's banner")
}

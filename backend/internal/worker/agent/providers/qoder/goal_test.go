package qoder

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// newGoalAgent builds an agent whose process stdin the test reads. The frame
// handlers need no process; the control round trip writes to stdin and waits.
func newGoalAgent(t *testing.T) (*Agent, *agenttest.Sink, *agenttest.Stdin) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stdin := &agenttest.Stdin{}
	sink := &agenttest.Sink{}
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "test-agent", ProviderName: "qoder", Ctx: ctx, Cancel: cancel,
			Stdin: agenttest.NopStdin(stdin),
		}),
		sink:           agent.NewModelProgressResetSink(agent.NewProviderServices(sink)),
		sessionID:      "session-1",
		permissionMode: "default",
		pendingControl: make(map[string]chan<- qoderControlResult),
	}
	return a, sink, stdin
}

// goalFrame is one `system` line of the goal report.
func goalFrame(t *testing.T, subtype string, fields map[string]any) []byte {
	t.Helper()
	frame := map[string]any{
		"type":       "system",
		"subtype":    subtype,
		"uuid":       "u-1",
		"session_id": "session-1",
	}
	for key, value := range fields {
		frame[key] = value
	}
	raw, err := json.Marshal(frame)
	require.NoError(t, err)
	return raw
}

func TestQoderGoalStatusMapping(t *testing.T) {
	t.Parallel()
	for wire, want := range map[string]agent.GoalStatus{
		"active":         agent.GoalStatusActive,
		"paused":         agent.GoalStatusPaused,
		"complete":       agent.GoalStatusDone,
		"blocked":        agent.GoalStatusBlocked,
		"usage_limited":  agent.GoalStatusBlocked,
		"budget_limited": agent.GoalStatusBlocked,
		"something-new":  agent.GoalStatusBlocked,
	} {
		assert.Equal(t, want, qoderGoalStatus(wire), wire)
	}
}

func TestQoderGoalStatusDetail(t *testing.T) {
	t.Parallel()
	assert.Empty(t, qoderGoalStatusDetail("active", "set"), "an active goal keeps no reason")
	assert.Empty(t, qoderGoalStatusDetail("complete", "completed"), "a finished goal keeps no reason")
	assert.Equal(t, "usage limited", qoderGoalStatusDetail("usage_limited", ""))
	assert.Equal(t, "usage limited", qoderGoalStatusDetail("usage_limited", "usage_limited"))
	assert.Equal(t, "usage limited: out of credits", qoderGoalStatusDetail("usage_limited", " out of credits "))
	assert.Equal(t, "budget limited", qoderGoalStatusDetail("budget_limited", ""))
	assert.Equal(t, "safety-limit", qoderGoalStatusDetail("paused", " safety-limit "))
	assert.Equal(t, "blocked", qoderGoalStatusDetail("blocked", "blocked"))
	assert.Empty(t, qoderGoalStatusDetail("paused", "   "), "a blank reason adds nothing")
}

func TestQoderGoalReportReachesTheCard(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)
	created := time.Date(2026, 9, 27, 10, 30, 0, 0, time.UTC)

	a.HandleOutput(goalFrame(t, "goal_updated", map[string]any{
		"goal": map[string]any{
			"id": "goal-9", "objective": "Reply with DONE", "status": "paused",
			"turns_used": 3, "time_used_seconds": 45,
			"max_turns": 10, "credits_budget": 250, "credits_used": 40,
			"created_at": created.UnixMilli(), "updated_at": created.UnixMilli(),
		},
		"reason": "safety-limit",
	}))

	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "goal-9", goal.NativeID)
	assert.Equal(t, "Reply with DONE", goal.Objective)
	assert.Equal(t, agent.GoalStatusPaused, goal.Status)
	assert.Equal(t, "safety-limit", goal.StatusDetail)
	assert.True(t, goal.CreatedAt.Equal(created), "created_at is Unix MILLISECONDS")
	require.NotNil(t, goal.Iterations)
	assert.Equal(t, int32(3), *goal.Iterations)
	require.NotNil(t, goal.TimeUsedSeconds)
	assert.Equal(t, int64(45), *goal.TimeUsedSeconds)
	// A turn limit and a credit balance have no neutral counter. The panel
	// omits a row it has no number for rather than printing credits under a
	// token label.
	assert.Nil(t, goal.TokenBudget)
	assert.Nil(t, goal.TokensUsed)
}

func TestQoderGoalReportWithoutAGoalClearsTheCard(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)

	a.HandleOutput(goalFrame(t, "goal_updated", map[string]any{"goal": nil}))
	a.HandleOutput(goalFrame(t, "goal_updated", map[string]any{"goal": map[string]any{"id": "", "objective": "x", "status": "active"}}))

	assert.Equal(t, 2, sink.GoalClears())
}

func TestQoderGoalClearedRemovesTheGoal(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)

	a.HandleOutput(goalFrame(t, "goal_cleared", map[string]any{"goal_id": "goal-9", "reason": "user"}))

	assert.Equal(t, 1, sink.GoalClears())
	assert.Equal(t, []bool{false}, sink.GoalClearSnapshots(), "the user just cleared it, so it is a real transition")
}

func TestQoderUnreadableGoalReportChangesNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)

	a.HandleOutput([]byte(`{"type":"system","subtype":"goal_updated","goal":"text"}`))

	_, ok := sink.LastGoal()
	assert.False(t, ok)
	assert.Zero(t, sink.GoalClears(), "a frame that cannot be read states nothing")
}

func TestQoderGoalReportLeavesAbsentCountersAbsent(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)

	a.HandleOutput(goalFrame(t, "goal_updated", map[string]any{
		"goal": map[string]any{"id": "goal-1", "objective": "Ship", "status": "active", "created_at": -5},
	}))

	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, agent.GoalStatusActive, goal.Status)
	assert.Empty(t, goal.StatusDetail)
	assert.True(t, goal.CreatedAt.IsZero(), "a creation time before the epoch is no time that Qoder states")
	assert.Nil(t, goal.Iterations, "a count that Qoder did not send is absent, not zero")
	assert.Nil(t, goal.TimeUsedSeconds)
}

func TestQoderGoalControlRequestBodies(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		action    agent.GoalAction
		objective string
		want      string
	}{
		{action: agent.GoalActionSet, objective: "Ship the\nrelease", want: `{"objective":"Ship the release","subtype":"set_goal"}`},
		{action: agent.GoalActionClear, want: `{"subtype":"clear_goal"}`},
		{action: agent.GoalActionPause, want: `{"status":"paused","subtype":"set_goal"}`},
		{action: agent.GoalActionResume, want: `{"subtype":"resume_goal"}`},
	} {
		body, err := qoderGoalRequestBody(tc.action, tc.objective)
		require.NoError(t, err)
		assert.JSONEq(t, tc.want, body)
	}
}

func TestQoderGoalSetRefusesABlankObjective(t *testing.T) {
	t.Parallel()
	for _, objective := range []string{"", " \n\t "} {
		body, err := qoderGoalRequestBody(agent.GoalActionSet, objective)
		assert.Error(t, err, "%q", objective)
		assert.Empty(t, body, "a refused goal sends nothing")
	}
}

func TestQoderGoalRequestBodyRefusesAnUnknownAction(t *testing.T) {
	t.Parallel()
	_, err := qoderGoalRequestBody(agent.GoalAction(42), "x")
	assert.ErrorIs(t, err, agent.ErrGoalControlUnsupported)
}

func TestQoderSupportedGoalActionsFollowTheNegotiatedCapabilities(t *testing.T) {
	t.Parallel()
	a, _, _ := newGoalAgent(t)

	assert.Empty(t, a.SupportedGoalActions(), "nothing is negotiated yet")

	a.mu.Lock()
	a.capabilities = []string{"plan_mode_v1"}
	a.mu.Unlock()
	assert.Empty(t, a.SupportedGoalActions(), "a runtime without goal_v1 has no goal to act on")

	a.mu.Lock()
	a.capabilities = []string{"goal_v1"}
	a.mu.Unlock()
	assert.Equal(t, []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear, agent.GoalActionPause},
		a.SupportedGoalActions(), "resuming needs its own capability")

	a.mu.Lock()
	a.capabilities = []string{"goal_v1", "goal_resume_v1"}
	a.mu.Unlock()
	assert.Equal(t, []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear, agent.GoalActionPause, agent.GoalActionResume},
		a.SupportedGoalActions())
}

// One action makes the round trip: the request goes out on stdin, and the
// matching control_response completes it.
func TestQoderGoalActionSendsOneControlRequest(t *testing.T) {
	t.Parallel()
	a, _, stdin := newGoalAgent(t)

	type result struct {
		outcome agent.GoalOutcome
		err     error
	}
	done := make(chan result, 1)
	go func() {
		outcome, err := a.PerformGoalAction(agent.GoalActionSet, "Ship it")
		done <- result{outcome, err}
	}()

	var sent string
	require.Eventually(t, func() bool {
		sent = stdin.String()
		return strings.Contains(sent, "set_goal")
	}, 30*time.Second, 5*time.Millisecond, "the goal request reaches the process")

	var envelope struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype   string `json:"subtype"`
			Objective string `json:"objective"`
		} `json:"request"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(sent)), &envelope))
	assert.Equal(t, "control_request", envelope.Type)
	assert.Equal(t, "set_goal", envelope.Request.Subtype)
	assert.Equal(t, "Ship it", envelope.Request.Objective)

	a.HandleOutput([]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"` +
		envelope.RequestID + `","response":{"id":"goal-1"}}}`))

	select {
	case got := <-done:
		require.NoError(t, got.err)
		assert.Empty(t, got.outcome.QueuedInput, "a control request starts no turn")
	case <-time.After(30 * time.Second):
		t.Fatal("the goal action never completed")
	}
}

// A refused control request reports the CLI's own error, and the action leaves
// no queued input behind.
func TestQoderGoalActionReportsAControlError(t *testing.T) {
	t.Parallel()
	a, _, stdin := newGoalAgent(t)

	done := make(chan error, 1)
	go func() {
		_, err := a.PerformGoalAction(agent.GoalActionClear, "")
		done <- err
	}()

	var sent string
	require.Eventually(t, func() bool {
		sent = stdin.String()
		return strings.Contains(sent, "clear_goal")
	}, 30*time.Second, 5*time.Millisecond, "the goal request reaches the process")

	var envelope struct {
		RequestID string `json:"request_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(sent)), &envelope))

	a.HandleOutput([]byte(`{"type":"control_response","response":{"subtype":"error","request_id":"` +
		envelope.RequestID + `","error":"set_goal failed"}}`))

	select {
	case err := <-done:
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "set_goal failed")
	case <-time.After(30 * time.Second):
		t.Fatal("the goal action never completed")
	}
}

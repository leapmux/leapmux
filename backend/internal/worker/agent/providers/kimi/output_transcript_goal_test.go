package kimi

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (r *kimiOutputRig) publishGoalMarker(t *testing.T, payload map[string]any) {
	t.Helper()
	publishKimiGoalMarker(t, r.agent, payload)
}

func (r *kimiTestRig) publishGoalMarker(t *testing.T, payload map[string]any) {
	t.Helper()
	publishKimiGoalMarker(t, r.agent, payload)
}

// publishKimiGoalMarker builds a native marker from its native snapshot and change fields.
func publishKimiGoalMarker(t *testing.T, a *Agent, payload map[string]any) {
	t.Helper()
	agentID := kimiMainAgentID
	if value, present := payload["agentId"]; present {
		var valid bool
		agentID, valid = value.(string)
		require.True(t, valid)
		require.NotEmpty(t, agentID)
	}
	publishKimiMarker(t, a, agentID, "goal", payload)
}

func (r *kimiOutputRig) publishMarker(t *testing.T, agentID, markerType string, payload map[string]any) {
	t.Helper()
	publishKimiMarker(t, r.agent, agentID, markerType, payload)
}

func (r *kimiTestRig) publishMarker(t *testing.T, agentID, markerType string, payload map[string]any) {
	t.Helper()
	publishKimiMarker(t, r.agent, agentID, markerType, payload)
}

func publishKimiMarker(t *testing.T, a *Agent, agentID, markerType string, payload map[string]any) {
	t.Helper()
	fields := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		fields[key] = value
	}
	fields["agentId"] = agentID
	a.dispatchMu.Lock()
	seq := a.run(agentID).transcript.seq + 1
	a.Mu.Lock()
	sessionID := a.sessionID
	a.Mu.Unlock()
	a.dispatchMu.Unlock()
	raw, err := json.Marshal(map[string]any{
		"type": "transcript.ops", "session_id": sessionID,
		"payload": map[string]any{"type": "transcript.ops", "agent_id": agentID, "seq": seq,
			"ops": []any{map[string]any{"op": "marker.upsert", "item": map[string]any{
				"kind": "marker", "markerId": fmt.Sprintf("test-%s-%d", markerType, seq), "marker": markerType, "payload": fields, "at": a.clock.Now().Format(time.RFC3339Nano),
			}}}},
	})
	require.NoError(t, err)
	a.HandleOutput(raw)
}

func TestKimiNativeGoalMarkerRetainsMetadata(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	item, err := json.Marshal(map[string]any{
		"kind": "marker", "markerId": "live-m1", "marker": "goal",
		"payload": map[string]any{"snapshot": goalSnapshot("active")},
	})
	require.NoError(t, err)
	rig.transcriptOps(t, 1, `{"op":"marker.upsert","item":`+string(item)+`}`)
	goal, present := rig.sink.LastGoal()
	require.True(t, present)
	assert.Equal(t, "goal_1", goal.NativeID)
	assert.Equal(t, agent.GoalStatusActive, goal.Status)
	require.NotNil(t, goal.TokenBudget)
	assert.EqualValues(t, 50000, *goal.TokenBudget)
	assert.False(t, goal.Snapshot)
	createdAt := goal.CreatedAt
	rig.agent.HandleOutput([]byte(`{"type":"goal","session_id":"session_1","agent_id":"main","goal":{"objective":"Ship the release","status":"paused","turns_used":4,"tokens_used":12001,"wall_clock_ms":96000}}`))
	goal, present = rig.sink.LastGoal()
	require.True(t, present)
	assert.Equal(t, "goal_1", goal.NativeID)
	assert.Equal(t, createdAt, goal.CreatedAt)
	require.NotNil(t, goal.TokenBudget)
	assert.EqualValues(t, 50000, *goal.TokenBudget)
	assert.Equal(t, agent.GoalStatusPaused, goal.Status)
	assert.EqualValues(t, 12001, *goal.TokensUsed)
	rig.transcriptOps(t, 2, `{"op":"marker.upsert","item":{"kind":"marker","markerId":"live-m2","marker":"goal","payload":{"snapshot":{"goalId":"goal_1","objective":"Ship the release","status":"paused"}}}}`)
	goal, _ = rig.sink.LastGoal()
	assert.Nil(t, goal.TokenBudget, "a full native snapshot can remove the budget")
	rig.transcriptOps(t, 3, `{"op":"marker.upsert","item":{"kind":"marker","markerId":"live-m3","marker":"goal","payload":{"snapshot":null}}}`)
	assert.Empty(t, rig.agent.goal.snapshot.Objective)
}

func TestKimiNativeChildGoalMarkerDoesNotChangeSessionGoal(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"frame.upsert","turnId":"parent","stepId":"parent.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	rig.transcriptAgentOps(t, "agent-0", 1, `{"op":"marker.upsert","item":{"kind":"marker","markerId":"live-m1","marker":"goal","payload":{"snapshot":{"objective":"Child objective","status":"active"}}}}`)
	_, present := rig.sink.LastGoal()
	assert.False(t, present)
}

func TestKimiNativeGoalResetPublishesOnlyTheLatestSnapshot(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptReset(t, 1,
		`{"kind":"marker","markerId":"live-m1","marker":"goal","payload":{"snapshot":{"objective":"Old objective","status":"active"}}}`,
		`{"kind":"marker","markerId":"live-m2","marker":"goal","payload":{"snapshot":{"objective":"Current objective","status":"paused"},"change":{"reason":"Waiting for input"}}}`)
	goals := rig.sink.Goals()
	require.Len(t, goals, 1)
	assert.Equal(t, "Current objective", goals[0].Objective)
	assert.Equal(t, agent.GoalStatusPaused, goals[0].Status)
	assert.Equal(t, "Waiting for input", goals[0].StatusDetail)
	assert.True(t, goals[0].Snapshot)
	rig.transcriptOps(t, 2, `{"op":"marker.upsert","item":{"kind":"marker","markerId":"live-m3","marker":"goal","payload":{}}}`)
	assert.Equal(t, "Current objective", rig.agent.goal.snapshot.Objective)
	rig.transcriptReset(t, 3,
		`{"kind":"marker","markerId":"live-m4","marker":"goal","payload":{"snapshot":null}}`)
	assert.Empty(t, rig.agent.goal.snapshot.Objective)
	assert.Equal(t, []bool{true}, rig.sink.GoalClearSnapshots())
}

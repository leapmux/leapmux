package pi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// piGoalRecord is the state that pi-goal-x supplies in tool results and goal files.
type piGoalRecord struct {
	ID          string `json:"id"`
	Objective   string `json:"objective"`
	Status      string `json:"status"`
	CreatedAt   string `json:"createdAt"`
	TokenBudget *int64 `json:"tokenBudget"`
	Usage       struct {
		TokensUsed    *int64 `json:"tokensUsed"`
		ActiveSeconds *int64 `json:"activeSeconds"`
	} `json:"usage"`
}

func piGoalStatus(status string) agent.GoalStatus {
	switch status {
	case "active":
		return agent.GoalStatusActive
	case "paused":
		return agent.GoalStatusPaused
	case "complete":
		return agent.GoalStatusDone
	default:
		// Blocked, budget_limited, and unknown states require user input.
		return agent.GoalStatusBlocked
	}
}

func (a *Agent) reportPiGoalResult(toolName string, result json.RawMessage) {
	switch toolName {
	case "create_goal", "get_goal", "update_goal", "set_goal_tasks", "update_goal_task":
	default:
		return
	}
	var envelope struct {
		Details struct {
			Version int             `json:"version"`
			Goal    json.RawMessage `json:"goal"`
		} `json:"details"`
	}
	if json.Unmarshal(result, &envelope) != nil || envelope.Details.Version != 3 || len(envelope.Details.Goal) == 0 {
		return
	}
	if bytes.Equal(bytes.TrimSpace(envelope.Details.Goal), []byte("null")) {
		a.publishPiGoal(nil, false, nil)
		a.schedulePiGoalRefresh(false)
		return
	}
	var record piGoalRecord
	if err := json.Unmarshal(envelope.Details.Goal, &record); err != nil {
		slog.Warn("read Pi goal result", "agent_id", a.AgentID(), "error", err)
		return
	}
	if record.ID == "" || strings.TrimSpace(record.Objective) == "" {
		return
	}
	a.publishPiGoal(&record, false, nil)
	a.schedulePiGoalRefresh(false)
}

// publishPiGoal serializes state publication with session changes and shutdown.
// A native read cannot replace a newer event that arrived during its I/O.
//
// It reports whether the update reached the sink. The refresh loop reads that
// answer to decide whether its pending snapshot intent is spent.
func (a *Agent) publishPiGoal(record *piGoalRecord, snapshot bool, expectedRevision *uint64) bool {
	a.goal.publishMu.Lock()
	defer a.goal.publishMu.Unlock()
	a.Mu.Lock()
	if a.goal.stopping || a.StoppedLocked() || a.IsDiscardingOutput() || (expectedRevision != nil && *expectedRevision != a.goal.revision) {
		a.Mu.Unlock()
		return false
	}
	if expectedRevision == nil {
		a.goal.revision++
	}
	a.Mu.Unlock()
	if record == nil {
		a.sink.ClearGoal(snapshot)
		return true
	}
	createdAt, err := time.Parse(time.RFC3339Nano, record.CreatedAt)
	if err != nil {
		createdAt = time.Time{}
	}
	a.sink.UpsertGoal(agent.GoalUpdate{
		NativeID: record.ID, Objective: record.Objective, Status: piGoalStatus(record.Status),
		StatusDetail: record.Status, CreatedAt: createdAt,
		TokensUsed: record.Usage.TokensUsed, TimeUsedSeconds: record.Usage.ActiveSeconds, TokenBudget: record.TokenBudget,
		Snapshot: snapshot,
	})
	return true
}

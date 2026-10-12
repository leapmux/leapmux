package deepseekharness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

func (a *Agent) SupportedGoalActions() []agent.GoalAction {
	return []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear, agent.GoalActionPause, agent.GoalActionResume}
}

func (a *Agent) PerformGoalAction(action agent.GoalAction, objective string) (agent.GoalOutcome, error) {
	a.opMu.Lock()
	defer a.opMu.Unlock()
	sessionID := a.session()
	if sessionID == "" {
		return agent.GoalOutcome{}, agent.ErrInputSessionChanged
	}
	if action == agent.GoalActionSet && strings.TrimSpace(objective) == "" {
		return agent.GoalOutcome{}, fmt.Errorf("DeepSeek Harness goal objective is empty")
	}
	current, err := a.readGoal(sessionID)
	if err != nil {
		return agent.GoalOutcome{}, err
	}
	args := map[string]any{"agentId": sessionID}
	method := ""
	switch action {
	case agent.GoalActionSet:
		method = "goals/create"
		args["request"] = map[string]string{"objective": objective}
		if current != nil && current.Phase != "complete" {
			method = "goals/edit"
			args["ref"] = current.ref()
		}
	case agent.GoalActionClear:
		if current == nil {
			return agent.GoalOutcome{}, nil
		}
		method = "goals/clear"
		args["ref"] = current.ref()
	case agent.GoalActionPause:
		if current == nil {
			return agent.GoalOutcome{}, fmt.Errorf("DeepSeek Harness has no current goal to pause")
		}
		method = "goals/pause"
		args["ref"] = current.ref()
	case agent.GoalActionResume:
		if current == nil {
			return agent.GoalOutcome{}, fmt.Errorf("DeepSeek Harness has no current goal to resume")
		}
		method = "goals/resume"
		args["ref"] = current.ref()
	default:
		return agent.GoalOutcome{}, fmt.Errorf("DeepSeek Harness goal action is invalid")
	}
	return agent.GoalOutcome{}, a.rpc.call(a.Context(), method, args, nil)
}

type nativeGoal struct {
	ID        string `json:"id"`
	Revision  int64  `json:"revision"`
	Objective string `json:"objective"`
	Phase     string `json:"phase"`
	Rounds    int64  `json:"roundsStarted"`
	Blocked   *struct {
		Message string `json:"message"`
	} `json:"blockedReason"`
}

func (g nativeGoal) ref() map[string]any { return map[string]any{"id": g.ID, "revision": g.Revision} }

func (a *Agent) readGoal(sessionID string) (*nativeGoal, error) {
	raw, err := a.rpc.value(a.Context(), "goals/get", map[string]any{"agentId": sessionID})
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var goal nativeGoal
	if json.Unmarshal(raw, &goal) != nil || goal.ID == "" || goal.Revision <= 0 || goal.Objective == "" {
		return nil, fmt.Errorf("DeepSeek Harness returned an invalid current goal")
	}
	return &goal, nil
}

func (a *Agent) applyGoalProjection(raw []byte, snapshot bool) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		a.sink.ClearGoal(snapshot)
		return nil
	}
	var projection struct {
		Goal   *nativeGoal `json:"goal"`
		Rounds int64       `json:"roundsStarted"`
	}
	if err := json.Unmarshal(raw, &projection); err != nil {
		return err
	}
	if projection.Goal == nil || projection.Goal.ID == "" || projection.Goal.Revision <= 0 {
		return fmt.Errorf("DeepSeek Harness goal projection has no valid native identity")
	}
	projection.Goal.Rounds = projection.Rounds
	return a.publishGoal(*projection.Goal, snapshot)
}

func (a *Agent) applyGoalEvent(raw []byte, snapshot bool) error {
	var change struct {
		Operation string      `json:"operation"`
		Goal      *nativeGoal `json:"goal"`
		Rounds    int64       `json:"roundsStarted"`
	}
	if err := json.Unmarshal(raw, &change); err != nil {
		return err
	}
	if change.Operation == "clear" {
		a.sink.ClearGoal(snapshot)
		return nil
	}
	if change.Goal == nil {
		return fmt.Errorf("DeepSeek Harness goal update has no goal")
	}
	change.Goal.Rounds = change.Rounds
	return a.publishGoal(*change.Goal, snapshot)
}

func (a *Agent) publishGoal(goal nativeGoal, snapshot bool) error {
	if goal.ID == "" || goal.Objective == "" {
		return fmt.Errorf("DeepSeek Harness goal has no identity or objective")
	}
	status := agent.GoalStatusNone
	switch goal.Phase {
	case "active":
		status = agent.GoalStatusActive
	case "paused":
		status = agent.GoalStatusPaused
	case "blocked":
		status = agent.GoalStatusBlocked
	case "complete":
		status = agent.GoalStatusDone
	default:
		return fmt.Errorf("DeepSeek Harness goal phase is invalid")
	}
	update := agent.GoalUpdate{NativeID: goal.ID, Objective: goal.Objective, Status: status, Snapshot: snapshot}
	if goal.Rounds >= 0 && goal.Rounds <= math.MaxInt32 {
		rounds := int32(goal.Rounds)
		update.Iterations = &rounds
	}
	if goal.Blocked != nil {
		update.StatusDetail = goal.Blocked.Message
	}
	a.sink.UpsertGoal(update)
	return nil
}

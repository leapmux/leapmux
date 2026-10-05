package qwen

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Qwen states the whole goal on the `_meta.goalState` of a message after every
// change. The report is the source of truth for the card, so LeapMux does not
// observe the actions that it sends.
//
// LeapMux changes the goal through the goal control of Qwen. The goal control
// is a request beside the conversation:
//
//   - `qwen/control/session/goal/control` takes the requests that the `/goal`
//     command of Qwen hands to its goal runtime: `create`, `replace`, `pause`,
//     `resume` and `clear`.
//   - `qwen/control/session/goal/get` reads the goal that a request must name.
//
// Each action takes effect at once, also while a goal round runs. Qwen cancels
// the running round itself.
//
// A `/goal` prompt cannot carry an action. Qwen starts the next round of a goal
// when the previous round ends. The worker therefore never finds the agent idle
// while the goal runs. A prompt that waits in the queue of the worker reaches
// Qwen only after the goal stopped by itself. Qwen then refuses a pause: "Only
// an active Goal can be paused".
const (
	qwenGoalAdvertisedCommand = "goal"
	qwenGoalGetMethod         = "qwen/control/session/goal/get"
	qwenGoalControlMethod     = "qwen/control/session/goal/control"
)

// Qwen's goal control actions.
const (
	qwenGoalControlCreate  = "create"
	qwenGoalControlReplace = "replace"
	qwenGoalControlPause   = "pause"
	qwenGoalControlResume  = "resume"
	qwenGoalControlClear   = "clear"
)

var _ agent.GoalWriter = (*Agent)(nil)

// SupportedGoalActions reports the four verbs, and only while Qwen advertises
// the `goal` command: a build without it has no goal runtime.
func (a *Agent) SupportedGoalActions() []agent.GoalAction {
	if !a.HasAvailableCommand(qwenGoalAdvertisedCommand) {
		return nil
	}
	return []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear, agent.GoalActionPause, agent.GoalActionResume}
}

// PerformGoalAction changes Qwen's goal through its goal control, and queues
// nothing: the goal state then reports what Qwen did. It reads the current goal
// first, because every request but `create` names the goal and the revision it
// acts on, as Qwen's own `/goal` command does. An action that
// SupportedGoalActions does not list sends no request at all.
func (a *Agent) PerformGoalAction(action agent.GoalAction, objective string) (agent.GoalOutcome, error) {
	if !slices.Contains(a.SupportedGoalActions(), action) {
		return agent.GoalOutcome{}, agent.ErrGoalControlUnsupported
	}
	objective = strings.TrimSpace(objective)
	if action == agent.GoalActionSet && objective == "" {
		return agent.GoalOutcome{}, fmt.Errorf("qwen goal: an objective is required")
	}
	err := a.WithSessionID(func(sessionID string) error {
		// A prompt of the base refuses these two cases in the same words.
		// Qwen would answer a request with no session id with a protocol error.
		if a.IsStopped() {
			return fmt.Errorf("agent is stopped")
		}
		if sessionID == "" {
			return fmt.Errorf("agent has no active session")
		}
		current, err := a.readNativeGoal(sessionID)
		if err != nil {
			return err
		}
		request, err := qwenGoalControlRequest(action, objective, current)
		if err != nil || request == nil {
			return err
		}
		params, err := json.Marshal(map[string]any{"sessionId": sessionID, "request": request})
		if err != nil {
			return fmt.Errorf("marshal the Qwen goal %s: %w", request["action"], err)
		}
		if _, err := a.SendRequest(qwenGoalControlMethod, params, a.APITimeout()); err != nil {
			return fmt.Errorf("qwen goal %s: %w", request["action"], err)
		}
		return nil
	})
	return agent.GoalOutcome{}, err
}

// qwenGoalVersion names one goal and the revision of it that a control request
// expects. Qwen refuses a request whose goal changed since.
type qwenGoalVersion struct {
	GoalID   string `json:"goalId"`
	Revision int64  `json:"revision"`
}

// readNativeGoal reads the goal of sessionID. It returns nil when the session
// has no goal.
func (a *Agent) readNativeGoal(sessionID string) (*qwenGoalVersion, error) {
	params, err := json.Marshal(map[string]string{"sessionId": sessionID})
	if err != nil {
		return nil, fmt.Errorf("marshal the Qwen goal read: %w", err)
	}
	response, err := a.SendRequest(qwenGoalGetMethod, params, a.APITimeout())
	if err != nil {
		return nil, fmt.Errorf("read the Qwen goal: %w", err)
	}
	var reply struct {
		Snapshot *struct {
			Goal *qwenGoalVersion `json:"goal"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal(response, &reply); err != nil || reply.Snapshot == nil {
		return nil, fmt.Errorf("read the Qwen goal: the answer states no goal snapshot")
	}
	goal := reply.Snapshot.Goal
	if goal == nil {
		return nil, nil
	}
	if goal.GoalID == "" || goal.Revision < 1 {
		return nil, fmt.Errorf("read the Qwen goal: the goal states no id or revision")
	}
	return goal, nil
}

// qwenGoalControlRequest builds the control request of one action, as Qwen's
// `/goal` command builds it: a set creates a goal, or replaces the current one,
// and every other action names the current goal. A clear with no goal needs no
// request, and returns nil. A pause or a resume with no goal is refused, as
// Qwen's command refuses it.
//
// A pause states no reason. Qwen's command states "Paused with /goal pause.",
// which no command did here, so the goal card shows the pause with no reason.
func qwenGoalControlRequest(action agent.GoalAction, objective string, current *qwenGoalVersion) (map[string]any, error) {
	versioned := func(verb string) map[string]any {
		return map[string]any{"action": verb, "expectedGoalId": current.GoalID, "expectedRevision": current.Revision}
	}
	switch action {
	case agent.GoalActionSet:
		if current == nil {
			return map[string]any{"action": qwenGoalControlCreate, "objective": objective}, nil
		}
		request := versioned(qwenGoalControlReplace)
		request["objective"] = objective
		return request, nil
	case agent.GoalActionClear:
		if current == nil {
			return nil, nil
		}
		return versioned(qwenGoalControlClear), nil
	case agent.GoalActionPause:
		if current == nil {
			return nil, fmt.Errorf("qwen goal: there is no goal to pause")
		}
		return versioned(qwenGoalControlPause), nil
	case agent.GoalActionResume:
		if current == nil {
			return nil, fmt.Errorf("qwen goal: there is no goal to resume")
		}
		return versioned(qwenGoalControlResume), nil
	default:
		return nil, agent.ErrGoalControlUnsupported
	}
}

// qwenGoalSnapshot is Qwen's goal state (`GoalSnapshotV2`).
type qwenGoalSnapshot struct {
	Goal *struct {
		GoalID       string `json:"goalId"`
		Objective    string `json:"objective"`
		Status       string `json:"status"`
		TurnCount    *int32 `json:"turnCount"`
		ActiveTimeMs *int64 `json:"activeTimeMs"`
		TokensUsed   *int64 `json:"tokensUsed"`
		TokenBudget  *int64 `json:"tokenBudget"`
		CreatedAt    int64  `json:"createdAt"`
		LastReason   string `json:"lastReason"`
	} `json:"goal"`
	Activity string `json:"activity"`
}

// Qwen's goal status words.
const (
	qwenGoalStatusActive       = "active"
	qwenGoalStatusPaused       = "paused"
	qwenGoalStatusBlocked      = "blocked"
	qwenGoalStatusUsageLimited = "usage_limited"
	qwenGoalStatusComplete     = "complete"
)

// qwenGoalStatus maps Qwen's five status words onto the four neutral ones. An
// unknown word maps to blocked: a status this build cannot read is one it must
// not offer Pause for.
func qwenGoalStatus(wire string) agent.GoalStatus {
	switch wire {
	case qwenGoalStatusActive:
		return agent.GoalStatusActive
	case qwenGoalStatusPaused:
		return agent.GoalStatusPaused
	case qwenGoalStatusComplete:
		return agent.GoalStatusDone
	default:
		return agent.GoalStatusBlocked
	}
}

// qwenGoalStatusDetail keeps what the neutral status loses: that a goal ran out
// of usage, that a check runs, and the reason for a pause or a block.
func qwenGoalStatusDetail(status, activity, reason string) string {
	var parts []string
	if status == qwenGoalStatusUsageLimited {
		parts = append(parts, "usage limited")
	}
	if status == qwenGoalStatusActive && activity == "verifying" {
		parts = append(parts, "verifying")
	}
	if status != qwenGoalStatusActive && status != qwenGoalStatusComplete {
		if reason = strings.TrimSpace(reason); reason != "" {
			parts = append(parts, reason)
		}
	}
	return strings.Join(parts, ": ")
}

// handleGoalState folds one goal state into the card. A state with no goal
// states that no goal exists.
func (a *Agent) handleGoalState(raw json.RawMessage) {
	var snapshot qwenGoalSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		slog.Warn("qwen goal state unreadable", "agent_id", a.AgentID(), "error", err)
		return
	}
	goal := snapshot.Goal
	if goal == nil || goal.GoalID == "" {
		a.Sink().ClearGoal(false)
		return
	}
	var seconds *int64
	if goal.ActiveTimeMs != nil {
		value := *goal.ActiveTimeMs / 1000
		seconds = &value
	}
	var createdAt time.Time
	if goal.CreatedAt > 0 {
		createdAt = time.UnixMilli(goal.CreatedAt).UTC()
	}
	a.Sink().UpsertGoal(agent.GoalUpdate{
		NativeID:        goal.GoalID,
		Objective:       goal.Objective,
		Status:          qwenGoalStatus(goal.Status),
		StatusDetail:    qwenGoalStatusDetail(goal.Status, snapshot.Activity, goal.LastReason),
		CreatedAt:       createdAt,
		TokensUsed:      goal.TokensUsed,
		TokenBudget:     goal.TokenBudget,
		TimeUsedSeconds: seconds,
		Iterations:      goal.TurnCount,
	})
}

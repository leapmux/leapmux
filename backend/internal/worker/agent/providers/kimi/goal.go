package kimi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Kimi Code's session goal.
//
// The server owns a goal of its own and reports every change with
// `goal.updated`, whose `snapshot` is the whole goal or null. LeapMux writes it
// through the session profile: `goal_objective` creates one and `goal_control`
// pauses, resumes or cancels it. So the provider is a side-band GoalWriter, with
// one exception the TUI states too: creating a goal starts no turn, so a Set
// also returns the objective as the user message that starts the work.
//
// The server removes a goal that completed -- the snapshot turns null right
// after the completion -- and the card follows it. The completion itself reaches
// the transcript first, as the Done transition.

// kimiGoalSnapshot is `goal.updated.snapshot` and the reply of GET .../goal.
type kimiGoalSnapshot struct {
	GoalID      string `json:"goalId"`
	Objective   string `json:"objective"`
	Status      string `json:"status"`
	TurnsUsed   *int32 `json:"turnsUsed"`
	TokensUsed  *int64 `json:"tokensUsed"`
	WallClockMs *int64 `json:"wallClockMs"`
	Budget      *struct {
		TokenBudget *int64 `json:"tokenBudget"`
	} `json:"budget"`
}

// kimiGoalState is what the agent remembers about the session's goal.
type kimiGoalState struct {
	// goalID and createdAt identify the current goal. The server states no
	// creation time, so the worker records when it first saw the goal.
	goalID    string
	createdAt time.Time
	status    agent.GoalStatus
}

// kimiGoalStatus maps a goal status word onto the neutral status. A word this
// build does not know reads as Blocked: a state LeapMux cannot read is one it
// must not offer Pause for.
func kimiGoalStatus(status string) agent.GoalStatus {
	switch status {
	case kimiGoalActive:
		return agent.GoalStatusActive
	case kimiGoalPaused:
		return agent.GoalStatusPaused
	case kimiGoalComplete:
		return agent.GoalStatusDone
	case kimiGoalBlocked:
		return agent.GoalStatusBlocked
	default:
		return agent.GoalStatusBlocked
	}
}

func (a *Agent) handleGoalUpdated(event kimiEvent) {
	if event.AgentID != kimiMainAgentID {
		// A subagent's goal is not the session's. See GoalServices.UpsertGoal.
		return
	}
	var payload struct {
		Snapshot *kimiGoalSnapshot `json:"snapshot"`
	}
	if !event.decode(&payload) {
		return
	}
	a.applyGoal(payload.Snapshot, false)
}

// applyGoal records one goal report. snapshot marks a report that restates the
// goal -- the read at a resume -- rather than a change.
func (a *Agent) applyGoal(goal *kimiGoalSnapshot, snapshot bool) {
	if goal == nil || strings.TrimSpace(goal.Objective) == "" {
		a.Mu.Lock()
		// A goal that completed is gone by the server's rule, and its completion is
		// already in the transcript; the removal needs no row of its own.
		quiet := snapshot || a.goal.status == agent.GoalStatusDone
		a.goal = kimiGoalState{}
		a.Mu.Unlock()
		// The clear runs even when this agent knew of no goal. The worker's store
		// can hold one from a previous process, which this copy never saw
		// (GoalServices.ClearGoal), and a store with no goal writes no row.
		a.sink.ClearGoal(quiet)
		return
	}
	status := kimiGoalStatus(goal.Status)
	a.Mu.Lock()
	if a.goal.goalID != goal.GoalID || a.goal.createdAt.IsZero() {
		a.goal.goalID = goal.GoalID
		a.goal.createdAt = a.clock.Now().UTC()
	}
	a.goal.status = status
	createdAt := a.goal.createdAt
	a.Mu.Unlock()

	update := agent.GoalUpdate{
		NativeID:     goal.GoalID,
		Objective:    goal.Objective,
		Status:       status,
		StatusDetail: goal.Status,
		CreatedAt:    createdAt,
		TokensUsed:   goal.TokensUsed,
		Iterations:   goal.TurnsUsed,
		Snapshot:     snapshot,
	}
	if goal.Budget != nil {
		update.TokenBudget = goal.Budget.TokenBudget
	}
	if goal.WallClockMs != nil {
		seconds := *goal.WallClockMs / 1000
		update.TimeUsedSeconds = &seconds
	}
	a.sink.UpsertGoal(update)
}

// readGoal restates the session's goal after a resume.
func (a *Agent) readGoal(ctx context.Context, sessionID string) {
	if !a.features[kimiFeatureGoal] {
		return
	}
	var goal *kimiGoalSnapshot
	if err := a.api.get(ctx, kimiSessionPath(sessionID, "/goal"), &goal); err != nil {
		slog.Debug("kimi read goal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.applyGoal(goal, true)
}

// SupportedGoalActions reports every action once the engine runs its goal
// feature: the profile route states each one.
func (a *Agent) SupportedGoalActions() []agent.GoalAction {
	if !a.features[kimiFeatureGoal] {
		return nil
	}
	return []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear, agent.GoalActionPause, agent.GoalActionResume}
}

// PerformGoalAction writes the goal through the session profile.
func (a *Agent) PerformGoalAction(action agent.GoalAction, objective string) (agent.GoalOutcome, error) {
	if !a.features[kimiFeatureGoal] {
		return agent.GoalOutcome{}, agent.ErrGoalControlUnsupported
	}
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	a.Mu.Lock()
	sessionID, busy, hasGoal := a.sessionID, a.turnActive, a.goal.goalID != ""
	a.Mu.Unlock()
	if err := kimiCheckID("session", sessionID); err != nil {
		return agent.GoalOutcome{}, err
	}
	ctx, cancel := a.requestContext()
	defer cancel()
	switch action {
	case agent.GoalActionSet:
		objective = strings.TrimSpace(objective)
		if objective == "" {
			return agent.GoalOutcome{}, errors.New("a goal needs an objective")
		}
		if hasGoal {
			// The server refuses a second goal, so a new objective replaces the
			// current one: cancel it, then create. A goal that the server removed
			// already leaves nothing to replace.
			if _, err := a.cancelGoal(ctx, sessionID); err != nil {
				return agent.GoalOutcome{}, err
			}
		}
		if err := a.postProfile(ctx, sessionID, map[string]any{kimiConfigGoalObjective: objective}); err != nil {
			if code, stated := kimiErrorCode(err); stated && code == kimiCodeGoalExists {
				return agent.GoalOutcome{}, fmt.Errorf("the session already runs a goal: %w", err)
			}
			return agent.GoalOutcome{}, err
		}
		// Creating a goal starts no turn. The objective, sent as the user's
		// message, is what starts the work -- the TUI's /goal does the same.
		return agent.GoalOutcome{QueuedInput: objective}, nil
	case agent.GoalActionClear:
		gone, err := a.cancelGoal(ctx, sessionID)
		if err != nil {
			return agent.GoalOutcome{}, err
		}
		if gone {
			// The session has no goal, which is the state the reader asked for. The
			// goal.updated that removed it can be lost with a stream gap, so
			// LeapMux's copy goes here too. dispatchMu keeps the removal in order
			// with the goal events.
			a.dispatchMu.Lock()
			a.applyGoal(nil, false)
			a.dispatchMu.Unlock()
		}
		return agent.GoalOutcome{}, nil
	case agent.GoalActionPause:
		if err := a.goalControl(ctx, sessionID, kimiGoalControlPause); err != nil {
			return agent.GoalOutcome{}, err
		}
		if busy {
			// A pause stops the continuation turns, and the TUI stops the running
			// one as well: a paused goal must not keep working.
			if err := a.api.post(ctx, kimiSessionPath(sessionID, kimiActionAbort), nil, nil); err != nil {
				slog.Warn("kimi abort the turn of a paused goal", "agent_id", a.AgentID(), "error", err)
			}
		}
		return agent.GoalOutcome{}, nil
	case agent.GoalActionResume:
		// The server starts the continuation turn itself.
		return agent.GoalOutcome{}, a.goalControl(ctx, sessionID, kimiGoalControlResume)
	default:
		return agent.GoalOutcome{}, agent.ErrGoalControlUnsupported
	}
}

func (a *Agent) goalControl(ctx context.Context, sessionID, control string) error {
	return a.postProfile(ctx, sessionID, map[string]any{kimiConfigGoalControl: control})
}

// cancelGoal cancels the session's goal. gone is true when the server had no
// goal to cancel: it removes a goal by itself once the goal completes, so a
// Clear or a Set can reach a session whose goal the reader still sees.
func (a *Agent) cancelGoal(ctx context.Context, sessionID string) (gone bool, err error) {
	err = a.goalControl(ctx, sessionID, kimiGoalControlCancel)
	if code, stated := kimiErrorCode(err); stated && code == kimiCodeGoalNotFound {
		return true, nil
	}
	return false, err
}

// postProfile posts an `agent_config` patch to the session's profile.
func (a *Agent) postProfile(ctx context.Context, sessionID string, config map[string]any) error {
	return a.api.post(ctx, kimiSessionPath(sessionID, "/profile"), map[string]any{"agent_config": config}, &json.RawMessage{})
}

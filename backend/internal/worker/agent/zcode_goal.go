package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

// ZCode's session goal, which its own code calls a "target" -- goal is the
// user-facing word and target is the storage and runtime one.
//
// `/goal [pause|resume|clear|replace <objective>|<objective>]` drives it, and
// the app-server exposes the same operations as a request:
//
//	session/goal {sessionId, expectedRevision, inputId, action, objective?}
//
// The state rides two paths, both handled here: the `goal` key of a session
// snapshot (session/create, session/resume, session/read) and the same key
// inside a `state.updated` patch.
const (
	zcodeMethodSessionGoal = "session/goal"

	// The actions LeapMux issues. `show` and the bare-objective form exist too;
	// `show` has no use here because the state arrives unsolicited, and the bare
	// form is what `replace` aliases.
	zcodeGoalActionReplace = "replace"
	zcodeGoalActionClear   = "clear"
	zcodeGoalActionPause   = "pause"
	zcodeGoalActionResume  = "resume"
)

// ZCode's own status words.
const (
	zcodeGoalStatusActive       = "active"
	zcodeGoalStatusPaused       = "paused"
	zcodeGoalStatusVerifying    = "verifying"
	zcodeGoalStatusVerified     = "verified"
	zcodeGoalStatusNotSatisfied = "notSatisfied"
	zcodeGoalStatusFailed       = "failed"
)

// zcodeGoalState is the `goal` object in a snapshot or a patch.
type zcodeGoalState struct {
	TargetID        string `json:"targetId"`
	Objective       string `json:"objective"`
	Status          string `json:"status"`
	TimeUsedSeconds *int64 `json:"timeUsedSeconds"`
	Iteration       *int32 `json:"iteration"`
	TokenBudget     *int64 `json:"tokenBudget"`
}

// zcodeGoalStatus maps ZCode's six status words onto the four neutral ones.
//
// `verifying` maps to ACTIVE, not to a state of its own: the goal is still
// being pursued, and the run is between "working" and "done" rather than
// stopped. The word survives in StatusDetail, which is where a reader learns
// that a completion check is running.
//
// An unknown word maps to blocked for the reason Codex's does: a status this
// build cannot interpret is one it must not offer Pause for.
func zcodeGoalStatus(wire string) GoalStatus {
	switch wire {
	case zcodeGoalStatusActive, zcodeGoalStatusVerifying:
		return GoalStatusActive
	case zcodeGoalStatusPaused:
		return GoalStatusPaused
	case zcodeGoalStatusVerified:
		return GoalStatusDone
	case zcodeGoalStatusNotSatisfied, zcodeGoalStatusFailed:
		return GoalStatusBlocked
	default:
		return GoalStatusBlocked
	}
}

// reportZCodeGoal folds the `goal` key of a snapshot or a patch into the sink.
//
// The three cases are distinct and all three matter:
//
//   - ABSENT (a patch that changed something else): nothing is reported, or a
//     settings patch would clear the goal.
//   - null: the goal is gone.
//   - an object: the current goal.
//
// snapshot marks the state as a restatement rather than an announcement. A
// session/create, session/resume or session/read reply RESTATES a goal that may
// be hours old, exactly like Codex's resume push; only a `state.updated` patch
// reports a change as it happens.
func (a *zcodeAgent) reportZCodeGoal(raw json.RawMessage, snapshot bool) {
	if len(raw) == 0 {
		return
	}
	if string(raw) == "null" {
		a.sink.ClearGoal()
		return
	}
	var state zcodeGoalState
	if err := json.Unmarshal(raw, &state); err != nil {
		slog.Warn("zcode goal state unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}
	if state.Objective == "" && state.Status == "" {
		return
	}
	a.sink.UpsertGoal(GoalUpdate{
		Objective:    state.Objective,
		Status:       zcodeGoalStatus(state.Status),
		StatusDetail: state.Status,
		// ZCode gives the goal a real id, and it is the only identity available:
		// the state carries no creation time. Feeding the id through CreatedAt
		// would be a lie about what the field means, so the identity falls back
		// to the applier's own stamp and a REPLACED goal is recognized by its
		// changed objective. Two goals with the same objective text in one
		// session read as one, which is the same trade Codex's createdAt avoids
		// and ZCode gives no way to avoid.
		TimeUsedSeconds: state.TimeUsedSeconds,
		Iterations:      state.Iteration,
		TokenBudget:     state.TokenBudget,
		// No token usage: ZCode reports a budget but never a consumed count.
		Snapshot: snapshot,
	})
}

// --- GoalController ---

// SupportedGoalActions: ZCode is the second provider with a complete
// acknowledged API. session/goal takes pause, resume, clear and replace.
func (a *zcodeAgent) SupportedGoalActions() []GoalAction {
	return []GoalAction{GoalActionSet, GoalActionClear, GoalActionPause, GoalActionResume}
}

// SetGoal uses `replace` rather than the bare-objective form.
//
// ZCode's own help says "Setting a new objective overwrites an existing goal;
// replace is an explicit alias", so the two do the same thing -- and the alias
// says which of them was meant, which matters because the bare form is
// positional and an objective that begins with the word `pause` would otherwise
// parse as a different action.
func (a *zcodeAgent) SetGoal(objective string) error {
	return a.sendZCodeGoal(zcodeGoalActionReplace, objective)
}

func (a *zcodeAgent) ClearGoal() error { return a.sendZCodeGoal(zcodeGoalActionClear, "") }
func (a *zcodeAgent) PauseGoal() error { return a.sendZCodeGoal(zcodeGoalActionPause, "") }

func (a *zcodeAgent) ResumeGoal() error { return a.sendZCodeGoal(zcodeGoalActionResume, "") }

// sendZCodeGoal issues one session/goal request.
//
// expectedRevision is ZCode's optimistic-concurrency check: the app-server
// refuses the write when the session moved on since the revision the caller
// saw. The value comes from the last runtime state LeapMux observed. Sending
// the CURRENT known revision -- rather than omitting the field -- is what makes
// a goal write racing an in-flight turn fail loudly instead of overwriting a
// change the agent just made.
func (a *zcodeAgent) sendZCodeGoal(action, objective string) error {
	a.mu.Lock()
	sessionID := a.sessionID
	revision := a.stateRevision
	a.mu.Unlock()
	if sessionID == "" {
		return fmt.Errorf("zcode %s: agent has no ZCode session", zcodeMethodSessionGoal)
	}
	params := map[string]any{
		"sessionId":        sessionID,
		"expectedRevision": revision,
		"inputId":          generateRequestID(),
		"action":           action,
	}
	if objective != "" {
		params["objective"] = objective
	}
	// The reply carries the resulting snapshot. It is not applied: the
	// app-server also emits a state.updated patch for the same change, so
	// reading both would give the stored goal two writers with no ordering
	// between them.
	if _, err := a.sendZCodeRequest(zcodeMethodSessionGoal, params, a.APITimeout()); err != nil {
		return err
	}
	return nil
}

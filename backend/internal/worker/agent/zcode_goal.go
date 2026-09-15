package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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

// zcodeGoalStatusDetail keeps the provider's own word only when the neutral status
// loses it.
//
// The detail exists for `verifying`, which maps to ACTIVE, and for `notSatisfied`
// and `failed`, which both map to BLOCKED: there the word tells the reader
// something the status cannot. A word that maps to its own name tells them
// nothing, and the card renders it beside the status -- "active (active)", and
// "Session goal active, active:" in the live region a screen reader hears.
func zcodeGoalStatusDetail(wire string, status GoalStatus) string {
	if strings.EqualFold(wire, GoalStatusWire(status)) {
		return ""
	}
	return wire
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
		// A session snapshot RESTATES the absence; a state patch announces it.
		a.sink.ClearGoal(snapshot)
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
	status := zcodeGoalStatus(state.Status)
	a.sink.UpsertGoal(GoalUpdate{
		NativeID:        state.TargetID,
		Objective:       state.Objective,
		Status:          status,
		StatusDetail:    zcodeGoalStatusDetail(state.Status, status),
		TimeUsedSeconds: state.TimeUsedSeconds,
		Iterations:      state.Iteration,
		TokenBudget:     state.TokenBudget,
		// No token usage: ZCode reports a budget but never a consumed count.
		Snapshot: snapshot,
	})
}

// --- GoalWriter ---

// SupportedGoalActions: ZCode is the second provider with a complete
// acknowledged API. session/goal takes pause, resume, clear and replace.
func (a *zcodeAgent) SupportedGoalActions() []GoalAction {
	return []GoalAction{GoalActionSet, GoalActionClear, GoalActionPause, GoalActionResume}
}

var _ GoalWriter = (*zcodeAgent)(nil)

// PerformGoalAction runs one action through session/goal. Every action is a
// side-band request that completes here, so the caller has nothing left to do.
//
// A SET uses `replace` rather than the bare-objective form. ZCode's own help
// says "Setting a new objective overwrites an existing goal; replace is an
// explicit alias", so the two do the same thing -- and the alias says which of
// them was meant, which matters because the bare form is positional and an
// objective that begins with the word `pause` would otherwise parse as a
// different action.
func (a *zcodeAgent) PerformGoalAction(action GoalAction, objective string) (GoalOutcome, error) {
	switch action {
	case GoalActionSet:
		return GoalOutcome{}, a.sendZCodeGoal(zcodeGoalActionReplace, objective)
	case GoalActionClear:
		return GoalOutcome{}, a.sendZCodeGoal(zcodeGoalActionClear, "")
	case GoalActionPause:
		return GoalOutcome{}, a.sendZCodeGoal(zcodeGoalActionPause, "")
	case GoalActionResume:
		return GoalOutcome{}, a.sendZCodeGoal(zcodeGoalActionResume, "")
	default:
		return GoalOutcome{}, ErrGoalControlUnsupported
	}
}

// sendZCodeGoal issues one session/goal request.
//
// expectedRevision is ZCode's optimistic-concurrency check. The app-server
// refuses the write when the session moved on since the revision the caller
// saw. The value comes from the last runtime state LeapMux observed. LeapMux
// sends the CURRENT known revision rather than omitting the field. A goal write
// that races an in-flight turn then fails loudly, instead of overwriting a
// change the agent just made.
//
// A goal action can itself start or stop a turn and advance the revision again,
// so one conflict retries. The retry sends the revision the CONFLICT REPLY
// carried, not the cached one: noteZCodeStateRevision only raises the cache, so
// a server revision below it would leave the retry byte-identical to the
// request that just failed.
//
// The retry accepts the app-server's revision whatever advanced it, so for that
// one round it does not refuse a change somebody else made in the window. The
// wire says only WHAT the revision is, never WHO moved it, so no narrower rule
// is available from the reply. One retry caps the exposure; a second conflict
// is reported.
func (a *zcodeAgent) sendZCodeGoal(action, objective string) error {
	a.mu.Lock()
	sessionID := a.sessionID
	revision := a.stateRevision
	a.mu.Unlock()
	if sessionID == "" {
		return fmt.Errorf("zcode %s: agent has no ZCode session", zcodeMethodSessionGoal)
	}
	// A fresh inputId per REQUEST, never per call: two requests that carry one
	// id and different parameters are the worst input for any server-side
	// duplicate check.
	send := func(revision int64) (json.RawMessage, error) {
		params := map[string]any{
			"sessionId":        sessionID,
			"expectedRevision": revision,
			"inputId":          generateRequestID(),
			"action":           action,
		}
		if objective != "" {
			params["objective"] = objective
		}
		return a.sendZCodeRequest(zcodeMethodSessionGoal, params, a.APITimeout())
	}
	raw, err := send(revision)
	if err != nil {
		actual, ok := zcodeActualRevision(err)
		if !ok {
			return err
		}
		a.noteZCodeStateRevision(actual)
		if raw, err = send(actual); err != nil {
			return err
		}
	}
	// Its REVISION matters, and skipping it broke the second action in a row.
	// This call bumps the app-server's counter, and nothing else refreshes ours
	// until a turn ends -- so Pause followed by Resume sent the same pre-pause
	// expectedRevision twice and the app-server refused the second for a
	// conflict that did not exist.
	//
	// The reply's GOAL is applied too, as a RESTATEMENT. It was once dropped
	// here on the grounds that "the app-server also emits a state.updated patch
	// for the same change", and that is not true of the shipped build: a live
	// run set a goal, ZCode accepted it and started a turn whose input source
	// was `goal-continuation`, and the worker's whole debug log for that run
	// held no `state.updated` at all. The reply was the only statement of the
	// goal, so dropping it left the card empty for a goal the agent was already
	// pursuing.
	//
	// Applying it as a restatement is what keeps the two writers ordered. A
	// build that DOES send the patch still announces the transition exactly
	// once, from the patch, because a snapshot only restates.
	if snap, ok := a.parseStateSnapshot(raw); ok {
		a.noteZCodeStateRevision(snap.Runtime.StateRevision)
		a.reportZCodeGoal(snap.goalState(), true)
	}
	return nil
}

// zcodeActualRevision reports the revision that a revision-mismatch reply
// carried. It answers false for every other error, and for a reply that states
// no revision.
func zcodeActualRevision(err error) (int64, bool) {
	var wireErr *zcodeError
	if !errors.As(err, &wireErr) || wireErr.Code != ZCodeErrRevisionMismatch {
		return 0, false
	}
	var data struct {
		ActualRevision *int64 `json:"actualRevision"`
	}
	if json.Unmarshal(wireErr.Data, &data) != nil || data.ActualRevision == nil {
		return 0, false
	}
	return *data.ActualRevision, true
}

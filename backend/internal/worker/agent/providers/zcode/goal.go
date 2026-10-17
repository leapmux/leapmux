package zcode

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// ZCode's session goal, which its own code calls a "target" -- goal is the
// user-facing word and target is the storage and runtime one.
//
// `/goal [pause|resume|clear|replace <objective>|<objective>]` drives it, and
// the app-server exposes the same operations as a request:
//
//	session/goal {sessionId, expectedRevision, inputId, action, objective?}
//
// ZCode 3.14.5 states the goal on two paths, and both are handled here:
//
//   - `session.target` of a state snapshot: the reply of session/create,
//     session/resume and session/goal. A snapshot RESTATES the goal.
//   - `target` of a `target_changed` event, which the app-server sends on
//     `session.updated` for each goal transition. The event ANNOUNCES the goal.
//
// ZCode 3.14.5 sends no `goal` key in its strict snapshot schema.
// Its `state.updated` patches carry run status or settings, not goal state.
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

// ZCode's goal status words: the closed set of four that the `target` of
// `session.target` and of `target_changed` carries.
//
// ZCode's desktop application uses six words: `active`, `paused`, `verifying`,
// `verified`, `notSatisfied` and `failed`. It derives them in its own projection
// from the goal check events, and the app-server never sends that projection.
// The last four are therefore unknown words to LeapMux.
const (
	zcodeGoalStatusActive        = "active"
	zcodeGoalStatusPaused        = "paused"
	zcodeGoalStatusComplete      = "complete"
	zcodeGoalStatusBudgetLimited = "budget_limited"
)

// zcodeGoalDetailVerifying is the detail of an ACTIVE goal while its goal check
// runs. It is a LeapMux phrase, not a ZCode status word: the check's start event
// is what sets it.
const zcodeGoalDetailVerifying = "verifying"

// zcodeGoalState reads `session.target` from a snapshot and `target` from a goal-change event.
// Those sources use different spellings for the native ID.
type zcodeGoalState struct {
	TargetID        string `json:"targetId"`
	TargetIDUpper   string `json:"targetID"`
	Objective       string `json:"objective"`
	Status          string `json:"status"`
	TimeUsedSeconds *int64 `json:"timeUsedSeconds"`
	Iteration       *int32 `json:"iteration"`
	TokenBudget     *int64 `json:"tokenBudget"`
	TokensUsed      *int64 `json:"tokensUsed"`
}

// nativeID returns the target's own id under the one spelling a writer used.
func (s zcodeGoalState) nativeID() string {
	if s.TargetID != "" {
		return s.TargetID
	}
	return s.TargetIDUpper
}

// zcodeGoalStatus maps ZCode's status words onto the four neutral ones.
//
// `complete` states the end of the pursuit. `budget_limited` maps to BLOCKED,
// as LeapMux maps Codex's `budgetLimited`: the goal needs the user before it can
// continue.
//
// An unknown word maps to BLOCKED for the reason that Codex's does: this build
// cannot interpret that status, so it must not offer Pause for it.
func zcodeGoalStatus(wire string) agent.GoalStatus {
	switch wire {
	case zcodeGoalStatusActive:
		return agent.GoalStatusActive
	case zcodeGoalStatusPaused:
		return agent.GoalStatusPaused
	case zcodeGoalStatusComplete:
		return agent.GoalStatusDone
	default:
		return agent.GoalStatusBlocked
	}
}

// zcodeGoalStatusDetail keeps the provider's own word only when the neutral status
// loses it.
//
// `active`, `paused` and `complete` each map to a neutral status that states the
// whole word. A detail beside one would only repeat it: the card would read
// "active (active)" or "done (complete)", the transcript row "Goal achieved: X
// (complete)", and the live region that a screen reader hears "Session goal
// active, active:". `budget_limited` reads as a phrase, so the card shows no
// snake_case wire token. An unknown word stays, because it is the only statement
// of why the card shows BLOCKED.
func zcodeGoalStatusDetail(wire string, status agent.GoalStatus) string {
	switch wire {
	case zcodeGoalStatusActive, zcodeGoalStatusPaused, zcodeGoalStatusComplete:
		return ""
	case zcodeGoalStatusBudgetLimited:
		return "budget limited"
	}
	if strings.EqualFold(wire, agent.GoalStatusWire(status)) {
		return ""
	}
	return wire
}

// reportZCodeGoal folds the goal state of a snapshot's `session.target` or of a
// goal change into the sink.
//
// The three cases are distinct and all three matter:
//
//   - ABSENT (a document without the key): nothing is reported, so a document
//     that states no goal leaves the goal alone.
//   - null: the goal is gone.
//   - an object: the current goal.
//
// snapshot marks the state as a restatement rather than an announcement. A
// session/create, session/resume or session/goal reply RESTATES a goal that may
// be hours old, exactly like Codex's resume push. A `target_changed` event
// reports a change as it happens.
func (a *Agent) reportZCodeGoal(raw json.RawMessage, snapshot bool) {
	if len(raw) == 0 {
		return
	}
	if string(raw) == "null" {
		// A session snapshot RESTATES the absence; a goal change announces it.
		a.clearZCodeGoal(snapshot)
		return
	}
	var state zcodeGoalState
	if err := json.Unmarshal(raw, &state); err != nil {
		slog.Warn("zcode goal state unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	if state.Objective == "" && state.Status == "" {
		return
	}
	status := zcodeGoalStatus(state.Status)
	update := agent.GoalUpdate{
		NativeID:        state.nativeID(),
		Objective:       state.Objective,
		Status:          status,
		StatusDetail:    zcodeGoalStatusDetail(state.Status, status),
		TimeUsedSeconds: state.TimeUsedSeconds,
		Iterations:      state.Iteration,
		TokenBudget:     state.TokenBudget,
		TokensUsed:      state.TokensUsed,
		Snapshot:        snapshot,
	}
	a.Mu.Lock()
	// The goal check's lifecycle reports this goal again with its own detail.
	// The agent keeps the last report for that lifecycle.
	a.lastGoalUpdate = update
	a.Mu.Unlock()
	a.sink.UpsertGoal(update)
}

// clearZCodeGoal reports that the session has no goal, and forgets the goal that
// the goal check reports again. Every clear goes through here. A cache that kept
// the cleared goal would let a later goal check put that goal back on the card.
func (a *Agent) clearZCodeGoal(snapshot bool) {
	a.Mu.Lock()
	a.lastGoalUpdate = agent.GoalUpdate{}
	a.Mu.Unlock()
	a.sink.ClearGoal(snapshot)
}

// handleZCodeGoalChanged folds a `target_changed` announcement into the sink.
//
// The announcement is the app-server's own statement of a goal transition. It
// reports the change LIVE rather than as a snapshot. A real status or objective
// transition then writes the neutral `goal_updated` row. A progress-only action
// updates the card alone. `cleared` and a null target state the removal.
func (a *Agent) handleZCodeGoalChanged(payload zcodeSessionUpdatedPayload) {
	action, _ := zcodeRawString(payload.Action)
	if action == contracts.ZCodeGoalActionCleared || string(payload.Target) == "null" {
		a.clearZCodeGoal(false)
		return
	}
	a.reportZCodeGoal(payload.Target, false)
}

// handleZCodeGoalVerification folds a `target_completion_verification` lifecycle.
//
// The goal check runs between "working" and "done". Its start marks the goal
// "verifying" in the detail and writes no row; its finals are rows of their own,
// and they drop the detail again.
//
// A check states the target that it checks. A check of a target other than the
// known goal is about a goal that the session replaced. ZCode skips the
// continuation of such a check, so its verdict does not apply to the current
// goal: the check marks nothing and writes no row. A check that runs while no
// goal is known keeps its row, because nothing contradicts it.
func (a *Agent) handleZCodeGoalVerification(event zcodeEventEnvelope, payload zcodeSessionUpdatedPayload) {
	status, _ := zcodeRawString(payload.Status)
	switch status {
	case contracts.ZCodeGoalVerificationStatusStarted,
		contracts.ZCodeGoalVerificationStatusCompleted,
		contracts.ZCodeGoalVerificationStatusFailedClosed,
		contracts.ZCodeGoalVerificationStatusCancelled:
	default:
		return
	}
	a.Mu.Lock()
	update := a.lastGoalUpdate
	a.Mu.Unlock()
	if targetID, _ := zcodeRawString(payload.TargetID); update.NativeID != "" && targetID != update.NativeID {
		return
	}
	if status == contracts.ZCodeGoalVerificationStatusStarted {
		if update.Objective == "" {
			return
		}
		update.StatusDetail = zcodeGoalDetailVerifying
		a.sink.UpsertGoal(update)
		return
	}
	a.persistZCodeNotification(event)
	if update.Objective == "" {
		return
	}
	a.sink.UpsertGoal(update)
}

// --- GoalWriter ---

// SupportedGoalActions: ZCode is the second provider with a complete
// acknowledged API. session/goal takes pause, resume, clear and replace.
func (a *Agent) SupportedGoalActions() []agent.GoalAction {
	return []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear, agent.GoalActionPause, agent.GoalActionResume}
}

var _ agent.GoalWriter = (*Agent)(nil)

// PerformGoalAction runs one action through session/goal. Every action is a
// side-band request that completes here, so the caller has nothing left to do.
//
// A SET uses `replace` rather than the bare-objective form. ZCode's own help
// says "Setting a new objective overwrites an existing goal; replace is an
// explicit alias", so the two do the same thing -- and the alias says which of
// them was meant, which matters because the bare form is positional and an
// objective that begins with the word `pause` would otherwise parse as a
// different action.
func (a *Agent) PerformGoalAction(action agent.GoalAction, objective string) (agent.GoalOutcome, error) {
	switch action {
	case agent.GoalActionSet:
		return agent.GoalOutcome{}, a.sendZCodeGoal(zcodeGoalActionReplace, objective)
	case agent.GoalActionClear:
		return agent.GoalOutcome{}, a.sendZCodeGoal(zcodeGoalActionClear, "")
	case agent.GoalActionPause:
		return agent.GoalOutcome{}, a.sendZCodeGoal(zcodeGoalActionPause, "")
	case agent.GoalActionResume:
		return agent.GoalOutcome{}, a.sendZCodeGoal(zcodeGoalActionResume, "")
	default:
		return agent.GoalOutcome{}, agent.ErrGoalControlUnsupported
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
func (a *Agent) sendZCodeGoal(action, objective string) error {
	a.Mu.Lock()
	sessionID := a.sessionID
	revision := a.stateRevision
	a.Mu.Unlock()
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
			"inputId":          id.Short(),
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
	// The reply's GOAL is applied too, as a RESTATEMENT. The `target_changed`
	// event announces the transition (handleZCodeGoalChanged), and the reply
	// only brings the card up to date. The app-server sends no `state.updated`
	// patch for a goal change.
	//
	// The goal rows depend on the ORDER of the two lines. The app-server appends
	// the event before it writes this reply, so the event line arrives first:
	// its live report writes the `goal_updated` row, and this restatement then
	// finds the same goal and writes nothing. If the reply arrived first, the
	// restatement would bring the card up to date with no row, and the live
	// report after it would find no transition to write.
	if snap, ok := a.parseStateSnapshot(raw); ok {
		a.noteZCodeStateRevision(snap.Runtime.StateRevision)
		a.reportZCodeGoal(snap.Session.Target, true)
	}
	return nil
}

// zcodeActualRevision reports the revision that a revision-mismatch reply
// carried. It answers false for every other error, and for a reply that states
// no revision.
func zcodeActualRevision(err error) (int64, bool) {
	var wireErr *zcodeError
	if !errors.As(err, &wireErr) || wireErr.Code != ErrRevisionMismatch {
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

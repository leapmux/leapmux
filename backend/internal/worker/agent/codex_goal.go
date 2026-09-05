package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// Codex's session goal.
//
// Wire shapes (codex-rs/app-server-protocol/src/protocol/v2/thread.rs):
//
//	thread/goal/updated {threadId, turnId: string|null, goal: ThreadGoal}
//	thread/goal/cleared {threadId}
//	ThreadGoal {threadId, objective, status, tokenBudget: number|null,
//	            tokensUsed, timeUsedSeconds, createdAt, updatedAt}
//
// Requests: thread/goal/set {threadId, objective?, status?} -> {goal},
// thread/goal/clear {threadId} -> {cleared}.
//
// createdAt and updatedAt are Unix SECONDS, not milliseconds.

const (
	codexMethodGoalUpdated = "thread/goal/updated"
	codexMethodGoalCleared = "thread/goal/cleared"
	codexMethodGoalSet     = "thread/goal/set"
	codexMethodGoalClear   = "thread/goal/clear"
)

// Codex's own status words. `blocked`, `usageLimited` and `budgetLimited` all
// mean "stopped and needs you", which is the one neutral status they share.
const (
	codexGoalStatusActive        = "active"
	codexGoalStatusPaused        = "paused"
	codexGoalStatusBlocked       = "blocked"
	codexGoalStatusUsageLimited  = "usageLimited"
	codexGoalStatusBudgetLimited = "budgetLimited"
	codexGoalStatusComplete      = "complete"
)

type codexGoalNotification struct {
	ThreadID string `json:"threadId"`
	// A POINTER, because null and absent must be told apart. Codex sends
	// turnId: null for the snapshot it pushes on thread/resume, and a snapshot
	// updates state without writing a transcript row.
	TurnID *string        `json:"turnId"`
	Goal   *codexGoalBody `json:"goal"`
}

type codexGoalBody struct {
	Objective       string `json:"objective"`
	Status          string `json:"status"`
	TokenBudget     *int64 `json:"tokenBudget"`
	TokensUsed      *int64 `json:"tokensUsed"`
	TimeUsedSeconds *int64 `json:"timeUsedSeconds"`
	CreatedAt       int64  `json:"createdAt"`
}

type codexGoalClearedNotification struct {
	ThreadID string `json:"threadId"`
}

// codexGoalStatus maps Codex's six status words onto the four neutral ones.
// An unknown word reads as blocked rather than active: a status this build does
// not recognize is one it cannot act on, and offering Pause for it would send a
// command the goal cannot accept.
func codexGoalStatus(wire string) GoalStatus {
	switch wire {
	case codexGoalStatusActive:
		return GoalStatusActive
	case codexGoalStatusPaused:
		return GoalStatusPaused
	case codexGoalStatusComplete:
		return GoalStatusDone
	case codexGoalStatusBlocked, codexGoalStatusUsageLimited, codexGoalStatusBudgetLimited:
		return GoalStatusBlocked
	default:
		return GoalStatusBlocked
	}
}

// handleGoalUpdated reports one Codex goal frame to the sink.
func (a *CodexAgent) handleGoalUpdated(params json.RawMessage) {
	var notif codexGoalNotification
	if err := json.Unmarshal(params, &notif); err != nil {
		slog.Warn("codex goal updated parse", "agent_id", a.agentID, "error", err)
		return
	}
	if notif.Goal == nil {
		return
	}
	// A collab CHILD thread is a thread, so it can carry a goal of its own.
	// Reporting it here would overwrite the session's goal with a subagent's,
	// because goal state is keyed by the root agent. Child goals are dropped
	// rather than shown: the panel describes what the SESSION is pursuing, and
	// a subagent's own objective already reads in its transcript.
	if !a.isMainThreadID(notif.ThreadID) {
		return
	}
	body := notif.Goal
	a.sink.UpsertGoal(GoalUpdate{
		Objective:    body.Objective,
		Status:       codexGoalStatus(body.Status),
		StatusDetail: body.Status,
		CreatedAt:    codexGoalTime(body.CreatedAt),
		// Codex reports no iteration count, so Iterations stays nil and the
		// panel omits that row rather than showing a zero.
		TokensUsed:      body.TokensUsed,
		TokenBudget:     body.TokenBudget,
		TimeUsedSeconds: body.TimeUsedSeconds,
		// A report that arrives during the RESUME HANDSHAKE restates a goal that
		// may be hours old, so it must not announce itself in the transcript as
		// though it just happened.
		//
		// The discriminator is the handshake, NOT `turnId == null`, although the
		// resume snapshot does carry a null one. Codex sends null for every
		// report made outside a turn -- including the one that acknowledges a
		// thread/goal/set this worker just issued. Reading null as "snapshot"
		// therefore swallowed exactly the events the user caused: the goal
		// changed on screen and the transcript never said so.
		Snapshot: a.resumingThread.Load(),
	})
}

// handleGoalCleared reports that the thread has no goal.
//
// Codex sends this in two situations, and the second is easy to miss: after a
// clear that removed a goal, AND on thread/resume for a thread that has none.
// The second is a SNAPSHOT meaning "there is nothing here", which is exactly
// the case where the worker's in-memory copy is cold and the database still
// holds a goal from a previous process -- so the sink issues the delete from
// what it reads, never from what it remembers.
func (a *CodexAgent) handleGoalCleared(params json.RawMessage) {
	var notif codexGoalClearedNotification
	if err := json.Unmarshal(params, &notif); err != nil {
		slog.Warn("codex goal cleared parse", "agent_id", a.agentID, "error", err)
		return
	}
	if !a.isMainThreadID(notif.ThreadID) {
		return
	}
	a.sink.ClearGoal()
}

// codexGoalTime converts Codex's Unix SECONDS to a time.Time. Zero means the
// field was absent, and the caller keeps whatever identity it already had.
func codexGoalTime(unixSeconds int64) time.Time {
	if unixSeconds <= 0 {
		return time.Time{}
	}
	return time.Unix(unixSeconds, 0).UTC()
}

// --- GoalController ---

// SupportedGoalActions: Codex is the one provider with a complete, acknowledged
// side-band API for all four. thread/goal/set carries both the objective and
// the status, so pause and resume are the same call with a different status.
func (a *CodexAgent) SupportedGoalActions() []GoalAction {
	return []GoalAction{GoalActionSet, GoalActionClear, GoalActionPause, GoalActionResume}
}

func (a *CodexAgent) SetGoal(objective string) error {
	return a.sendGoalSet(map[string]interface{}{"objective": objective})
}

func (a *CodexAgent) PauseGoal() error {
	return a.sendGoalSet(map[string]interface{}{"status": codexGoalStatusPaused})
}

func (a *CodexAgent) ResumeGoal() error {
	return a.sendGoalSet(map[string]interface{}{"status": codexGoalStatusActive})
}

func (a *CodexAgent) ClearGoal() error {
	threadID := a.currentThreadID()
	if threadID == "" {
		return fmt.Errorf("codex %s: no active thread", codexMethodGoalClear)
	}
	params, err := json.Marshal(map[string]string{"threadId": threadID})
	if err != nil {
		return fmt.Errorf("marshal %s params: %w", codexMethodGoalClear, err)
	}
	// The response reports whether a goal was actually removed. It is not read:
	// Codex emits thread/goal/cleared for a real removal, and that notification
	// -- not this reply -- is what updates the stored goal. Acting on both would
	// give the state two writers.
	if _, err := a.sendRequest(codexMethodGoalClear, params, a.APITimeout()); err != nil {
		return err
	}
	return nil
}

// sendGoalSet issues thread/goal/set with the given fields plus the thread id.
//
// The resulting goal comes back in the response and is deliberately ignored:
// Codex also emits thread/goal/updated for the same change, so applying the
// response too would write the state twice, from two paths, with no ordering
// between them.
func (a *CodexAgent) sendGoalSet(fields map[string]interface{}) error {
	threadID := a.currentThreadID()
	if threadID == "" {
		return fmt.Errorf("codex %s: no active thread", codexMethodGoalSet)
	}
	payload := map[string]interface{}{"threadId": threadID}
	for k, v := range fields {
		payload[k] = v
	}
	params, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal %s params: %w", codexMethodGoalSet, err)
	}
	if _, err := a.sendRequest(codexMethodGoalSet, params, a.APITimeout()); err != nil {
		return err
	}
	return nil
}

// currentThreadID reads the active thread id under the lock.
func (a *CodexAgent) currentThreadID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.threadID
}

package mimo

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// MiMo's session goal.
//
// `/goal <condition>` sets a stop condition: the condition is also the prompt
// of the turn it starts, and before the main loop stops, a judge model reads
// the transcript and decides whether the condition holds. Until it does, MiMo
// re-enters the loop, up to its own cap. `/goal clear` removes the goal. MiMo
// has no pause and no resume, and it holds the goal in memory only, so a new
// process starts with none.
//
// LeapMux sets and clears the goal through the command route, and reads its
// state from session.goal, which MiMo publishes for a set, for each verdict and
// for a clear.

// The goal command and the argument that clears it. The browser never sends
// either, so they stay in Go.
const (
	goalCommand       = "goal"
	goalClearArgument = "clear"
)

// goalClearWords are the arguments MiMo reads as a clear rather than as a
// condition, after it trims them. The comparison is exact, as MiMo's is. An
// objective equal to one of them would clear the goal it was meant to set.
var goalClearWords = []string{"", goalClearArgument, "reset"}

// Status details for the ends of a goal that are not a plain success.
const (
	goalDetailImpossible  = "impossible"
	goalDetailGaveUp      = "retry limit reached"
	goalDetailJudgeFailed = "the judge failed"
)

// mimoGoalState is the goal as MiMo last reported it.
type mimoGoalState struct {
	condition string
	createdAt time.Time
	// ended is true once a verdict ended the goal. MiMo then clears it, and that
	// clear must not erase the verdict the transcript just stated.
	ended bool
	// setWaiter is the PerformGoalAction call that waits for MiMo to confirm
	// its goal.
	setWaiter *goalWaiter
}

// goalWaiter waits for session.goal to state a goal. Any goal confirms the
// wait: MiMo sets the goal it was sent, and one caller waits at a time.
type goalWaiter struct {
	done chan struct{}
}

// mimoGoalEvent is session.goal.
type mimoGoalEvent struct {
	SessionID string `json:"sessionID"`
	Goal      *struct {
		Condition string `json:"condition"`
	} `json:"goal"`
	LastVerdict *struct {
		OK         bool   `json:"ok"`
		Impossible bool   `json:"impossible"`
		Reason     string `json:"reason"`
		Attempt    int32  `json:"attempt"`
		// Error marks a verdict that MiMo made up because its judge failed. MiMo
		// then lets the loop stop, which ends the goal without an answer.
		Error bool `json:"error"`
	} `json:"lastVerdict"`
}

func (a *Agent) handleSessionGoal(event mimoEvent) {
	var payload mimoGoalEvent
	if err := json.Unmarshal(event.Properties, &payload); err != nil {
		slog.Warn("mimo session.goal unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	// The clock is read before the lock, so an injected clock never runs under
	// a.Mu.
	now := a.clock.Now().UTC()
	a.Mu.Lock()
	if payload.SessionID != a.sessionID {
		a.Mu.Unlock()
		return
	}
	verdict := payload.LastVerdict
	switch {
	case payload.Goal != nil && strings.TrimSpace(payload.Goal.Condition) != "":
		condition := strings.TrimSpace(payload.Goal.Condition)
		if condition != a.goal.condition || a.goal.ended {
			a.goal.createdAt = now
		}
		a.goal.condition, a.goal.ended = condition, false
		if waiter := a.goal.setWaiter; waiter != nil {
			close(waiter.done)
			a.goal.setWaiter = nil
		}
		update := agent.GoalUpdate{Objective: condition, Status: agent.GoalStatusActive, CreatedAt: a.goal.createdAt}
		if verdict != nil {
			// The judge found the condition unmet, and its reason is what the
			// next pass works on.
			attempt := verdict.Attempt
			update.Iterations = &attempt
			update.StatusDetail = strings.TrimSpace(verdict.Reason)
		}
		a.Mu.Unlock()
		a.sink.UpsertGoal(update)
	case verdict != nil && a.goal.condition != "":
		// A verdict with no goal is the one that ended it: the judge found the
		// condition met or impossible, or MiMo reached its re-entry cap.
		update := agent.GoalUpdate{Objective: a.goal.condition, Status: agent.GoalStatusDone, CreatedAt: a.goal.createdAt}
		reason := strings.TrimSpace(verdict.Reason)
		switch {
		case verdict.Error:
			update.Status, update.StatusDetail = agent.GoalStatusBlocked, goalDetail(goalDetailJudgeFailed, reason)
		case verdict.OK:
			update.StatusDetail = reason
		case verdict.Impossible:
			update.Status, update.StatusDetail = agent.GoalStatusBlocked, goalDetail(goalDetailImpossible, reason)
		default:
			update.Status, update.StatusDetail = agent.GoalStatusBlocked, goalDetail(goalDetailGaveUp, reason)
		}
		attempt := verdict.Attempt
		update.Iterations = &attempt
		a.goal.ended = true
		a.Mu.Unlock()
		a.sink.UpsertGoal(update)
	case payload.Goal == nil && verdict == nil:
		ended := a.goal.ended
		a.goal = mimoGoalState{setWaiter: a.goal.setWaiter}
		a.Mu.Unlock()
		// After a verdict, MiMo clears the goal it just ended. The goal card keeps
		// the verdict, so that clear writes nothing. A clear with no verdict is the
		// user's, and it removes the goal.
		if !ended {
			a.sink.ClearGoal(false)
		}
	default:
		a.Mu.Unlock()
	}
}

// goalDetail states why a goal ended without success, and the judge's last
// reason when it gave one.
func goalDetail(cause, reason string) string {
	if reason == "" {
		return cause
	}
	return cause + ": " + reason
}

// SupportedGoalActions reports Set and Clear while the session runs. MiMo has
// no pause and no resume.
func (a *Agent) SupportedGoalActions() []agent.GoalAction {
	a.Mu.Lock()
	ready := a.sessionID != "" && !a.StoppedLocked()
	a.Mu.Unlock()
	if !ready {
		return nil
	}
	return []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear}
}

// PerformGoalAction sets or clears the goal through the goal command.
func (a *Agent) PerformGoalAction(action agent.GoalAction, objective string) (agent.GoalOutcome, error) {
	switch action {
	case agent.GoalActionSet:
		objective = strings.TrimSpace(objective)
		if slices.Contains(goalClearWords, objective) {
			return agent.GoalOutcome{}, agent.ErrGoalObjectiveIsCommand
		}
		return agent.GoalOutcome{}, a.setGoal(objective)
	case agent.GoalActionClear:
		return agent.GoalOutcome{}, a.clearGoal()
	default:
		return agent.GoalOutcome{}, agent.ErrGoalControlUnsupported
	}
}

// setGoal sends the goal command and waits until MiMo confirms the goal.
//
// The command route answers only after the turn that the goal starts, so the
// request runs on its own goroutine. MiMo publishes the goal before that turn
// starts, and that event is the confirmation this waits for.
func (a *Agent) setGoal(objective string) error {
	a.Mu.Lock()
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	sessionID := a.sessionID
	request := a.goalCommandLocked(objective)
	waiter := &goalWaiter{done: make(chan struct{})}
	a.goal.setWaiter = waiter
	a.Mu.Unlock()
	defer func() {
		a.Mu.Lock()
		if a.goal.setWaiter == waiter {
			a.goal.setWaiter = nil
		}
		a.Mu.Unlock()
	}()
	if sessionID == "" {
		return fmt.Errorf("agent has no MiMo session")
	}

	result := make(chan error, 1)
	go func() {
		err := a.rpc.command(a.Context(), sessionID, request)
		if err != nil {
			slog.Warn("mimo goal command failed", "agent_id", a.AgentID(), "error", err)
		}
		result <- err
	}()
	timeout := a.APITimeout()
	timer := a.clock.NewTimer(timeout, mimoGoalConfirmTimerTag)
	defer timer.Stop(mimoGoalConfirmTimerTag)
	select {
	case <-waiter.done:
		return nil
	case err := <-result:
		if err != nil {
			return classifyDeliveryError("goal", err)
		}
		// The command finished before this call read the event that confirms the
		// goal. The command succeeded, so the goal was set.
		return nil
	case <-timer.C:
		return fmt.Errorf("MiMo did not confirm the goal within %s", timeout)
	case <-a.ProcessDone():
		return a.ProcessExitError()
	}
}

// clearGoal sends the clear command. MiMo answers once the goal is gone, and
// session.goal then clears the card.
func (a *Agent) clearGoal() error {
	a.Mu.Lock()
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	sessionID := a.sessionID
	request := a.goalCommandLocked(goalClearArgument)
	a.Mu.Unlock()
	if sessionID == "" {
		return fmt.Errorf("agent has no MiMo session")
	}
	ctx, cancel := context.WithTimeout(a.Context(), a.APITimeout())
	defer cancel()
	if err := a.rpc.command(ctx, sessionID, request); err != nil {
		return classifyDeliveryError("goal clear", err)
	}
	return nil
}

// goalCommandLocked builds the goal command for the current settings. The
// caller holds a.Mu.
func (a *Agent) goalCommandLocked(arguments string) mimoCommandRequest {
	return mimoCommandRequest{
		Command:   goalCommand,
		Arguments: arguments,
		Agent:     a.mode,
		Model:     a.model,
		Variant:   a.catalog.resolveEffort(a.model, a.effort),
	}
}

package copilot

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// ClearContext suspends the current session and creates a replacement.
// If initialization fails, it attempts to resume the previous session with pending work interrupted.
// Suspension preserves stored session data, but it can interrupt pending controls.
func (a *Agent) ClearContext() (string, error) {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	if a.IsStopped() {
		return "", fmt.Errorf("the Copilot process is stopped")
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("create Copilot session ID: %w", err)
	}
	oldID := a.currentNativeSessionID()
	// Release the subscriptions of the session that is about to go, while it still
	// accepts session work. The runtime keeps every registered interest until its
	// own handle is released. See CP-009.
	a.releaseNativeControlEvents()
	if _, err := a.requestNativeSession("suspend", nil); err != nil {
		return "", err
	}
	opts := a.sessionLaunchOptions()
	a.forgetNativeSessionState(id.String())
	_, err = a.openSession(opts, id.String(), false, a.APITimeout())
	if err == nil {
		// A new identity starts with no confirmed setting. The restore below is what
		// fills the snapshot again, so a value the replacement refuses cannot survive
		// as the previous session's answer.
		a.stateMu.Lock()
		a.options = make(optionmap.Map)
		a.stateMu.Unlock()
		err = a.prepareNativeSession(opts.Options)
	}
	if err != nil {
		// Stop the replacement before restoring the previous session.
		// The failed operation can leave a native session even when its reply is lost.
		a.releaseNativeControlEvents()
		_, suspendErr := a.requestNativeSession("suspend", nil)
		a.forgetNativeSessionState(oldID)
		restoreErr := a.resumeNativeSessionWithoutPendingWork(opts, oldID)
		if restoreErr == nil {
			restoreErr = a.prepareNativeSession(opts.Options)
		}
		if restoreErr != nil {
			a.stopNativeConnection()
		}
		return "", errors.Join(err, suspendErr, restoreErr)
	}
	a.sink.UpdateSessionID(id.String())
	a.sink.ResetSpans()
	a.sink.ReportProgress(agent.ResetProgress())
	a.goalMu.Lock()
	a.goal = copilotGoalSnapshot{}
	a.goalMu.Unlock()
	a.sink.ClearGoal(false)
	return id.String(), nil
}

// restoreNativeSettings re-applies the stored options to a session and reports the
// axes the runtime refused.
//
// An axis that changes what the session DOES is fatal: a model the replacement
// refused, or a permission mode it refused, must not survive as the previous
// session's answer -- that is the whole reason this check is strict.
//
// EFFORT is not such an axis. It is a quality dial, the runtime reports it only
// through `model.getCurrent`, and an empty `reasoningEffort` there deletes the
// option outright (see refreshNativeSettings), so a fresh session that has not
// re-applied it yet reads back as "". Failing the whole operation for that
// aborted the thing the USER asked for -- clearing a goal -- over a tier nobody
// chose in that moment. It is reported and carried on from instead.
func (a *Agent) restoreNativeSettings(options optionmap.Map) error {
	applied := a.applyNativeSettings(options)
	for key, value := range options {
		if value == "" || applied.Settlements[key].State == agent.OptionSettlementConfirmed {
			continue
		}
		if !copilotRestoreIsFatal(key) {
			slog.Warn("The Copilot runtime did not restore a setting",
				"option", key, "requested", value, "surfaced", applied.SurfacedOptions[key])
			continue
		}
		return fmt.Errorf("the Copilot runtime did not restore the %s setting", key)
	}
	return nil
}

// copilotRestoreIsFatal reports whether an axis the runtime refused must fail the
// whole restore.
//
// Every axis is fatal EXCEPT effort. Model and permission mode change what the
// session does -- one picks the engine, the other decides what runs without
// asking -- so a replacement that refused either must not keep the previous
// session's answer. Effort is a quality dial with no such consequence, and the
// runtime surfaces it only through `model.getCurrent`, where an empty
// `reasoningEffort` deletes the option outright. Treating that as fatal aborted
// a goal clear the user had asked for.
func copilotRestoreIsFatal(option string) bool {
	return option != agent.OptionIDEffort
}

// runUnderNativeSession runs work while the session stays in place. A stopped process
// answers nothing, so the work does not start.
func (a *Agent) runUnderNativeSession(work func()) {
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	if a.IsStopped() {
		return
	}
	work()
}

// forgetNativeSessionState drops everything the outgoing session owns, and gives the
// agent nextSessionID. An empty nextSessionID keeps the current identity, which is
// what the goal clear needs: it opens the SAME session again.
//
// A turn the replacement inherits would latch the agent busy for good, because no idle
// event can reach a session that no longer exists. A child transcript, an open tool
// call and a pending control request belong to that session too, and its event
// subscriptions die with it.
//
// The caller holds sessionMu for writing, so no input and no setting change can reach
// the session while this runs.
func (a *Agent) forgetNativeSessionState(nextSessionID string) {
	a.setNativeTurnActive(false)
	a.outputMu.Lock()
	// Store and drop what the OUTGOING session produced before the identity moves, so
	// every row this sweep writes carries the session that produced it.
	a.clearNativeChildren()
	a.clearNativeControls()
	if nextSessionID != "" {
		a.stateMu.Lock()
		a.sessionID = nextSessionID
		a.stateMu.Unlock()
	}
	a.outputMu.Unlock()
	a.forgetNativeControlEvents()
}

// prepareNativeSession subscribes an open session to the control events and restores
// the settings the previous session carried.
//
// Every path that opens a session again needs both, in this order: the context clear,
// its own rollback, and the goal clear. The subscription comes first because a setting
// change can raise a control request, and a request that arrives before the
// subscription exists reaches no reader.
func (a *Agent) prepareNativeSession(options optionmap.Map) error {
	if err := a.registerNativeControlEvents(); err != nil {
		return err
	}
	return a.restoreNativeSettings(options)
}

func (a *Agent) currentNativeSessionID() string {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.sessionID
}

// requestNativeSession runs while the caller holds sessionMu.
func (a *Agent) requestNativeSession(method string, values map[string]any) (json.RawMessage, error) {
	return a.requestSession(a.currentNativeSessionID(), method, values, a.APITimeout())
}

package copilot

import (
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
func (a *copilotAgent) ClearContext() (string, error) {
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
func (a *copilotAgent) restoreNativeSettings(options optionmap.Map) error {
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

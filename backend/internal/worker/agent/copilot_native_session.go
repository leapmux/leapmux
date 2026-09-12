package agent

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/internal/util/optionmap"
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
	a.setNativeTurnActive(false)
	a.outputMu.Lock()
	a.stateMu.Lock()
	opts := a.opts
	opts.Options = a.options.Clone()
	a.sessionID = id.String()
	a.stateMu.Unlock()
	a.clearNativeChildren()
	a.clearNativeControls()
	a.outputMu.Unlock()
	_, err = a.openSession(opts, id.String(), false, a.APITimeout())
	if err == nil {
		err = a.registerNativeControlEvents()
	}
	if err == nil {
		a.stateMu.Lock()
		a.options = make(optionmap.Map)
		a.stateMu.Unlock()
		err = a.restoreNativeSettings(opts.Options)
	}
	if err != nil {
		// Stop the replacement before restoring the previous session.
		// The failed operation can leave a native session even when its reply is lost.
		a.releaseNativeControlEvents()
		_, suspendErr := a.requestNativeSession("suspend", nil)
		a.outputMu.Lock()
		a.clearNativeChildren()
		a.clearNativeControls()
		a.stateMu.Lock()
		a.sessionID = oldID
		a.stateMu.Unlock()
		a.outputMu.Unlock()
		config := newCopilotSessionConfig(opts, oldID, true)
		continueWork := false
		config.ContinuePendingWork = &continueWork
		_, restoreErr := a.sendNativeSessionConfig("session.resume", config, a.APITimeout())
		if restoreErr == nil {
			restoreErr = a.registerNativeControlEvents()
		}
		if restoreErr == nil {
			restoreErr = a.restoreNativeSettings(opts.Options)
		}
		if restoreErr != nil {
			a.outputMu.Lock()
			a.closing = true
			a.clearNativeChildren()
			a.clearNativeControls()
			a.outputMu.Unlock()
			a.forgetNativeControlEvents()
			a.copilotConnection.Stop()
		}
		return "", errors.Join(err, suspendErr, restoreErr)
	}
	a.sink.UpdateSessionID(id.String())
	a.sink.ResetSpans()
	a.sink.ReportProgress(ResetProgress())
	a.goalMu.Lock()
	a.goal = copilotGoalSnapshot{}
	a.goalMu.Unlock()
	a.sink.ClearGoal(false)
	return id.String(), nil
}

func (a *copilotAgent) restoreNativeSettings(options optionmap.Map) error {
	applied := a.applyNativeSettings(options)
	for key, value := range options {
		if value != "" && applied.Settlements[key].State != OptionSettlementConfirmed {
			return fmt.Errorf("the Copilot runtime did not restore the %s setting", key)
		}
	}
	return nil
}

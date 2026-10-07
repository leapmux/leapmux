package pi

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// rememberOpenDialog records a published dialog that Pi waits on.
func (a *Agent) rememberOpenDialog(id string) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.openDialogs == nil {
		a.openDialogs = make(map[string]uint64)
	}
	a.dialogRevision++
	a.openDialogs[id] = a.dialogRevision
}

// forgetOpenDialog retires a dialog after the reader answers or Pi's deadline passes.
// A LeapMux cancellation also retires the dialog.
func (a *Agent) forgetOpenDialog(id string) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	delete(a.openDialogs, id)
}

// settleOpenDialogs cancels each dialog that the stop still owns and withdraws its card.
//
// Pi 1.0.0 cancels a dialog only through the extension's abort signal.
// The rpiv-ask-user-question extension passes no signal to ui.select or ui.input.
// Pi waits for idle before it acknowledges an abort.
// The extension's tool waits on its dialog, so the agent cannot become idle.
// This cancellation releases the tool. Without it, the turn and extension wait indefinitely.
func (a *Agent) settleOpenDialogs(scope piInterruptScope, stop agent.StopContext) error {
	a.dialogCancelMu.Lock()
	defer a.dialogCancelMu.Unlock()
	a.Mu.Lock()
	if a.StoppedLocked() {
		// A stopped process answers nothing, and its own path withdraws the cards.
		a.Mu.Unlock()
		return nil
	}
	type dialog struct {
		id       string
		revision uint64
	}
	ids := make([]dialog, 0, len(a.openDialogs))
	for id, revision := range a.openDialogs {
		ids = append(ids, dialog{id, revision})
	}
	a.Mu.Unlock()
	// Answer in a stable order, so one interrupt always writes the same lines.
	slices.SortFunc(ids, func(first, second dialog) int { return strings.Compare(first.id, second.id) })
	var failures []error
	for _, entry := range ids {
		a.Mu.Lock()
		current := a.piInterruptScopeCurrentLocked(scope)
		open := a.openDialogs[entry.id] == entry.revision
		a.Mu.Unlock()
		if !current {
			break
		}
		if !open {
			continue
		}
		if err := a.cancelOpenDialog(entry.id, stop); err != nil {
			failures = append(failures, err)
			continue
		}
		a.Mu.Lock()
		retired := a.openDialogs[entry.id] == entry.revision
		if retired {
			delete(a.openDialogs, entry.id)
			if question := a.questionDialogs[entry.id]; question != nil {
				delete(a.customQuestionAnswers, question.Key)
				delete(a.questionDialogs, entry.id)
			}
		}
		a.Mu.Unlock()
		if retired {
			a.dialogDeadlines.Disarm(entry.id)
			a.sink.CancelControlRequest(entry.id)
		}
	}
	return errors.Join(failures...)
}

// cancelOpenDialog answers one dialog with a cancellation. It writes to the process
// directly, because SendRawInput reads a response as the reader's own answer to a
// question.
func (a *Agent) cancelOpenDialog(id string, stop agent.StopContext) error {
	response, err := json.Marshal(map[string]any{"type": contracts.PiEventExtensionUIResponse, "id": id, "cancelled": true})
	if err != nil {
		return fmt.Errorf("encode the Pi cancellation for dialog %s: %w", id, err)
	}
	if err := a.Process.SendRawInput(response, stop); err != nil {
		return fmt.Errorf("send the Pi cancellation for dialog %s: %w", id, err)
	}
	return nil
}

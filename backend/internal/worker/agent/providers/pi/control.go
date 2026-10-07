package pi

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
)

// rememberOpenDialog records a published dialog that Pi waits on.
func (a *Agent) rememberOpenDialog(id string) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.openDialogs == nil {
		a.openDialogs = make(map[string]struct{})
	}
	a.openDialogs[id] = struct{}{}
}

// forgetOpenDialog drops a dialog that Pi no longer waits on: the reader answered it,
// Pi answered it at its own deadline, or LeapMux cancelled it.
func (a *Agent) forgetOpenDialog(id string) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	delete(a.openDialogs, id)
}

// settleOpenDialogs answers each dialog that Pi still waits on with a cancellation,
// and withdraws its card.
//
// Interrupt calls it. Pi's own abort settles no dialog: the RPC host of Pi 1.0.0
// settles a dialog at an abort only through the signal that the extension passes
// with it, and the rpiv-ask-user-question extension passes none to `ui.select` and
// `ui.input`. Pi's abort then waits until the agent is idle, and the agent waits on
// the extension's tool, which waits on the dialog. Without this answer the turn never
// ends, and the extension waits for ever.
func (a *Agent) settleOpenDialogs() error {
	a.dialogCancelMu.Lock()
	defer a.dialogCancelMu.Unlock()
	a.Mu.Lock()
	if a.StoppedLocked() {
		// A stopped process answers nothing, and its own path withdraws the cards.
		a.Mu.Unlock()
		return nil
	}
	ids := make([]string, 0, len(a.openDialogs))
	for id := range a.openDialogs {
		ids = append(ids, id)
	}
	a.Mu.Unlock()
	// Answer in a stable order, so one interrupt always writes the same lines.
	slices.Sort(ids)
	var failures []error
	for _, id := range ids {
		if err := a.cancelOpenDialog(id); err != nil {
			failures = append(failures, err)
			continue
		}
		a.dialogDeadlines.Disarm(id)
		a.forgetOpenDialog(id)
		a.sink.CancelControlRequest(id)
	}
	return errors.Join(failures...)
}

// cancelOpenDialog answers one dialog with a cancellation. It writes to the process
// directly, because SendRawInput reads a response as the reader's own answer to a
// question.
func (a *Agent) cancelOpenDialog(id string) error {
	response, err := json.Marshal(map[string]any{"type": contracts.PiEventExtensionUIResponse, "id": id, "cancelled": true})
	if err != nil {
		return fmt.Errorf("encode the Pi cancellation for dialog %s: %w", id, err)
	}
	if err := a.Process.SendRawInput(response); err != nil {
		return fmt.Errorf("send the Pi cancellation for dialog %s: %w", id, err)
	}
	return nil
}

package agent

import (
	"encoding/json"
	"log/slog"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
)

// A "Start fresh and implement" answer does not start the fresh session by
// itself. Pi's plan extension follows the plan-ready dialog with a second
// one -- "Fresh implementation settings" -- that only picks one-shot model and
// thinking defaults before the extension replaces the session and transfers
// the plan. LeapMux's approval UI already collected the whole decision, so the
// worker answers the settings dialog itself with the extension's defaults and
// lets the extension run its native handoff (new session, transferred plan,
// implementation kickoff). The worker then observes the replacement session the
// same way it observes any extension-driven session change.

// notePiPlanFreshApproval records that a plan menu was answered with the
// fresh-implementation choice, so the settings dialog that follows can be
// answered by the worker. Guarded by a.mu.
func (a *PiAgent) notePiPlanFreshApproval() {
	a.mu.Lock()
	a.freshImplementationPending = true
	a.mu.Unlock()
}

// answerPiFreshSettingsDialog answers the extension's follow-up settings dialog
// when a fresh implementation is pending. It reports whether the dialog was
// answered, meaning the caller must not publish it to the UI.
func (a *PiAgent) answerPiFreshSettingsDialog(id string, raw []byte) bool {
	var dialog piQuestionDialog
	if json.Unmarshal(raw, &dialog) != nil {
		return false
	}
	a.mu.Lock()
	// Consume the mark only when this really is the settings dialog, or a
	// later unrelated dialog would silently drop the auto-answer.
	match := a.freshImplementationPending &&
		dialog.Method == contracts.PiDialogMethodSelect &&
		piDialogTitleFirstLine(dialog.Title) == PiPlanDialogFreshSettingsTitle &&
		slices.Contains(dialog.Options, PiPlanActionStartFresh)
	if match {
		a.freshImplementationPending = false
	}
	a.mu.Unlock()
	if !match {
		return false
	}
	response, err := json.Marshal(map[string]any{
		"type":  contracts.PiEventExtensionUIResponse,
		"id":    id,
		"value": PiPlanActionStartFresh,
	})
	if err == nil {
		err = a.processBase.SendRawInput(response)
	}
	if err != nil {
		// The extension still waits on the dialog, so publish it and let the
		// user answer manually instead of leaving Pi blocked.
		slog.Warn("answer pi fresh implementation settings", "agent_id", a.agentID, "request_id", id, "error", err)
		return false
	}
	slog.Info("answered pi fresh implementation settings", "agent_id", a.agentID, "request_id", id)
	return true
}

// piDialogTitleFirstLine strips the descriptions a dialog title may carry under
// its heading, mirroring the plan-approval detection on the browser side.
func piDialogTitleFirstLine(title string) string {
	line, _, _ := strings.Cut(title, "\n")
	return strings.TrimSuffix(line, "\r")
}

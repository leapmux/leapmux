package muse

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// onceApprovalReply grants one offered requirement without creating a persistent rule.
func onceApprovalReply(params controlParams) map[string]any {
	if params.ApprovalID == "" || params.UserInputID != "" {
		return nil
	}
	if _, err := approvalControlID(params); err != nil {
		return nil
	}
	choices, ok := readApprovalChoices(params.Choices)
	if !ok {
		return nil
	}
	choiceID := ""
	for _, choice := range choices {
		if choice.Decision != contracts.MuseApprovalDecisionApproved || choice.Scope != contracts.MuseChoiceScopeOnce {
			continue
		}
		if choiceID != "" {
			return nil
		}
		choiceID = choice.ID
	}
	if choiceID == "" {
		return nil
	}
	return map[string]any{"sessionId": params.SessionID, "approvalId": params.ApprovalID, "requirementId": slices.Clone(params.Requirement), "choiceId": choiceID}
}

// The settings reply confirms the configured root session only.
// Child sessions retain their own native permission policy.
func (a *Agent) canBypassSessionLocked(id string, state *sessionState) bool {
	return a.connection != nil && id == a.sessionID && a.currentSessionState(id, state) &&
		a.settings[agent.OptionIDPermissionMode] == contracts.MuseApprovalModeAllowAll && !a.IsStopped()
}

func (a *Agent) queuePermissionApprovals() {
	a.stateMu.Lock()
	needed := false
	rootID, root := a.sessionID, a.sessions[a.sessionID]
	rootBypass := a.canBypassSessionLocked(rootID, root)
	for sessionID, state := range a.sessions {
		if !a.currentSessionState(sessionID, state) {
			continue
		}
		bypass := a.canBypassSessionLocked(sessionID, state)
		for _, control := range state.controls {
			if !control.responding && (bypass && control.automaticReply != nil && !control.automaticFailed || !control.published && len(control.raw) > 0) {
				needed = true
			}
		}
	}
	a.stateMu.Unlock()
	if !needed && rootBypass {
		a.controlMu.Lock()
		for _, record := range a.controlQueue {
			if record.kind == controlPublish && record.state == root && record.sessionID == rootID && record.automaticReply != nil && !record.forceManual {
				needed = true
				break
			}
		}
		a.controlMu.Unlock()
	}
	if !needed {
		return
	}
	a.approvalWork.Run("permissions", func() {
		a.drainControls()
		a.sendMu.Lock()
		a.approvePendingPermissions()
		a.sendMu.Unlock()
		a.drainControls()
	})
}

type pendingApproval struct {
	state     *sessionState
	sessionID string
	nativeID  string
	control   controlState
}

type queuedControlIdentity struct {
	state    *sessionState
	nativeID string
}

// approvePendingPermissions runs under sendMu. Native requirement guards stay intact.
func (a *Agent) approvePendingPermissions() {
	a.controlMu.Lock()
	queued := make(map[queuedControlIdentity]controlState)
	for _, record := range a.controlQueue {
		if record.kind == controlPublish {
			queued[queuedControlIdentity{record.state, record.nativeID}] = controlState{key: record.key, fingerprint: record.fingerprint}
		}
	}
	a.controlMu.Unlock()
	a.stateMu.Lock()
	var claimed []pendingApproval
	var publish []*controlRecord
	for sessionID, state := range a.sessions {
		if !a.currentSessionState(sessionID, state) {
			continue
		}
		bypass := a.canBypassSessionLocked(sessionID, state)
		for id, control := range state.controls {
			if control.responding {
				continue
			}
			if newer, exists := queued[queuedControlIdentity{state, id}]; exists && (newer.key != control.key || newer.fingerprint != control.fingerprint) {
				continue
			}
			if bypass && control.automaticReply != nil && !control.automaticFailed {
				control.responding = true
				state.controls[id] = control
				claimed = append(claimed, pendingApproval{state: state, sessionID: sessionID, nativeID: id, control: control})
			} else if !control.published && len(control.raw) > 0 {
				publish = append(publish, manualControlPublication(state, sessionID, id, control))
			}
		}
	}
	a.stateMu.Unlock()
	for _, record := range publish {
		a.enqueueControl(record)
	}
	for _, approval := range claimed {
		a.stateMu.Lock()
		current := a.currentSessionState(approval.sessionID, approval.state) && approval.state.controls[approval.nativeID].key == approval.control.key &&
			approval.state.controls[approval.nativeID].fingerprint == approval.control.fingerprint && approval.state.controls[approval.nativeID].responding
		a.stateMu.Unlock()
		if !current {
			continue
		}
		_, err := a.command(methodApprovalDecide, approval.control.automaticReply, a.APITimeout(), nil)
		if err != nil {
			err = providerkit.ClassifyJSONRPCDeliveryError("Muse permission answer", err)
		}
		a.finishControlDecision(approval.state, approval.sessionID, approval.nativeID, approval.control, err, true)
		if err != nil {
			slog.Warn("Muse could not approve a pending requirement", "request_id", approval.control.key, "error", err)
		}
	}
}

func manualControlPublication(state *sessionState, sessionID, nativeID string, control controlState) *controlRecord {
	return &controlRecord{kind: controlPublish, state: state, sessionID: sessionID, nativeID: nativeID, key: control.key,
		fingerprint: control.fingerprint, raw: slices.Clone(control.raw), automaticReply: control.automaticReply,
		forceManual: true, publicationReady: make(chan struct{})}
}

func (a *Agent) finishControlDecision(state *sessionState, sessionID, nativeID string, claimed controlState, err error, automatic bool) {
	if err == nil || errors.Is(err, agent.ErrDeliveryUncertain) {
		return
	}
	a.stateMu.Lock()
	current := state.controls[nativeID]
	var publish *controlRecord
	if a.currentSessionState(sessionID, state) && current.key == claimed.key && current.fingerprint == claimed.fingerprint {
		current.responding = false
		current.automaticFailed = current.automaticFailed || automatic
		state.controls[nativeID] = current
		if !current.published {
			publish = manualControlPublication(state, sessionID, nativeID, current)
		}
	}
	a.stateMu.Unlock()
	if publish != nil {
		a.enqueueControl(publish)
	}
}

func (a *Agent) awaitControlPublication(state *sessionState, nativeID, key string) error {
	a.controlMu.Lock()
	var publication *controlRecord
	for index := len(a.controlQueue) - 1; index >= 0; index-- {
		record := a.controlQueue[index]
		if record.kind == controlPublish && record.state == state && record.nativeID == nativeID {
			publication = record
			break
		}
	}
	a.controlMu.Unlock()
	if publication == nil {
		return nil
	}
	if publication.key != key {
		return fmt.Errorf("the Muse control requirement is no longer current")
	}
	if publication.publicationReady == nil {
		return nil
	}
	wait := func(timeout <-chan time.Time) error {
		select {
		case <-publication.publicationReady:
			return nil
		case <-a.Context().Done():
			return a.Context().Err()
		case <-a.ProcessDone():
			return a.ProcessExitError()
		case <-timeout:
			return fmt.Errorf("the Muse control publication did not finish")
		}
	}
	if a.APITimeout() <= 0 {
		return wait(nil)
	}
	timer := a.Clock().NewTimer(a.APITimeout(), "muse-control-publication")
	defer timer.Stop("muse-control-publication")
	return wait(timer.C)
}

func (a *Agent) retireControlPublications(state *sessionState) {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	for _, record := range a.controlQueue {
		if record.state == state {
			record.completePublication()
		}
	}
}

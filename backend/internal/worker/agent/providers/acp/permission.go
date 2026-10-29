package acp

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// WithOptionValue claims approvals while one native option retains its value.
// The callback must not wait for delivery or acquire the settings lock again.
func (b *Base) WithOptionValue(id, value string, claim func()) {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	current := b.options.values[id]
	if id == agent.OptionIDPermissionMode {
		current = b.permissionMode
	}
	if !b.StoppedLocked() && value != "" && current == value {
		claim()
	}
}

func (b *Base) approvePendingPermissions() {
	if b.hooks.WithBypassPermission != nil {
		b.hooks.WithBypassPermission(func() { b.ApproveOutstandingControlRequests(b.sink, nil) })
	}
}

// PublishSessionControlRequest checks ownership before publication or automatic approval.
// Foreign and retired sessions receive a refusal because their work appears in no transcript.
// Accepted requests retain their owning session. The Worker accepts answers only in that session.
// Streamed text is stored first so the reader sees it before the request.
// The provider observer runs before publication, so a later withdrawal finds the registered request.
func (b *Base) PublishSessionControlRequest(line *providerkit.ParsedLine, cancelAnswer any) {
	sessionID := controlRequestSessionID(line.Params)
	if !b.ServesSession(sessionID) {
		b.refuseControlRequest(line, sessionID, cancelAnswer)
		return
	}
	b.storeTextBeforeControlRequest(sessionID)
	if b.hooks.ControlRequestObserver != nil {
		b.hooks.ControlRequestObserver(line)
	}
	// A provider that can answer its own control request does so here, before
	// the bypass branch. After a delivery failure the published card keeps the
	// reject answer, and the bypass sweep can send it later. This is consistent
	// with LeapMux's own decision.
	if b.hooks.AnswerControlRequest != nil {
		if answer, ok := b.hooks.AnswerControlRequest(line); ok {
			ownerSessionID := b.agentSessionOfControlRequest(sessionID)
			queued := b.ApproveUnpublishedSessionControlRequest(b.sink, ownerSessionID, line.Raw, cancelAnswer, answer)
			if b.hooks.ControlRequestAnswered != nil {
				b.hooks.ControlRequestAnswered(line, queued)
			}
			if queued {
				return
			}
			publication := b.PublishSessionControlRequestWithApproval(b.sink, ownerSessionID, line.Raw, cancelAnswer, nil)
			b.bindURLElicitation(line, publication)
			return
		}
	}
	ownerSessionID := b.agentSessionOfControlRequest(sessionID)
	var answer any
	if line.Method == acpMethodSessionRequestPermission && b.hooks.WithBypassPermission != nil &&
		b.hooks.PermissionAllowsBypass != nil && b.hooks.PermissionAllowsBypass(line) {
		answer = permissionApprovalAnswer(line.Params)
	}
	if answer == nil {
		publication := b.PublishSessionControlRequestWithApproval(b.sink, ownerSessionID, line.Raw, cancelAnswer, nil)
		b.bindURLElicitation(line, publication)
		return
	}
	queued := false
	b.hooks.WithBypassPermission(func() {
		queued = b.ApproveUnpublishedSessionControlRequest(b.sink, ownerSessionID, line.Raw, cancelAnswer, answer)
	})
	if queued {
		return
	}
	publication := b.PublishSessionControlRequestWithApproval(b.sink, ownerSessionID, line.Raw, cancelAnswer, answer)
	// A mode change can finish between the first check and request registration.
	b.hooks.WithBypassPermission(func() { b.ApproveOutstandingControlRequest(b.sink, publication) })
}

// permissionApprovalAnswer selects one offered approval without granting a lasting rule.
// Ambiguous or absent options require a manual decision.
func permissionApprovalAnswer(params json.RawMessage) any {
	var request struct {
		Options []struct {
			ID   string `json:"optionId"`
			Kind string `json:"kind"`
		} `json:"options"`
	}
	if json.Unmarshal(params, &request) != nil {
		return nil
	}
	selected := ""
	ids := make(map[string]bool, len(request.Options))
	for _, option := range request.Options {
		if option.ID == "" || ids[option.ID] {
			return nil
		}
		ids[option.ID] = true
		if option.Kind == contracts.ACPPermissionKindAllowOnce {
			if selected != "" {
				return nil
			}
			selected = option.ID
		}
	}
	if selected == "" {
		return nil
	}
	return map[string]any{"outcome": map[string]string{"outcome": contracts.ACPPermissionOutcomeSelected, "optionId": selected}}
}

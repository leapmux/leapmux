package acp

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// WithOptionValue claims approvals while the effective option retains its value.
// The callback must not wait for delivery or acquire the settings lock again.
func (b *Base) WithOptionValue(id, value string, claim func()) {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	current := b.options.values[id]
	if deferred, exists := b.options.deferredWrites[id]; exists {
		current = deferred
	}
	if id == agent.OptionIDPermissionMode {
		current = b.permissionMode
	}
	if !b.StoppedLocked() && value != "" && current == value {
		claim()
	}
}

func (b *Base) approvePendingPermissions() {
	if b.hooks.WithBypassPermission != nil {
		b.hooks.WithBypassPermission(func() { b.ApproveOutstandingControlRequests(nil) })
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
	needsTurn := cancelAnswer != nil || !b.hooks.AnswerlessControlsOutliveTurns
	var sink agent.ControlServices = b.sink
	owner := providerkit.ControlRequestOwner{SessionID: b.agentSessionOfControlRequest(sessionID)}
	if b.hooks.ControlOwner != nil {
		resolved, err := b.hooks.ControlOwner(sessionID, line, needsTurn)
		if err != nil {
			slog.Warn("resolve the control request owner", "agent_id", b.AgentID(), "session_id", sessionID, "error", err)
			b.replyToRefusedControlRequest(line, cancelAnswer, err.Error())
			return
		}
		if resolved.SessionID != "" && resolved.SessionID != sessionID {
			if !b.ServesSession(resolved.SessionID) {
				b.refuseControlRequest(line, resolved.SessionID, cancelAnswer)
				return
			}
			sessionID = resolved.SessionID
			owner.SessionID = b.agentSessionOfControlRequest(sessionID)
		}
		if resolved.Refuse {
			b.refuseControlRequest(line, sessionID, cancelAnswer)
			return
		}
		if resolved.Sink != nil {
			sink = resolved.Sink
			if sessionID != "" {
				owner.SessionID = sessionID
			}
		}
		if needsTurn && resolved.TurnID != "" {
			owner.TurnID = resolved.TurnID
			owner.InputIDs = resolved.InputIDs
		}
	}
	if needsTurn && owner.TurnID == "" {
		owner.TurnID = b.controlRequestTurnID(sessionID, line)
	}
	b.storeTextBeforeControlRequest(sessionID)
	if b.hooks.ControlRequestObserver != nil {
		b.hooks.ControlRequestObserver(sessionID, line)
	}
	var answer any
	if line.Method == acpMethodSessionRequestPermission && b.hooks.WithBypassPermission != nil &&
		b.hooks.PermissionAllowsBypass != nil && b.hooks.PermissionAllowsBypass(line) {
		answer = permissionApprovalAnswer(line.Params)
	}
	if answer == nil {
		publication := b.PublishControlRequest(sink, providerkit.ControlRequestPublication{
			Owner: owner, Content: line.Raw, CancelAnswer: cancelAnswer,
		})
		b.bindURLElicitation(line, publication)
		return
	}
	request := providerkit.ControlRequestPublication{
		Owner: owner, Content: line.Raw, CancelAnswer: cancelAnswer, ApproveAnswer: answer,
	}
	queued := false
	b.hooks.WithBypassPermission(func() {
		queued = b.ApproveUnpublishedControlRequest(sink, request)
	})
	if queued {
		return
	}
	publication := b.PublishControlRequest(sink, request)
	// A mode change can finish between the first check and request registration.
	b.hooks.WithBypassPermission(func() { b.ApproveOutstandingControlRequest(publication) })
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

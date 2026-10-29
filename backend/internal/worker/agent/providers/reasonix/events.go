package reasonix

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const reasonixMethodStatusUpdate = "_reasonix.io/session/status_update"

const reasonixMethodMCPStatusUpdate = "_reasonix.io/session/mcpStatus_update"

// handleExtraMethod handles Reasonix session status and MCP health notifications.
func (a *Agent) handleExtraMethod(line *providerkit.ParsedLine) bool {
	switch line.Method {
	case reasonixMethodStatusUpdate:
		if line.HasID() {
			return false
		}
		var source struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(line.Params, &source); err != nil {
			slog.Warn("reasonix status_update unmarshal failed", "agent_id", a.AgentID(), "error", err)
			return true
		}
		if source.SessionID == "" {
			slog.Warn("reasonix status_update has no session ID", "agent_id", a.AgentID())
			return true
		}
		return a.RouteSessionNotification(acp.SessionNotification{
			Raw:            line.Params,
			ResolveSession: func(json.RawMessage) (string, bool) { return source.SessionID, true },
			Dispatch:       func(_ string, raw json.RawMessage) { a.handleReasonixStatusUpdate(raw) },
		})
	case reasonixMethodMCPStatusUpdate:
		// The status subscription includes MCP health. It carries no conversation output.
		return !line.HasID()
	default:
		return false
	}
}

type reasonixStatusUpdate struct {
	SessionID string                 `json:"sessionId"`
	Sequence  uint64                 `json:"sequence"`
	Event     string                 `json:"event"`
	Status    *reasonixSessionStatus `json:"status"`
}

// reasonixSessionStatus is the flat status RPC result and the payload inside a status notification.
type reasonixSessionStatus struct {
	SessionID string        `json:"sessionId"`
	Sequence  uint64        `json:"sequence"`
	Mode      string        `json:"mode"`
	Goal      *reasonixGoal `json:"goal"`
	// Phase is the live state of the turn (`implementing`, `waiting_permission`,
	// `review_ready`, ...). It is one word, not a sentence, so the row reads it
	// through a table rather than printing the token.
	Phase string `json:"phase"`
	// Usage supplies cumulative cost. Standard ACP usage updates own the context gauge.
	Usage *struct {
		Cumulative *reasonixUsage `json:"cumulative"`
	} `json:"usage"`
	// TurnOutcome states how the turn ended. The update of an `error` event
	// classifies the failure in it (see observeReasonixFailure).
	TurnOutcome *reasonixTurnOutcome `json:"turnOutcome"`
}

// reasonixUsage holds the cumulative cost. ACP usage_update owns the current context gauge.
type reasonixUsage struct {
	EstimatedCost *float64 `json:"estimatedCost"`
	Currency      string   `json:"currency"`
}

type reasonixStatusCursor struct {
	sessionID string
	sequence  uint64
}

// The caller holds goalStatusMu. Equal sequences can reveal an objective after the starting status.
func (a *Agent) acceptReasonixStatusSequenceLocked(sessionID string, sequence uint64) bool {
	if a.statusCursor.sessionID == sessionID && sequence < a.statusCursor.sequence {
		return false
	}
	a.statusCursor = reasonixStatusCursor{sessionID: sessionID, sequence: sequence}
	return true
}

func (a *Agent) handleReasonixStatusUpdate(params json.RawMessage) {
	if len(params) == 0 {
		return
	}
	var update reasonixStatusUpdate
	if err := json.Unmarshal(params, &update); err != nil {
		slog.Warn("reasonix status_update unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	if !a.applyReasonixStatusGoal(update.SessionID, update.Sequence, update.Status) {
		return
	}
	// The session and sequence checks above also keep the failure of an old
	// session's turn, and a stale restatement, out of the record.
	a.observeReasonixFailure(update, params)
	if status := update.Status; status != nil {
		a.reportReasonixUsage(status.Usage)
		a.reportReasonixPhase(status.Phase)
	}
}

// The phase words Reasonix reports that a reader must see, and the sentence each
// one becomes.
//
// A phase is one wire token, so a row that printed it would show
// `waiting_permission`. Only the phases that state something a reader can ACT on
// are here: the working phases (`starting`, `implementing`) say what every other
// surface in the app already says, and repeating them would put a row in the
// transcript for each step of every turn.
var reasonixPhaseLabels = map[string]string{
	"checking_readiness": "Checking whether the work is ready",
	"readiness_paused":   "Paused: the work is not ready",
	"review_ready":       "Ready for review",
	"paused":             "Paused",
}

// reportReasonixPhase states one phase change, once.
//
// Reasonix restates its WHOLE status on every change, so the same phase arrives
// many times per turn. Only a move to a new phase says anything.
func (a *Agent) reportReasonixPhase(phase string) {
	if phase == "" || phase == a.lastStatusPhase {
		return
	}
	a.lastStatusPhase = phase
	label, ok := reasonixPhaseLabels[phase]
	if !ok {
		return
	}
	a.Sink().PersistLeapMuxNotification(map[string]interface{}{
		contracts.NotificationFieldType: contracts.NotificationTypeAgentStatus,
		contracts.NotificationFieldText: label,
	})
}

// reportReasonixUsage broadcasts a cumulative cost stated in US dollars.
//
// Ephemeral, like every other provider's usage report: the meter reads it live
// and the transcript holds no row for it.
func (a *Agent) reportReasonixUsage(usage *struct {
	Cumulative *reasonixUsage `json:"cumulative"`
}) {
	if usage == nil || usage.Cumulative == nil {
		return
	}
	total := usage.Cumulative
	info := map[string]interface{}{}
	// The CUMULATIVE cost, which is a total rather than a delta: a restatement
	// carries the same number and cannot double it.
	if total.EstimatedCost != nil && *total.EstimatedCost >= 0 && total.Currency == "USD" {
		info[contracts.SessionInfoKeyTotalCostUsd] = *total.EstimatedCost
	}
	if len(info) > 0 {
		a.Sink().BroadcastSessionInfo(info)
	}
}

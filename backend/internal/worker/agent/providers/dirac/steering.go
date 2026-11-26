package dirac

import (
	"encoding/json"
	"log/slog"
	"maps"
	"slices"
	"sync"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

var _ agent.SteerEchoReporter = (*Agent)(nil)

// SteerRowAtEcho keeps a Dirac whisper in AWAITING_ECHO until Dirac emits
// `dev.dirac/steering_status` with status `sent` at model injection.
func (*Agent) SteerRowAtEcho() bool { return true }

type diracSteerState struct {
	mu          sync.Mutex
	unassigned  []*diracPendingSteer
	byMessageID map[string]*diracPendingSteer
}

type diracPendingSteer struct {
	sessionID  string
	resolution agent.SteerResolution
}

func (a *Agent) registerDiracSteer(sessionID string, request agent.SteerRequest) *diracPendingSteer {
	if request.InputID == "" || request.AttemptID == "" {
		return nil
	}
	steer := &diracPendingSteer{
		sessionID: sessionID,
		resolution: agent.SteerResolution{
			InputID:   request.InputID,
			AttemptID: request.AttemptID,
		},
	}
	a.steers.mu.Lock()
	a.steers.unassigned = append(a.steers.unassigned, steer)
	a.steers.mu.Unlock()
	return steer
}

func (a *Agent) cancelDiracSteer(steer *diracPendingSteer) bool {
	if steer == nil {
		return false
	}
	a.steers.mu.Lock()
	defer a.steers.mu.Unlock()
	for i, candidate := range a.steers.unassigned {
		if candidate == steer {
			a.steers.unassigned = append(a.steers.unassigned[:i], a.steers.unassigned[i+1:]...)
			return true
		}
	}
	for id, candidate := range a.steers.byMessageID {
		if candidate == steer {
			delete(a.steers.byMessageID, id)
			return true
		}
	}
	return false
}

func (a *Agent) popUnassignedDiracSteerLocked(sessionID string) *diracPendingSteer {
	for i, candidate := range a.steers.unassigned {
		if candidate.sessionID == sessionID {
			a.steers.unassigned = append(a.steers.unassigned[:i], a.steers.unassigned[i+1:]...)
			return candidate
		}
	}
	return nil
}

func (a *Agent) handleSteeringStatus(params json.RawMessage) {
	var payload struct {
		SessionID         string `json:"sessionId"`
		SteeringMessageID string `json:"steeringMessageId"`
		Status            string `json:"status"`
		Reason            string `json:"reason"`
	}
	if json.Unmarshal(params, &payload) != nil {
		return
	}
	slog.Debug("dirac extension notification", "agent_id", a.AgentID(), "method", "dev.dirac/steering_status",
		"status", payload.Status, "steering_message_id", payload.SteeringMessageID, "reason", payload.Reason)
	if payload.SessionID != "" && !a.IsCurrentSession(payload.SessionID) {
		return
	}
	sessionID := payload.SessionID
	if sessionID == "" {
		sessionID = a.CurrentSessionID()
	}
	if sessionID == "" || payload.SteeringMessageID == "" {
		return
	}

	switch payload.Status {
	case "queued":
		a.steers.mu.Lock()
		if steer := a.popUnassignedDiracSteerLocked(sessionID); steer != nil {
			if a.steers.byMessageID == nil {
				a.steers.byMessageID = make(map[string]*diracPendingSteer)
			}
			a.steers.byMessageID[payload.SteeringMessageID] = steer
		}
		a.steers.mu.Unlock()
	case "sent":
		steer := a.takeDiracSteer(sessionID, payload.SteeringMessageID)
		if steer == nil {
			return
		}
		a.FlushBufferedOutput(agent.MessageCompletionComplete)
		report := steer.resolution
		report.Outcome = agent.SteerOutcomeEchoed
		a.RetainSteerWrite("record Dirac steer outcome", a.CaptureRootSteer(sessionID, payload.SteeringMessageID, report))
	case "":
	default:
		steer := a.takeDiracSteer(sessionID, payload.SteeringMessageID)
		if steer == nil {
			return
		}
		report := steer.resolution
		report.Outcome = agent.SteerOutcomeDropped
		a.RetainSteerWrite("record Dirac steer outcome", a.Sink().CaptureSteerResolution(report))
	}
}

func (a *Agent) takeDiracSteer(sessionID, messageID string) *diracPendingSteer {
	a.steers.mu.Lock()
	defer a.steers.mu.Unlock()
	steer := a.steers.byMessageID[messageID]
	if steer != nil {
		delete(a.steers.byMessageID, messageID)
		if steer.sessionID != sessionID {
			return nil
		}
		return steer
	}
	return a.popUnassignedDiracSteerLocked(sessionID)
}

func (a *Agent) resolvePendingDiracSteers(outcome agent.SteerOutcome) {
	a.steers.mu.Lock()
	pending := a.steers.unassigned
	a.steers.unassigned = nil
	byID := a.steers.byMessageID
	a.steers.byMessageID = nil
	a.steers.mu.Unlock()

	for _, id := range slices.Sorted(maps.Keys(byID)) {
		pending = append(pending, byID[id])
	}
	for _, steer := range pending {
		report := steer.resolution
		report.Outcome = outcome
		a.RetainSteerWrite("record Dirac steer outcome", a.Sink().CaptureSteerResolution(report))
	}
}

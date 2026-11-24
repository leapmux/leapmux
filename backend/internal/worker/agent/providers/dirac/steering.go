package dirac

import (
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
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

func (a *Agent) cancelDiracSteer(steer *diracPendingSteer) {
	if steer == nil {
		return
	}
	a.steers.mu.Lock()
	defer a.steers.mu.Unlock()
	for i, candidate := range a.steers.unassigned {
		if candidate == steer {
			a.steers.unassigned = append(a.steers.unassigned[:i], a.steers.unassigned[i+1:]...)
			return
		}
	}
	for id, candidate := range a.steers.byMessageID {
		if candidate == steer {
			delete(a.steers.byMessageID, id)
			return
		}
	}
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
		a.steers.mu.Lock()
		steer := a.steers.byMessageID[payload.SteeringMessageID]
		if steer != nil {
			delete(a.steers.byMessageID, payload.SteeringMessageID)
			if steer.sessionID != sessionID {
				steer = nil
			}
		} else {
			steer = a.popUnassignedDiracSteerLocked(sessionID)
		}
		a.steers.mu.Unlock()
		if steer == nil {
			return
		}
		a.FlushBufferedOutput(agent.MessageCompletionComplete)
		report := steer.resolution
		report.Outcome = agent.SteerOutcomeEchoed
		a.retainDiracSteerWrite(a.CaptureRootSteer(sessionID, payload.SteeringMessageID, report))
	}
}

func (a *Agent) handleDiracPromptEnded(error, bool) acp.PromptEnding {
	a.resolvePendingDiracSteers(agent.SteerOutcomeDropped)
	return acp.PromptEnding{}
}

func (a *Agent) resolvePendingDiracSteers(outcome agent.SteerOutcome) {
	a.steers.mu.Lock()
	pending := a.steers.unassigned
	a.steers.unassigned = nil
	for _, steer := range a.steers.byMessageID {
		pending = append(pending, steer)
	}
	a.steers.byMessageID = nil
	a.steers.mu.Unlock()

	for _, steer := range pending {
		report := steer.resolution
		report.Outcome = outcome
		a.retainDiracSteerWrite(a.Sink().CaptureSteerResolution(report))
	}
}

func (a *Agent) retainDiracSteerWrite(write func() error) {
	publish, err := agent.ReserveTranscriptSequence(a.Sink(), "record Dirac steer outcome", func(agent.TranscriptSequence) error {
		return write()
	})
	if err == nil {
		err = publish()
	}
	if err != nil && !agent.OutputRetained(err) {
		slog.Error("record Dirac steer outcome", "agent_id", a.AgentID(), "error", err)
	}
}

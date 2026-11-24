package qoder

import (
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// prepareQueuedSteer records the attempt before stdin can reach Qoder. The
// native command can start before the Worker handles the write's return.
func (a *Agent) prepareQueuedSteer(request agent.SteerRequest) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.active {
		return "", agent.ErrNoActiveTurn
	}
	if request.AttemptID == "" {
		return "", nil
	}
	if a.sessionID == "" {
		return "", fmt.Errorf("the Qoder session is not initialized")
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("create the Qoder steer command ID: %w", err)
	}
	commandID := id.String()
	if a.queuedSteers == nil {
		a.queuedSteers = make(map[string]*inputReply)
	}
	a.queuedSteers[commandID] = &inputReply{
		receipt: agent.NativeInputReceipt{SessionID: a.sessionID, InputID: commandID, TurnID: commandID},
		steer:   &agent.SteerResolution{InputID: request.InputID, AttemptID: request.AttemptID},
	}
	return commandID, nil
}

// handleQueuedSteer moves a started steer into the reply slot. Completion of
// the preceding command leaves the other steers in Qoder's queue.
func (a *Agent) handleQueuedSteer(sessionID, commandID, state string) bool {
	a.mu.Lock()
	reply := a.queuedSteers[commandID]
	if reply == nil || reply.receipt.SessionID != sessionID {
		a.mu.Unlock()
		return false
	}
	switch state {
	case commandStarted:
		delete(a.queuedSteers, commandID)
		previous := a.inputReply
		reply.started = true
		// A start before the preceding result can belong to a shared query.
		reply.incomplete = previous != nil && previous.started && previous.result == nil
		a.inputReply = reply
		a.active = true
		a.activityRev++
		a.interruptRequested = false
		a.mu.Unlock()
		a.finishUnprovedInput(previous)
		a.PublishTurnActive()
	case commandDiscarded, commandCancelled:
		delete(a.queuedSteers, commandID)
		a.mu.Unlock()
		a.resolveQueuedSteer(reply, agent.SteerOutcomeDropped)
	case commandCompleted:
		delete(a.queuedSteers, commandID)
		a.mu.Unlock()
		a.resolveQueuedSteer(reply, agent.SteerOutcomeUncertain)
	default:
		a.mu.Unlock()
	}
	return true
}

// retireQueuedSteers reports unread inputs at process exit. A closed pipe
// does not prove that Qoder never read a message.
func (a *Agent) retireQueuedSteers() {
	a.mu.Lock()
	queued := a.queuedSteers
	a.queuedSteers = nil
	a.mu.Unlock()
	for _, reply := range queued {
		a.resolveQueuedSteer(reply, agent.SteerOutcomeUncertain)
	}
}

func (a *Agent) resolveQueuedSteer(reply *inputReply, outcome agent.SteerOutcome) bool {
	resolution := *reply.steer
	resolution.Outcome = outcome
	if outcome == agent.SteerOutcomeEchoed {
		resolution.Receipt = reply.receipt
	}
	err := agent.RetainSteerResolution(a.sink, resolution)
	if !agent.OutputRetained(err) {
		slog.Warn("Qoder could not report the steer outcome", "agent_id", a.AgentID(), "input_id", resolution.InputID, "error", err)
		return false
	}
	return true
}

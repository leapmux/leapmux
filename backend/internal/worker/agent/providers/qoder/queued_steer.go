package qoder

import (
	"fmt"
	"log/slog"
	"slices"

	"github.com/google/uuid"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// prepareQueuedSteer records the attempt before stdin can reach Qoder. The
// native command can start before the Worker handles the write's return.
func (a *Agent) prepareQueuedSteer(request agent.SteerRequest) (string, *inputReply, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.active {
		return "", nil, agent.ErrNoActiveTurn
	}
	if request.AttemptID == "" {
		return "", nil, nil
	}
	if a.sessionID == "" {
		return "", nil, fmt.Errorf("the Qoder session is not initialized")
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return "", nil, fmt.Errorf("create the Qoder steer command ID: %w", err)
	}
	commandID := id.String()
	if a.queuedSteers == nil {
		a.queuedSteers = make(map[string]*inputReply)
	}
	steer := &inputReply{
		receipt: agent.NativeInputReceipt{SessionID: a.sessionID, InputID: commandID, TurnID: commandID},
		steer:   &agent.SteerResolution{InputID: request.InputID, AttemptID: request.AttemptID},
	}
	a.queuedSteers[commandID] = steer
	return commandID, steer, nil
}

// forgetQueuedSteer drops an unsent steer so exit teardown never reports it as uncertain.
func (a *Agent) forgetQueuedSteer(commandID string, steer *inputReply) {
	if commandID == "" {
		return
	}
	a.mu.Lock()
	if a.queuedSteers[commandID] == steer {
		delete(a.queuedSteers, commandID)
	}
	a.mu.Unlock()
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
	if len(queued) == 0 {
		return
	}
	commandIDs := make([]string, 0, len(queued))
	for commandID := range queued {
		commandIDs = append(commandIDs, commandID)
	}
	slices.Sort(commandIDs)
	for _, commandID := range commandIDs {
		a.resolveQueuedSteer(queued[commandID], agent.SteerOutcomeUncertain)
	}
}

func (a *Agent) resolveQueuedSteer(reply *inputReply, outcome agent.SteerOutcome) bool {
	if a.sink == nil {
		return false
	}
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

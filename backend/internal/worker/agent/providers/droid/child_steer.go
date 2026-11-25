package droid

import (
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Droid child steering.
//
// The stream JSON-RPC adapter ignores params.sessionId when it runs a turn.
// It uses the one session loaded in that process. A child send therefore uses
// a separate process that loaded the child session through droid.load_session.
// The child_session_available notification supplies its stable session id.
//
// A native background child without a bound process is busy but cannot take a
// steer through the root process. The input queue holds a send until it ends.

var _ agent.ChildSteerer = (*Agent)(nil)

// SendChildInput sends through a process loaded with the child session. A
// running native background child makes the queue hold the message.
func (a *Agent) SendChildInput(childKey string, input agent.ChildInput) (agent.ChildInputResult, error) {
	childID, err := a.registeredChildID(childKey)
	if err != nil {
		return agent.ChildInputResult{StartsTurn: true}, err
	}
	a.Mu.Lock()
	backgroundRunning := a.childTurns[childKey]
	stopped := a.stopped
	a.Mu.Unlock()
	if stopped {
		return agent.ChildInputResult{StartsTurn: true}, errAgentStopped
	}
	if backgroundRunning {
		return agent.ChildInputResult{StartsTurn: true}, agent.ErrAgentBusy
	}
	a.dispatchMu.Lock()
	if a.childAgents == nil {
		a.childAgents = make(map[string]string)
	}
	if a.childAgents[childKey] == "" {
		a.childAgents[childKey] = childID
	}
	a.dispatchMu.Unlock()
	a.pollChildTail(childKey)
	child, err := a.childConnection(childKey, childID)
	if err != nil {
		return agent.ChildInputResult{StartsTurn: true}, err
	}
	receipt, err := child.sendNativeInput(&childKey, input.Content, input.Attachments, droidInputDispatch{
		mode: droidInputOrdinary,
	})
	if err != nil {
		return agent.ChildInputResult{StartsTurn: true}, err
	}
	providerkit.LogRegistryRefusal("droid", "revive child", a.sink.ReviveBackgroundTask(childKey))
	a.finishExitedChild(childKey, child)
	return agent.ChildInputResult{StartsTurn: true, NativeReceipt: receipt}, nil
}

// SteerChildInput injects a steered input into an active bound child process
// and settles the queue item at Droid's native user echo or discard.
func (a *Agent) SteerChildInput(childKey string, request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	mode := droidInputSteer
	if request.AttemptID == "" {
		mode = droidInputChildSteer
	}
	return agent.NativeInputReceipt{}, a.steerChild(childKey, request, mode)
}

// ChildSteerPolicy holds the steer row until echo and retains accepted child
// steers until Droid echoes or explicitly discards them.
func (*Agent) ChildSteerPolicy() agent.SteerPolicy {
	return agent.SteerPolicy{RowAtEcho: true, SurvivesTurnEnd: true}
}

func (a *Agent) steerChild(childKey string, request agent.SteerRequest, mode droidInputMode) error {
	if _, err := a.registeredChildID(childKey); err != nil {
		return err
	}
	if !a.ActiveChildTurnState(childKey).Steerable {
		return agent.ErrNoActiveTurn
	}
	a.childConnMu.Lock()
	connection := a.childConns[childKey]
	a.childConnMu.Unlock()
	if connection == nil {
		return agent.ErrNoActiveTurn
	}
	<-connection.ready
	if connection.err != nil {
		return connection.err
	}
	_, err := connection.agent.sendNativeInput(&childKey, request.Content, request.Attachments, droidInputDispatch{
		mode: mode, queueInputID: request.InputID, attemptID: request.AttemptID,
	})
	return err
}

// ActiveChildTurnState reports a child's turn after a refusal. The queue reads
// it to decide whether a refused message is offered as a steer.
func (a *Agent) ActiveChildTurnState(childKey string) agent.TurnState {
	a.Mu.Lock()
	backgroundRunning := a.childTurns[childKey]
	a.Mu.Unlock()
	if backgroundRunning {
		return agent.TurnState{Active: true, Steerable: false}
	}
	a.childConnMu.Lock()
	connection := a.childConns[childKey]
	a.childConnMu.Unlock()
	if connection != nil {
		select {
		case <-connection.ready:
			if connection.agent != nil {
				connection.agent.Mu.Lock()
				active := connection.agent.turnActive
				connection.agent.Mu.Unlock()
				return agent.TurnState{Active: active, Steerable: active}
			}
		default:
			return agent.TurnState{Active: true, Steerable: false}
		}
	}
	return agent.TurnState{}
}

// armChildTurn marks a child's turn running. A repeat publishes nothing.
func (a *Agent) armChildTurn(childKey string) {
	a.Mu.Lock()
	if a.childTurns == nil {
		a.childTurns = make(map[string]bool)
	}
	if a.childTurns[childKey] {
		a.Mu.Unlock()
		return
	}
	a.childTurns[childKey] = true
	a.Mu.Unlock()
}

// disarmChildTurn marks a child's turn over. A repeat publishes nothing.
func (a *Agent) disarmChildTurn(childKey string) {
	a.Mu.Lock()
	if !a.childTurns[childKey] {
		a.Mu.Unlock()
		return
	}
	a.childTurns[childKey] = false
	a.Mu.Unlock()
}

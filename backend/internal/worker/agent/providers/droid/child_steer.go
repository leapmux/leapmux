package droid

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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

// child_session_available is the notification type that announces a child
// session. It is not yet in the droid-protocol contract table; the binary
// defines it in its notification schema.
const droidNotificationChildSessionAvailable = "child_session_available"

var _ agent.ChildSteerer = (*Agent)(nil)

// SendChildInput sends through a process loaded with the child session. A
// running native background child makes the queue hold the message.
func (a *Agent) SendChildInput(childKey, content string, attachments []*leapmuxv1.Attachment) error {
	childID, err := a.registeredChildID(childKey)
	if err != nil {
		return err
	}
	a.Mu.Lock()
	backgroundRunning := a.childTurns[childKey]
	stopped := a.stopped
	a.Mu.Unlock()
	if stopped {
		return errAgentStopped
	}
	if backgroundRunning {
		return agent.ErrAgentBusy
	}
	child, err := a.childConnection(childKey, childID)
	if err != nil {
		return err
	}
	if err := child.SendInputForSession(childKey, content, attachments); err != nil {
		return err
	}
	providerkit.LogRegistryRefusal("droid", "revive child", a.sink.ReviveBackgroundTask(childKey))
	a.resumeChildTail(childKey)
	a.finishExitedChild(childKey, child)
	return nil
}

// SteerChildInput uses only an active process already bound to the child.
func (a *Agent) SteerChildInput(childKey, content string, attachments []*leapmuxv1.Attachment) error {
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
	return connection.agent.SteerInput(content, attachments)
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

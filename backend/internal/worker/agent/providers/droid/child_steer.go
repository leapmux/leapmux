package droid

import (
	"errors"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Droid child steering.
//
// The daemon routes `droid.add_user_message` by `params.sessionId` (the binary's
// `handleAddUserMessage` claims the turn submission under
// `r.params.sessionId`), so a child session id addresses the child. The
// `child_session_available` notification announces each child as
// `{childSessionId, toolUseId, subagentType, description}`, and the registry
// row key is that `childSessionId`.
//
// A send uses queuePlacement "end_of_turn" and refuses a busy child, so the
// LeapMux input queue holds the message. A steer uses "end_of_loop" and needs a
// running turn.

// child_session_available is the notification type that announces a child
// session. It is not yet in the droid-protocol contract table; the binary
// defines it in its notification schema.
const droidNotificationChildSessionAvailable = "child_session_available"

var _ agent.ChildSteerer = (*Agent)(nil)

// SendChildInput sends a user message to a child session. The message is
// queued for the child's next turn ("end_of_turn"). A running child refuses
// with ErrAgentBusy so the LeapMux queue holds the message.
func (a *Agent) SendChildInput(childKey, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendChildMessage(childKey, content, attachments, droidQueueEndOfTurn)
}

// SteerChildInput adds a message to a child's running turn. The message is
// injected at the child's next interruption point ("end_of_loop").
func (a *Agent) SteerChildInput(childKey, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendChildMessage(childKey, content, attachments, droidQueueEndOfLoop)
}

// ActiveChildTurnState reports a child's turn after a refusal. The queue reads
// it to decide whether a refused message is offered as a steer.
func (a *Agent) ActiveChildTurnState(childKey string) agent.TurnState {
	a.Mu.Lock()
	running := a.childTurns[childKey]
	a.Mu.Unlock()
	return agent.TurnState{Active: running, Steerable: running}
}

// sendChildMessage writes one droid.add_user_message addressed to a child.
//
// The child session id IS the registry row key (the `childSessionId` of
// `child_session_available`), so the key the service resolved from the registry
// is the id the daemon routes by. A send arms the child's turn; a steer needs
// one already running.
func (a *Agent) sendChildMessage(childKey, content string, attachments []*leapmuxv1.Attachment, queuePlacement string) error {
	if childKey == "" {
		return errors.New("the child session id is empty")
	}
	text, err := buildUserText(content, attachments)
	if err != nil {
		return err
	}

	a.sendMu.Lock()
	defer a.sendMu.Unlock()

	a.Mu.Lock()
	if a.stopped {
		a.Mu.Unlock()
		return errAgentStopped
	}
	running := a.childTurns[childKey]
	a.Mu.Unlock()

	steer := queuePlacement == droidQueueEndOfLoop
	if steer {
		if !running {
			return agent.ErrNoActiveTurn
		}
	} else if running {
		return agent.ErrAgentBusy
	}

	params := addUserMessageParams{
		SessionID:      childKey,
		Text:           text,
		QueuePlacement: queuePlacement,
	}
	if !steer {
		a.armChildTurn(childKey)
	}
	if err := a.request(droidMethodAddUserMessage, params); err != nil {
		if !steer {
			a.disarmChildTurn(childKey)
		}
		return err
	}
	return nil
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

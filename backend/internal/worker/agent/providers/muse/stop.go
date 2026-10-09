package muse

import (
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

func (a *Agent) Interrupt(stop agent.StopContext) error {
	a.stateMu.Lock()
	id := a.sessionID
	state := a.sessions[id]
	turn := ""
	if state != nil {
		turn = state.turnID
	}
	a.stateMu.Unlock()
	if turn == "" {
		return nil
	}
	_, err := a.command(methodTurnInterrupt, map[string]any{"sessionId": id, "turnId": turn}, a.APITimeout(), nil)
	return err
}

func (a *Agent) SendRawInput(raw []byte, stop agent.StopContext) error {
	var message frame
	if err := json.Unmarshal(raw, &message); err != nil {
		return err
	}
	if message.Method == methodTurnInterrupt {
		return a.Interrupt(stop)
	}
	switch message.Method {
	case methodApprovalDecide, methodUserInputAnswer, methodUserInputCancel:
		a.sendMu.Lock()
		defer a.sendMu.Unlock()
		var params map[string]any
		if json.Unmarshal(message.Params, &params) != nil {
			return fmt.Errorf("the Muse control answer is invalid")
		}
		var identity controlParams
		if json.Unmarshal(message.Params, &identity) != nil {
			return fmt.Errorf("the Muse control identity is invalid")
		}
		if message.Method == methodApprovalDecide {
			identity.Requirement = json.RawMessage(paramsRawField(message.Params, "requirementId"))
		}
		key, err := controlID(identity)
		if err != nil {
			return err
		}
		var supplied string
		if json.Unmarshal(message.ID, &supplied) != nil || supplied != key {
			return fmt.Errorf("the Muse control answer belongs to another requirement")
		}
		nativeID := identity.ApprovalID
		if nativeID == "" {
			nativeID = identity.UserInputID
		}
		a.stateMu.Lock()
		state := a.sessions[identity.SessionID]
		current := state != nil && (state.childID != "" || identity.SessionID == a.sessionID) && state.controls[nativeID].key == key
		a.stateMu.Unlock()
		if !current {
			return fmt.Errorf("the Muse control requirement is no longer current")
		}
		_, err = a.command(message.Method, params, a.APITimeout(), nil)
		return err
	default:
		return fmt.Errorf("the Muse raw input method %q is unsupported", message.Method)
	}
}

func paramsRawField(raw []byte, key string) []byte {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	return fields[key]
}

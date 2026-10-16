package muse

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
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
		var params map[string]any
		decoder := json.NewDecoder(bytes.NewReader(message.Params))
		decoder.UseNumber()
		if decoder.Decode(&params) != nil {
			return fmt.Errorf("the Muse control answer is invalid")
		}
		var identity controlParams
		if json.Unmarshal(message.Params, &identity) != nil {
			return fmt.Errorf("the Muse control identity is invalid")
		}
		if message.Method == methodApprovalDecide {
			if identity.ApprovalID == "" || identity.UserInputID != "" {
				return fmt.Errorf("the Muse approval answer has an invalid identity")
			}
			identity.Requirement = json.RawMessage(paramsRawField(message.Params, "requirementId"))
		} else if identity.UserInputID == "" || identity.ApprovalID != "" {
			return fmt.Errorf("the Muse question answer has an invalid identity")
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
		a.stateMu.Unlock()
		if err := a.awaitControlPublication(state, nativeID, key); err != nil {
			return err
		}
		a.sendMu.Lock()
		defer func() { a.sendMu.Unlock(); a.drainControls() }()
		a.stateMu.Lock()
		control := controlState{}
		if state != nil {
			control = state.controls[nativeID]
		}
		current := a.currentSessionState(identity.SessionID, state) && control.key == key && control.published && !control.responding
		if current {
			control.responding = true
			state.controls[nativeID] = control
		}
		a.stateMu.Unlock()
		if !current {
			return fmt.Errorf("the Muse control requirement is no longer current")
		}
		_, err = a.command(message.Method, params, a.APITimeout(), nil)
		if err != nil {
			err = providerkit.ClassifyJSONRPCDeliveryError("Muse control answer", err)
		}
		a.finishControlDecision(state, identity.SessionID, nativeID, control, err, false)
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

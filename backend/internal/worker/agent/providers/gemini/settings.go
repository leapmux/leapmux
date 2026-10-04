package gemini

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// Gemini uses the same native values in its session modes and permission modes.
func geminiModes() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: contracts.GeminiModeDefault, Name: "Default", Description: "Ask before tool execution"},
		{Id: contracts.GeminiModeAutoEdit, Name: "Auto Edit", Description: "Approve file edits automatically"},
		{Id: contracts.GeminiModeYolo, Name: "YOLO", Description: "Approve every tool automatically"},
		{Id: contracts.GeminiModePlan, Name: "Plan", Description: "Plan with read-only tools"},
	}
}

// The reply observer records setter authority before the stdout reader handles
// another event. A recovered older tool record cannot replace that setting.
func (a *Agent) observeModeSetterReply(sessionID, mode string, result json.RawMessage, replyErr error, acknowledged func(string)) bool {
	if acknowledged == nil || !geminiSettingReplyIsObject(result, replyErr) {
		return false
	}
	a.modeMu.Lock()
	defer a.modeMu.Unlock()
	if sessionID != a.CurrentSessionID() {
		return false
	}
	a.modeGeneration++
	acknowledged(mode)
	return true
}

func geminiSettingReplyIsObject(result json.RawMessage, replyErr error) bool {
	var acknowledgment map[string]json.RawMessage
	return replyErr == nil && json.Unmarshal(result, &acknowledgment) == nil && acknowledgment != nil
}

// setNativeModel records the accepted native model on the reader before waiter delivery.
// WithSessionID prevents a context clear from replacing that session during the request.
func (a *Agent) setNativeModel(model string) error {
	if strings.TrimSpace(model) == "" {
		return errors.New("the Gemini model write requires a model ID")
	}
	return a.WithSessionID(func(sessionID string) error {
		if sessionID == "" {
			return errors.New("the Gemini model write has no native session")
		}
		params, err := json.Marshal(map[string]string{"sessionId": sessionID, "modelId": model})
		if err != nil {
			return err
		}
		accepted := false
		_, err = a.SendRequestObserved(acp.MethodSessionSetModel, params, a.APITimeout(), func(result json.RawMessage, replyErr error) {
			if !geminiSettingReplyIsObject(result, replyErr) || sessionID != a.CurrentSessionID() {
				return
			}
			a.SetCurrentModel(model)
			accepted = true
		})
		if err != nil {
			return err
		}
		if !accepted {
			return errors.New("the Gemini model reply did not confirm the current native session")
		}
		return nil
	})
}

func (a *Agent) setNativePermissionMode(mode string, acknowledged func(string)) error {
	if acknowledged == nil {
		return errors.New("the Gemini mode writer requires an acknowledgment callback")
	}
	offered := false
	for _, option := range a.AvailableModes() {
		if option.GetId() == mode {
			offered = true
			break
		}
	}
	if !offered || mode == "" {
		return fmt.Errorf("the Gemini session does not offer mode %q", mode)
	}
	if a.CurrentSessionID() == "" {
		return errors.New("the Gemini mode write has no native session")
	}
	return a.WithSessionID(func(sessionID string) error {
		params, err := json.Marshal(map[string]string{"sessionId": sessionID, "modeId": mode})
		if err != nil {
			return err
		}
		accepted := false
		_, err = a.SendRequestObserved(acp.MethodSessionSetMode, params, a.APITimeout(), func(result json.RawMessage, replyErr error) {
			accepted = a.observeModeSetterReply(sessionID, mode, result, replyErr, acknowledged)
		})
		if err != nil {
			return err
		}
		if !accepted {
			return errors.New("the Gemini mode reply did not confirm the current native session")
		}
		return nil
	})
}

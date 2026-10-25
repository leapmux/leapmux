package codebuddy

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// CodeBuddy restores a session by replaying its history before accepting input.
// The Worker already owns those rows. Start waits for this replay without
// publishing it again or applying historical tool results to the current state.
type codebuddyResumeState struct {
	started bool // Guarded by Agent.mu.
	done    chan error
}

func (a *Agent) consumeResumeFrame(line *providerkit.ParsedLine) bool {
	if line.Type == frameTypeControlRequest || line.Type == frameTypeControlResponse {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.resumeState
	if state == nil {
		return false
	}
	var frame struct {
		Subtype         string          `json:"subtype"`
		SessionID       string          `json:"session_id"`
		Model           string          `json:"model"`
		ParentToolUseID string          `json:"parent_tool_use_id"`
		RequestID       json.RawMessage `json:"_requestId"`
		IsError         *bool           `json:"is_error"`
		Errors          []string        `json:"errors"`
	}
	if json.Unmarshal(line.Raw, &frame) != nil {
		return state.started
	}
	root := frame.SessionID == a.sessionID && frame.ParentToolUseID == ""
	if root && line.Type == contracts.CodebuddyFrameKindSystem && frame.Subtype == contracts.CodebuddySystemSubtypeInit && frame.Model == "unknown" {
		state.started = true
		return true
	}
	if root && line.Type == contracts.CodebuddyFrameKindResult && !providerkit.RawPresent(frame.RequestID) && frame.IsError != nil {
		var err error
		if !state.started {
			if !*frame.IsError {
				return false
			}
			message := "CodeBuddy could not restore the session."
			if details := strings.TrimSpace(strings.Join(frame.Errors, "\n")); details != "" {
				message += "\n" + details
			}
			err = errors.New(message)
		}
		// A failed historical turn is still a successful history replay.
		a.resumeState = nil
		state.done <- err
		return true
	}
	return state.started
}

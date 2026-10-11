package acp

import (
	"bytes"
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
)

// acpToolRequestContent records the last published request for conditional updates.
type acpToolRequestContent struct {
	original     []byte
	supplemental []byte
	revision     int64
}

// notePermissionToolCall reads the tool call of one session/request_permission
// and folds it into the request row of that call. The row is in the
// conversation that holds the call open (see conversation.notePermissionToolCall).
// A request that states no tool call id changes no row.
func (b *Base) notePermissionToolCall(params json.RawMessage) {
	var request struct {
		SessionID string                     `json:"sessionId"`
		ToolCall  map[string]json.RawMessage `json:"toolCall"`
	}
	if json.Unmarshal(params, &request) != nil || len(request.ToolCall) == 0 {
		return
	}
	var toolCallID string
	if json.Unmarshal(request.ToolCall[contracts.ACPSupplementIdentityToolCallID], &toolCallID) != nil || toolCallID == "" {
		return
	}
	b.withOpenToolConversation(request.SessionID, toolCallID, func(c *conversation) {
		c.notePermissionToolCall(toolCallID, request.ToolCall)
	})
}

// revisedToolRequestSupplement folds the request fields of one update into the
// supplement of a published request row. It reports false when the update
// revises no field, or when the stored row cannot be read.
func revisedToolRequestSupplement(previous *acpToolRequestContent, fields map[string]json.RawMessage) ([]byte, bool) {
	var original map[string]json.RawMessage
	if err := json.Unmarshal(previous.original, &original); err != nil {
		return nil, false
	}
	supplement := NewToolSupplement(original)
	if len(previous.supplemental) > 0 && json.Unmarshal(previous.supplemental, &supplement) != nil {
		return nil, false
	}
	changed := false
	for _, key := range contracts.ACPSupplementRequestKeys {
		value, exists := fields[key]
		current, supplemented := supplement[key]
		if !supplemented {
			current = original[key]
		}
		if !exists || bytes.Equal(value, []byte("null")) || bytes.Equal(current, value) {
			continue
		}
		supplement[key] = value
		changed = true
	}
	if !changed {
		return nil, false
	}
	next, err := json.Marshal(supplement)
	if err != nil {
		slog.Warn("Encode updated ACP tool input", "error", err)
		return nil, false
	}
	return next, true
}

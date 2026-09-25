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

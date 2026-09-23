package acp

import (
	"bytes"
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// acpToolRequestContent records the last published request for conditional updates.
type acpToolRequestContent struct {
	original     []byte
	supplemental []byte
	revision     int64
}

func (b *Base) rememberACPToolRequest(toolID string, content []byte) {
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	if b.toolRequestContents == nil {
		b.toolRequestContents = make(map[string]*acpToolRequestContent)
	}
	b.toolRequestContents[toolID] = &acpToolRequestContent{original: append([]byte(nil), content...)}
}

// enrichACPToolRequest publishes late input fields while the tool still runs.
// Output and completion remain on the result row.
func (b *Base) enrichACPToolRequest(toolID string, fields map[string]json.RawMessage) {
	b.turnMu.Lock()
	previous := b.toolRequestContents[toolID]
	b.turnMu.Unlock()
	if previous == nil {
		return
	}
	var original map[string]json.RawMessage
	if err := json.Unmarshal(previous.original, &original); err != nil {
		return
	}
	supplement := NewToolSupplement(original)
	if len(previous.supplemental) > 0 && json.Unmarshal(previous.supplemental, &supplement) != nil {
		return
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
		return
	}
	next, err := json.Marshal(supplement)
	if err != nil {
		slog.Warn("Encode updated ACP tool input", "error", err)
		return
	}
	updated, err := b.sink.EnrichMessage(agent.MessageEnrichment{
		SpanID: toolID, OriginalContent: previous.original,
		PreviousRevision: previous.revision, SupplementalContent: next,
	})
	if err != nil {
		slog.Warn("Publish updated ACP tool input", "error", err)
		return
	}
	if !updated {
		return
	}
	b.turnMu.Lock()
	if b.toolRequestContents[toolID] == previous {
		b.toolRequestContents[toolID] = &acpToolRequestContent{original: previous.original, supplemental: next, revision: previous.revision + 1}
	}
	b.turnMu.Unlock()
}

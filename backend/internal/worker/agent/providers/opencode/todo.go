package opencode

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

func (p FamilyProvider) ExtractTodoEvent(spanType string, content []byte, pairedToolUse func() []byte) (todoevents.Event, bool) {
	if event, ok := p.Provider.ExtractTodoEvent(spanType, content, pairedToolUse); ok {
		return event, true
	}
	var result struct {
		SessionUpdate string `json:"sessionUpdate"`
		Status        string `json:"status"`
		RawOutput     struct {
			Metadata struct {
				Todos json.RawMessage `json:"todos"`
			} `json:"metadata"`
		} `json:"rawOutput"`
	}
	if json.Unmarshal(content, &result) != nil || (result.SessionUpdate != contracts.ACPUpdateToolCallUpdate && result.SessionUpdate != contracts.ACPUpdateToolCall) || result.Status != "completed" {
		return todoevents.Event{}, false
	}
	var entries []json.RawMessage
	if json.Unmarshal(result.RawOutput.Metadata.Todos, &entries) != nil || entries == nil {
		return todoevents.Event{}, false
	}
	items := make([]todoevents.Item, 0, len(entries))
	for _, raw := range entries {
		var entry struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		}
		if json.Unmarshal(raw, &entry) != nil || strings.TrimSpace(entry.Content) == "" {
			continue
		}
		items = append(items, todoevents.Item{Content: entry.Content, Status: todoevents.StatusFromProviderWord(entry.Status)})
	}
	if len(entries) > 0 && len(items) == 0 {
		return todoevents.Event{}, false
	}
	return todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: items}, true
}

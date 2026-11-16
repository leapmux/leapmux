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
	return openCodeTodoSnapshot(result.RawOutput.Metadata.Todos)
}

// ExtractTodoEvent reads successful native tool results, including recovered parts.
func (opencodeProvider) ExtractTodoEvent(spanType string, content []byte, _ func() []byte) (todoevents.Event, bool) {
	var frame struct {
		Type string `json:"type"`
		Name string `json:"name"`
		Data struct {
			Name     string `json:"name"`
			Metadata struct {
				Todos json.RawMessage `json:"todos"`
			} `json:"metadata"`
		} `json:"data"`
		State struct {
			Status   string `json:"status"`
			Metadata struct {
				Todos json.RawMessage `json:"todos"`
			} `json:"metadata"`
		} `json:"state"`
	}
	if json.Unmarshal(content, &frame) != nil {
		return todoevents.Event{}, false
	}
	switch frame.Type {
	case contracts.OpenCodeNativeEventToolSuccess:
		if frame.Data.Name == contracts.OpenCodeNativeToolTodoWrite || (frame.Data.Name == "" && spanType == contracts.OpenCodeNativeToolTodoWrite) {
			return openCodeTodoSnapshot(frame.Data.Metadata.Todos)
		}
	case contracts.OpenCodeNativePartTypeTool:
		if frame.Name == contracts.OpenCodeNativeToolTodoWrite && frame.State.Status == contracts.OpenCodeNativeToolStatusCompleted {
			return openCodeTodoSnapshot(frame.State.Metadata.Todos)
		}
	}
	return todoevents.Event{}, false
}

// An empty native list clears the snapshot; an absent list makes no change.
func openCodeTodoSnapshot(rawTodos json.RawMessage) (todoevents.Event, bool) {
	var entries []json.RawMessage
	if json.Unmarshal(rawTodos, &entries) != nil || entries == nil {
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

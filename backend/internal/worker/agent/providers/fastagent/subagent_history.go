package fastagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type fastagentToolEntry struct {
	ID   string
	Data json.RawMessage
}

// fastagentObjectEntries reads a native tool map in its stored order.
func fastagentObjectEntries(raw json.RawMessage) ([]fastagentToolEntry, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	open, err := decoder.Token()
	if err != nil || open != json.Delim('{') {
		return nil, errors.New("the fastagent tool collection is not an object")
	}
	var entries []fastagentToolEntry
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		id, ok := token.(string)
		if !ok || id == "" {
			return nil, errors.New("the fastagent tool has no call id")
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("the fastagent tool call id %q repeats", id)
		}
		seen[id] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		entries = append(entries, fastagentToolEntry{ID: id, Data: value})
	}
	closeToken, err := decoder.Token()
	if err != nil || closeToken != json.Delim('}') {
		return nil, errors.New("the fastagent tool collection has no end")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("the fastagent tool collection has trailing data")
	}
	return entries, nil
}

// fastagentHistoryUpdates maps one stored message to ordered ACP updates.
func fastagentHistoryUpdates(message fastagentHistoryMessage, first bool, prompt string) ([]json.RawMessage, error) {
	var updates []json.RawMessage
	appendUpdate := func(value any) error {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		updates = append(updates, encoded)
		return nil
	}
	appendContent := func(updateType string) error {
		for _, block := range message.Content {
			var content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(block, &content); err != nil {
				return err
			}
			if content.Type == "" {
				return errors.New("the fastagent content block has no type")
			}
			if first && updateType == "user_message_chunk" && content.Type == "text" && content.Text == prompt {
				continue
			}
			if err := appendUpdate(map[string]any{"sessionUpdate": updateType, "content": block}); err != nil {
				return err
			}
		}
		return nil
	}
	appendChannel := func(name, updateType string) error {
		for _, block := range message.Channels[name] {
			if err := appendUpdate(map[string]any{"sessionUpdate": updateType, "content": block}); err != nil {
				return err
			}
		}
		return nil
	}
	appendTools := func(raw json.RawMessage, result bool) error {
		entries, err := fastagentObjectEntries(raw)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if result {
				var value struct {
					Content []json.RawMessage `json:"content"`
					IsError bool              `json:"isError"`
				}
				if err := json.Unmarshal(entry.Data, &value); err != nil {
					return err
				}
				status := "completed"
				if value.IsError {
					status = "failed"
				}
				content := make([]map[string]any, 0, len(value.Content))
				for _, block := range value.Content {
					content = append(content, map[string]any{"type": "content", "content": block})
				}
				if err := appendUpdate(map[string]any{
					"sessionUpdate": "tool_call_update", "toolCallId": entry.ID,
					"status": status, "content": content, "rawOutput": entry.Data,
				}); err != nil {
					return err
				}
				continue
			}
			var value struct {
				Method string `json:"method"`
				Params struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				} `json:"params"`
			}
			if err := json.Unmarshal(entry.Data, &value); err != nil {
				return err
			}
			if value.Method != "tools/call" || strings.TrimSpace(value.Params.Name) == "" {
				return errors.New("the fastagent tool request has no native name")
			}
			if err := appendUpdate(map[string]any{
				"sessionUpdate": "tool_call", "toolCallId": entry.ID,
				"title": value.Params.Name, "kind": "other", "status": "in_progress",
				"rawInput": value.Params.Arguments,
			}); err != nil {
				return err
			}
		}
		return nil
	}

	switch message.Role {
	case "assistant":
		if err := appendChannel("reasoning", "agent_thought_chunk"); err != nil {
			return nil, err
		}
		if err := appendContent("agent_message_chunk"); err != nil {
			return nil, err
		}
		if err := appendChannel("fast-agent-error", "agent_message_chunk"); err != nil {
			return nil, err
		}
		if err := appendTools(message.ToolCalls, false); err != nil {
			return nil, err
		}
	case "user":
		if err := appendTools(message.ToolResults, true); err != nil {
			return nil, err
		}
		if err := appendContent("user_message_chunk"); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("the fastagent history role %q is unsupported", message.Role)
	}
	return updates, nil
}

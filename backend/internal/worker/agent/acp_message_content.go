package agent

import (
	"encoding/json"
	"log/slog"
)

// acpMessageContent keeps the incoming event intact and retains missing protocol fields separately.
func (b *acpBase) acpMessageContent(original, state json.RawMessage) MessageContent {
	content := MessageContent{Original: original}
	var originalFields, stateFields map[string]json.RawMessage
	if json.Unmarshal(original, &originalFields) != nil || originalFields == nil || json.Unmarshal(state, &stateFields) != nil || stateFields == nil {
		return content
	}
	var originalID, stateID string
	if json.Unmarshal(originalFields["toolCallId"], &originalID) != nil || originalID == "" || json.Unmarshal(stateFields["toolCallId"], &stateID) != nil || originalID != stateID {
		return content
	}
	supplement := acpToolSupplement(originalFields)
	protocol := make(map[string]json.RawMessage)
	for key, value := range stateFields {
		if _, present := originalFields[key]; !present {
			protocol[key] = value
		}
	}
	if len(protocol) > 0 {
		supplement["protocol"], _ = json.Marshal(protocol)
	}
	var blocks []struct {
		Type       string `json:"type"`
		TerminalID string `json:"terminalId"`
	}
	if json.Unmarshal(stateFields["content"], &blocks) == nil {
		terminals := make(map[string]acpTerminalResult)
		for _, block := range blocks {
			if block.Type != "terminal" || block.TerminalID == "" {
				continue
			}
			if result, present := b.takeCompletedTerminal(block.TerminalID); present {
				terminals[block.TerminalID] = result
			}
		}
		if len(terminals) > 0 {
			supplement["terminals"], _ = json.Marshal(terminals)
		}
	}
	if len(protocol) == 0 && len(supplement["terminals"]) == 0 {
		return content
	}
	encoded, err := json.Marshal(supplement)
	if err != nil {
		slog.Warn("Encode ACP tool supplement", "agent_id", b.agentID, "error", err)
		return content
	}
	content.Supplemental = encoded
	return content
}

// ResolveProviderData gives semantic extractors the same retained provider fields as the frontend.
func (acpProvider) ResolveProviderData(content MessageContent) []byte {
	return resolveACPMessageContent(content)
}

func resolveACPMessageContent(content MessageContent) []byte {
	if len(content.Supplemental) == 0 {
		return content.Original
	}
	var original, supplement map[string]json.RawMessage
	if json.Unmarshal(content.Original, &original) != nil || original == nil || json.Unmarshal(content.Supplemental, &supplement) != nil || supplement == nil {
		return content.Original
	}
	for _, key := range []string{"sessionUpdate", "toolCallId", "status"} {
		before, present := original[key]
		after, supplied := supplement[key]
		if present != supplied {
			return content.Original
		}
		if !present {
			continue
		}
		var first, second string
		if json.Unmarshal(before, &first) != nil || json.Unmarshal(after, &second) != nil || first != second {
			return content.Original
		}
	}
	var protocol map[string]json.RawMessage
	_ = json.Unmarshal(supplement["protocol"], &protocol)
	changed := false
	for key, value := range protocol {
		if _, exists := original[key]; !exists {
			original[key] = value
			changed = true
		}
	}
	for _, key := range []string{"title", "kind", "rawInput", "locations"} {
		if value, exists := supplement[key]; exists {
			if key == "rawInput" {
				var before, after map[string]json.RawMessage
				if json.Unmarshal(original[key], &before) == nil && before != nil && json.Unmarshal(value, &after) == nil && after != nil {
					for name, field := range after {
						before[name] = field
					}
					value, _ = json.Marshal(before)
				}
			}
			original[key] = value
			changed = true
		}
	}
	if !changed {
		return content.Original
	}
	encoded, err := json.Marshal(original)
	if err != nil {
		return content.Original
	}
	return encoded
}

// acpToolCallID validates the tool envelope before the shared transcript tracker uses its ID.
func acpToolCallID(original []byte) string {
	var tool struct {
		SessionUpdate string `json:"sessionUpdate"`
		ToolCallID    string `json:"toolCallId"`
	}
	if json.Unmarshal(original, &tool) != nil || (tool.SessionUpdate != acpUpdateToolCall && tool.SessionUpdate != acpUpdateToolCallUpdate) {
		return ""
	}
	return tool.ToolCallID
}

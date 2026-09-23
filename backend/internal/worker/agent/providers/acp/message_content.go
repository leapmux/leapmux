package acp

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// acpMessageContent keeps the incoming event intact and retains missing protocol fields separately.
func (b *Base) acpMessageContent(original, state json.RawMessage) agent.MessageContent {
	content := agent.MessageContent{Original: original}
	var originalFields, stateFields map[string]json.RawMessage
	if json.Unmarshal(original, &originalFields) != nil || originalFields == nil || json.Unmarshal(state, &stateFields) != nil || stateFields == nil {
		return content
	}
	var originalID, stateID string
	if json.Unmarshal(originalFields[contracts.ACPSupplementIdentityToolCallID], &originalID) != nil || originalID == "" ||
		json.Unmarshal(stateFields[contracts.ACPSupplementIdentityToolCallID], &stateID) != nil || originalID != stateID {
		return content
	}
	supplement := NewToolSupplement(originalFields)
	protocol := make(map[string]json.RawMessage)
	for key, value := range stateFields {
		if _, present := originalFields[key]; !present {
			protocol[key] = value
		}
	}
	if len(protocol) > 0 {
		if err := supplement.setProtocol(protocol); err != nil {
			slog.Warn("Encode ACP tool supplement", "agent_id", b.AgentID(), "error", err)
			return content
		}
	}
	terminals := b.acpToolTerminals(stateFields[contracts.ACPContentBlockContent])
	if len(terminals) > 0 {
		if err := supplement.setTerminals(terminals); err != nil {
			slog.Warn("Encode ACP tool supplement", "agent_id", b.AgentID(), "error", err)
			return content
		}
	}
	if len(protocol) == 0 && len(terminals) == 0 {
		return content
	}
	encoded, err := json.Marshal(supplement)
	if err != nil {
		slog.Warn("Encode ACP tool supplement", "agent_id", b.AgentID(), "error", err)
		return content
	}
	content.Supplemental = encoded
	return content
}

// acpToolTerminals reads the output of every terminal this tool call refers to.
//
// A terminal LeapMux no longer holds is left out rather than stored empty, so the row
// states that nothing can be read for it instead of showing an empty stream.
func (b *Base) acpToolTerminals(content json.RawMessage) map[string]contracts.ACPTerminalResult {
	var blocks []contracts.ACPToolContentBlock
	if json.Unmarshal(content, &blocks) != nil {
		return nil
	}
	terminals := make(map[string]contracts.ACPTerminalResult)
	for _, block := range blocks {
		if block.Type != contracts.ACPBlockTypeTerminal || block.TerminalID == "" {
			continue
		}
		if result, present := b.terminalResultFor(block.TerminalID); present {
			terminals[block.TerminalID] = result
		}
	}
	return terminals
}

// ResolveProviderData gives semantic extractors the same retained provider fields as the frontend.
func (Provider) ResolveProviderData(content agent.MessageContent) []byte {
	return ResolveMessageContent(content)
}

func ResolveMessageContent(content agent.MessageContent) []byte {
	if len(content.Supplemental) == 0 {
		return content.Original
	}
	var original map[string]json.RawMessage
	var supplement ToolSupplement
	if json.Unmarshal(content.Original, &original) != nil || original == nil || json.Unmarshal(content.Supplemental, &supplement) != nil || supplement == nil {
		return content.Original
	}
	if !supplement.IdentityMatches(original) {
		return content.Original
	}
	var protocol map[string]json.RawMessage
	_ = json.Unmarshal(supplement[contracts.ACPSupplementProtocol], &protocol)
	changed := false
	for key, value := range protocol {
		if _, exists := original[key]; !exists {
			original[key] = value
			changed = true
		}
	}
	for _, key := range contracts.ACPSupplementRequestKeys {
		if value, exists := supplement[key]; exists {
			if key == contracts.ACPSupplementRequestRawInput {
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

// ToolCallID validates the tool envelope before the shared transcript tracker uses its ID.
func ToolCallID(original []byte) string {
	var tool struct {
		SessionUpdate string `json:"sessionUpdate"`
		ToolCallID    string `json:"toolCallId"`
	}
	if json.Unmarshal(original, &tool) != nil || (tool.SessionUpdate != UpdateToolCall && tool.SessionUpdate != UpdateToolCallUpdate) {
		return ""
	}
	return tool.ToolCallID
}

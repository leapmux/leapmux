package pi

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/jsonfield"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// piEventType reads one Pi event's discriminator, or the empty string.
func piEventType(raw []byte) string {
	var envelope struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return ""
	}
	return envelope.Type
}

// buildPiIncompleteToolSupplement encodes the last partial result when the call reported one.
func buildPiIncompleteToolSupplement(toolCallID string, tool piToolState) ([]byte, error) {
	if len(tool.PartialResult) == 0 {
		return nil, nil
	}
	return json.Marshal(contracts.PiIncompleteToolSupplement{
		ToolCallID:    toolCallID,
		ToolName:      tool.ToolName,
		PartialResult: tool.PartialResult,
	})
}

// resolvePiIncompleteTool adds a retained call's partial result to its start frame.
// Match both identity fields before changing the frame.
// Native frame keys and LeapMux supplement keys use separate contract tables.
func resolvePiIncompleteTool(content agent.MessageContent) []byte {
	var extra contracts.PiIncompleteToolSupplement
	if json.Unmarshal(content.Supplemental, &extra) != nil ||
		extra.ToolCallID == "" || len(extra.PartialResult) == 0 {
		return content.Original
	}
	var original map[string]json.RawMessage
	if json.Unmarshal(content.Original, &original) != nil || original == nil {
		return content.Original
	}
	var toolCallID, toolName string
	if json.Unmarshal(original[contracts.PiResultFieldToolCallID], &toolCallID) != nil || toolCallID != extra.ToolCallID {
		return content.Original
	}
	if json.Unmarshal(original[contracts.PiResultFieldToolName], &toolName) != nil || toolName != extra.ToolName {
		return content.Original
	}
	// Return the same bytes when a caller resolves an already resolved frame.
	if jsonfield.Equal(original[contracts.PiResultFieldResult], extra.PartialResult) {
		return content.Original
	}
	original[contracts.PiResultFieldResult] = extra.PartialResult
	resolved, err := json.Marshal(original)
	if err != nil {
		return content.Original
	}
	return resolved
}

// Resolve only an incomplete start frame whose native identity matches its supplement.
func (piProvider) ResolveProviderData(content agent.MessageContent) []byte {
	if len(content.Supplemental) != 0 && piEventType(content.Original) == contracts.PiEventToolExecutionStart {
		return resolvePiIncompleteTool(content)
	}
	return content.Original
}

package agent

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
)

// supplementZCodeControlInput recovers arguments from an interaction request for the same tool call.
func (a *zcodeAgent) supplementZCodeControlInput(toolCallID, toolName string, input json.RawMessage) (bool, error) {
	if toolCallID == "" || zcodeInputIsAbsent(input) {
		return false, nil
	}
	sink := a.zcodeSinkForToolCall(toolCallID)
	stored, err := sink.ReadToolRequest(toolCallID)
	if err != nil {
		return false, err
	}
	if stored == nil {
		return false, nil
	}
	event, ok := parseZCodeEvent(stored.Content.Original)
	if !ok || event.Type != contracts.ZCodeEventToolUpdated {
		return false, nil
	}
	var request zcodeToolUpdated
	if json.Unmarshal(event.Payload, &request) != nil || request.Kind != contracts.ZCodeToolKindScheduled || request.ToolCallID != toolCallID ||
		(toolName != "" && request.ToolName != "" && request.ToolName != toolName) {
		return false, nil
	}
	// Resolve earlier recovery through the same reader as the semantic extractors.
	resolved, ok := parseZCodeEvent((zcodeProvider{}).ResolveProviderData(stored.Content))
	if !ok {
		return false, nil
	}
	var previous zcodeToolUpdated
	if err := json.Unmarshal(resolved.Payload, &previous); err != nil {
		return false, fmt.Errorf("read recovered tool input: %w", err)
	}
	supplement, err := zcodeToolInputSupplement(request, previous.Input, input)
	if err != nil || len(supplement) == 0 {
		return err == nil, err
	}
	combined, err := mergeToolSupplements(stored.Content.Supplemental, supplement)
	if err != nil {
		return false, fmt.Errorf("merge control tool input: %w", err)
	}
	if bytes.Equal(combined, stored.Content.Supplemental) {
		return true, nil
	}
	return sink.EnrichMessage(MessageEnrichment{
		Seq: stored.Seq, SpanID: toolCallID, OriginalContent: stored.Content.Original,
		PreviousRevision: stored.Revision, SupplementalContent: combined,
	})
}

// zcodeInputIsAbsent includes empty JSON objects regardless of whitespace.
func zcodeInputIsAbsent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return true
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil && len(object) == 0
}

// zcodeMissingToolInput selects absent argument fields. Explicit original values remain authoritative.
func zcodeMissingToolInput(payload zcodeToolUpdated, input json.RawMessage) map[string]json.RawMessage {
	if payload.Kind != contracts.ZCodeToolKindScheduled || payload.ToolCallID == "" {
		return nil
	}
	var original, recovered map[string]json.RawMessage
	if !zcodeInputIsAbsent(payload.Input) && json.Unmarshal(payload.Input, &original) != nil {
		return nil
	}
	if json.Unmarshal(input, &recovered) != nil {
		return nil
	}
	for key := range original {
		delete(recovered, key)
	}
	return recovered
}

// zcodeToolInputSupplement keeps recovered arguments separate from the native request.
func zcodeToolInputSupplement(payload zcodeToolUpdated, inputs ...json.RawMessage) ([]byte, error) {
	missing := make(map[string]json.RawMessage)
	for _, input := range inputs {
		for key, value := range zcodeMissingToolInput(payload, input) {
			missing[key] = value
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}
	return json.Marshal(map[string]any{
		"type": contracts.ZCodeEventToolUpdated,
		"payload": map[string]any{
			"kind": payload.Kind, "toolCallId": payload.ToolCallID, "input": missing,
		},
	})
}

// ResolveProviderData supplies validated stream arguments to semantic extractors.
func (zcodeProvider) ResolveProviderData(content MessageContent) []byte {
	if len(content.Supplemental) == 0 {
		return content.Original
	}
	original, ok := parseZCodeEvent(content.Original)
	if !ok {
		return content.Original
	}
	supplement, ok := parseZCodeEvent(content.Supplemental)
	if !ok || supplement.Type != original.Type {
		return content.Original
	}
	if original.Type != contracts.ZCodeEventToolUpdated {
		return content.Original
	}
	var request, extra zcodeToolUpdated
	if json.Unmarshal(original.Payload, &request) != nil || json.Unmarshal(supplement.Payload, &extra) != nil ||
		request.ToolCallID == "" || request.ToolCallID != extra.ToolCallID || request.Kind != extra.Kind {
		return content.Original
	}
	missing := zcodeMissingToolInput(request, extra.Input)
	if len(missing) == 0 {
		return content.Original
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(original.Payload, &payload) != nil {
		return content.Original
	}
	var input map[string]json.RawMessage
	if !zcodeInputIsAbsent(request.Input) && json.Unmarshal(request.Input, &input) != nil {
		return content.Original
	}
	for key, value := range input {
		missing[key] = value
	}
	encodedInput, err := json.Marshal(missing)
	if err != nil {
		return content.Original
	}
	payload["input"] = encodedInput
	encoded, err := json.Marshal(payload)
	if err != nil {
		return content.Original
	}
	resolved := original.withPayload(encoded).persistBytes()
	if resolved == nil {
		return content.Original
	}
	return resolved
}

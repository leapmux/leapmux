package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
)

type zcodeToolReference struct {
	Kind               string `json:"kind"`
	ToolCallID         string `json:"toolCallId"`
	ToolName           string `json:"toolName"`
	AssistantMessageID string `json:"assistantMessageId"`
	AgentID            string `json:"agentId"`
	ChildSessionID     string `json:"childSessionId"`
}

func zcodeToolReferenceFrom(raw []byte) (zcodeToolReference, bool) {
	var envelope struct {
		Type    string             `json:"type"`
		Payload zcodeToolReference `json:"payload"`
	}
	err := json.Unmarshal(raw, &envelope)
	return envelope.Payload, err == nil && envelope.Type == contracts.ZCodeEventToolUpdated && envelope.Payload.ToolCallID != ""
}

func (ref zcodeToolReference) lookup(previous zcodeToolLookup) (zcodeToolLookup, bool) {
	if ref.ToolName != "" {
		previous.toolName = ref.ToolName
	}
	if ref.AssistantMessageID != "" {
		previous.messageID = ref.AssistantMessageID
	}
	if ref.ChildSessionID != "" {
		previous.sessionID = ref.ChildSessionID
	}
	if ref.AgentID != "" {
		prefix := contracts.ZCodeToolPrefixSubagent + ref.AgentID + "_"
		if !strings.HasPrefix(ref.ToolCallID, prefix) || len(ref.ToolCallID) == len(prefix) {
			return zcodeToolLookup{}, false
		}
		previous.callID = strings.TrimPrefix(ref.ToolCallID, prefix)
	}
	return previous, true
}

func newZCodeToolTranscript(ctx context.Context, services ProviderServices, locate func() zcodeToolStoreLocation) *toolTranscript {
	requests := make(map[string]zcodeToolLookup)
	var location zcodeToolStoreLocation
	clearRequests := func() { clear(requests) }
	return &toolTranscript{
		ProviderServices: services,
		ctx:              ctx,
		providerName:     "ZCode",
		locate: func(_ string) toolTranscriptLocation {
			location = locate()
			return toolTranscriptLocation{sessionKey: location.sessionID, path: location.databasePath}
		},
		toolCallID: func(raw []byte) string {
			ref, valid := zcodeToolReferenceFrom(raw)
			if valid && (ref.Kind == contracts.ZCodeToolKindResult || ref.Kind == contracts.ZCodeToolKindError) {
				return ref.ToolCallID
			}
			return ""
		},
		observeMessage: func(content MessageContent, span SpanInfo) {
			ref, valid := zcodeToolReferenceFrom(content.Original)
			if !valid || ref.Kind != contracts.ZCodeToolKindScheduled || ref.ToolCallID != span.SpanID {
				return
			}
			if lookup, valid := ref.lookup(zcodeToolLookup{}); valid {
				requests[ref.ToolCallID] = lookup
			}
		},
		resetRecords: clearRequests,
		finishTurn:   clearRequests,
		newChild: func(child ProviderServices) *toolTranscript {
			return newZCodeToolTranscript(ctx, child, locate)
		},
		readSupplements: func(ctx context.Context, _ string, pending map[string]MessageContent, final bool) (map[string][]byte, error) {
			lookups := make(map[string]zcodeToolLookup)
			for id, original := range pending {
				ref, valid := zcodeToolReferenceFrom(original.Original)
				if !valid || ref.ToolCallID != id {
					continue
				}
				if lookup, valid := ref.lookup(requests[id]); valid {
					lookups[id] = lookup
				}
			}
			records, readErr := readZCodeToolRecords(ctx, location, lookups)
			out := make(map[string][]byte)
			for id, record := range records {
				if !record.ready && !final {
					continue
				}
				supplement, err := zcodeToolResultSupplement(pending[id].Original, record)
				if err != nil {
					readErr = errors.Join(readErr, err)
					continue
				}
				out[id] = supplement
				delete(requests, id)
			}
			return out, readErr
		},
	}
}

func zcodeToolResultSupplement(original []byte, record zcodeToolRecord) ([]byte, error) {
	ref, valid := zcodeToolReferenceFrom(original)
	if !valid {
		return nil, fmt.Errorf("invalid ZCode tool result")
	}
	encoded, err := json.Marshal(map[string]any{
		"type": contracts.ZCodeEventToolUpdated,
		"payload": map[string]string{
			"kind": ref.Kind, "toolCallId": ref.ToolCallID,
		},
		"nativeTool": record.native,
		"artifacts":  record.artifacts,
	})
	if err != nil {
		return nil, err
	}
	if len(encoded) > liveStdoutMaxTokenSize() {
		return nil, fmt.Errorf("ZCode tool supplement exceeds the message size limit")
	}
	return encoded, nil
}

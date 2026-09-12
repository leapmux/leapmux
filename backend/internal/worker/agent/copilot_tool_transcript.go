package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/leapmux/leapmux/generated/contracts"
)

func newCopilotToolTranscript(ctx context.Context, services ProviderServices, locate func() string) *toolTranscript {
	return &toolTranscript{
		ProviderServices: services,
		ctx:              ctx,
		providerName:     "Copilot",
		toolCallID:       acpToolCallID,
		locate: func(_ string) toolTranscriptLocation {
			path := locate()
			return toolTranscriptLocation{sessionKey: path, path: path}
		},
		initialSupplement: func(ctx context.Context, path string, original []byte, span SpanInfo) ([]byte, error) {
			id := acpToolCallID(original)
			if id == "" || id != span.SpanID {
				return nil, nil
			}
			record, err := readCopilotNativeTool(ctx, path, id)
			if err != nil || record == nil {
				return nil, err
			}
			return copilotToolSupplement(original, record)
		},
		readSupplements: func(ctx context.Context, path string, pending map[string]MessageContent, _ bool) (map[string][]byte, error) {
			out := make(map[string][]byte)
			var failures error
			for id, original := range pending {
				record, err := readCopilotNativeTool(ctx, path, id)
				if err != nil {
					failures = errors.Join(failures, err)
					continue
				}
				if record == nil || record.Result == nil {
					continue
				}
				supplement, err := copilotToolSupplement(original.Original, record)
				if err != nil {
					failures = errors.Join(failures, err)
					continue
				}
				if len(supplement) > 0 {
					out[id] = supplement
				}
			}
			return out, failures
		},
		newChild: func(child ProviderServices) *toolTranscript { return newCopilotToolTranscript(ctx, child, locate) },
	}
}

func copilotToolSupplement(original []byte, record *copilotNativeTool) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(original, &fields); err != nil {
		return nil, err
	}
	id := acpToolCallID(original)
	var request struct {
		Data struct {
			ToolCallID string `json:"toolCallId"`
		} `json:"data"`
	}
	if id == "" || json.Unmarshal(record.Request, &request) != nil || request.Data.ToolCallID != id {
		return nil, nil
	}
	events := make([]json.RawMessage, 0, 4)
	for _, event := range []json.RawMessage{record.Request, record.Started, record.Result, record.Finished} {
		if len(event) > 0 {
			events = append(events, event)
		}
	}
	supplement := acpToolSupplement(fields)
	raw, err := json.Marshal(events)
	if err != nil {
		return nil, err
	}
	supplement[contracts.CopilotSupplementNativeEvents] = raw
	return json.Marshal(supplement)
}

package agent

import (
	"context"
	"encoding/json"
)

func newCursorToolTranscript(ctx context.Context, services ProviderServices, storePath func() string) *toolTranscript {
	store := &cursorToolStore{}
	return &toolTranscript{
		ProviderServices: services,
		toolCallID:       acpToolCallID,
		ctx:              ctx,
		locate: func(_ string) toolTranscriptLocation {
			path := storePath()
			return toolTranscriptLocation{sessionKey: path, path: path}
		},
		providerName: "Cursor",
		resetRecords: store.reset,
		readSupplements: func(ctx context.Context, path string, pending map[string]MessageContent, _ bool) (map[string][]byte, error) {
			ids := make([]string, 0, len(pending))
			for id := range pending {
				ids = append(ids, id)
			}
			records, err := store.read(ctx, path, ids)
			if err != nil {
				return nil, err
			}
			out := make(map[string][]byte, len(records))
			for id, record := range records {
				supplement, err := cursorToolSupplement(pending[id].Original, record)
				if err != nil {
					return out, err
				}
				out[id] = supplement
			}
			return out, nil
		},
	}
}

func cursorToolSupplement(original []byte, record cursorToolRecord) ([]byte, error) {
	var tool map[string]json.RawMessage
	if err := json.Unmarshal(original, &tool); err != nil {
		return nil, err
	}
	supplement := acpToolSupplement(tool)
	output := make(map[string]json.RawMessage)
	// Keep the native record shape so the frontend owns tool-specific extraction.
	if providerOptions := record.result["providerOptions"]; len(providerOptions) > 0 {
		output["providerOptions"] = providerOptions
	}
	content, err := json.Marshal([]json.RawMessage{record.content})
	if err != nil {
		return nil, err
	}
	output["content"] = content
	if len(record.arguments) > 0 {
		output["toolArguments"] = record.arguments
	}
	supplement["rawOutput"], err = json.Marshal(output)
	if err != nil {
		return nil, err
	}
	return json.Marshal(supplement)
}

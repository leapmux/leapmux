package agent

import (
	"context"
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
)

// cursorToolSource reads Cursor's tool records out of one session's own store.
type cursorToolSource struct {
	noopToolSupplementSource
	// store keeps the database handle and the blob index of the session's store file.
	// It carries its own mutex, which is what lets the supplement worker read it while
	// the reader goroutine resets it.
	store *cursorToolStore
	// resolveStorePath reports the store file of the session that runs now. The
	// transcript calls it for every agent message.
	resolveStorePath func() string
}

func newCursorToolTranscript(ctx context.Context, services ProviderServices, storePath func() string) *toolTranscript {
	source := &cursorToolSource{store: &cursorToolStore{}, resolveStorePath: storePath}
	// The store holds one database handle for the session's store file. The agent's
	// context ending is what releases it, because the transcript has no teardown call.
	context.AfterFunc(ctx, source.store.reset)
	return newToolTranscript(ctx, services, source)
}

func (c *cursorToolSource) providerName() string { return "Cursor" }

func (c *cursorToolSource) locate(string) toolTranscriptLocation {
	path := c.resolveStorePath()
	return toolTranscriptLocation{sessionKey: path, path: path, ready: path != ""}
}

func (c *cursorToolSource) resetRecords() { c.store.reset() }

func (c *cursorToolSource) toolCallID(original []byte) string { return acpToolCallID(original) }

func (c *cursorToolSource) readSupplements(ctx context.Context, path string, pending map[string]MessageContent, _ bool) (map[string][]byte, error) {
	ids := make([]string, 0, len(pending))
	for id := range pending {
		ids = append(ids, id)
	}
	records, err := c.store.read(ctx, path, ids)
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
}

func cursorToolSupplement(original []byte, record cursorToolRecord) ([]byte, error) {
	var tool map[string]json.RawMessage
	if err := json.Unmarshal(original, &tool); err != nil {
		return nil, err
	}
	supplement := newACPToolSupplement(tool)
	output := contracts.CursorStoredToolOutput{Content: []json.RawMessage{record.content}}
	// Keep the native record shape so the frontend owns tool-specific extraction.
	if providerOptions := record.result[contracts.CursorStoredToolProviderOptions]; len(providerOptions) > 0 {
		output.ProviderOptions = providerOptions
	}
	if len(record.arguments) > 0 {
		output.ToolArguments = record.arguments
	}
	if err := supplement.setRawOutput(output); err != nil {
		return nil, err
	}
	return json.Marshal(supplement)
}

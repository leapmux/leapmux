package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"maps"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/tooltranscript"
)

type geminiToolSource struct {
	tooltranscript.SourceDefaults
	query       agent.StoredSessionQuery
	observeMode func(string, json.RawMessage)
}

func newGeminiToolTranscript(ctx context.Context, services agent.ProviderServices, query agent.StoredSessionQuery, observeMode func(string, json.RawMessage)) *tooltranscript.Transcript {
	return tooltranscript.New(ctx, services, &geminiToolSource{query: query, observeMode: observeMode})
}

func (*geminiToolSource) ProviderName() string { return "gemini" }

func (*geminiToolSource) Locate(sessionID string) tooltranscript.Location {
	return tooltranscript.Location{SessionKey: sessionID, Path: sessionID, Ready: validGeminiSessionID(sessionID)}
}

func (*geminiToolSource) ToolCallID(original []byte) string { return acp.ToolCallID(original) }

func (source *geminiToolSource) ReadSupplements(ctx context.Context, sessionID string, pending map[string]agent.MessageContent, _ bool) (map[string][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := locateGeminiSession(source.query, sessionID)
	if err != nil {
		return nil, err
	}
	session, err := readGeminiSession(source.query, path)
	if err != nil {
		return nil, err
	}
	_, hash := geminiProjectIdentity(source.query.WorkingDir)
	if session.SessionID != sessionID || session.ProjectHash != hash || session.Kind == "subagent" {
		return nil, errors.New("the Gemini transcript belongs to another session")
	}
	output := make(map[string][]byte)
	for _, message := range session.Messages {
		for _, record := range message.ToolCalls {
			if err := ctx.Err(); err != nil {
				return output, err
			}
			var identity geminiToolIdentity
			if json.Unmarshal(record, &identity) != nil || identity.ID == "" || identity.Name == "" {
				continue
			}
			content, exists := pending[identity.ID]
			if !exists {
				continue
			}
			extra, err := geminiToolSupplement(content.Original, record)
			if err != nil {
				return output, err
			}
			output[identity.ID] = extra
			if source.observeMode != nil {
				source.observeMode(sessionID, record)
			}
		}
	}
	return output, nil
}

// ResolveProviderData gives the worker's semantic extractors the native record
// that the tool transcript stored beside a frame.
//
// Gemini CLI 0.62.0 states the outcome of a tool call only in its session
// record: a tool_call_update carries no rawOutput. geminiToolSupplement stores
// that record in the row's supplement, and the shared ACP resolve copies only the
// request fields that a later update revises. Without this resolve,
// ExtractTodoEvent reads no write_todos list, and the worker keeps no to-do
// snapshot. The browser plugin reads the same record from the supplement.
//
// It delegates to the embedded Provider first, so a later shared resolve rule
// reaches Gemini too. It applies the same identity gate as the shared resolve and
// the browser plugin: a record stored beside one frame never reaches another.
func (p geminiProvider) ResolveProviderData(content agent.MessageContent) []byte {
	resolved := p.Provider.ResolveProviderData(content)
	if len(content.Supplemental) == 0 {
		return resolved
	}
	var supplement acp.ToolSupplement
	if json.Unmarshal(content.Supplemental, &supplement) != nil {
		return resolved
	}
	var stored map[string]json.RawMessage
	if json.Unmarshal(supplement[contracts.ACPSupplementRawOutput], &stored) != nil || len(stored) == 0 {
		return resolved
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(resolved, &fields) != nil || fields == nil || !supplement.IdentityMatches(fields) {
		return resolved
	}
	// A rawOutput of the frame's own keeps every key, and the stored record joins
	// it under the key that LeapMux chose. A rawOutput that is not an object
	// cannot hold that key, so the frame stays as the agent sent it.
	var output map[string]json.RawMessage
	if own, exists := fields[contracts.ACPSupplementRawOutput]; exists && json.Unmarshal(own, &output) != nil {
		return resolved
	}
	if output == nil {
		output = make(map[string]json.RawMessage, len(stored))
	}
	maps.Copy(output, stored)
	encoded, err := json.Marshal(output)
	if err != nil {
		return resolved
	}
	fields[contracts.ACPSupplementRawOutput] = encoded
	merged, err := json.Marshal(fields)
	if err != nil {
		return resolved
	}
	return merged
}

func geminiToolSupplement(original, record []byte) ([]byte, error) {
	var frame map[string]json.RawMessage
	if err := json.Unmarshal(original, &frame); err != nil {
		return nil, err
	}
	var identity geminiToolIdentity
	if err := json.Unmarshal(record, &identity); err != nil {
		return nil, err
	}
	if identity.ID == "" || identity.Name == "" || acp.ToolCallID(original) != identity.ID {
		return nil, errors.New("the Gemini tool record belongs to another call")
	}
	supplement := acp.NewToolSupplement(frame)
	if err := supplement.SetRawOutput(map[string]json.RawMessage{contracts.GeminiSupplementStoredToolRecord: record}); err != nil {
		return nil, err
	}
	return json.Marshal(supplement)
}

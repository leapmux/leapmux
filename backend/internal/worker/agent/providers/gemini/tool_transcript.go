package gemini

import (
	"context"
	"encoding/json"
	"errors"

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

package gemini

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type geminiOutputServices struct {
	agent.ProviderServices
	query          agent.StoredSessionQuery
	currentSession func() string
	onSession      func(string)
}

func (services *geminiOutputServices) UpdateSessionID(sessionID string) {
	services.ProviderServices.UpdateSessionID(sessionID)
	if services.onSession != nil {
		services.onSession(sessionID)
	}
}

// Gemini's prompt quota sums every request of the turn. The last native model
// record states the current context, so only that record supplies this surface.
func (services *geminiOutputServices) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	sessionID := content.AgentSessionID
	if content.Publication == nil && services.currentSession != nil {
		sessionID = services.currentSession()
	}
	if content.WriteReceipt == nil {
		content.WriteReceipt = agent.NewTranscriptWriteReceipt()
	}
	content = services.CaptureMessage(content, span)
	if err := services.ProviderServices.PersistTurnEnd(content, span); err != nil {
		return err
	}
	if !content.WriteReceipt.ClaimContextUsage() {
		return nil
	}
	var owner agent.TranscriptOwner
	if content.Publication != nil {
		owner = content.Publication.Owner()
		if owner != nil && !owner.IsCurrent() {
			return nil
		}
	}
	path, err := locateGeminiSession(services.query, sessionID)
	if err != nil {
		return nil
	}
	session, err := readGeminiSession(services.query, path)
	if err != nil {
		return nil
	}
	if usage := geminiSessionUsage(session); usage != nil {
		info := map[string]any{contracts.SessionInfoKeyContextUsage: usage}
		if owner != nil {
			owner.PublishSessionInfo(info)
		} else {
			services.BroadcastSessionInfo(info)
		}
	}
	return nil
}

func geminiSessionUsage(session geminiSession) map[string]any {
	for index := len(session.Messages) - 1; index >= 0; index-- {
		message := session.Messages[index]
		if message.Type != "gemini" {
			continue
		}
		var tokens struct {
			Input  *int64 `json:"input"`
			Output *int64 `json:"output"`
			Cached int64  `json:"cached"`
		}
		if json.Unmarshal(message.Tokens, &tokens) != nil || tokens.Input == nil || tokens.Output == nil ||
			*tokens.Input < 0 || *tokens.Output < 0 || tokens.Cached < 0 || tokens.Cached > *tokens.Input {
			return nil
		}
		return providerkit.ContextUsageMap(providerkit.ContextTokenCounts{Input: *tokens.Input - tokens.Cached, CacheRead: tokens.Cached, Output: *tokens.Output})
	}
	return nil
}

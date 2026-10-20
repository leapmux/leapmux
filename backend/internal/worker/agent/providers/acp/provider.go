package acp

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Provider recognizes ACP cancellation and answers Model Context Protocol (MCP) elicitation.
// Each concrete provider embeds it and supplies its own session store and attachment policy.
// Classify does nothing unless a concrete provider overrides it for native notifications.
// ListStoredSessions and ValidateAttachment also retain the defaults unless the provider overrides them.
type Provider struct {
	agent.ProviderDefaults
}

func (Provider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	// ACP defines no `_meta` scope for an accepted elicitation, so an answer
	// that carries one is refused.
	if result, ok := providerkit.ResolveMCPElicitationResponse(ctx, contracts.MCPElicitationMethodACP, nil); ok {
		return result
	}
	return agent.DefaultControlResponseResolution(ctx)
}

func (Provider) IsInterrupt(content string) bool {
	var msg struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal([]byte(content), &msg); err != nil {
		return false
	}
	return msg.Method == "session/cancel"
}

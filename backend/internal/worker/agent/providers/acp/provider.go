package acp

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Provider recognizes ACP's `session/cancel` notification (and the bare
// `cancel` form retained for legacy producers) and answers ACP's MCP
// elicitation. Every ACP provider embeds it in a plugin type of its own, and
// that type adds what only its provider knows: where its session store lives,
// and any attachment policy.
// ACP doesn't consolidate notifications today, so Classify/Merge inherit the
// no-op embedding, and so do ListStoredSessions and ValidateAttachment for a
// provider that declares neither.
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
	return msg.Method == "session/cancel" || msg.Method == "cancel"
}

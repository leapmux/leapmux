package reasonix

import (
	"context"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// reasonixProvider is the wire-format plugin for Reasonix. Reasonix speaks ACP,
// so the embedded Provider answers the protocol questions; this type adds
// where Reasonix keeps its sessions and its text-only attachment policy.
type reasonixProvider struct {
	acp.Provider
}

// ResolveControlResponse answers Reasonix's MCP request on its own method.
func (p reasonixProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	if result, ok := providerkit.ResolveMCPElicitationResponse(ctx, contracts.ReasonixMethodMcpRequestInteraction, nil); ok {
		return result
	}
	return p.Provider.ResolveControlResponse(ctx)
}

// ListStoredSessions reads Reasonix's own session store; see sessions.go.
func (reasonixProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return reasonixStoredSessions(ctx, q)
}

// ValidateAttachment enforces Reasonix's text-only policy: it advertises
// image:false/audio:false and drops any non-text content block, so reject
// everything but text up front.
func (reasonixProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	if attachment.Kind != agent.AttachmentKindText {
		return fmt.Errorf("reasonix only supports text attachments: %s", attachment.Filename)
	}
	return nil
}

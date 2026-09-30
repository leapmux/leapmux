package junie

import (
	"context"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// junieProvider is the wire-format plugin for Junie. Junie speaks ACP with
// JetBrains extensions, so the embedded Provider answers every protocol
// question; this type owns Junie's session store and attachment policy.
type junieProvider struct {
	acp.Provider
}

// ListStoredSessions reads Junie's own session index; see sessions.go.
func (junieProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return junieStoredSessions(ctx, q)
}

// ValidateAttachment rejects resources that Junie's model input cannot read.
func (junieProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	return providerkit.RejectPDFAndBinaryAttachment("Junie", attachment)
}

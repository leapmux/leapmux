package junie

import (
	"context"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// junieProvider is the wire-format plugin for Junie. Junie speaks ACP with
// JetBrains extensions, so the embedded Provider answers every protocol
// question; this type adds the one fact only Junie knows: where its session
// store lives.
type junieProvider struct {
	acp.Provider
}

// ListStoredSessions reads Junie's own session index; see sessions.go.
func (junieProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return junieStoredSessions(ctx, q)
}

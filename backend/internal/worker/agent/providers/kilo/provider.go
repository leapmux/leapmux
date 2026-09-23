package kilo

import (
	"context"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
)

// kiloProvider is the wire-format plugin for Kilo, a fork of OpenCode that
// shares its protocol and its session-store schema.
type kiloProvider struct{ opencode.FamilyProvider }

// ListStoredSessions reads Kilo's own session store; see sessions.go.
func (kiloProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return kiloStoredSessions(ctx, q)
}

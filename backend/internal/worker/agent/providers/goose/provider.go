package goose

import (
	"context"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// gooseProvider is the wire-format plugin for Goose. Goose speaks plain ACP, so
// the embedded Provider answers every protocol question; this type adds the
// one fact only Goose knows: where its session store lives.
type gooseProvider struct {
	acp.Provider
}

// ListStoredSessions reads Goose's own session store; see sessions.go.
func (gooseProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return gooseStoredSessions(ctx, q)
}

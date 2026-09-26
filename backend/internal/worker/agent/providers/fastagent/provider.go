package fastagent

import (
	"context"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// fastagentProvider is the wire-format plugin for fast-agent. fast-agent
// speaks plain ACP, so the embedded Provider answers every protocol question;
// this type adds the one fact only fast-agent knows: where its session store
// lives.
type fastagentProvider struct {
	acp.Provider
}

// ListStoredSessions reads fast-agent's own session store; see sessions.go.
func (fastagentProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return fastagentStoredSessions(ctx, q)
}

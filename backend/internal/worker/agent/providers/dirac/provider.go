package dirac

import (
	"context"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// diracProvider is the wire-format plugin for Dirac. Dirac speaks ACP with
// `dev.dirac/*` extensions, so the embedded Provider answers every protocol
// question; this type adds the one fact only Dirac knows: where its task
// history lives.
type diracProvider struct {
	acp.Provider
}

// ListStoredSessions reads Dirac's own task-history store; see sessions.go.
func (diracProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return diracStoredSessions(ctx, q)
}

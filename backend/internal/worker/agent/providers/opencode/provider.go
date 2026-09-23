package opencode

import (
	"context"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// FamilyProvider is the plugin half OpenCode and Kilo share: both are
// ACP providers, and both carry native tool-result metadata (a to-do list, see
// todo.go) in addition to ACP plan events. Each provider embeds it in
// a plugin type of its own, which adds where that provider keeps its sessions.
type FamilyProvider struct{ acp.Provider }

// opencodeProvider is the wire-format plugin for OpenCode.
type opencodeProvider struct{ FamilyProvider }

// ListStoredSessions reads OpenCode's own session store; see sessions.go.
func (opencodeProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return opencodeStoredSessions(ctx, q)
}

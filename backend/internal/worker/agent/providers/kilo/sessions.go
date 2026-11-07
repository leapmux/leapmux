package kilo

import (
	"context"
	"path/filepath"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode/opencodestore"
)

// Kilo is a fork of OpenCode and ships OpenCode's `session` table unchanged, so
// this file supplies only the database path and delegates the query to
// opencodestore.ListSessions.

// kiloDataDir is where Kilo keeps its data. Same `xdg-basedir` rule as
// OpenCode, under the fork's own application name.
func kiloDataDir(q agent.StoredSessionQuery) string {
	base := q.XDGDataHome()
	if base == "" {
		return ""
	}
	return filepath.Join(base, "kilo")
}

// kiloDBPath resolves the current Kilo database. KILO_DB overrides its filename
// with an absolute path or a bare name under the current data directory.
func kiloDBPath(q agent.StoredSessionQuery) string {
	dir := kiloDataDir(q)
	if path, ok := sessionstore.OverridePath(q, "KILO_DB", dir); ok {
		return path
	}
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "kilo.db")
}

// kiloStoredSessions is Kilo's Provider.ListStoredSessions.
func kiloStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return opencodestore.ListSessions(ctx, kiloDBPath(q), q)
}

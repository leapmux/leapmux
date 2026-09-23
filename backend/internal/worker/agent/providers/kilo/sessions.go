package kilo

import (
	"context"
	"os"
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

// kiloDBPath resolves Kilo's session database.
//
// KILO_DB behaves like OPENCODE_DB: absolute, or a bare name under the data
// directory. Without it, `kilo.db` is the current name and `opencode.db` is the
// name a store carried before the fork renamed it -- Kilo still reads that one,
// so an installation that never re-created its database keeps working here.
// The legacy name is only used when the current one is absent, so a machine
// that holds both reads the live store.
func kiloDBPath(q agent.StoredSessionQuery) string {
	dir := kiloDataDir(q)
	if path, ok := sessionstore.OverridePath(q, "KILO_DB", dir); ok {
		return path
	}
	if dir == "" {
		return ""
	}
	current := filepath.Join(dir, "kilo.db")
	if _, err := os.Stat(current); err == nil {
		return current
	}
	legacy := filepath.Join(dir, "opencode.db")
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	// Neither is present. Return the current name so the caller reports the
	// absent store against the path a reader would expect to find.
	return current
}

// kiloStoredSessions is Kilo's Provider.ListStoredSessions.
func kiloStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return opencodestore.ListSessions(ctx, kiloDBPath(q), q)
}

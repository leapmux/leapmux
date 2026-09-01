package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// Kilo is a fork of OpenCode and ships OpenCode's `session` table unchanged, so
// this file supplies only the database path and delegates the query to
// openCodeFamilySessions.

// kiloDataDir is where Kilo keeps its data. Same `xdg-basedir` rule as
// OpenCode, under the fork's own application name.
func kiloDataDir(q StoredSessionQuery) string {
	base := q.xdgDataHome()
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
func kiloDBPath(q StoredSessionQuery) string {
	dir := kiloDataDir(q)
	if override := strings.TrimSpace(q.env("KILO_DB")); override != "" {
		if filepath.IsAbs(override) {
			return override
		}
		if dir == "" {
			return ""
		}
		return filepath.Join(dir, override)
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
func kiloStoredSessions(ctx context.Context, q StoredSessionQuery) ([]StoredSession, error) {
	return openCodeFamilySessions(ctx, kiloDBPath(q), q)
}

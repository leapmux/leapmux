package opencode

import (
	"context"
	"path/filepath"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode/opencodestore"
)

// opencodeDataDir is where OpenCode keeps its data, following the `xdg-basedir`
// npm package it is built on -- `~/.local/share` on every platform, macOS
// included. OPENCODE_CONFIG_DIR is deliberately NOT consulted: it moves the
// config, not the data.
func opencodeDataDir(q agent.StoredSessionQuery) string {
	base := q.XDGDataHome()
	if base == "" {
		return ""
	}
	return filepath.Join(base, "opencode")
}

// opencodeDBPath resolves OpenCode's session database.
//
// OPENCODE_DB takes either an absolute path or a bare file name relative to the
// data directory, which is how the CLI itself reads it.
//
// A non-stable install gives the file the name `opencode-<channel>.db`, and
// this reader does not find that: nothing in the launch path tells the worker
// which channel the installed CLI is, and probing every name that matches the
// pattern would pick a store the running CLI does not use. An operator on such
// a channel points OPENCODE_DB at the file.
func opencodeDBPath(q agent.StoredSessionQuery) string {
	dir := opencodeDataDir(q)
	if path, ok := sessionstore.OverridePath(q, "OPENCODE_DB", dir); ok {
		return path
	}
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "opencode.db")
}

// opencodeStoredSessions is OpenCode's Provider.ListStoredSessions.
func opencodeStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return opencodestore.ListSessions(ctx, opencodeDBPath(q), q)
}

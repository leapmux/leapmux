package agent

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gooseSessionsDDL is the subset of Goose's `sessions` table this reader
// touches. `updated_at` is TIMESTAMP, which SQLite stores as the text that was
// written.
const gooseSessionsDDL = `
CREATE TABLE sessions (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL DEFAULT '',
  session_type TEXT NOT NULL DEFAULT 'user',
  working_dir TEXT NOT NULL,
  created_at TIMESTAMP,
  updated_at TIMESTAMP,
  archived_at TIMESTAMP,
  parent_session_id TEXT
);`

type gooseSessionRow struct {
	id         string
	name       string
	kind       string
	workingDir string
	createdAt  string
	updatedAt  string
	archivedAt any
	parentID   any
}

func seedGooseDB(t *testing.T, path string, rows []gooseSessionRow) {
	t.Helper()
	db := newFixtureDB(t, path, gooseSessionsDDL)
	for _, r := range rows {
		_, err := db.Exec(
			`INSERT INTO sessions (id, name, session_type, working_dir, created_at, updated_at, archived_at, parent_session_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			r.id, r.name, r.kind, r.workingDir, r.createdAt, r.updatedAt, r.archivedAt, r.parentID)
		require.NoError(t, err)
	}
}

func TestGooseStoredSessions(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	home := t.TempDir()
	seedGooseDB(t, filepath.Join(home, ".local", "share", "goose", "sessions", "sessions.db"), []gooseSessionRow{
		{id: "20260830_2", name: "Newest", kind: "acp", workingDir: dir, createdAt: "2026-08-30 10:00:00", updatedAt: "2026-08-30 15:54:13"},
		{id: "20260830_1", name: "Older", kind: "user", workingDir: dir, createdAt: "2026-08-30 09:00:00", updatedAt: "2026-08-30 09:30:00"},
		{id: "20260830_3", name: "Delegated task", kind: "sub_agent", workingDir: dir, createdAt: "2026-08-30 16:00:00", updatedAt: "2026-08-30 16:00:00", parentID: "20260830_2"},
		{id: "20260830_4", name: "Put away", kind: "acp", workingDir: dir, createdAt: "2026-08-30 17:00:00", updatedAt: "2026-08-30 17:00:00", archivedAt: "2026-08-30 17:05:00"},
		{id: "20260830_5", name: "Elsewhere", kind: "acp", workingDir: "/other/dir", createdAt: "2026-08-30 18:00:00", updatedAt: "2026-08-30 18:00:00"},
	})

	got, err := gooseStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"20260830_2", "20260830_1"}, handlesOf(got),
		"the subagent, the archived row and the other directory are excluded")
	assert.Equal(t, "Newest", got[0].Title)
	assert.Equal(t, time.Date(2026, 8, 30, 15, 54, 13, 0, time.UTC), got[0].UpdatedAt)
}

// TestGooseStoredSessions_KeepsACPSessions pins the filter that a narrower one
// would break. LeapMux launches Goose over ACP, so every session it creates is
// typed `acp` -- filtering to `session_type = 'user'` would hide exactly the
// sessions this picker exists to offer.
func TestGooseStoredSessions_KeepsACPSessions(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	home := t.TempDir()
	seedGooseDB(t, filepath.Join(home, ".local", "share", "goose", "sessions", "sessions.db"), []gooseSessionRow{
		{id: "acp_only", name: "Started by LeapMux", kind: "acp", workingDir: dir, createdAt: "2026-08-30 10:00:00", updatedAt: "2026-08-30 10:00:00"},
	})

	got, err := gooseStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"acp_only"}, handlesOf(got))
}

func TestGooseStoredSessions_AbsentStoreIsEmpty(t *testing.T) {
	t.Parallel()
	got, err := gooseStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: "/Users/dev/project", HomeDir: t.TempDir(), Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestGooseStoredSessions_NeedsTheSessionsSubdirectory pins the path trap: the
// database is `<data>/sessions/sessions.db`, and the doubled name is easy to
// drop. A store one level up is simply absent.
func TestGooseStoredSessions_NeedsTheSessionsSubdirectory(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	home := t.TempDir()
	seedGooseDB(t, filepath.Join(home, ".local", "share", "goose", "sessions.db"), []gooseSessionRow{
		{id: "s1", name: "n", kind: "acp", workingDir: dir, createdAt: "2026-08-30 10:00:00", updatedAt: "2026-08-30 10:00:00"},
	})

	got, err := gooseStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestGooseDBCandidates(t *testing.T) {
	t.Parallel()
	home := "/home/dev"

	// An ABSOLUTE GOOSE_PATH_ROOT wins and is probed first.
	rooted := StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(map[string]string{"GOOSE_PATH_ROOT": "/goose-root"})}
	assert.Equal(t, filepath.Join("/goose-root", "data", "sessions", "sessions.db"), gooseDBCandidates(rooted)[0])

	// A RELATIVE one is ignored, exactly as Goose ignores it.
	relative := StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(map[string]string{"GOOSE_PATH_ROOT": "relative/path"})}
	assert.Equal(t, filepath.Join(home, ".local", "share", "goose", "sessions", "sessions.db"), gooseDBCandidates(relative)[0])

	// The legacy Apple location is probed on macOS, after the XDG one.
	plain := gooseDBCandidates(StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(nil)})
	assert.Equal(t, filepath.Join(home, ".local", "share", "goose", "sessions", "sessions.db"), plain[0])
	if runtime.GOOS == "darwin" {
		assert.Contains(t, plain[len(plain)-1], filepath.Join("Library", "Application Support", "Block", "goose"))
	}
}

func TestGooseDBPath_PrefersTheCandidateThatExists(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	// Only the legacy location holds a store, so it wins over the XDG path
	// that resolves first but is empty.
	legacy := filepath.Join(home, "Library", "Application Support", "Block", "goose", "sessions", "sessions.db")
	writeFixtureFile(t, legacy, "")

	q := StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(nil)}
	if runtime.GOOS != "darwin" {
		t.Skip("the legacy Apple location is only probed on macOS")
	}
	assert.Equal(t, legacy, gooseDBPath(q))
}

func TestParseGooseTimestamp(t *testing.T) {
	t.Parallel()
	want := time.Date(2026, 8, 30, 15, 54, 13, 0, time.UTC)
	assert.Equal(t, want, parseGooseTimestamp("2026-08-30 15:54:13"))
	assert.Equal(t, want, parseGooseTimestamp("2026-08-30T15:54:13"))
	assert.Equal(t, want, parseGooseTimestamp("2026-08-30T15:54:13Z"))
	assert.Equal(t, want.Add(500*time.Millisecond), parseGooseTimestamp("2026-08-30 15:54:13.5"))
	// A value with no zone is UTC, not local: the column holds what
	// CURRENT_TIMESTAMP wrote, and that is UTC.
	assert.Equal(t, time.UTC, parseGooseTimestamp("2026-08-30 15:54:13").Location())
	assert.True(t, parseGooseTimestamp("").IsZero())
	assert.True(t, parseGooseTimestamp("nonsense").IsZero())
}

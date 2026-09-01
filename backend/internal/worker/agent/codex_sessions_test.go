package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codexThreadsDDL is the subset of Codex's `threads` table this reader touches.
const codexThreadsDDL = `
CREATE TABLE threads (
  id TEXT PRIMARY KEY,
  rollout_path TEXT,
  cwd TEXT,
  title TEXT,
  name TEXT,
  preview TEXT,
  archived INTEGER,
  updated_at INTEGER,
  updated_at_ms INTEGER,
  recency_at_ms INTEGER
);`

type codexThreadRow struct {
	id          string
	cwd         string
	title       string
	name        string
	preview     string
	archived    int
	updatedAt   int64
	updatedAtMS int64
	recencyMS   int64
}

func seedCodexDB(t *testing.T, path string, rows []codexThreadRow) {
	t.Helper()
	db := newFixtureDB(t, path, codexThreadsDDL)
	for _, r := range rows {
		requireHostAbsDir(t, r.cwd)
		_, err := db.Exec(
			`INSERT INTO threads (id, cwd, title, name, preview, archived, updated_at, updated_at_ms, recency_at_ms)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.id, r.cwd, r.title, r.name, r.preview, r.archived, r.updatedAt, r.updatedAtMS, r.recencyMS)
		require.NoError(t, err)
	}
}

func TestCodexStoredSessions(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")
	home := t.TempDir()
	seedCodexDB(t, filepath.Join(home, ".codex", "state_5.sqlite"), []codexThreadRow{
		{id: "t_named", cwd: dir, name: "What the user called it", title: "model title", preview: "prompt", updatedAtMS: 5_000},
		{id: "t_title", cwd: dir, title: "model title", preview: "prompt", updatedAtMS: 4_000},
		{id: "t_preview", cwd: dir, preview: "just the prompt", updatedAtMS: 3_000},
		{id: "t_recency", cwd: dir, title: "recency only", recencyMS: 2_000},
		{id: "t_seconds", cwd: dir, title: "seconds only", updatedAt: 1},
		{id: "t_archived", cwd: dir, title: "put away", archived: 1, updatedAtMS: 9_000},
		{id: "t_elsewhere", cwd: absPath("other", "dir"), title: "elsewhere", updatedAtMS: 9_000},
	})

	got, err := codexStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)

	assert.Equal(t,
		[]string{"t_named", "t_title", "t_preview", "t_recency", "t_seconds"},
		handlesOf(got),
		"newest first; the archived thread and the other directory are excluded")

	// Title precedence: the user's name, then the model's title, then the
	// stored preview of the prompt.
	assert.Equal(t, "What the user called it", got[0].Title)
	assert.Equal(t, "model title", got[1].Title)
	assert.Equal(t, "just the prompt", got[2].Title)

	// updated_at_ms, then recency_at_ms, then the SECONDS column scaled up.
	assert.Equal(t, time.UnixMilli(5_000).UTC(), got[0].UpdatedAt)
	assert.Equal(t, time.UnixMilli(2_000).UTC(), got[3].UpdatedAt)
	assert.Equal(t, time.UnixMilli(1_000).UTC(), got[4].UpdatedAt)
}

func TestCodexStoredSessions_AbsentStoreIsEmpty(t *testing.T) {
	t.Parallel()
	got, err := codexStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: absPath("Users", "dev", "project"), HomeDir: t.TempDir(), Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestCodexStoredSessions_EmptyWorkingDirListsNothing(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	seedCodexDB(t, filepath.Join(home, ".codex", "state_5.sqlite"), []codexThreadRow{
		{id: "t1", cwd: absPath("Users", "dev", "project"), title: "x", updatedAtMS: 1},
	})
	got, err := codexStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: "", HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestCodexStateDBPath(t *testing.T) {
	t.Parallel()
	home := absPath("home", "dev")

	plain := StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(nil)}
	assert.Equal(t, filepath.Join(home, ".codex", "state_5.sqlite"), codexStateDBPath(plain))

	// CODEX_HOME moves the whole home, and may be written with a leading `~`.
	moved := StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(map[string]string{"CODEX_HOME": "~/alt-codex"})}
	assert.Equal(t, filepath.Join(home, "alt-codex", "state_5.sqlite"), codexStateDBPath(moved))

	// CODEX_SQLITE_HOME moves only the databases, and wins over CODEX_HOME.
	fastDisk := absPath("fast-disk")
	split := StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(map[string]string{
		"CODEX_HOME": absPath("codex"), "CODEX_SQLITE_HOME": fastDisk,
	})}
	assert.Equal(t, filepath.Join(fastDisk, "state_5.sqlite"), codexStateDBPath(split))
}

func TestCodexStoredSessions_ReadsThroughTheProvider(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")
	home := t.TempDir()
	seedCodexDB(t, filepath.Join(home, ".codex", "state_5.sqlite"), []codexThreadRow{
		{id: "t1", cwd: dir, title: "Hi", updatedAtMS: 1_000},
	})

	got, err := codexProvider{}.ListStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"t1"}, handlesOf(got))
}

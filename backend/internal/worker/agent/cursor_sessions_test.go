package agent

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cursorStoreDDL is the pair of tables a Cursor session database holds. Only
// `meta` is read; `blobs` is the transcript.
const cursorStoreDDL = `
CREATE TABLE blobs (id TEXT PRIMARY KEY, data BLOB);
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT);`

// writeCursorSession writes one `chats/<md5 cwd>/<agent id>/store.db` holding
// the hex-encoded metadata row Cursor stores.
func writeCursorSession(t *testing.T, home, cwd, agentID, metaJSON string, at time.Time) {
	t.Helper()
	dir := filepath.Join(home, ".cursor", "chats", cursorChatsDirFor(cwd), agentID)
	path := filepath.Join(dir, "store.db")
	db := newFixtureDB(t, path, cursorStoreDDL)
	_, err := db.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)`,
		cursorMetaKey, hex.EncodeToString([]byte(metaJSON)))
	require.NoError(t, err)
	require.NoError(t, db.Close())
	touchFixture(t, path, at)
	touchFixture(t, dir, at)
}

func TestCursorChatsDirFor(t *testing.T) {
	t.Parallel()
	// Pinned against the real directory names on the machine this was written
	// against: the name is the lowercase hex MD5 of the path.
	assert.Equal(t, "8be94af324fccc7c78826d90b29fd261", cursorChatsDirFor("/Users/trustin"))
	assert.Equal(t, "7fdc7fd0bcc9d27782563a373fdb6adf", cursorChatsDirFor("/Users/trustin/Workspaces/leapmux"))
}

func TestCursorStoredSessions(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	home := t.TempDir()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	writeCursorSession(t, home, dir, "agent-new",
		`{"agentId":"agent-new","name":"Enforce Gating Mechanically","mode":"default","createdAt":1775273208025}`, base)
	writeCursorSession(t, home, dir, "agent-old",
		`{"agentId":"agent-old","name":"New Agent","mode":"default","createdAt":1775273108025}`, base.Add(-time.Hour))
	// A subagent, stored as a SIBLING of the session that spawned it.
	writeCursorSession(t, home, dir, "agent-sub",
		`{"agentId":"agent-sub","name":"New Agent","subagentInfo":{"parentAgentId":"agent-new"}}`, base.Add(time.Hour))
	// Another working directory hashes to another folder entirely.
	writeCursorSession(t, home, "/other/dir", "agent-elsewhere",
		`{"agentId":"agent-elsewhere","name":"Elsewhere"}`, base.Add(2*time.Hour))

	got, err := cursorStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"agent-new", "agent-old"}, handlesOf(got),
		"the subagent and the other directory are excluded")
	assert.Equal(t, "Enforce Gating Mechanically", got[0].Title)
	assert.Equal(t, "New Agent", got[1].Title, "Cursor's own default label is a truthful answer")
}

// TestCursorStoredSessions_UsesTheWALModificationTime pins why the -wal is
// consulted: a session written to but not checkpointed leaves store.db
// untouched, so store.db alone dates an active session to when it was created.
func TestCursorStoredSessions_UsesTheWALModificationTime(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	home := t.TempDir()
	old := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	writeCursorSession(t, home, dir, "agent-1", `{"agentId":"agent-1","name":"Active"}`, old)
	wal := filepath.Join(home, ".cursor", "chats", cursorChatsDirFor(dir), "agent-1", "store.db-wal")
	writeFixtureFile(t, wal, "not a real wal, only its timestamp matters here")
	touchFixture(t, wal, recent)

	got, err := cursorStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, recent, got[0].UpdatedAt.UTC())
}

func TestCursorStoredSessions_SkipsUnreadableSessions(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	home := t.TempDir()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	chats := filepath.Join(home, ".cursor", "chats", cursorChatsDirFor(dir))

	writeCursorSession(t, home, dir, "agent-good", `{"agentId":"agent-good","name":"Fine"}`, base)
	// A session directory with no database.
	writeFixtureFile(t, filepath.Join(chats, "agent-nodb", "notes.txt"), "x")
	// A database with the tables but no metadata row.
	newFixtureDB(t, filepath.Join(chats, "agent-nometa", "store.db"), cursorStoreDDL)
	// A metadata row whose value decodes to neither hex-JSON nor JSON.
	db := newFixtureDB(t, filepath.Join(chats, "agent-junk", "store.db"), cursorStoreDDL)
	_, err := db.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)`, cursorMetaKey, "zz not hex zz")
	require.NoError(t, err)

	got, err := cursorStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"agent-good"}, handlesOf(got))
}

func TestDecodeCursorMeta(t *testing.T) {
	t.Parallel()

	// The stored form: JSON, hex-encoded into a TEXT column.
	meta, ok := decodeCursorMeta(hex.EncodeToString([]byte(`{"agentId":"a","name":"n"}`)))
	require.True(t, ok)
	assert.Equal(t, "a", meta.AgentID)
	assert.Equal(t, "n", meta.Name)

	// Plain JSON is accepted too, so a future version that drops the encoding
	// still reads here rather than reporting no sessions.
	meta, ok = decodeCursorMeta(`{"agentId":"b","name":"m"}`)
	require.True(t, ok)
	assert.Equal(t, "b", meta.AgentID)

	_, ok = decodeCursorMeta("not hex and not json")
	assert.False(t, ok)
}

func TestCursorStoredSessions_AbsentStoreIsEmpty(t *testing.T) {
	t.Parallel()
	got, err := cursorStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: "/Users/dev/project", HomeDir: t.TempDir(), Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestCursorStoredSessions_FallsBackToTheDirectoryName(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	home := t.TempDir()
	writeCursorSession(t, home, dir, "agent-from-dir", `{"name":"No id in the row"}`,
		time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))

	got, err := cursorStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"agent-from-dir"}, handlesOf(got),
		"the directory name is the agent id, so a metadata row that lost it is still resumable")
}

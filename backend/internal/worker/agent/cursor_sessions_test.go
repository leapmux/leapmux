package agent

import (
	"context"
	"encoding/hex"
	"os"
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

// TestCursorStoredSessions_TimesByStoreDBOnly is the regression test for a
// defect this reader had against real stores: every Cursor session was dated to
// the moment LeapMux first listed it.
//
// Reading a session opens its database, and a read-only open of a WAL database
// still creates the `-shm` sidecar (see
// TestOpenReadOnly_CreatesShmButLeavesTheDatabaseFile). Adding that file
// updates the mtime of the session DIRECTORY that holds it, and it can create
// the `-wal` too. So neither is Cursor's writing -- both report LeapMux's own
// footprint, and a reader that trusts them collapses every row to one
// timestamp and loses the order the picker exists to give.
//
// Both are set here to a time later than `store.db`, and neither may win.
func TestCursorStoredSessions_TimesByStoreDBOnly(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	home := t.TempDir()
	stored := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	footprint := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	writeCursorSession(t, home, dir, "agent-1", `{"agentId":"agent-1","name":"Active"}`, stored)
	sessionDir := filepath.Join(home, ".cursor", "chats", cursorChatsDirFor(dir), "agent-1")
	wal := filepath.Join(sessionDir, "store.db-wal")
	writeFixtureFile(t, wal, "a sidecar the read-only open can create")
	touchFixture(t, wal, footprint)
	touchFixture(t, sessionDir, footprint)

	got, err := cursorStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, stored, got[0].UpdatedAt.UTC(),
		"neither the session directory nor the -wal may date a session")
}

// The ordering runs on the same signal, so it must survive the same footprint:
// the newest `store.db` sorts first even when an older session's directory was
// touched later.
func TestCursorStoredSessions_OrdersByStoreDBNotDirectory(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	home := t.TempDir()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	writeCursorSession(t, home, dir, "agent-newest", `{"agentId":"agent-newest","name":"Newest"}`, base)
	writeCursorSession(t, home, dir, "agent-oldest", `{"agentId":"agent-oldest","name":"Oldest"}`, base.Add(-48*time.Hour))
	// The older session's directory looks brand new, as it would after LeapMux
	// listed it once and left a sidecar behind.
	touchFixture(t, filepath.Join(home, ".cursor", "chats", cursorChatsDirFor(dir), "agent-oldest"),
		base.Add(time.Hour))

	got, err := cursorStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"agent-newest", "agent-oldest"}, handlesOf(got))
}

// The candidate cut runs before any database opens, so a store the scan cannot
// stat is not a session. A directory with no `store.db` is Cursor's own
// leftover, and counting it would spend one of the `Limit` slots on nothing.
func TestCursorStoredSessions_SkipsDirectoriesWithNoStore(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	home := t.TempDir()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	writeCursorSession(t, home, dir, "agent-real", `{"agentId":"agent-real","name":"Real"}`, base)
	require.NoError(t, os.MkdirAll(
		filepath.Join(home, ".cursor", "chats", cursorChatsDirFor(dir), "agent-empty"), 0o755))

	got, err := cursorStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil), Limit: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"agent-real"}, handlesOf(got))
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

// A stray file in the chats folder is not a session. The guard used to be a
// `keep` filter handed to `newestEntries`; it moved into the reader's own scan
// with the store-time fix, and it is the same invariant either way.
func TestCursorStoredSessions_IgnoresNonDirectoryEntries(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	home := t.TempDir()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	writeCursorSession(t, home, dir, "agent-real", `{"agentId":"agent-real","name":"Real"}`, base)
	// Newer than the session, so it would sort FIRST if it were counted.
	stray := filepath.Join(home, ".cursor", "chats", cursorChatsDirFor(dir), "index.json")
	writeFixtureFile(t, stray, `{"not":"a session"}`)
	touchFixture(t, stray, base.Add(time.Hour))

	got, err := cursorStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"agent-real"}, handlesOf(got))
}

// The cut happens on the candidates, before any database opens, so a limit
// smaller than the directory keeps the NEWEST rows and not the first ones read.
func TestCursorStoredSessions_RespectsTheLimit(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	home := t.TempDir()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	writeCursorSession(t, home, dir, "agent-c", `{"agentId":"agent-c","name":"C"}`, base.Add(-3*time.Hour))
	writeCursorSession(t, home, dir, "agent-a", `{"agentId":"agent-a","name":"A"}`, base)
	writeCursorSession(t, home, dir, "agent-b", `{"agentId":"agent-b","name":"B"}`, base.Add(-time.Hour))

	got, err := cursorStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil), Limit: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"agent-a", "agent-b"}, handlesOf(got))
}

// Two sessions written in the same filesystem tick must still list in one
// fixed order. Without the tie-break the answer follows the directory read
// order, so the menu reshuffles between two openings of the same dialog.
func TestCursorStoredSessions_BreaksTimeTiesByName(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	home := t.TempDir()
	same := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	writeCursorSession(t, home, dir, "agent-zulu", `{"agentId":"agent-zulu","name":"Z"}`, same)
	writeCursorSession(t, home, dir, "agent-alpha", `{"agentId":"agent-alpha","name":"A"}`, same)

	got, err := cursorStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"agent-alpha", "agent-zulu"}, handlesOf(got))
}

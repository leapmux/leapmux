package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const zcodeArtifactFixtureID = "tool-result-00000000-0000-4000-8000-000000000001"
const zcodeArtifactFixtureURI = "zcode-artifact://session/" + zcodeArtifactFixtureID
const zcodeArtifactFixtureData = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAYAAABytg0kAAAAEUlEQVR4nGP4z8DwH4QZYAwAR8oH+WdZbrcAAAAASUVORK5CYII="
const zcodeToolStoreDDL = `CREATE TABLE part (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, message_id TEXT NOT NULL, data TEXT NOT NULL);
CREATE INDEX part_message_idx ON part(message_id);
CREATE INDEX part_session_idx ON part(session_id);`

func zcodeNativeToolFixture(status string) string {
	return `{"type":"tool","callID":"call","tool":"mcp__docs__read","futureCounter":9007199254740993,"state":{"status":"` + status + `","input":{"topic":"retained"},"output":"image","metadata":{"modelContentLayout":[{"type":"text","text":"Before image"},{"type":"attachment","attachmentIndex":0}]},"attachments":[{"type":"file","sessionID":"session","messageID":"message","mime":"image/png","url":"` + zcodeArtifactFixtureURI + `"}]}}`
}

func TestZCodeToolStoreReadsOriginalRecordsAndArtifacts(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	location := zcodeToolStoreLocation{databasePath: filepath.Join(directory, "store.db"), artifactRoot: filepath.Join(directory, "artifacts"), sessionID: "session"}
	db := newFixtureDB(t, location.databasePath, zcodeToolStoreDDL)
	original := zcodeNativeToolFixture("completed")
	_, err := db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, original)
	require.NoError(t, err)
	artifactDirectory := filepath.Join(location.artifactRoot, "session")
	require.NoError(t, os.MkdirAll(artifactDirectory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(artifactDirectory, "call-media-1-"+zcodeArtifactFixtureID+".txt"), []byte(zcodeArtifactFixtureData), 0o600))

	for _, messageID := range []string{"message", ""} {
		records, err := readZCodeToolRecords(t.Context(), newZCodeToolStoreForTest(t), &zcodeArtifactCache{}, location, map[string]zcodeToolLookup{"call": {messageID: messageID, toolName: "mcp__docs__read"}})
		require.NoError(t, err)
		require.Len(t, records, 1)
		assert.Equal(t, original, string(records["call"].native.Data))
		assert.Equal(t, "part", records["call"].native.ID)
		assert.True(t, records["call"].ready)
		assert.Equal(t, zcodeArtifactFixtureData, records["call"].artifacts[zcodeArtifactFixtureURI])
	}
	var persisted string
	require.NoError(t, db.QueryRow(`SELECT data FROM part WHERE id = 'part'`).Scan(&persisted))
	assert.Equal(t, original, persisted)
}

func TestZCodeToolStoreWaitsForTheCompletedRecord(t *testing.T) {
	t.Parallel()
	location := zcodeToolStoreLocation{databasePath: filepath.Join(t.TempDir(), "store.db"), sessionID: "session"}
	db := newFixtureDB(t, location.databasePath, zcodeToolStoreDDL)
	_, err := db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, zcodeNativeToolFixture("running"))
	require.NoError(t, err)
	request := map[string]zcodeToolLookup{"call": {messageID: "message"}}
	records, err := readZCodeToolRecords(t.Context(), newZCodeToolStoreForTest(t), &zcodeArtifactCache{}, location, request)
	require.NoError(t, err)
	assert.Empty(t, records)
	_, err = db.Exec(`UPDATE part SET data = ?`, zcodeNativeToolFixture("completed"))
	require.NoError(t, err)
	records, err = readZCodeToolRecords(t.Context(), newZCodeToolStoreForTest(t), &zcodeArtifactCache{}, location, request)
	require.Error(t, err, "the artifact directory is absent")
	require.Len(t, records, 1, "missing artifacts must not discard the native record")
	assert.Empty(t, records["call"].artifacts)
	assert.False(t, records["call"].ready)
}

func TestZCodeToolStoreRejectsUnrelatedAndAmbiguousRecords(t *testing.T) {
	t.Parallel()
	location := zcodeToolStoreLocation{databasePath: filepath.Join(t.TempDir(), "store.db"), sessionID: "session"}
	db := newFixtureDB(t, location.databasePath, zcodeToolStoreDDL)
	_, err := db.Exec(`INSERT INTO part VALUES ('first', 'session', 'message', ?), ('second', 'session', 'other-message', ?)`, zcodeNativeToolFixture("completed"), zcodeNativeToolFixture("completed"))
	require.NoError(t, err)
	for _, request := range []map[string]zcodeToolLookup{
		{"call": {}},
		{"call": {messageID: "foreign-message"}},
		{"call": {messageID: "message", toolName: "other-tool"}},
		{"foreign-call": {messageID: "message"}},
		{"call' OR 1=1 --": {messageID: "message"}},
	} {
		records, err := readZCodeToolRecords(t.Context(), newZCodeToolStoreForTest(t), &zcodeArtifactCache{}, location, request)
		require.NoError(t, err)
		assert.Empty(t, records)
	}
	location.sessionID = "foreign-session"
	records, err := readZCodeToolRecords(t.Context(), newZCodeToolStoreForTest(t), &zcodeArtifactCache{}, location, map[string]zcodeToolLookup{"call": {messageID: "message"}})
	require.NoError(t, err)
	assert.Empty(t, records)
}

func TestZCodeToolStoreKeepsValidRecordsBesideMalformedData(t *testing.T) {
	t.Parallel()
	location := zcodeToolStoreLocation{databasePath: filepath.Join(t.TempDir(), "store.db"), sessionID: "session"}
	db := newFixtureDB(t, location.databasePath, zcodeToolStoreDDL)
	original := strings.Replace(zcodeNativeToolFixture("completed"), `"attachments":[`, `"attachments":[null,17,`, 1)
	_, err := db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?), ('bad-json', 'session', 'message', '{'), ('not-tool', 'session', 'message', '{"type":"text","callID":"call"}')`, original)
	require.NoError(t, err)
	records, err := readZCodeToolRecords(t.Context(), newZCodeToolStoreForTest(t), &zcodeArtifactCache{}, location, map[string]zcodeToolLookup{"call": {messageID: "message"}})
	require.Error(t, err, "the artifact directory is absent")
	require.Len(t, records, 1)
	assert.Equal(t, original, string(records["call"].native.Data))
}

func TestZCodeArtifactURIRejectsOtherSessionsAndPaths(t *testing.T) {
	t.Parallel()
	assert.Equal(t, zcodeArtifactFixtureID, zcodeArtifactID(zcodeArtifactFixtureURI, "session"))
	for _, uri := range []string{
		"file:///session/" + zcodeArtifactFixtureID,
		"zcode-artifact://foreign/" + zcodeArtifactFixtureID,
		"zcode-artifact://user@session/" + zcodeArtifactFixtureID,
		zcodeArtifactFixtureURI + "?query=1",
		zcodeArtifactFixtureURI + "?",
		zcodeArtifactFixtureURI + "#fragment",
		"zcode-artifact://session/../" + zcodeArtifactFixtureID,
		"zcode-artifact://session/%2e%2e/" + zcodeArtifactFixtureID,
		"zcode-artifact://session/prefix-" + zcodeArtifactFixtureID,
	} {
		assert.Empty(t, zcodeArtifactID(uri, "session"), uri)
	}
	assert.Equal(t, "a__b", zcodeArtifactSegment("a😀b"))
	assert.Equal(t, "a_b", zcodeArtifactSegment("a/b"))
	assert.Equal(t, strings.Repeat("x", 120), zcodeArtifactSegment(strings.Repeat("x", 121)))
	assert.Equal(t, "unknown", zcodeArtifactSegment(""))
}

func TestReadZCodeArtifactSupportsBinaryAndEnforcesTheSizeLimit(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "binary.png"), []byte{1, 2, 3}, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "image.txt"), []byte(zcodeArtifactFixtureData), 0o600))
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	data, err := readZCodeArtifact(root, "binary.png", "image/png", 100)
	require.NoError(t, err)
	assert.Equal(t, "data:image/png;base64,AQID", data)
	_, err = readZCodeArtifact(root, "binary.png", "image/png", 3)
	require.Error(t, err, "base64 encoding also counts toward the limit")
	_, err = readZCodeArtifact(root, "image.txt", "image/png", 10)
	require.Error(t, err)
	_, err = readZCodeArtifact(root, "missing.txt", "image/png", 100)
	require.Error(t, err)
	_, err = readZCodeArtifact(root, ".", "image/png", 100)
	require.Error(t, err)
}

func TestZCodeToolStoreDoesNotCreateAnAbsentDatabase(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "absent.db")
	records, err := readZCodeToolRecords(t.Context(), newZCodeToolStoreForTest(t), &zcodeArtifactCache{}, zcodeToolStoreLocation{databasePath: path, sessionID: "session"}, map[string]zcodeToolLookup{"call": {}})
	require.NoError(t, err)
	assert.Empty(t, records)
	_, err = os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestZCodeStoredToolMarshalsWithoutChangingProviderNumbers(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(zcodeStoredTool{Data: json.RawMessage(zcodeNativeToolFixture("completed"))})
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"futureCounter":9007199254740993`)
}

// The store keeps one handle on the session database, and it keeps the artifacts
// it already read. A second read answers from both: the transcript reads the
// store for EVERY agent message, and a directory sweep for each of those reads
// spent the budget that the read itself has.
func TestZCodeToolStoreAnswersASecondReadFromItsOwnCache(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	location := zcodeToolStoreLocation{databasePath: filepath.Join(directory, "store.db"), artifactRoot: filepath.Join(directory, "artifacts"), sessionID: "session"}
	db := newFixtureDB(t, location.databasePath, zcodeToolStoreDDL)
	_, err := db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, zcodeNativeToolFixture("completed"))
	require.NoError(t, err)
	artifactDirectory := filepath.Join(location.artifactRoot, "session")
	require.NoError(t, os.MkdirAll(artifactDirectory, 0o700))
	artifact := filepath.Join(artifactDirectory, "call-media-1-"+zcodeArtifactFixtureID+".txt")
	require.NoError(t, os.WriteFile(artifact, []byte(zcodeArtifactFixtureData), 0o600))

	store := newZCodeToolStoreForTest(t)
	artifacts := &zcodeArtifactCache{}
	request := map[string]zcodeToolLookup{"call": {messageID: "message", toolName: "mcp__docs__read"}}
	records, err := readZCodeToolRecords(t.Context(), store, artifacts, location, request)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, zcodeArtifactFixtureData, records["call"].artifacts[zcodeArtifactFixtureURI])

	// The whole artifact tree goes. A read that swept the directory now fails.
	require.NoError(t, os.RemoveAll(location.artifactRoot))
	records, err = readZCodeToolRecords(t.Context(), store, artifacts, location, request)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.True(t, records["call"].ready)
	assert.Equal(t, zcodeArtifactFixtureData, records["call"].artifacts[zcodeArtifactFixtureURI])
}

// newZCodeToolStoreForTest closes the store's database handle when the test ends,
// so an open file cannot defeat the temporary directory's own cleanup.
func newZCodeToolStoreForTest(t *testing.T) *zcodeToolStore {
	t.Helper()
	store := &zcodeToolStore{}
	t.Cleanup(store.close)
	return store
}

// The artifact cache belongs to ONE TRANSCRIPT'S TURN, not to the agent and not to
// the store that every transcript shares.
//
// Each entry is a fully decoded data URI as large as liveStdoutMaxTokenSize
// (16 MiB), and only a pass of the same turn can read one -- the turn end clears
// the pending set a later pass would ask about. Held for the life of the agent
// instead, a session of computer-use screenshots retained every screenshot. Shared
// with the children instead, one subagent finishing emptied the parent's.
func TestZCodeToolStoreDropsItsArtifactsAtTheTurnEnd(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	location := zcodeToolStoreLocation{databasePath: filepath.Join(directory, "store.db"), artifactRoot: filepath.Join(directory, "artifacts"), sessionID: "session"}
	db := newFixtureDB(t, location.databasePath, zcodeToolStoreDDL)
	_, err := db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, zcodeNativeToolFixture("completed"))
	require.NoError(t, err)
	artifactDirectory := filepath.Join(location.artifactRoot, "session")
	require.NoError(t, os.MkdirAll(artifactDirectory, 0o700))
	artifact := filepath.Join(artifactDirectory, "call-media-1-"+zcodeArtifactFixtureID+".txt")
	require.NoError(t, os.WriteFile(artifact, []byte(zcodeArtifactFixtureData), 0o600))

	store := newZCodeToolStoreForTest(t)
	source := newZCodeToolSource(store, func() zcodeToolStoreLocation { return location })
	child, ok := source.newChild().(*zcodeToolSource)
	require.True(t, ok)
	require.Same(t, store, child.store, "a child shares the one database handle")
	require.NotSame(t, source.artifacts, child.artifacts, "a child owns its own artifact cache")

	request := map[string]zcodeToolLookup{"call": {messageID: "message", toolName: "mcp__docs__read"}}
	_, err = readZCodeToolRecords(t.Context(), store, source.artifacts, location, request)
	require.NoError(t, err)
	require.Equal(t, zcodeArtifactFixtureData, source.artifacts.lookup(zcodeArtifactFixtureURI),
		"the read caches the artifact it decoded")

	// A subagent reaches its turn end the instant its Agent result lands, which is
	// mid-turn for the parent. It must not empty what the parent already decoded.
	child.finishTurn()
	assert.Equal(t, zcodeArtifactFixtureData, source.artifacts.lookup(zcodeArtifactFixtureURI),
		"a child's turn end must not drop the parent's artifact bodies")

	source.finishTurn()

	assert.Empty(t, source.artifacts.lookup(zcodeArtifactFixtureURI), "the turn end drops the artifact bodies it cached")
	store.mu.Lock()
	handle := store.db
	store.mu.Unlock()
	assert.NotNil(t, handle, "the database handle is per agent and must survive the turn")
}

// A stat that fails says nothing about the handle already open.
//
// The stat exists to notice a store REPLACED at the same path. Treating any stat
// failure as a replacement discarded a working handle for a rename window or an EIO,
// and openSessionStoreDB then failed too and reported errSessionStoreAbsent -- so the
// pass enriched nothing and every artifact it had decoded was read again.
func TestZCodeToolStoreKeepsItsHandleWhenAStatFails(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "store.db")
	location := zcodeToolStoreLocation{databasePath: path, sessionID: "session"}
	db := newFixtureDB(t, path, zcodeToolStoreDDL)
	_, err := db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, zcodeNativeToolFixture("completed"))
	require.NoError(t, err)

	store := newZCodeToolStoreForTest(t)
	artifacts := &zcodeArtifactCache{}
	request := map[string]zcodeToolLookup{"call": {messageID: "message", toolName: "Bash"}}
	_, err = readZCodeToolRecords(t.Context(), store, artifacts, location, request)
	require.NoError(t, err)
	store.mu.Lock()
	first := store.db
	store.mu.Unlock()
	require.NotNil(t, first)

	// A stat failure that is NOT a replacement: the directory loses its search
	// permission, so os.Stat fails with EACCES while the file is still there and the
	// open handle still answers.
	require.NoError(t, os.Chmod(directory, 0o000))
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
	if _, statErr := os.Stat(path); statErr == nil {
		t.Skip("this user can stat through a directory with no search permission")
	}

	store.mu.Lock()
	kept, err := store.handle(t.Context(), path)
	store.mu.Unlock()
	require.NoError(t, err)
	assert.Same(t, first, kept, "a stat failure must not close a working handle")
}

// A store deleted and recreated at the SAME path must reopen.
//
// The cached handle holds one connection with no lifetime, so it stays open on the
// unlinked inode and answers every later read from the deleted file. A path
// comparison cannot see that, because ZCode's database path never changes within
// one agent. Cursor and Reasonix already compared by inode; see sessionStoreMoved.
func TestZCodeToolStoreReopensAStoreReplacedAtTheSamePath(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "store.db")
	location := zcodeToolStoreLocation{databasePath: path, artifactRoot: filepath.Join(directory, "artifacts"), sessionID: "session"}
	db := newFixtureDB(t, path, zcodeToolStoreDDL)
	_, err := db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, zcodeNativeToolFixture("completed"))
	require.NoError(t, err)

	store := newZCodeToolStoreForTest(t)
	artifacts := &zcodeArtifactCache{}
	request := map[string]zcodeToolLookup{"call": {messageID: "message", toolName: "Bash"}}
	_, err = readZCodeToolRecords(t.Context(), store, artifacts, location, request)
	require.NoError(t, err)
	store.mu.Lock()
	first := store.db
	store.mu.Unlock()
	require.NotNil(t, first)

	// The runtime replaces its store: the old inode is unlinked and a new file
	// takes the same name.
	require.NoError(t, os.Remove(path))
	replacement := newFixtureDB(t, path, zcodeToolStoreDDL)
	_, err = replacement.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, zcodeNativeToolFixture("completed"))
	require.NoError(t, err)

	_, err = readZCodeToolRecords(t.Context(), store, artifacts, location, request)
	require.NoError(t, err)
	store.mu.Lock()
	second := store.db
	store.mu.Unlock()
	assert.NotSame(t, first, second, "a store replaced at the same path must reopen, not answer from the unlinked file")
}

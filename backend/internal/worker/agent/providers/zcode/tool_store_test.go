package zcode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const zcodeOutputFileFixtureID = "tool-result-00000000-0000-4000-8000-000000000001"
const zcodeOutputFileFixtureURI = "zcode-artifact://session/" + zcodeOutputFileFixtureID
const zcodeOutputFileFixtureData = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAYAAABytg0kAAAAEUlEQVR4nGP4z8DwH4QZYAwAR8oH+WdZbrcAAAAASUVORK5CYII="
const zcodeToolStoreDDL = `CREATE TABLE part (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, message_id TEXT NOT NULL, data TEXT NOT NULL);
CREATE INDEX part_message_idx ON part(message_id);
CREATE INDEX part_session_idx ON part(session_id);`

func zcodeNativeToolFixture(status string) string {
	return `{"type":"tool","callID":"call","tool":"mcp__docs__read","futureCounter":9007199254740993,"state":{"status":"` + status + `","input":{"topic":"retained"},"output":"image","metadata":{"modelContentLayout":[{"type":"text","text":"Before image"},{"type":"attachment","attachmentIndex":0}]},"attachments":[{"type":"file","sessionID":"session","messageID":"message","mime":"image/png","url":"` + zcodeOutputFileFixtureURI + `"}]}}`
}

func TestZCodeToolStoreReadsOriginalRecordsAndOutputFiles(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	location := zcodeToolStoreLocation{databasePath: filepath.Join(directory, "store.db"), outputFileRoot: filepath.Join(directory, "artifacts"), sessionID: "session"}
	db := agenttest.NewFixtureDB(t, location.databasePath, zcodeToolStoreDDL)
	original := zcodeNativeToolFixture("completed")
	_, err := db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, original)
	require.NoError(t, err)
	outputFileDirectory := filepath.Join(location.outputFileRoot, "session")
	require.NoError(t, os.MkdirAll(outputFileDirectory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(outputFileDirectory, "call-media-1-"+zcodeOutputFileFixtureID+".txt"), []byte(zcodeOutputFileFixtureData), 0o600))

	for _, messageID := range []string{"message", ""} {
		records, err := readZCodeToolRecords(t.Context(), newZCodeToolStoreForTest(t), &zcodeOutputFileCache{}, location, map[string]zcodeToolLookup{"call": {messageID: messageID, toolName: "mcp__docs__read"}})
		require.NoError(t, err)
		require.Len(t, records, 1)
		assert.Equal(t, original, string(records["call"].native.Data))
		assert.Equal(t, "part", records["call"].native.ID)
		assert.True(t, records["call"].ready)
		assert.Equal(t, zcodeOutputFileFixtureData, records["call"].outputFiles[zcodeOutputFileFixtureURI])
	}
	var persisted string
	require.NoError(t, db.QueryRow(`SELECT data FROM part WHERE id = 'part'`).Scan(&persisted))
	assert.Equal(t, original, persisted)
}

func TestZCodeToolStoreWaitsForTheCompletedRecord(t *testing.T) {
	t.Parallel()
	location := zcodeToolStoreLocation{databasePath: filepath.Join(t.TempDir(), "store.db"), sessionID: "session"}
	db := agenttest.NewFixtureDB(t, location.databasePath, zcodeToolStoreDDL)
	_, err := db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, zcodeNativeToolFixture("running"))
	require.NoError(t, err)
	request := map[string]zcodeToolLookup{"call": {messageID: "message"}}
	records, err := readZCodeToolRecords(t.Context(), newZCodeToolStoreForTest(t), &zcodeOutputFileCache{}, location, request)
	require.NoError(t, err)
	assert.Empty(t, records)
	_, err = db.Exec(`UPDATE part SET data = ?`, zcodeNativeToolFixture("completed"))
	require.NoError(t, err)
	records, err = readZCodeToolRecords(t.Context(), newZCodeToolStoreForTest(t), &zcodeOutputFileCache{}, location, request)
	require.Error(t, err, "the artifact directory is absent")
	require.Len(t, records, 1, "missing artifacts must not discard the native record")
	assert.Empty(t, records["call"].outputFiles)
	assert.False(t, records["call"].ready)
}

func TestZCodeToolStoreRejectsUnrelatedAndAmbiguousRecords(t *testing.T) {
	t.Parallel()
	location := zcodeToolStoreLocation{databasePath: filepath.Join(t.TempDir(), "store.db"), sessionID: "session"}
	db := agenttest.NewFixtureDB(t, location.databasePath, zcodeToolStoreDDL)
	_, err := db.Exec(`INSERT INTO part VALUES ('first', 'session', 'message', ?), ('second', 'session', 'other-message', ?)`, zcodeNativeToolFixture("completed"), zcodeNativeToolFixture("completed"))
	require.NoError(t, err)
	for _, request := range []map[string]zcodeToolLookup{
		{"call": {}},
		{"call": {messageID: "foreign-message"}},
		{"call": {messageID: "message", toolName: "other-tool"}},
		{"foreign-call": {messageID: "message"}},
		{"call' OR 1=1 --": {messageID: "message"}},
	} {
		records, err := readZCodeToolRecords(t.Context(), newZCodeToolStoreForTest(t), &zcodeOutputFileCache{}, location, request)
		require.NoError(t, err)
		assert.Empty(t, records)
	}
	location.sessionID = "foreign-session"
	records, err := readZCodeToolRecords(t.Context(), newZCodeToolStoreForTest(t), &zcodeOutputFileCache{}, location, map[string]zcodeToolLookup{"call": {messageID: "message"}})
	require.NoError(t, err)
	assert.Empty(t, records)
}

func TestZCodeToolStoreKeepsValidRecordsBesideMalformedData(t *testing.T) {
	t.Parallel()
	location := zcodeToolStoreLocation{databasePath: filepath.Join(t.TempDir(), "store.db"), sessionID: "session"}
	db := agenttest.NewFixtureDB(t, location.databasePath, zcodeToolStoreDDL)
	original := strings.Replace(zcodeNativeToolFixture("completed"), `"attachments":[`, `"attachments":[null,17,`, 1)
	_, err := db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?), ('bad-json', 'session', 'message', '{'), ('not-tool', 'session', 'message', '{"type":"text","callID":"call"}')`, original)
	require.NoError(t, err)
	records, err := readZCodeToolRecords(t.Context(), newZCodeToolStoreForTest(t), &zcodeOutputFileCache{}, location, map[string]zcodeToolLookup{"call": {messageID: "message"}})
	require.Error(t, err, "the artifact directory is absent")
	require.Len(t, records, 1)
	assert.Equal(t, original, string(records["call"].native.Data))
}

func TestZCodeOutputFileURIRejectsOtherSessionsAndPaths(t *testing.T) {
	t.Parallel()
	assert.Equal(t, zcodeOutputFileFixtureID, zcodeOutputFileID(zcodeOutputFileFixtureURI, "session"))
	for _, uri := range []string{
		"file:///session/" + zcodeOutputFileFixtureID,
		"zcode-artifact://foreign/" + zcodeOutputFileFixtureID,
		"zcode-artifact://user@session/" + zcodeOutputFileFixtureID,
		zcodeOutputFileFixtureURI + "?query=1",
		zcodeOutputFileFixtureURI + "?",
		zcodeOutputFileFixtureURI + "#fragment",
		"zcode-artifact://session/../" + zcodeOutputFileFixtureID,
		"zcode-artifact://session/%2e%2e/" + zcodeOutputFileFixtureID,
		"zcode-artifact://session/prefix-" + zcodeOutputFileFixtureID,
	} {
		assert.Empty(t, zcodeOutputFileID(uri, "session"), uri)
	}
	assert.Equal(t, "a__b", zcodeOutputFileSegment("a😀b"))
	assert.Equal(t, "a_b", zcodeOutputFileSegment("a/b"))
	assert.Equal(t, strings.Repeat("x", 120), zcodeOutputFileSegment(strings.Repeat("x", 121)))
	assert.Equal(t, "unknown", zcodeOutputFileSegment(""))
}

func TestReadZCodeOutputFileSupportsBinaryAndEnforcesTheSizeLimit(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "binary.png"), []byte{1, 2, 3}, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "image.txt"), []byte(zcodeOutputFileFixtureData), 0o600))
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	data, err := readZCodeOutputFile(root, "binary.png", "image/png", 100)
	require.NoError(t, err)
	assert.Equal(t, "data:image/png;base64,AQID", data)
	_, err = readZCodeOutputFile(root, "binary.png", "image/png", 3)
	require.Error(t, err, "base64 encoding also counts toward the limit")
	_, err = readZCodeOutputFile(root, "image.txt", "image/png", 10)
	require.Error(t, err)
	_, err = readZCodeOutputFile(root, "missing.txt", "image/png", 100)
	require.Error(t, err)
	_, err = readZCodeOutputFile(root, ".", "image/png", 100)
	require.Error(t, err)
}

func TestZCodeToolStoreDoesNotCreateAnAbsentDatabase(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "absent.db")
	records, err := readZCodeToolRecords(t.Context(), newZCodeToolStoreForTest(t), &zcodeOutputFileCache{}, zcodeToolStoreLocation{databasePath: path, sessionID: "session"}, map[string]zcodeToolLookup{"call": {}})
	require.NoError(t, err)
	assert.Empty(t, records)
	_, err = os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestZCodeStoredToolMarshalsWithoutChangingProviderNumbers(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(contracts.ZCodeStoredTool{Data: json.RawMessage(zcodeNativeToolFixture("completed"))})
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"futureCounter":9007199254740993`)
}

// The store keeps one handle on the session database, and it keeps the output files
// it already read. A second read answers from both: the transcript reads the
// store for EVERY agent message, and a directory sweep for each of those reads
// spent the budget that the read itself has.
func TestZCodeToolStoreAnswersASecondReadFromItsOwnCache(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	location := zcodeToolStoreLocation{databasePath: filepath.Join(directory, "store.db"), outputFileRoot: filepath.Join(directory, "artifacts"), sessionID: "session"}
	db := agenttest.NewFixtureDB(t, location.databasePath, zcodeToolStoreDDL)
	_, err := db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, zcodeNativeToolFixture("completed"))
	require.NoError(t, err)
	outputFileDirectory := filepath.Join(location.outputFileRoot, "session")
	require.NoError(t, os.MkdirAll(outputFileDirectory, 0o700))
	outputFile := filepath.Join(outputFileDirectory, "call-media-1-"+zcodeOutputFileFixtureID+".txt")
	require.NoError(t, os.WriteFile(outputFile, []byte(zcodeOutputFileFixtureData), 0o600))

	store := newZCodeToolStoreForTest(t)
	outputFiles := &zcodeOutputFileCache{}
	request := map[string]zcodeToolLookup{"call": {messageID: "message", toolName: "mcp__docs__read"}}
	records, err := readZCodeToolRecords(t.Context(), store, outputFiles, location, request)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, zcodeOutputFileFixtureData, records["call"].outputFiles[zcodeOutputFileFixtureURI])

	// The whole output file tree goes. A read that swept the directory now fails.
	require.NoError(t, os.RemoveAll(location.outputFileRoot))
	records, err = readZCodeToolRecords(t.Context(), store, outputFiles, location, request)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.True(t, records["call"].ready)
	assert.Equal(t, zcodeOutputFileFixtureData, records["call"].outputFiles[zcodeOutputFileFixtureURI])
}

// newZCodeToolStoreForTest closes the store's database handle when the test ends,
// so an open file cannot defeat the temporary directory's own cleanup.
func newZCodeToolStoreForTest(t *testing.T) *zcodeToolStore {
	t.Helper()
	store := &zcodeToolStore{}
	t.Cleanup(store.close)
	return store
}

// The output file cache belongs to ONE TRANSCRIPT'S TURN, not to the agent and not to
// the store that every transcript shares.
//
// Each entry is a fully decoded data URI as large as LiveMaxMessageSize
// (16 MiB), and only a pass of the same turn can read one -- the turn end clears
// the pending set a later pass would ask about. Held for the life of the agent
// instead, a session of computer-use screenshots retained every screenshot. Shared
// with the children instead, one subagent finishing emptied the parent's.
func TestZCodeToolStoreDropsItsOutputFilesAtTheTurnEnd(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	location := zcodeToolStoreLocation{databasePath: filepath.Join(directory, "store.db"), outputFileRoot: filepath.Join(directory, "artifacts"), sessionID: "session"}
	db := agenttest.NewFixtureDB(t, location.databasePath, zcodeToolStoreDDL)
	_, err := db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, zcodeNativeToolFixture("completed"))
	require.NoError(t, err)
	outputFileDirectory := filepath.Join(location.outputFileRoot, "session")
	require.NoError(t, os.MkdirAll(outputFileDirectory, 0o700))
	outputFile := filepath.Join(outputFileDirectory, "call-media-1-"+zcodeOutputFileFixtureID+".txt")
	require.NoError(t, os.WriteFile(outputFile, []byte(zcodeOutputFileFixtureData), 0o600))

	store := newZCodeToolStoreForTest(t)
	source := newZCodeToolSource(store, func() zcodeToolStoreLocation { return location })
	child, ok := source.NewChild("", nil).(*zcodeToolSource)
	require.True(t, ok)
	require.Same(t, store, child.store, "a child shares the one database handle")
	require.NotSame(t, source.outputFiles, child.outputFiles, "a child owns its own artifact cache")

	request := map[string]zcodeToolLookup{"call": {messageID: "message", toolName: "mcp__docs__read"}}
	_, err = readZCodeToolRecords(t.Context(), store, source.outputFiles, location, request)
	require.NoError(t, err)
	require.Equal(t, zcodeOutputFileFixtureData, source.outputFiles.lookup(zcodeOutputFileFixtureURI),
		"the read caches the artifact it decoded")

	// A subagent reaches its turn end the instant its Agent result lands, which is
	// mid-turn for the parent. It must not empty what the parent already decoded.
	child.FinishTurn()
	assert.Equal(t, zcodeOutputFileFixtureData, source.outputFiles.lookup(zcodeOutputFileFixtureURI),
		"a child's turn end must not drop the parent's artifact bodies")

	source.FinishTurn()

	assert.Empty(t, source.outputFiles.lookup(zcodeOutputFileFixtureURI), "the turn end drops the artifact bodies it cached")
	store.mu.Lock()
	handle := store.db
	store.mu.Unlock()
	assert.NotNil(t, handle, "the database handle is per agent and must survive the turn")
}

// A stat that fails says nothing about the handle already open.
//
// The stat exists to notice a store REPLACED at the same path. Treating any stat
// failure as a replacement discarded a working handle for a rename window or an EIO,
// and sessionstore.OpenDB then failed too and reported sessionstore.ErrAbsent -- so the
// pass enriched nothing and every output file it had decoded was read again.
func TestZCodeToolStoreKeepsItsHandleWhenAStatFails(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "store.db")
	location := zcodeToolStoreLocation{databasePath: path, sessionID: "session"}
	db := agenttest.NewFixtureDB(t, path, zcodeToolStoreDDL)
	_, err := db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, zcodeNativeToolFixture("completed"))
	require.NoError(t, err)

	store := newZCodeToolStoreForTest(t)
	outputFiles := &zcodeOutputFileCache{}
	request := map[string]zcodeToolLookup{"call": {messageID: "message", toolName: "Bash"}}
	_, err = readZCodeToolRecords(t.Context(), store, outputFiles, location, request)
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
// one agent. Cursor and Reasonix already compared by inode; see sessionstore.Moved.
//
// Windows cannot stage this, and it also cannot reach the defect. SQLite opens every
// file with FILE_SHARE_READ|FILE_SHARE_WRITE and never FILE_SHARE_DELETE, so the open
// handle below refuses the os.Remove that the replacement needs -- and it refuses the
// owning runtime the same delete, which is what makes the cached handle unable to
// outlive its file there. See the Windows note on sessionstore.Moved.
func TestZCodeToolStoreReopensAStoreReplacedAtTheSamePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows refuses to delete a file that an open SQLite handle holds")
	}
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "store.db")
	location := zcodeToolStoreLocation{databasePath: path, outputFileRoot: filepath.Join(directory, "artifacts"), sessionID: "session"}
	db := agenttest.NewFixtureDB(t, path, zcodeToolStoreDDL)
	_, err := db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, zcodeNativeToolFixture("completed"))
	require.NoError(t, err)

	store := newZCodeToolStoreForTest(t)
	outputFiles := &zcodeOutputFileCache{}
	request := map[string]zcodeToolLookup{"call": {messageID: "message", toolName: "Bash"}}
	_, err = readZCodeToolRecords(t.Context(), store, outputFiles, location, request)
	require.NoError(t, err)
	store.mu.Lock()
	first := store.db
	store.mu.Unlock()
	require.NotNil(t, first)

	// The runtime replaces its store: the old inode is unlinked and a new file
	// takes the same name.
	require.NoError(t, os.Remove(path))
	replacement := agenttest.NewFixtureDB(t, path, zcodeToolStoreDDL)
	_, err = replacement.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, zcodeNativeToolFixture("completed"))
	require.NoError(t, err)

	_, err = readZCodeToolRecords(t.Context(), store, outputFiles, location, request)
	require.NoError(t, err)
	store.mu.Lock()
	second := store.db
	store.mu.Unlock()
	assert.NotSame(t, first, second, "a store replaced at the same path must reopen, not answer from the unlinked file")
}

func TestZCodeToolStoreKeepsNativeSerializationMetadataWithoutReadingText(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	location := zcodeToolStoreLocation{databasePath: filepath.Join(directory, "store.db"), outputFileRoot: filepath.Join(directory, "cli", "artifacts"), sessionID: "session"}
	db := agenttest.NewFixtureDB(t, location.databasePath, zcodeToolStoreDDL)
	outputFileDirectory := filepath.Join(location.outputFileRoot, "session")
	require.NoError(t, os.MkdirAll(outputFileDirectory, 0o700))
	path := filepath.Join(outputFileDirectory, "call-"+zcodeOutputFileFixtureID+".txt")
	complete := "<run_id>dwfrun-native</run_id>\n<status>completed</status>\n<result>\n" + strings.Repeat("x", 260000) + "computed77" + strings.Repeat("y", 260000) + "\n</result>"
	preview := "<persisted-output>\nOutput too large (520 KB). Full output saved to: " + path + "\n\nPreview (first 2 KB):\nhead\n...\n</persisted-output>"
	require.NoError(t, os.WriteFile(path, []byte(complete), 0o600))
	data, err := json.Marshal(map[string]any{
		"type": "tool", "callID": "call", "tool": "GetWorkflowRun",
		"state": map[string]any{
			"status": "completed", "input": map[string]any{"run_id": "dwfrun-native"},
			"output": preview,
			"metadata": map[string]any{"serialization": map[string]any{
				"truncated": true, "originalBytes": len(complete), "returnedBytes": len(preview),
				"budgetStrategy": "artifact", "artifactPath": path,
			}},
		},
	})
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, string(data))
	require.NoError(t, err)
	records, err := readZCodeToolRecords(t.Context(), newZCodeToolStoreForTest(t), &zcodeOutputFileCache{}, location, map[string]zcodeToolLookup{"call": {messageID: "message", toolName: "GetWorkflowRun"}})
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, string(data), string(records["call"].native.Data))
	assert.True(t, records["call"].ready)
	assert.Empty(t, records["call"].outputFiles, "native serialization metadata must not read external text")
	assert.Equal(t, "session", records["call"].native.SessionID)
	assert.Equal(t, "message", records["call"].native.MessageID)
	assert.Contains(t, string(records["call"].native.Data), path)
	assert.Contains(t, string(records["call"].native.Data), "dwfrun-native")
	assert.NotContains(t, string(records["call"].native.Data), "computed77")

}

func TestZCodeToolStoreDoesNotReadTextAttachmentsAsImages(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	location := zcodeToolStoreLocation{databasePath: filepath.Join(directory, "store.db"), outputFileRoot: filepath.Join(directory, "artifacts"), sessionID: "session"}
	db := agenttest.NewFixtureDB(t, location.databasePath, zcodeToolStoreDDL)
	native := strings.Replace(zcodeNativeToolFixture("completed"), `"mime":"image/png"`, `"mime":"text/plain"`, 1)
	_, err := db.Exec(`INSERT INTO part VALUES ('part','session','message',?)`, native)
	require.NoError(t, err)
	path := filepath.Join(location.outputFileRoot, "session", "call-media-1-"+zcodeOutputFileFixtureID+".txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	const hidden = "NATIVE_TEXT_ATTACHMENT_MUST_NOT_ENTER_WORKER_9037"
	require.NoError(t, os.WriteFile(path, []byte(hidden), 0o600))
	records, err := readZCodeToolRecords(t.Context(), newZCodeToolStoreForTest(t), &zcodeOutputFileCache{}, location, map[string]zcodeToolLookup{"call": {messageID: "message", toolName: "mcp__docs__read"}})
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, native, string(records["call"].native.Data))
	assert.Empty(t, records["call"].outputFiles, "a text/plain output attachment must not enter the native image byte map")
	assert.True(t, records["call"].ready, "native metadata stays ready without external text recovery")
}

func TestZCodeToolStoreIgnoresNonImageMimeBeforeOpeningTheFileDirectory(t *testing.T) {
	t.Parallel()
	for _, mime := range []string{"", "text/plain", "application/pdf", "application/octet-stream", "IMAGE/png", " image/png"} {
		t.Run(mime, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			location := zcodeToolStoreLocation{databasePath: filepath.Join(directory, "store.db"), outputFileRoot: filepath.Join(directory, "absent-images"), sessionID: "session"}
			db := agenttest.NewFixtureDB(t, location.databasePath, zcodeToolStoreDDL)
			native := strings.Replace(zcodeNativeToolFixture("completed"), `"mime":"image/png"`, `"mime":`+strconv.Quote(mime), 1)
			_, err := db.Exec(`INSERT INTO part VALUES ('part','session','message',?)`, native)
			require.NoError(t, err)
			records, err := readZCodeToolRecords(t.Context(), newZCodeToolStoreForTest(t), &zcodeOutputFileCache{}, location, map[string]zcodeToolLookup{"call": {messageID: "message"}})
			require.NoError(t, err, "non-image metadata must not open the absent image directory")
			require.Len(t, records, 1)
			assert.Equal(t, native, string(records["call"].native.Data))
			assert.True(t, records["call"].ready)
			assert.Empty(t, records["call"].outputFiles)
		})
	}
}

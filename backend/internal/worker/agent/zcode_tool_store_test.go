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
		records, err := readZCodeToolRecords(t.Context(), location, map[string]zcodeToolLookup{"call": {messageID: messageID, toolName: "mcp__docs__read"}})
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
	records, err := readZCodeToolRecords(t.Context(), location, request)
	require.NoError(t, err)
	assert.Empty(t, records)
	_, err = db.Exec(`UPDATE part SET data = ?`, zcodeNativeToolFixture("completed"))
	require.NoError(t, err)
	records, err = readZCodeToolRecords(t.Context(), location, request)
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
		records, err := readZCodeToolRecords(t.Context(), location, request)
		require.NoError(t, err)
		assert.Empty(t, records)
	}
	location.sessionID = "foreign-session"
	records, err := readZCodeToolRecords(t.Context(), location, map[string]zcodeToolLookup{"call": {messageID: "message"}})
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
	records, err := readZCodeToolRecords(t.Context(), location, map[string]zcodeToolLookup{"call": {messageID: "message"}})
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
	records, err := readZCodeToolRecords(t.Context(), zcodeToolStoreLocation{databasePath: path, sessionID: "session"}, map[string]zcodeToolLookup{"call": {}})
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

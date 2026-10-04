package zcode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type zcodeSerializationFixture struct {
	callID   string
	location zcodeToolStoreLocation
	path     string
	uri      string
	body     string
	metadata map[string]any
	part     map[string]any
}

func newZCodeSerializationFixture(t *testing.T) zcodeSerializationFixture {
	t.Helper()
	directory := t.TempDir()
	location := zcodeToolStoreLocation{databasePath: filepath.Join(directory, "store.db"), outputFileRoot: filepath.Join(directory, "cli", "artifacts"), sessionID: "session"}
	path := filepath.Join(location.outputFileRoot, "session", "call-"+zcodeOutputFileFixtureID+".txt")
	body := "native head42\ncomplete middle77\nnative tail66"
	metadata := map[string]any{"budgetStrategy": "artifact", "artifactPath": path, "truncated": true, "originalBytes": len(body), "returnedBytes": 2}
	part := map[string]any{"type": "tool", "callID": "call", "tool": "GetWorkflowRun", "state": map[string]any{"status": "completed", "output": "<persisted-output>native preview</persisted-output>", "metadata": map[string]any{"serialization": metadata}}}
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return zcodeSerializationFixture{callID: "call", location: location, path: path, uri: zcodeOutputFileFixtureURI, body: body, metadata: metadata, part: part}
}

func (f zcodeSerializationFixture) read(t *testing.T, outputFiles *zcodeOutputFileCache) (zcodeToolRecord, error) {
	t.Helper()
	db := agenttest.NewFixtureDB(t, f.location.databasePath, zcodeToolStoreDDL)
	data, err := json.Marshal(f.part)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, string(data))
	require.NoError(t, err)
	records, readErr := readZCodeToolRecords(t.Context(), newZCodeToolStoreForTest(t), outputFiles, f.location, map[string]zcodeToolLookup{f.callID: {messageID: "message", toolName: "GetWorkflowRun"}})
	require.Len(t, records, 1)
	record := records[f.callID]
	assert.Equal(t, string(data), string(record.native.Data), "keep the original native record")
	return record, readErr
}

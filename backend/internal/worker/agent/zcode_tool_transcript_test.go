package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const zcodeStoredImageRequest = `{"type":"tool.updated","payload":{"kind":"scheduled","toolCallId":"call","assistantMessageId":"message","toolName":"mcp__docs__read"}}`
const zcodeStoredImageResult = `{"type":"tool.updated","payload":{"kind":"result","toolCallId":"call","result":{"success":true,"content":"[Attached image/png: MCP image]"}},"_leapmux":"provider value"}`

func TestZCodeToolTranscriptRecoversCompletedImagesAfterProcessCancellation(t *testing.T) {
	t.Parallel()
	f := newZCodeTranscriptFixture(t, true)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.transcript = newZCodeToolTranscript(ctx, f.sink, func() zcodeToolStoreLocation { return f.location })
	_, err := f.db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, zcodeNativeToolFixture("completed"))
	require.NoError(t, err)
	f.writeArtifact(t, "session")
	f.persistPair(t)
	cancel()
	require.NoError(t, f.transcript.PersistTurnEnd(MessageContent{Original: []byte(`{"done":true}`)}, SpanInfo{}))
	assert.Contains(t, string(f.sink.Messages()[1].SupplementalContent), zcodeArtifactFixtureData)
}

type zcodeTranscriptFixture struct {
	transcript *toolTranscript
	sink       *testSink
	db         *sql.DB
	location   zcodeToolStoreLocation
}

func newZCodeTranscriptFixture(t *testing.T, createDatabase bool) *zcodeTranscriptFixture {
	t.Helper()
	directory := t.TempDir()
	f := &zcodeTranscriptFixture{
		sink: &testSink{},
		location: zcodeToolStoreLocation{
			databasePath: filepath.Join(directory, "store.db"), artifactRoot: filepath.Join(directory, "artifacts"), sessionID: "session",
		},
	}
	if createDatabase {
		f.db = newFixtureDB(t, f.location.databasePath, zcodeToolStoreDDL)
	}
	f.transcript = newZCodeToolTranscript(t.Context(), f.sink, func() zcodeToolStoreLocation { return f.location })
	return f
}

func (f *zcodeTranscriptFixture) persistPair(t *testing.T) {
	t.Helper()
	require.NoError(t, f.transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: []byte(zcodeStoredImageRequest)}, SpanInfo{SpanID: "call"}))
	require.NoError(t, f.transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: []byte(zcodeStoredImageResult)}, SpanInfo{SpanID: "call", Closing: true}))
}

func (f *zcodeTranscriptFixture) boundary(t *testing.T) {
	t.Helper()
	require.NoError(t, f.transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: []byte(`{"type":"assembled_message","kind":"text","text":"boundary","completion":"complete"}`)}, SpanInfo{}))
}

func (f *zcodeTranscriptFixture) writeArtifact(t *testing.T, sessionID string) {
	t.Helper()
	directory := filepath.Join(f.location.artifactRoot, zcodeArtifactSegment(sessionID))
	require.NoError(t, os.MkdirAll(directory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "call-media-1-"+zcodeArtifactFixtureID+".txt"), []byte(zcodeArtifactFixtureData), 0o600))
}

func TestZCodeToolTranscriptRetriesLateRecordsAndArtifacts(t *testing.T) {
	t.Parallel()
	f := newZCodeTranscriptFixture(t, true)
	_, err := f.db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, zcodeNativeToolFixture("running"))
	require.NoError(t, err)
	f.persistPair(t)
	f.boundary(t)
	assert.Empty(t, f.sink.Messages()[1].SupplementalContent)
	_, err = f.db.Exec(`UPDATE part SET data = ?`, zcodeNativeToolFixture("completed"))
	require.NoError(t, err)
	f.boundary(t)
	assert.Empty(t, f.sink.Messages()[1].SupplementalContent)
	f.writeArtifact(t, "session")
	f.boundary(t)
	result := f.sink.Messages()[1]
	assert.Equal(t, zcodeStoredImageResult, string(result.Content))
	assert.Contains(t, string(result.SupplementalContent), zcodeArtifactFixtureData)
	assert.Contains(t, string(result.SupplementalContent), `"futureCounter":9007199254740993`)
	assert.Empty(t, f.transcript.pending)
}

func TestZCodeToolTranscriptKeepsUnavailableImageMetadataAtTurnEnd(t *testing.T) {
	t.Parallel()
	f := newZCodeTranscriptFixture(t, true)
	_, err := f.db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, zcodeNativeToolFixture("completed"))
	require.NoError(t, err)
	f.persistPair(t)
	require.NoError(t, f.transcript.PersistTurnEnd(MessageContent{Original: []byte(`{"done":true}`)}, SpanInfo{}))
	var extra map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(f.sink.Messages()[1].SupplementalContent, &extra))
	assert.NotEmpty(t, extra["nativeTool"])
	assert.JSONEq(t, `{}`, string(extra["artifacts"]))
	assert.Equal(t, zcodeStoredImageResult, string(f.sink.Messages()[1].Content))
}

func TestZCodeToolTranscriptClearsRequestBindingsWhenTheDatabaseIsAbsent(t *testing.T) {
	t.Parallel()
	f := newZCodeTranscriptFixture(t, false)
	f.persistPair(t)
	require.NoError(t, f.transcript.PersistTurnEnd(MessageContent{Original: []byte(`{"done":true}`)}, SpanInfo{}))
	f.db = newFixtureDB(t, f.location.databasePath, zcodeToolStoreDDL)
	_, err := f.db.Exec(`INSERT INTO part VALUES ('old', 'session', 'message', ?), ('new', 'session', 'other-message', ?)`, zcodeNativeToolFixture("completed"), zcodeNativeToolFixture("completed"))
	require.NoError(t, err)
	f.writeArtifact(t, "session")
	require.NoError(t, f.transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: []byte(zcodeStoredImageResult)}, SpanInfo{SpanID: "call", Closing: true}))
	require.NoError(t, f.transcript.PersistTurnEnd(MessageContent{Original: []byte(`{"done":true}`)}, SpanInfo{}))
	assert.Empty(t, f.sink.Messages()[3].SupplementalContent, "the old request must not disambiguate a later call")
}

func TestZCodeToolTranscriptEnrichesChildCallsInTheChildTranscript(t *testing.T) {
	t.Parallel()
	f := newZCodeTranscriptFixture(t, true)
	_, err := f.db.Exec(`CREATE TABLE session (id TEXT PRIMARY KEY, parent_id TEXT); INSERT INTO session VALUES ('session', NULL), ('child-session', 'session')`)
	require.NoError(t, err)
	native := strings.ReplaceAll(zcodeNativeToolFixture("completed"), `"session"`, `"child-session"`)
	native = strings.ReplaceAll(native, "zcode-artifact://session/", "zcode-artifact://child-session/")
	_, err = f.db.Exec(`INSERT INTO part VALUES ('part', 'child-session', 'message', ?)`, native)
	require.NoError(t, err)
	f.writeArtifact(t, "child-session")
	childID, err := f.transcript.EnsureChildAgent("spawn", "child-key", "Child")
	require.NoError(t, err)
	child := f.transcript.ChildSink(childID)
	require.Same(t, child, f.transcript.ChildSink(childID))
	request := `{"type":"tool.updated","payload":{"kind":"scheduled","toolCallId":"tool_subagent_agent_call","assistantMessageId":"message","toolName":"mcp__docs__read","agentId":"agent","childSessionId":"child-session"}}`
	result := `{"type":"tool.updated","payload":{"kind":"result","toolCallId":"tool_subagent_agent_call","toolName":"mcp__docs__read","agentId":"agent","childSessionId":"child-session","result":{"success":true,"content":"image"}}}`
	require.NoError(t, child.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: []byte(request)}, SpanInfo{SpanID: "tool_subagent_agent_call"}))
	require.NoError(t, f.transcript.PersistChildMessage(childID, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, []byte(result), SpanInfo{SpanID: "tool_subagent_agent_call", Closing: true}))
	require.NoError(t, f.transcript.PersistTurnEnd(MessageContent{Original: []byte(`{"done":true}`)}, SpanInfo{}))
	childSink := f.sink.ChildSink(childID).(*testSink)
	stored := childSink.Messages()[1]
	assert.Equal(t, result, string(stored.Content))
	assert.Contains(t, string(stored.SupplementalContent), zcodeArtifactFixtureData)
	assert.Contains(t, string(stored.SupplementalContent), `"callID":"call"`)
	assert.Len(t, f.sink.Messages(), 1, "the parent stores only its turn end")
}

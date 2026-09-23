package zcode

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/tooltranscript"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/tooltranscript/tooltranscripttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const zcodeStoredImageRequest = `{"type":"tool.updated","payload":{"kind":"scheduled","toolCallId":"call","assistantMessageId":"message","toolName":"mcp__docs__read"}}`
const zcodeStoredImageResult = `{"type":"tool.updated","payload":{"kind":"result","toolCallId":"call","result":{"success":true,"content":"[Attached image/png: MCP image]"}},"_leapmux":"provider value"}`

// CloseToolStoreForTest releases the store's database handle at once.
func (z *zcodeToolSource) CloseToolStoreForTest() { z.store.close() }

// ToolStoreHandleOpenForTest reports whether the store holds a database handle.
func (z *zcodeToolSource) ToolStoreHandleOpenForTest() bool {
	z.store.mu.Lock()
	defer z.store.mu.Unlock()
	return z.store.db != nil
}

func TestZCodeToolTranscriptRecoversCompletedImagesAfterProcessCancellation(t *testing.T) {
	t.Parallel()
	f := newZCodeTranscriptFixture(t, true)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.transcript = newZCodeToolTranscript(ctx, agent.NewProviderServices(f.sink), func() zcodeToolStoreLocation { return f.location })
	tooltranscripttest.ReleaseToolStoreAtTestEnd(t, f.transcript)
	_, err := f.db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, zcodeNativeToolFixture("completed"))
	require.NoError(t, err)
	f.writeArtifact(t, "session")
	f.persistPair(t)
	cancel()
	require.NoError(t, f.transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
	assert.Contains(t, string(f.sink.Messages()[1].SupplementalContent), zcodeArtifactFixtureData)
}

type zcodeTranscriptFixture struct {
	transcript *tooltranscript.Transcript
	sink       *agenttest.Sink
	db         *sql.DB
	location   zcodeToolStoreLocation
}

func newZCodeTranscriptFixture(t *testing.T, createDatabase bool) *zcodeTranscriptFixture {
	t.Helper()
	directory := t.TempDir()
	f := &zcodeTranscriptFixture{
		sink: &agenttest.Sink{},
		location: zcodeToolStoreLocation{
			databasePath: filepath.Join(directory, "store.db"), artifactRoot: filepath.Join(directory, "artifacts"), sessionID: "session",
		},
	}
	if createDatabase {
		f.db = agenttest.NewFixtureDB(t, f.location.databasePath, zcodeToolStoreDDL)
	}
	f.transcript = newZCodeToolTranscript(t.Context(), agent.NewProviderServices(f.sink), func() zcodeToolStoreLocation { return f.location })
	tooltranscripttest.ReleaseToolStoreAtTestEnd(t, f.transcript)
	return f
}

func (f *zcodeTranscriptFixture) persistPair(t *testing.T) {
	t.Helper()
	require.NoError(t, f.transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(zcodeStoredImageRequest)}, agent.SpanInfo{SpanID: "call"}))
	require.NoError(t, f.transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(zcodeStoredImageResult)}, agent.SpanInfo{SpanID: "call", Closing: true}))
}

// boundary persists one non-tool agent message, which is what asks the supplement
// worker for an interim pass. It joins that pass, because the worker performs the read
// and the caller reads the row that the pass enriched.
func (f *zcodeTranscriptFixture) boundary(t *testing.T) {
	t.Helper()
	require.NoError(t, f.transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"type":"assembled_message","kind":"text","text":"boundary","completion":"complete"}`)}, agent.SpanInfo{}))
	f.transcript.WaitForSupplementsForTest()
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
	assert.Empty(t, f.transcript.PendingSpanIDsForTest())
}

func TestZCodeToolTranscriptKeepsUnavailableImageMetadataAtTurnEnd(t *testing.T) {
	t.Parallel()
	f := newZCodeTranscriptFixture(t, true)
	_, err := f.db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, zcodeNativeToolFixture("completed"))
	require.NoError(t, err)
	f.persistPair(t)
	require.NoError(t, f.transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
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
	require.NoError(t, f.transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
	f.db = agenttest.NewFixtureDB(t, f.location.databasePath, zcodeToolStoreDDL)
	_, err := f.db.Exec(`INSERT INTO part VALUES ('old', 'session', 'message', ?), ('new', 'session', 'other-message', ?)`, zcodeNativeToolFixture("completed"), zcodeNativeToolFixture("completed"))
	require.NoError(t, err)
	f.writeArtifact(t, "session")
	require.NoError(t, f.transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(zcodeStoredImageResult)}, agent.SpanInfo{SpanID: "call", Closing: true}))
	require.NoError(t, f.transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
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
	require.NoError(t, child.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(request)}, agent.SpanInfo{SpanID: "tool_subagent_agent_call"}))
	require.NoError(t, f.transcript.PersistChildMessage(childID, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, []byte(result), agent.SpanInfo{SpanID: "tool_subagent_agent_call", Closing: true}))
	require.NoError(t, f.transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
	childSink := f.sink.Child(childID)
	stored := childSink.Messages()[1]
	assert.Equal(t, result, string(stored.Content))
	assert.Contains(t, string(stored.SupplementalContent), zcodeArtifactFixtureData)
	assert.Contains(t, string(stored.SupplementalContent), `"callID":"call"`)
	assert.Len(t, f.sink.Messages(), 1, "the parent stores only its turn end")
}

func TestZCodeReleaseToolStoreAtTestEndClosesTheHandle(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "zcode.db")
	agenttest.NewFixtureDB(t, path, zcodeToolStoreDDL)
	tooltranscripttest.AssertReleaseClosesTheToolStore(t, func(t *testing.T, ctx context.Context) *tooltranscript.Transcript {
		transcript := newZCodeToolTranscript(ctx, agent.NewProviderServices(&agenttest.Sink{}), func() zcodeToolStoreLocation {
			return zcodeToolStoreLocation{databasePath: path, sessionID: "session"}
		})
		tooltranscripttest.ReleaseToolStoreAtTestEnd(t, transcript)
		require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
			agent.MessageContent{Original: []byte(zcodeStoredImageRequest)}, agent.SpanInfo{SpanID: "call"}))
		require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
			agent.MessageContent{Original: []byte(zcodeStoredImageResult)}, agent.SpanInfo{SpanID: "call", Closing: true}))
		return transcript
	})
}

// TestZCodeTestsCloseTheToolStoreHandleTheyOpen runs agenttest.RequireToolStoreHandlesClosed
// over this package. Three Cursor tests once left a store handle open, and only
// Windows failed for it.
func TestZCodeTestsCloseTheToolStoreHandleTheyOpen(t *testing.T) {
	t.Parallel()
	agenttest.RequireToolStoreHandlesClosed(t, ".", agenttest.ToolStoreRule{
		Stores:       map[string]string{"zcodeToolStore": "newZCodeToolStoreForTest"},
		Constructors: []string{"newZCodeToolTranscript"},
		Release:      "tooltranscripttest.ReleaseToolStoreAtTestEnd",
	})
}

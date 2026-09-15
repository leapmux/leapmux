package agent

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

const cursorStoredRequest = `{"role":"assistant","content":[{"type":"tool-call","toolCallId":"search","toolName":"Grep","args":{"pattern":"answer","path":"/project"}}]}`
const cursorStoredResult = `{"role":"tool","id":"search","content":[{"type":"tool-result","toolCallId":"search","toolName":"Grep","result":"sample.py:1:answer = 42","experimental_content":[{"type":"text","text":"sample.py:1:answer = 42"}]}],"providerOptions":{"cursor":{"highLevelToolCallResult":{"output":{"success":{"totalMatchedLines":1}},"isError":false}}}}`

func insertCursorBlob(t *testing.T, db *sql.DB, id, data string) {
	t.Helper()
	_, err := db.Exec(`INSERT OR REPLACE INTO blobs (id, data) VALUES (?, ?)`, id, []byte(data))
	require.NoError(t, err)
}

func cursorContentField(t *testing.T, content json.RawMessage, field string) json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(content, &fields))
	return fields[field]
}

func TestCursorToolTranscriptPreservesNativeContentFields(t *testing.T) {
	t.Parallel()
	for _, sibling := range []string{"", `null,7,{"type":"tool-result","toolCallId":42},`} {
		t.Run(sibling, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "store.db")
			db := newFixtureDB(t, path, cursorStoreDDL)
			block := `{"type":"tool-result","toolCallId":"image","toolName":"mcp_probe_image","result":"image result","providerOptions":{"cursor":{"imageDescriptions":{"0":"A red square"}}},"futureDisplay":{"caption":"Native caption"}}`
			insertCursorBlob(t, db, "result", `{"role":"tool","content":[`+sibling+block+`]}`)
			sink := &testSink{}
			transcript := newCursorToolTranscript(t.Context(), sink, func() string { return path })
			releaseToolStoreAtTestEnd(t, transcript)
			original := []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"image","status":"completed"}`)
			require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: original}, SpanInfo{SpanID: "image", Closing: true}))
			require.NoError(t, transcript.PersistTurnEnd(MessageContent{Original: []byte(`{"stopReason":"end_turn"}`)}, SpanInfo{}))
			messages := sink.Messages()
			require.Len(t, messages, 2)
			assert.Equal(t, original, messages[0].Content)
			var supplement struct {
				RawOutput struct {
					Content []json.RawMessage `json:"content"`
				} `json:"rawOutput"`
			}
			require.NoError(t, json.Unmarshal(messages[0].SupplementalContent, &supplement))
			require.Len(t, supplement.RawOutput.Content, 1)
			assert.JSONEq(t, block, string(supplement.RawOutput.Content[0]))
		})
	}
}

// newCursorToolStoreForTest closes the store's database handle when the test ends, so
// an open file cannot defeat the temporary directory's own cleanup. The ZCode store
// has the same helper, and releaseToolStoreAtTestEnd states why the close must be
// synchronous.
func newCursorToolStoreForTest(t *testing.T) *cursorToolStore {
	t.Helper()
	store := &cursorToolStore{}
	t.Cleanup(store.reset)
	return store
}

func TestCursorToolStoreReadsMatchingRecords(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "store.db")
	db := newFixtureDB(t, path, cursorStoreDDL)
	insertCursorBlob(t, db, "binary", "\x00\xff\x01")
	insertCursorBlob(t, db, "system", `{"role":"system","content":"unrelated data"}`)
	insertCursorBlob(t, db, "request", cursorStoredRequest)
	insertCursorBlob(t, db, "result", cursorStoredResult)

	store := newCursorToolStoreForTest(t)
	found, err := store.read(t.Context(), path, []string{"search", "missing"})
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.JSONEq(t, `{"pattern":"answer","path":"/project"}`, string(found["search"].arguments))
	assert.JSONEq(t, `"Grep"`, string(cursorContentField(t, found["search"].content, "toolName")))
	assert.JSONEq(t, `"sample.py:1:answer = 42"`, string(cursorContentField(t, found["search"].content, "result")))
	assert.Len(t, store.requests, 1)
	assert.Len(t, store.results, 1)
}

func TestCursorToolStoreFindsLateRecordsAndReplacements(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "store.db")
	db := newFixtureDB(t, path, cursorStoreDDL)
	insertCursorBlob(t, db, "request", cursorStoredRequest)
	store := newCursorToolStoreForTest(t)
	found, err := store.read(t.Context(), path, []string{"search"})
	require.NoError(t, err)
	assert.Empty(t, found)
	insertCursorBlob(t, db, "result", cursorStoredResult)
	found, err = store.read(t.Context(), path, []string{"search"})
	require.NoError(t, err)
	require.Contains(t, found, "search")

	_, err = db.Exec(`DELETE FROM blobs WHERE id = ?`, "result")
	require.NoError(t, err)
	insertCursorBlob(t, db, "replacement", `{"role":"tool","content":[{"type":"tool-result","toolCallId":"search","toolName":"Grep","result":"updated result"}]}`)
	found, err = store.read(t.Context(), path, []string{"search"})
	require.NoError(t, err)
	assert.JSONEq(t, `"updated result"`, string(cursorContentField(t, found["search"].content, "result")))
	assert.NotContains(t, store.seen, "result")
}

func TestCursorToolStoreHandlesAbsentAndInvalidStores(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "absent.db")
	store := newCursorToolStoreForTest(t)
	found, err := store.read(t.Context(), path, []string{"search"})
	require.NoError(t, err)
	assert.Empty(t, found)
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "a read must not create a provider database")

	require.NoError(t, os.WriteFile(path, []byte("not a database"), 0o600))
	_, err = store.read(t.Context(), path, []string{"search"})
	require.Error(t, err)
}

func TestCursorToolStoreResetsAcrossSessions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first := filepath.Join(dir, "first.db")
	second := filepath.Join(dir, "second.db")
	insertCursorBlob(t, newFixtureDB(t, first, cursorStoreDDL), "result", cursorStoredResult)
	newFixtureDB(t, second, cursorStoreDDL)
	store := newCursorToolStoreForTest(t)
	found, err := store.read(t.Context(), first, []string{"search"})
	require.NoError(t, err)
	require.Contains(t, found, "search")
	found, err = store.read(t.Context(), second, []string{"search"})
	require.NoError(t, err)
	assert.Empty(t, found)
	assert.Empty(t, store.results)
}

func TestCursorToolTranscriptEnrichesAfterProviderWrites(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "store.db")
	db := newFixtureDB(t, path, cursorStoreDDL)
	insertCursorBlob(t, db, "request", cursorStoredRequest)
	sink := &testSink{}
	transcript := newCursorToolTranscript(t.Context(), sink, func() string { return path })
	releaseToolStoreAtTestEnd(t, transcript)
	original := []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"search","kind":"search","status":"completed","rawOutput":{"totalMatches":1}}`)
	initialSupplement := []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"search","status":"completed","protocol":{"title":"Search files"}}`)
	span := SpanInfo{SpanID: "search", SpanType: "search", Closing: true}
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: original, Supplemental: initialSupplement}, span))
	require.Len(t, sink.Messages(), 1)
	assert.JSONEq(t, string(original), string(sink.Messages()[0].Content))

	insertCursorBlob(t, db, "result", cursorStoredResult)
	require.NoError(t, transcript.PersistTurnEnd(MessageContent{Original: []byte(`{"stopReason":"end_turn"}`)}, SpanInfo{}))
	messages := sink.Messages()
	require.Len(t, messages, 2, "enrichment must not add another tool row")
	var enriched struct {
		ToolCallID string          `json:"toolCallId"`
		Protocol   json.RawMessage `json:"protocol"`
		RawOutput  struct {
			ToolArguments   json.RawMessage   `json:"toolArguments"`
			TotalMatches    int               `json:"totalMatches"`
			Content         []json.RawMessage `json:"content"`
			ProviderOptions json.RawMessage   `json:"providerOptions"`
		} `json:"rawOutput"`
	}
	assert.JSONEq(t, string(original), string(messages[0].Content))
	require.NoError(t, json.Unmarshal(messages[0].SupplementalContent, &enriched))
	assert.Equal(t, "search", enriched.ToolCallID)
	assert.JSONEq(t, `{"title":"Search files"}`, string(enriched.Protocol))
	assert.JSONEq(t, `{"pattern":"answer","path":"/project"}`, string(enriched.RawOutput.ToolArguments))
	assert.NotContains(t, string(messages[0].SupplementalContent), `"rawInput"`)
	assert.NotContains(t, string(messages[0].SupplementalContent), `"totalMatches"`)
	require.Len(t, enriched.RawOutput.Content, 1)
	assert.JSONEq(t, `"sample.py:1:answer = 42"`, string(cursorContentField(t, enriched.RawOutput.Content[0], "result")))
	assert.NotEmpty(t, enriched.RawOutput.ProviderOptions)
	assert.True(t, messages[0].Closing)
	assert.True(t, messages[1].TurnEnd)
	assert.Empty(t, pendingSpanIDs(transcript))
}

func TestCursorToolTranscriptPreservesResultsWithoutStoreData(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"", filepath.Join(t.TempDir(), "absent.db")} {
		sink := &testSink{}
		transcript := newCursorToolTranscript(t.Context(), sink, func() string { return path })
		releaseToolStoreAtTestEnd(t, transcript)
		original := []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"search","status":"failed","rawOutput":{"error":"permission denied"}}`)
		require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: original}, SpanInfo{SpanID: "search", Closing: true}))
		require.NoError(t, transcript.PersistTurnEnd(MessageContent{Original: []byte(`{"stopReason":"end_turn"}`)}, SpanInfo{}))
		assert.JSONEq(t, string(original), string(sink.Messages()[0].Content))
		assert.Empty(t, pendingSpanIDs(transcript))
	}
}

func TestCursorToolTranscriptEnrichesSessionReplay(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "store.db")
	db := newFixtureDB(t, path, cursorStoreDDL)
	insertCursorBlob(t, db, "request", cursorStoredRequest)
	insertCursorBlob(t, db, "result", cursorStoredResult)
	var activePath string
	sink := &testSink{}
	transcript := newCursorToolTranscript(t.Context(), sink, func() string { return activePath })
	releaseToolStoreAtTestEnd(t, transcript)
	original := []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"search","status":"completed","rawOutput":{"totalMatches":1}}`)
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: original}, SpanInfo{SpanID: "search", Closing: true}))
	assert.Empty(t, sink.Messages()[0].SupplementalContent)
	// Cursor replays messages before the session/load response supplies the session ID.
	activePath = path
	transcript.UpdateSessionID("resumed-session")
	// The session ID asks the supplement worker for a pass. The worker performs the
	// read, so this test joins it before it reads the row that the pass enriched.
	transcript.waitForSupplements()
	assert.Equal(t, 1, sink.SessionIDCount())
	assert.Equal(t, original, sink.Messages()[0].Content)
	assert.NotEmpty(t, sink.Messages()[0].SupplementalContent)
	assert.Empty(t, pendingSpanIDs(transcript))
}

func TestCursorToolTranscriptDiscardsPendingFromPreviousSession(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	activePath := filepath.Join(dir, "first.db")
	newFixtureDB(t, activePath, cursorStoreDDL)
	secondPath := filepath.Join(dir, "second.db")
	second := newFixtureDB(t, secondPath, cursorStoreDDL)
	insertCursorBlob(t, second, "result", cursorStoredResult)
	sink := &testSink{}
	transcript := newCursorToolTranscript(t.Context(), sink, func() string { return activePath })
	releaseToolStoreAtTestEnd(t, transcript)
	original := []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"search","status":"completed"}`)
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: original}, SpanInfo{SpanID: "search", Closing: true}))
	activePath = secondPath
	transcript.UpdateSessionID("different-session")
	assert.Equal(t, original, sink.Messages()[0].Content)
	assert.Empty(t, sink.Messages()[0].SupplementalContent)
	assert.Empty(t, pendingSpanIDs(transcript))
}

// The home directory comes from the query, not from the process environment. A
// resumed agent carries the home directory of the row it resumed, and the two
// differ whenever the worker runs for a different user than the session did.
func TestCursorACPStorePathResolvesAgainstTheQueryHome(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	query := StoredSessionQuery{HomeDir: home}
	assert.Equal(t, filepath.Join(home, ".cursor", "acp-sessions", "session-1", cursorStoreFileName),
		cursorACPStorePath(query, "session-1"))
	for _, sessionID := range []string{"", ".", "..", "../escape", "nested/id"} {
		assert.Empty(t, cursorACPStorePath(query, sessionID), "a session id that escapes the store resolves to no path")
	}
}

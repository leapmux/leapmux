package zcode

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
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

func TestZCodeToolTranscriptPreservesSerializationWithIndependentNativeStorageSettings(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	storage := filepath.Join(home, "native-output-storage")
	nativeDatabase := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
	outputFilePath := filepath.Join(storage, "cli", "artifacts", "session", "call-"+zcodeOutputFileFixtureID+".txt")
	body := "native head42\n文 complete middle77\nnative tail66\n"
	require.NoError(t, os.MkdirAll(filepath.Dir(outputFilePath), 0o700))
	require.NoError(t, os.WriteFile(outputFilePath, []byte(body), 0o600))
	db := agenttest.NewFixtureDB(t, nativeDatabase, zcodeToolStoreDDL)
	part := map[string]any{
		"type": "tool", "callID": "call", "tool": "GetWorkflowRun",
		"state": map[string]any{
			"status": "completed", "output": "native preview",
			"metadata": map[string]any{"serialization": map[string]any{
				"budgetStrategy": "artifact", "artifactPath": outputFilePath,
				"truncated": true, "originalBytes": len([]byte(body)), "returnedBytes": len("native preview"),
			}},
		},
	}
	encoded, err := json.Marshal(part)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, string(encoded))
	require.NoError(t, err)
	location := zcodeTestStorePaths(t, agent.StoredSessionQuery{
		HomeDir: home, Getenv: agenttest.FixtureEnv(map[string]string{"ZCODE_STORAGE_DIR": storage}),
	}, nativeDatabase)
	location.sessionID = "session"
	sink := &agenttest.Sink{}
	transcript := newZCodeToolTranscript(t.Context(), agent.NewProviderServices(sink), func() zcodeToolStoreLocation { return location }, nil)
	tooltranscripttest.ReleaseToolStoreAtTestEnd(t, transcript)
	request := `{"type":"tool.updated","sessionId":"session","payload":{"kind":"scheduled","toolCallId":"call","assistantMessageId":"message","toolName":"GetWorkflowRun"}}`
	result := `{"type":"tool.updated","sessionId":"session","payload":{"kind":"result","toolCallId":"call","result":{"success":true,"content":"native preview"}}}`
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(request)}, agent.SpanInfo{SpanID: "call"}))
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(result)}, agent.SpanInfo{SpanID: "call", Closing: true}))
	require.NoError(t, transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
	stored := sink.Messages()[1]
	assert.Equal(t, result, string(stored.Content), "keep the native preview unchanged")
	var supplement contracts.ZCodeToolResultEnvelope
	require.NotEmpty(t, stored.SupplementalContent, "read the native database independently of the output directory")
	require.NoError(t, json.Unmarshal(stored.SupplementalContent, &supplement))
	assert.Equal(t, "session", supplement.NativeTool.SessionID)
	assert.Equal(t, "message", supplement.NativeTool.MessageID)
	assert.JSONEq(t, string(encoded), string(supplement.NativeTool.Data))
	assert.Empty(t, supplement.OutputFiles, "native serialization metadata must not cause an external text read")
}

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
	f.transcript = newZCodeToolTranscript(ctx, agent.NewProviderServices(f.sink), func() zcodeToolStoreLocation { return f.location }, nil)
	tooltranscripttest.ReleaseToolStoreAtTestEnd(t, f.transcript)
	_, err := f.db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, zcodeNativeToolFixture("completed"))
	require.NoError(t, err)
	f.writeOutputFile(t, "session")
	f.persistPair(t)
	cancel()
	require.NoError(t, f.transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
	assert.Contains(t, string(f.sink.Messages()[1].SupplementalContent), zcodeOutputFileFixtureData)
}

func TestZCodeToolTranscript_FinalReadsCloseHandlesAfterContextRetirement(t *testing.T) {
	t.Parallel()
	for _, failRead := range []bool{false, true} {
		t.Run(fmt.Sprintf("query failure %t", failRead), func(t *testing.T) {
			t.Parallel()
			f := newZCodeTranscriptFixture(t, true)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			transcript := newZCodeToolTranscript(ctx, agent.NewProviderServices(f.sink), func() zcodeToolStoreLocation { return f.location }, nil)
			tooltranscripttest.ReleaseToolStoreAtTestEnd(t, transcript)
			source := transcript.SourceForTest().(*zcodeToolSource)
			_, err := f.db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, zcodeNativeToolFixture("completed"))
			require.NoError(t, err)
			f.writeOutputFile(t, "session")
			lookup := map[string]zcodeToolLookup{"call": {callID: "call", messageID: "message"}}
			records, err := readZCodeToolRecords(t.Context(), source.store, source.outputFiles, f.location, lookup)
			require.NoError(t, err)
			require.Len(t, records, 1)
			require.True(t, source.ToolStoreHandleOpenForTest())
			cancel()
			require.Eventually(t, func() bool { return !source.ToolStoreHandleOpenForTest() }, 30*time.Second, time.Millisecond)
			if failRead {
				_, err = f.db.Exec(`DROP TABLE part`)
				require.NoError(t, err)
			}
			records, err = readZCodeToolRecords(t.Context(), source.store, source.outputFiles, f.location, lookup)
			if failRead {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Len(t, records, 1)
			}
			assert.False(t, source.ToolStoreHandleOpenForTest(), "a final native read must release its temporary handle on every return")
		})
	}
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
			databasePath: filepath.Join(directory, "store.db"), outputFileRoot: filepath.Join(directory, "artifacts"), sessionID: "session",
		},
	}
	if createDatabase {
		f.db = agenttest.NewFixtureDB(t, f.location.databasePath, zcodeToolStoreDDL)
	}
	f.transcript = newZCodeToolTranscript(t.Context(), agent.NewProviderServices(f.sink), func() zcodeToolStoreLocation { return f.location }, nil)
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

type zcodeSupplementRead struct {
	err        error
	contextErr error
}

// retryAllowed permits only a read that exhausted its interim deadline.
// A successful empty read or another error must fail the test immediately.
func (r zcodeSupplementRead) retryAllowed() bool {
	return errors.Is(r.contextErr, context.DeadlineExceeded) && onlyZCodeDeadlineErrors(r.err)
}

// A joined read failure must contain no unrelated cause, even when one cause is a deadline.
func onlyZCodeDeadlineErrors(err error) bool {
	if err == nil || err == context.DeadlineExceeded {
		return true
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		children := wrapped.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !onlyZCodeDeadlineErrors(child) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		child := wrapped.Unwrap()
		return child != nil && onlyZCodeDeadlineErrors(child)
	default:
		return false
	}
}

// observedZCodeToolSource records the real native reader's result before the worker cancels its context.
type observedZCodeToolSource struct {
	*zcodeToolSource
	mu    sync.Mutex
	reads []zcodeSupplementRead
}

func (s *observedZCodeToolSource) ReadSupplements(ctx context.Context, location tooltranscript.Location, pending map[string]agent.MessageContent, final bool) (map[string][]byte, error) {
	result, err := s.zcodeToolSource.ReadSupplements(ctx, location, pending, final)
	s.mu.Lock()
	s.reads = append(s.reads, zcodeSupplementRead{err: err, contextErr: ctx.Err()})
	s.mu.Unlock()
	return result, err
}

func (s *observedZCodeToolSource) takeReads() []zcodeSupplementRead {
	s.mu.Lock()
	defer s.mu.Unlock()
	reads := s.reads
	s.reads = nil
	return reads
}

// awaitSupplement requests an interim pass and joins it before reading its result.
// Only an expired interim deadline permits another pass. The overall deadline limits a broken test.
func (f *zcodeTranscriptFixture) awaitSupplement(t *testing.T, source *observedZCodeToolSource) agenttest.Message {
	t.Helper()
	source.takeReads()
	deadline := time.Now().Add(30 * time.Second)
	for {
		f.boundary(t)
		reads := source.takeReads()
		require.Len(t, reads, 1, "each joined boundary must invoke the native reader once")
		result := f.sink.Messages()[1]
		if len(result.SupplementalContent) > 0 {
			return result
		}
		require.True(t, reads[0].retryAllowed(), "the available output needs a supplement: read error=%v, context error=%v", reads[0].err, reads[0].contextErr)
		require.True(t, time.Now().Before(deadline), "the interim reads exhausted the overall deadline")
	}
}

func TestZCodeSupplementReadRetriesOnlyDeadlineExpiry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		read zcodeSupplementRead
		want bool
	}{
		{name: "successful empty read"},
		{name: "context expired", read: zcodeSupplementRead{contextErr: context.DeadlineExceeded}, want: true},
		{name: "wrapped deadline", read: zcodeSupplementRead{err: fmt.Errorf("read: %w", context.DeadlineExceeded), contextErr: context.DeadlineExceeded}, want: true},
		{name: "unrelated failure", read: zcodeSupplementRead{err: errors.New("fixture failure"), contextErr: context.DeadlineExceeded}},
		{name: "mixed failure", read: zcodeSupplementRead{err: errors.Join(context.DeadlineExceeded, errors.New("fixture failure")), contextErr: context.DeadlineExceeded}},
		{name: "cancelled", read: zcodeSupplementRead{err: context.Canceled, contextErr: context.Canceled}},
		{name: "deadline without context expiry", read: zcodeSupplementRead{err: context.DeadlineExceeded}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.read.retryAllowed())
		})
	}
}

func (f *zcodeTranscriptFixture) writeOutputFile(t *testing.T, sessionID string) {
	t.Helper()
	directory := filepath.Join(f.location.outputFileRoot, zcodeOutputFileSegment(sessionID))
	require.NoError(t, os.MkdirAll(directory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "call-media-1-"+zcodeOutputFileFixtureID+".txt"), []byte(zcodeOutputFileFixtureData), 0o600))
}

func TestZCodeToolTranscriptRetriesLateRecordsAndOutputFiles(t *testing.T) {
	t.Parallel()
	f := newZCodeTranscriptFixture(t, true)
	source := &observedZCodeToolSource{zcodeToolSource: f.transcript.SourceForTest().(*zcodeToolSource)}
	f.transcript = tooltranscript.New(t.Context(), agent.NewProviderServices(f.sink), source)
	tooltranscripttest.ReleaseToolStoreAtTestEnd(t, f.transcript)
	_, err := f.db.Exec(`INSERT INTO part VALUES ('part', 'session', 'message', ?)`, zcodeNativeToolFixture("running"))
	require.NoError(t, err)
	f.persistPair(t)
	f.boundary(t)
	assert.Empty(t, f.sink.Messages()[1].SupplementalContent)
	_, err = f.db.Exec(`UPDATE part SET data = ?`, zcodeNativeToolFixture("completed"))
	require.NoError(t, err)
	f.boundary(t)
	assert.Empty(t, f.sink.Messages()[1].SupplementalContent)
	f.writeOutputFile(t, "session")
	result := f.awaitSupplement(t, source)
	assert.Equal(t, zcodeStoredImageResult, string(result.Content))
	assert.Contains(t, string(result.SupplementalContent), zcodeOutputFileFixtureData)
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
	assert.JSONEq(t, `{}`, string(extra[contracts.ZCodeSupplementOutputFiles]))
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
	f.writeOutputFile(t, "session")
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
	f.writeOutputFile(t, "child-session")
	childID, err := f.transcript.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "spawn", ProviderChildKey: "child-session", AgentSessionID: "child-session", Title: "Child"})
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
	assert.Contains(t, string(stored.SupplementalContent), zcodeOutputFileFixtureData)
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
		}, nil)
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

type zcodeConcurrentToolSource struct {
	*zcodeToolSource
	canFinalize func() bool
}

func (source *zcodeConcurrentToolSource) CanFinalizeTurn() bool { return source.canFinalize() }

func TestZCodeToolTranscript_KeepsConcurrentResultsUntilTheLastOwnerEnds(t *testing.T) {
	t.Parallel()
	f := newZCodeTranscriptFixture(t, true)
	f.location.sessionID = "sess-1"
	a := newZCodeTestAgent(t, agent.NewProviderServices(f.sink))
	source := &zcodeConcurrentToolSource{zcodeToolSource: newZCodeToolSource(newZCodeToolStoreForTest(t), func() zcodeToolStoreLocation { return f.location }), canFinalize: func() bool {
		a.Mu.Lock()
		defer a.Mu.Unlock()
		return !a.turnActive
	}}
	transcript := tooltranscript.New(t.Context(), a.sink, source)
	tooltranscripttest.ReleaseToolStoreAtTestEnd(t, transcript)
	a.sink = transcript
	a.toolTranscript = transcript
	a.HandleOutput(zcodeTurnEventLine(t, 1, "foreground", contracts.ZCodeEventTurnStarted, `{"turnNumber":1}`))
	a.HandleOutput(zcodeTurnEventLine(t, 2, "background", contracts.ZCodeEventTurnStarted, `{"turnNumber":2,"inputSource":"background_task"}`))
	for index, turnID := range []string{"foreground", "background"} {
		a.HandleOutput(zcodeTurnEventLine(t, int64(3+index), turnID, contracts.ZCodeEventToolUpdated, fmt.Sprintf(`{"kind":"scheduled","toolCallId":"same","assistantMessageId":"message-%d","toolName":"Bash","input":{"command":"pwd"}}`, index)))
	}
	openings := f.sink.Messages()
	require.Len(t, openings, 2)
	a.HandleOutput(zcodeTurnEventLine(t, 5, "foreground", contracts.ZCodeEventToolUpdated, `{"kind":"result","toolCallId":"same","result":{"content":"preview-a"}}`))
	a.HandleOutput(zcodeTurnEventLine(t, 6, "foreground", contracts.ZCodeEventTurnCompleted, `{"resultType":"success"}`))
	assert.Contains(t, transcript.PendingSpanIDsForTest(), openings[0].SpanID, "the first owner's boundary must preserve pending native enrichment")
	for index := 0; index < 2; index++ {
		native := fmt.Sprintf(`{"type":"tool","callID":"same","tool":"Bash","state":{"status":"completed","output":"result-%d"}}`, index)
		_, err := f.db.Exec(`INSERT INTO part VALUES (?, 'sess-1', ?, ?)`, fmt.Sprintf("part-%d", index), fmt.Sprintf("message-%d", index), native)
		require.NoError(t, err)
	}
	a.HandleOutput(zcodeTurnEventLine(t, 7, "background", contracts.ZCodeEventToolUpdated, `{"kind":"result","toolCallId":"same","result":{"content":"preview-b"}}`))
	a.HandleOutput(zcodeTurnEventLine(t, 8, "background", contracts.ZCodeEventTurnCompleted, `{"resultType":"success"}`))
	transcript.WaitForSupplementsForTest()
	for index, opening := range openings {
		var closing *agenttest.Message
		for _, row := range f.sink.Messages() {
			if row.SpanID == opening.SpanID && row.Closing {
				copied := row
				closing = &copied
			}
		}
		require.NotNil(t, closing)
		var supplement contracts.ZCodeToolResultEnvelope
		require.NotEmpty(t, closing.SupplementalContent)
		require.NoError(t, json.Unmarshal(closing.SupplementalContent, &supplement))
		assert.Equal(t, fmt.Sprintf("message-%d", index), supplement.NativeTool.MessageID)
		assert.Contains(t, string(supplement.NativeTool.Data), fmt.Sprintf("result-%d", index))
	}
	assert.Empty(t, transcript.PendingSpanIDsForTest())
}

func TestZCodeToolTranscript_QueuedHistoricalResultKeepsItsNativeImage(t *testing.T) {
	t.Parallel()
	for _, beforeFirstAttempt := range []bool{false, true} {
		t.Run(fmt.Sprintf("queued before first attempt %t", beforeFirstAttempt), func(t *testing.T) {
			t.Parallel()
			f := newZCodeTranscriptFixture(t, true)
			f.transcript.UpdateSessionID("session")
			_, err := f.db.Exec(`INSERT INTO part VALUES ('old-part','session','message',?)`, zcodeNativeToolFixture("completed"))
			require.NoError(t, err)
			f.writeOutputFile(t, "session")
			queue := agent.OutputQueueFor(f.transcript)
			var release atomic.Bool
			var blocker *agent.OutputTicket
			if beforeFirstAttempt {
				blocker = queue.Append(agent.OutputWrite{Label: "hold old session output", Run: func() error {
					if !release.Load() {
						return agent.ErrOutputInFlight
					}
					return nil
				}})[0]
				require.ErrorIs(t, queue.Flush(), agent.ErrOutputInFlight)
			}
			err = f.transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(zcodeStoredImageRequest)}, agent.SpanInfo{SpanID: "call", SpanType: "mcp__docs__read"})
			require.True(t, agent.OutputRetained(err))
			f.sink.PersistErr = errors.New("storage unavailable")
			metadata, err := json.Marshal(map[string]any{contracts.ZCodeWorkerMetadataInvocation: contracts.ZCodeInvocationMetadata{SpanID: "call", CallID: "call", MessageID: "message", SessionID: "session", ToolName: "mcp__docs__read"}})
			require.NoError(t, err)
			err = f.transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(zcodeStoredImageResult), Metadata: metadata}, agent.SpanInfo{SpanID: "call", SpanType: "mcp__docs__read", Closing: true})
			require.True(t, agent.OutputRetained(err))
			f.location.sessionID = "new-session"
			f.transcript.AdoptNativeSession("new-session")
			newRequest := `{"type":"tool.updated","payload":{"kind":"scheduled","toolCallId":"call","assistantMessageId":"new-message","toolName":"Bash"}}`
			newResult := `{"type":"tool.updated","payload":{"kind":"result","toolCallId":"call","result":{"success":true,"content":"new preview"}}}`
			require.True(t, agent.OutputRetained(f.transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(newRequest)}, agent.SpanInfo{SpanID: "call", SpanType: "Bash"})))
			require.True(t, agent.OutputRetained(f.transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(newResult)}, agent.SpanInfo{SpanID: "call", SpanType: "Bash", Closing: true})))
			progressBefore := f.sink.ProgressUpdates()
			f.sink.PersistErr = nil
			release.Store(true)
			if blocker != nil {
				require.NoError(t, queue.FlushAfterReady(blocker))
			} else {
				require.NoError(t, queue.Flush())
			}
			require.NoError(t, f.transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
			f.transcript.WaitForSupplementsForTest()
			var oldRow, newRow *agenttest.Message
			for _, row := range f.sink.Messages() {
				if !row.Closing || !row.Stored() {
					continue
				}
				copy := row
				if row.AgentSessionID == "session" {
					oldRow = &copy
				}
				if row.AgentSessionID == "new-session" {
					newRow = &copy
				}
			}
			require.NotNil(t, oldRow)
			require.NotNil(t, newRow)
			assert.Contains(t, string(oldRow.SupplementalContent), zcodeOutputFileFixtureData)
			assert.Empty(t, newRow.SupplementalContent, "historical enrichment must not target the newer row of a reused span")
			assert.Equal(t, []byte(zcodeStoredImageResult), oldRow.Content)
			assert.Equal(t, progressBefore, f.sink.ProgressUpdates(), "historical data must not publish replacement-session progress")
		})
	}
}

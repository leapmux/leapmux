package agent

import (
	"context"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testToolSource is a toolSupplementSource whose answers one test controls. Set each
// hook before the first message, because the supplement worker also reads them.
type testToolSource struct {
	noopToolSupplementSource
	locateHook func(sessionID string) toolTranscriptLocation
	readHook   func(ctx context.Context, pending map[string]MessageContent, final bool) map[string][]byte
	callIDHook func(original []byte) string
	childHook  func() toolSupplementSource
}

func (t *testToolSource) providerName() string { return "Test" }

func (t *testToolSource) locate(sessionID string) toolTranscriptLocation {
	return t.locateHook(sessionID)
}

func (t *testToolSource) toolCallID(original []byte) string {
	if t.callIDHook == nil {
		return "call"
	}
	return t.callIDHook(original)
}

func (t *testToolSource) readSupplements(ctx context.Context, _ string, pending map[string]MessageContent, final bool) (map[string][]byte, error) {
	return t.readHook(ctx, pending, final), nil
}

func (t *testToolSource) newChild() toolSupplementSource {
	if t.childHook == nil {
		return nil
	}
	return t.childHook()
}

// pendingSpanIDs reports the tool calls that the transcript still waits for. It joins
// the supplement worker first, so the read sees the finished state of every pass.
func pendingSpanIDs(s *toolTranscript) []string {
	s.waitForSupplements()
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.pending))
	for id := range s.pending {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// toolStoreSource is a transcript source that keeps a provider database handle.
//
// A source with no handle of its own declares neither method, and
// releaseToolStoreAtTestEnd then fails rather than pass over it in silence.
type toolStoreSource interface {
	// closeToolStore releases the handle.
	closeToolStore()
	// toolStoreHandleOpen reports whether a handle is open now.
	toolStoreHandleOpen() bool
}

func (z *zcodeToolSource) closeToolStore() { z.store.close() }

func (z *zcodeToolSource) toolStoreHandleOpen() bool {
	z.store.mu.Lock()
	defer z.store.mu.Unlock()
	return z.store.db != nil
}

// reset is the only close the Cursor store has: the handle and the blob index go
// together, because a store file that is gone invalidates both.
func (c *cursorToolSource) closeToolStore() { c.store.reset() }

func (c *cursorToolSource) toolStoreHandleOpen() bool {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	return c.store.db != nil
}

// releaseToolStoreAtTestEnd closes the transcript's provider database handle when the
// test ends, and closes it SYNCHRONOUSLY.
//
// Production releases that handle from context.AfterFunc, which runs on a goroutine
// of its own once the agent's context ends. A test context ends just BEFORE the
// test's cleanup functions run, so that goroutine races the RemoveAll of t.TempDir.
// Unix unlinks an open file without complaint, so the race is invisible there.
// Windows refuses to remove a file that any handle holds open, and SQLite opens
// every file -- read-only included -- with FILE_SHARE_READ|FILE_SHARE_WRITE and
// never FILE_SHARE_DELETE. The race therefore failed the cleanup of the temporary
// directory on Windows alone.
//
// Call this AFTER t.TempDir, because cleanup functions run in reverse order.
func releaseToolStoreAtTestEnd(t *testing.T, transcript *toolTranscript) {
	t.Helper()
	source, ok := transcript.source.(toolStoreSource)
	require.True(t, ok, "a %T transcript source keeps no provider database handle", transcript.source)
	t.Cleanup(source.closeToolStore)
}

// releaseToolStoreAtTestEnd must close a handle that is really open, for each
// provider that keeps one.
//
// TestEveryTestClosesTheProviderStoreHandleItOpens reads the call sites and cannot see
// this: a closeToolStore wired to the wrong object satisfies that scan and closes
// nothing. The assertion INSIDE the subtest is what keeps this test honest, because a
// handle that never opened would pass the one outside it for the wrong reason.
func TestReleaseToolStoreAtTestEndClosesTheProviderHandle(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	cursorPath := filepath.Join(directory, "cursor.db")
	newFixtureDB(t, cursorPath, cursorStoreDDL)
	zcodePath := filepath.Join(directory, "zcode.db")
	newFixtureDB(t, zcodePath, zcodeToolStoreDDL)

	// A context that THIS test ends, after the assertion below. Each constructor also
	// releases its handle from context.AfterFunc when the agent context ends, and
	// t.Context() ends at the subtest's cleanup -- so a subtest built on t.Context()
	// would assert what that AfterFunc did and never what the helper did. It passed
	// with closeToolStore emptied to a no-op, which is how the vacuity was found.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var sources []toolStoreSource
	// A subtest, because t.Run returns only after the subtest's cleanup functions
	// finish. That is the point in time this test is about.
	t.Run("one turn of each provider", func(t *testing.T) {
		cursor := newCursorToolTranscript(ctx, &testSink{}, func() string { return cursorPath })
		releaseToolStoreAtTestEnd(t, cursor)
		zcode := newZCodeToolTranscript(ctx, &testSink{}, func() zcodeToolStoreLocation {
			return zcodeToolStoreLocation{databasePath: zcodePath, sessionID: "session"}
		})
		releaseToolStoreAtTestEnd(t, zcode)

		// The turn end reads each store, which is what opens the handle.
		require.NoError(t, cursor.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
			MessageContent{Original: []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"search","status":"completed"}`)},
			SpanInfo{SpanID: "search", Closing: true}))
		require.NoError(t, zcode.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
			MessageContent{Original: []byte(zcodeStoredImageRequest)}, SpanInfo{SpanID: "call"}))
		require.NoError(t, zcode.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
			MessageContent{Original: []byte(zcodeStoredImageResult)}, SpanInfo{SpanID: "call", Closing: true}))
		for _, transcript := range []*toolTranscript{cursor, zcode} {
			require.NoError(t, transcript.PersistTurnEnd(MessageContent{Original: []byte(`{"done":true}`)}, SpanInfo{}))
			source, isStoreSource := transcript.source.(toolStoreSource)
			require.True(t, isStoreSource, "a %T source keeps no handle", transcript.source)
			require.True(t, source.toolStoreHandleOpen(), "the turn end must leave a handle open to close")
			sources = append(sources, source)
		}
	})

	for _, source := range sources {
		assert.False(t, source.toolStoreHandleOpen(),
			"%T still holds its database handle after the test that opened it ended", source)
	}
}

func TestToolTranscriptUsesClosingSpansAcrossProtocols(t *testing.T) {
	t.Parallel()
	for _, original := range []string{
		`{"type":"tool.updated","payload":{"kind":"result","toolCallId":"call","result":{"success":true,"content":"image"}}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call","status":"in_progress"}`,
	} {
		t.Run(original, func(t *testing.T) {
			t.Parallel()
			sink := &testSink{}
			source := &testToolSource{
				locateHook: func(string) toolTranscriptLocation {
					return toolTranscriptLocation{sessionKey: "session", path: "store", ready: true}
				},
				readHook: func(_ context.Context, pending map[string]MessageContent, _ bool) map[string][]byte {
					assert.Equal(t, original, string(pending["call"].Original))
					return map[string][]byte{"call": []byte(`{"recovered":true}`)}
				},
			}
			transcript := newToolTranscript(t.Context(), sink, source)
			content := MessageContent{Original: []byte(original), Completion: MessageCompletionInterrupted}
			require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, SpanInfo{SpanID: "call", Closing: true}))
			require.NoError(t, transcript.PersistTurnEnd(MessageContent{Original: []byte(`{"done":true}`)}, SpanInfo{}))
			result := sink.Messages()[0]
			assert.Equal(t, original, string(result.Content))
			assert.Equal(t, MessageCompletionInterrupted, result.Completion)
			assert.JSONEq(t, `{"recovered":true}`, string(result.SupplementalContent))
		})
	}
}

// newTestToolTranscript builds a transcript whose supplement reader a test controls.
func newTestToolTranscript(t *testing.T, sink ProviderServices, read func(map[string]MessageContent, bool) map[string][]byte) (*toolTranscript, *testToolSource) {
	t.Helper()
	source := &testToolSource{
		locateHook: func(sessionID string) toolTranscriptLocation {
			return toolTranscriptLocation{sessionKey: sessionID, ready: true}
		},
		readHook: func(_ context.Context, pending map[string]MessageContent, final bool) map[string][]byte {
			return read(pending, final)
		},
	}
	return newToolTranscript(t.Context(), sink, source), source
}

// A provider whose records need no path still reads them. Pi returned os.TempDir()
// purely to pass a guard that tested the path, which said nothing true about Pi.
func TestToolTranscriptEnrichesALocationWithNoPath(t *testing.T) {
	t.Parallel()
	sink := &testSink{}
	transcript, _ := newTestToolTranscript(t, sink, func(map[string]MessageContent, bool) map[string][]byte {
		return map[string][]byte{"call": []byte(`{"recovered":true}`)}
	})
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		MessageContent{Original: []byte(`{"toolCallId":"call"}`)}, SpanInfo{SpanID: "call", Closing: true}))
	require.NoError(t, transcript.PersistTurnEnd(MessageContent{Original: []byte(`{"done":true}`)}, SpanInfo{}))
	require.NotEmpty(t, sink.Messages())
	assert.JSONEq(t, `{"recovered":true}`, string(sink.Messages()[0].SupplementalContent))
}

// A location that is not ready reads nothing, so a provider that reports one costs no
// deadline and no supplement.
func TestToolTranscriptSkipsALocationThatIsNotReady(t *testing.T) {
	t.Parallel()
	sink := &testSink{}
	var reads atomic.Int64
	transcript, source := newTestToolTranscript(t, sink, func(map[string]MessageContent, bool) map[string][]byte {
		reads.Add(1)
		return nil
	})
	source.locateHook = func(sessionID string) toolTranscriptLocation {
		return toolTranscriptLocation{sessionKey: sessionID, path: "/a/store"}
	}
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		MessageContent{Original: []byte(`{"toolCallId":"call"}`)}, SpanInfo{SpanID: "call", Closing: true}))
	require.NoError(t, transcript.PersistTurnEnd(MessageContent{Original: []byte(`{"done":true}`)}, SpanInfo{}))
	assert.Zero(t, reads.Load())
}

// EnrichMessage reports whether a row took the supplement. A pass that discarded that
// answer deleted the entry anyway, and the turn end then cleared what was left, so the
// result was lost for good.
func TestToolTranscriptKeepsAPendingResultTheRowRefused(t *testing.T) {
	t.Parallel()
	sink := &testSink{}
	var passes atomic.Int64
	transcript, _ := newTestToolTranscript(t, sink, func(pending map[string]MessageContent, _ bool) map[string][]byte {
		if len(pending) == 0 {
			return nil
		}
		passes.Add(1)
		return map[string][]byte{"call": []byte(`{"recovered":true}`)}
	})
	// The row does not exist yet, so the first pass cannot enrich it.
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		MessageContent{Original: []byte(`{"toolCallId":"call"}`)}, SpanInfo{SpanID: "other", Closing: true}))
	transcript.mu.Lock()
	transcript.pending["call"] = MessageContent{Original: []byte(`{"toolCallId":"call"}`)}
	transcript.mu.Unlock()
	transcript.UpdateSessionID("session-1")
	assert.Equal(t, []string{"call"}, pendingSpanIDs(transcript),
		"a row that refused the supplement must stay pending for the next pass")
	assert.Equal(t, int64(1), passes.Load())
}

// CleanupChildAgent is part of the same interface, and the wrapper is the only holder
// of the child transcript. Without the override the map kept every dead child and each
// turn end re-ran that child's whole pass.
func TestToolTranscriptCleanupDropsTheChildTranscript(t *testing.T) {
	t.Parallel()
	sink := &testSink{}
	var childPasses atomic.Int64
	transcript, source := newTestToolTranscript(t, sink, func(map[string]MessageContent, bool) map[string][]byte { return nil })
	parentLocate := source.locateHook
	source.childHook = func() toolSupplementSource {
		child := &testToolSource{readHook: source.readHook}
		child.locateHook = func(sessionID string) toolTranscriptLocation {
			childPasses.Add(1)
			return parentLocate(sessionID)
		}
		return child
	}
	require.NotNil(t, transcript.ChildSink("child-1"))
	transcript.mu.Lock()
	require.Len(t, transcript.children, 1)
	transcript.mu.Unlock()
	childPasses.Store(0)

	transcript.CleanupChildAgent("child-1")
	transcript.mu.Lock()
	remaining := len(transcript.children)
	retired := len(transcript.retired)
	transcript.mu.Unlock()
	assert.Zero(t, remaining)
	assert.Equal(t, 1, retired, "the cleanup retires the child rather than dropping it")
	// The cleanup runs on the goroutine that drains the provider's stdout, and it runs
	// once per finished subagent, so it must read the provider's store ZERO times.
	assert.Zero(t, childPasses.Load(), "the cleanup must not read the provider's store")

	require.NoError(t, transcript.PersistTurnEnd(MessageContent{Original: []byte(`{"done":true}`)}, SpanInfo{}))
	// EXACTLY one: the turn end owes the retired child its final pass, and a count
	// rather than a lower limit is what refuses a cleanup that re-enters or recurses.
	assert.Equal(t, int64(1), childPasses.Swap(0), "the turn end drains the retired child exactly once")

	require.NoError(t, transcript.PersistTurnEnd(MessageContent{Original: []byte(`{"done":true}`)}, SpanInfo{}))
	assert.Zero(t, childPasses.Load(), "a second turn end must not re-run a pass for a drained child")
}

// Nothing calls PersistChildTurnEnd anywhere in the worker, so CleanupChildAgent is
// the ONLY end a child transcript ever reaches. A cleanup that dropped the child
// without draining it lost every supplement the child still held -- and a subagent's
// LAST tool call is always among them, because PersistMessage asks for its pass
// before it adds the entry, so no running pass covers it.
func TestToolTranscriptCleanupFlushesWhatTheChildStillHolds(t *testing.T) {
	t.Parallel()
	sink := &testSink{}
	transcript, source := newTestToolTranscript(t, sink, func(map[string]MessageContent, bool) map[string][]byte { return nil })
	source.childHook = func() toolSupplementSource {
		child := &testToolSource{}
		child.locateHook = source.locateHook
		child.readHook = func(_ context.Context, pending map[string]MessageContent, _ bool) map[string][]byte {
			out := make(map[string][]byte, len(pending))
			for id := range pending {
				out[id] = []byte(`{"nativeTool":{"recovered":true}}`)
			}
			return out
		}
		return child
	}
	childSink := transcript.ChildSink("child-1")
	require.NotNil(t, childSink)

	// The subagent's last tool result: a closing span, which is what enters pending.
	require.NoError(t, childSink.PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		MessageContent{Original: []byte(`{"toolCallId":"call","result":"done"}`)},
		SpanInfo{SpanID: "call", Closing: true},
	))

	transcript.CleanupChildAgent("child-1")

	childRecorder, ok := sink.ChildSink("child-1").(*testSink)
	require.True(t, ok)
	rows := childRecorder.Messages()
	require.Len(t, rows, 1)
	assert.NotContains(t, string(rows[0].SupplementalContent), "recovered",
		"the cleanup itself must not read the provider's store, because it runs on the stdout reader")

	require.NoError(t, transcript.PersistTurnEnd(MessageContent{Original: []byte(`{"done":true}`)}, SpanInfo{}))

	// The child's own recording sink holds the row the drain enriched.
	rows = childRecorder.Messages()
	require.Len(t, rows, 1)
	assert.Contains(t, string(rows[0].SupplementalContent), "recovered",
		"the turn end must flush the supplement the retired child still held")
}

// reset drops every child transcript, and adoptLocation drops them again when the
// session key changes. Both used to drop them WITHOUT a drain, so the child's last
// supplement was lost exactly as it was before CleanupChildAgent learned to drain --
// and adoptLocation dropped them without closing the worker either, which stranded the
// child's goroutine and its context watch for the life of the agent.
func TestToolTranscriptDroppingAChildDrainsAndClosesItOnEveryPath(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		drop func(transcript *toolTranscript)
	}{
		{name: "reset", drop: func(transcript *toolTranscript) { transcript.reset() }},
		{name: "session change", drop: func(transcript *toolTranscript) { transcript.UpdateSessionID("session-2") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &testSink{}
			transcript, source := newTestToolTranscript(t, sink, func(map[string]MessageContent, bool) map[string][]byte { return nil })
			source.childHook = func() toolSupplementSource {
				child := &testToolSource{}
				child.locateHook = source.locateHook
				child.readHook = func(_ context.Context, pending map[string]MessageContent, _ bool) map[string][]byte {
					out := make(map[string][]byte, len(pending))
					for id := range pending {
						out[id] = []byte(`{"nativeTool":{"recovered":true}}`)
					}
					return out
				}
				return child
			}
			transcript.UpdateSessionID("session-1")
			childSink := transcript.ChildSink("child-1")
			require.NotNil(t, childSink)
			require.NoError(t, childSink.PersistMessage(
				leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
				MessageContent{Original: []byte(`{"toolCallId":"call","result":"done"}`)},
				SpanInfo{SpanID: "call", Closing: true},
			))
			child, ok := childSink.(*toolTranscript)
			require.True(t, ok)

			tc.drop(transcript)

			childRecorder, ok := sink.ChildSink("child-1").(*testSink)
			require.True(t, ok)
			rows := childRecorder.Messages()
			require.Len(t, rows, 1)
			assert.Contains(t, string(rows[0].SupplementalContent), "recovered",
				"dropping a child must flush the supplement it still held")

			child.mu.Lock()
			defer child.mu.Unlock()
			assert.True(t, child.closed, "dropping a child must end its worker and its context watch")
			assert.Nil(t, child.stopWatch)
		})
	}
}

// A source with no child hook has no child transcript, so the wrapped sink serves the
// child directly and no goroutine opens for it.
func TestToolTranscriptChildSinkWithoutAChildSourceDelegates(t *testing.T) {
	t.Parallel()
	sink := &testSink{}
	transcript, _ := newTestToolTranscript(t, sink, func(map[string]MessageContent, bool) map[string][]byte { return nil })
	require.Same(t, sink.ChildSink("child-1"), transcript.ChildSink("child-1"))
	transcript.mu.Lock()
	defer transcript.mu.Unlock()
	assert.Empty(t, transcript.children)
}

// blockingToolSource holds one interim read until a test releases it, and records the
// order of every pass.
type blockingToolSource struct {
	noopToolSupplementSource
	// release frees every interim read. entered closes when the FIRST interim read
	// begins, so a test can wait for the worker to reach the store.
	release     chan struct{}
	entered     chan struct{}
	enteredOnce sync.Once
	mu          sync.Mutex
	passes      []bool
	inRead      int
	overlap     bool
}

func (b *blockingToolSource) providerName() string { return "Blocking" }

func (b *blockingToolSource) locate(sessionID string) toolTranscriptLocation {
	return toolTranscriptLocation{sessionKey: sessionID, ready: true}
}

func (b *blockingToolSource) toolCallID([]byte) string { return "call" }

func (b *blockingToolSource) readSupplements(_ context.Context, _ string, _ map[string]MessageContent, final bool) (map[string][]byte, error) {
	b.mu.Lock()
	b.inRead++
	b.overlap = b.overlap || b.inRead > 1
	b.passes = append(b.passes, final)
	b.mu.Unlock()
	if !final {
		b.enteredOnce.Do(func() { close(b.entered) })
		<-b.release
	}
	b.mu.Lock()
	b.inRead--
	b.mu.Unlock()
	return nil, nil
}

func (b *blockingToolSource) record() ([]bool, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]bool(nil), b.passes...), b.overlap
}

// blockedReaderWait caps how long a test waits for a call that must NOT block. It
// fires only when the supplement read stopped the reader goroutine, which is the
// defect under test, so it is never a timing window that a slow machine can close.
const blockedReaderWait = 30 * time.Second

func persistToolResult(t *testing.T, transcript *toolTranscript, spanID string) {
	t.Helper()
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		MessageContent{Original: []byte(`{"toolCallId":"call"}`)}, SpanInfo{SpanID: spanID, Closing: true}))
}

// The provider read runs on the supplement worker, so a store that answers slowly must
// not stop the goroutine that drains the provider's stdout. That goroutine used to
// perform the read itself, under the transcript's own mutex.
func TestToolTranscriptDoesNotBlockTheReaderOnASlowProviderRead(t *testing.T) {
	t.Parallel()
	sink := &testSink{}
	source := &blockingToolSource{release: make(chan struct{}), entered: make(chan struct{})}
	transcript := newToolTranscript(t.Context(), sink, source)

	persistToolResult(t, transcript, "call")
	// The next agent message asks for a pass. The worker then blocks inside the read.
	persistToolResult(t, transcript, "call")
	<-source.entered

	persisted := make(chan error, 1)
	go func() {
		persisted <- transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
			MessageContent{Original: []byte(`{"text":"more output"}`)}, SpanInfo{})
	}()
	select {
	case err := <-persisted:
		require.NoError(t, err)
	case <-time.After(blockedReaderWait):
		close(source.release)
		t.Fatal("the supplement read stopped the goroutine that persists provider output")
	}
	assert.Equal(t, 3, sink.MessageCount(), "the provider's output reached the sink while the read still ran")

	close(source.release)
	require.NoError(t, transcript.PersistTurnEnd(MessageContent{Original: []byte(`{"done":true}`)}, SpanInfo{}))
}

// The turn end joins the supplement worker. Without that join the final pass and an
// interim pass would read the provider's store at the same time, and the turn end
// would clear the pending entries that the interim pass still holds.
func TestToolTranscriptTurnEndJoinsTheSupplementWorker(t *testing.T) {
	t.Parallel()
	sink := &testSink{}
	source := &blockingToolSource{release: make(chan struct{}), entered: make(chan struct{})}
	transcript := newToolTranscript(t.Context(), sink, source)

	persistToolResult(t, transcript, "call")
	persistToolResult(t, transcript, "call")
	<-source.entered

	calling := make(chan struct{})
	ended := make(chan error, 1)
	go func() {
		close(calling)
		ended <- transcript.PersistTurnEnd(MessageContent{Original: []byte(`{"done":true}`)}, SpanInfo{})
	}()
	<-calling
	select {
	case <-ended:
		t.Fatal("the turn end completed while an interim supplement pass still ran")
	default:
	}

	close(source.release)
	select {
	case err := <-ended:
		require.NoError(t, err)
	case <-time.After(blockedReaderWait):
		t.Fatal("the turn end never completed after the interim pass finished")
	}
	passes, overlap := source.record()
	assert.False(t, overlap, "two supplement passes must never read the provider's store at the same time")
	assert.Equal(t, []bool{false, true}, passes, "the interim pass finishes, and the final pass follows it")
}

// The final pass gets a longer budget than an interim pass: it is the last chance, and
// what it does not read is provider output the reader never sees.
func TestToolTranscriptGivesTheFinalPassALongerBudget(t *testing.T) {
	t.Parallel()
	assert.Greater(t, toolSupplementFinalReadBudget, toolSupplementReadBudget)
	sink := &testSink{}
	budgets := make(chan time.Duration, 2)
	source := &testToolSource{
		locateHook: func(sessionID string) toolTranscriptLocation {
			return toolTranscriptLocation{sessionKey: sessionID, ready: true}
		},
		readHook: func(ctx context.Context, _ map[string]MessageContent, _ bool) map[string][]byte {
			deadline, ok := ctx.Deadline()
			require.True(t, ok, "every pass runs under a deadline")
			budgets <- time.Until(deadline)
			return nil
		},
	}
	transcript := newToolTranscript(t.Context(), sink, source)
	persistToolResult(t, transcript, "call")
	persistToolResult(t, transcript, "call")
	transcript.waitForSupplements()
	require.NoError(t, transcript.PersistTurnEnd(MessageContent{Original: []byte(`{"done":true}`)}, SpanInfo{}))
	close(budgets)

	var observed []time.Duration
	for budget := range budgets {
		observed = append(observed, budget)
	}
	require.Len(t, observed, 2)
	assert.LessOrEqual(t, observed[0], toolSupplementReadBudget)
	assert.Greater(t, observed[1], toolSupplementReadBudget)
	assert.LessOrEqual(t, observed[1], toolSupplementFinalReadBudget)
}

// The agent's context ending stops the supplement worker, so a finished agent leaves no
// goroutine waiting on its transcript.
func TestToolTranscriptStopsTheSupplementWorkerWithTheAgentContext(t *testing.T) {
	t.Parallel()
	sink := &testSink{}
	ctx, cancel := context.WithCancel(t.Context())
	transcript := newToolTranscript(ctx, sink, &testToolSource{
		locateHook: func(sessionID string) toolTranscriptLocation {
			return toolTranscriptLocation{sessionKey: sessionID, ready: true}
		},
		readHook: func(context.Context, map[string]MessageContent, bool) map[string][]byte { return nil },
	})
	persistToolResult(t, transcript, "call")
	persistToolResult(t, transcript, "call")
	transcript.waitForSupplements()
	transcript.mu.Lock()
	started := transcript.started
	transcript.mu.Unlock()
	require.True(t, started, "a pending result starts the supplement worker")

	cancel()
	require.Eventually(t, func() bool {
		transcript.mu.Lock()
		defer transcript.mu.Unlock()
		return transcript.closed
	}, blockedReaderWait, time.Millisecond)

	// A closed transcript still enriches at the turn end, because the final pass runs
	// on the caller's goroutine.
	require.NoError(t, transcript.PersistTurnEnd(MessageContent{Original: []byte(`{"done":true}`)}, SpanInfo{}))
}

// PersistChildPrompt and PersistChildUserMessage BYPASS this decorator: the raw
// sink's implementation resolves ChildSink on its own receiver, which Go's
// embedding cannot redirect. That is harmless only while both writes carry
// MESSAGE_SOURCE_USER, which this transcript passes straight through.
//
// A DIFFERENTIAL test, not an output assertion: it writes through the decorator and
// through the bare sink and requires the two rows to agree. It passes today and
// fails the moment either write starts carrying agent content, or this transcript
// starts acting on a user-source child write -- which is the exact change that
// would make the bypass a defect.
func TestToolTranscriptChildUserWritesAreUnaffectedByTheDecorator(t *testing.T) {
	t.Parallel()

	decorated := &testSink{}
	transcript, source := newTestToolTranscript(t, decorated, func(map[string]MessageContent, bool) map[string][]byte { return nil })
	source.childHook = func() toolSupplementSource { return &testToolSource{locateHook: source.locateHook} }
	require.NotNil(t, transcript.ChildSink("child-1"))

	bare := &testSink{}
	require.NotNil(t, bare.ChildSink("child-1"))

	for _, write := range []struct {
		name string
		run  func(services ChildServices) error
	}{
		{"prompt", func(services ChildServices) error { return services.PersistChildPrompt("child-1", "do the thing") }},
		{"user message", func(services ChildServices) error { return services.PersistChildUserMessage("child-1", "and this too") }},
	} {
		require.NoError(t, write.run(transcript), write.name)
		require.NoError(t, write.run(bare), write.name)
	}

	decoratedChild, ok := decorated.ChildSink("child-1").(*testSink)
	require.True(t, ok)
	bareChild, ok := bare.ChildSink("child-1").(*testSink)
	require.True(t, ok)
	require.Equal(t, bareChild.Messages(), decoratedChild.Messages(),
		"a decorated child user write must match the bare one; if it stops matching, the two writes need an override")
	for _, message := range decoratedChild.Messages() {
		assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, message.Source,
			"the bypass is only safe while these writes carry the USER source")
	}
}

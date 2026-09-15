package agent

import (
	"context"
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
	transcript.mu.Unlock()
	assert.Zero(t, remaining)

	require.NoError(t, transcript.PersistTurnEnd(MessageContent{Original: []byte(`{"done":true}`)}, SpanInfo{}))
	assert.Zero(t, childPasses.Load(), "a turn end must not reach a child the caller cleaned up")
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

package tooltranscript

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolTranscriptUsesClosingSpansAcrossProtocols(t *testing.T) {
	t.Parallel()
	for _, original := range []string{
		`{"type":"tool.updated","payload":{"kind":"result","toolCallId":"call","result":{"success":true,"content":"image"}}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call","status":"in_progress"}`,
	} {
		t.Run(original, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			source := &testToolSource{
				locateHook: func(string) Location {
					return Location{SessionKey: "session", Path: "store", Ready: true}
				},
				readHook: func(_ context.Context, pending map[string]agent.MessageContent, _ bool) map[string][]byte {
					assert.Equal(t, original, string(pending["call"].Original))
					return map[string][]byte{"call": []byte(`{"recovered":true}`)}
				},
			}
			transcript := New(t.Context(), agent.NewProviderServices(sink), source)
			content := agent.MessageContent{Original: []byte(original), Completion: agent.MessageCompletionInterrupted}
			require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{SpanID: "call", Closing: true}))
			require.NoError(t, transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
			result := sink.Messages()[0]
			assert.Equal(t, original, string(result.Content))
			assert.Equal(t, agent.MessageCompletionInterrupted, result.Completion)
			assert.JSONEq(t, `{"recovered":true}`, string(result.SupplementalContent))
		})
	}
}

func TestToolTranscriptKeepsSpanIdentitySeparateFromTheNativeToolID(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	transcript, source := newTestToolTranscript(t, agent.NewProviderServices(sink), func(pending map[string]agent.MessageContent, _ bool) map[string][]byte {
		assert.Contains(t, pending, "invocation-span")
		return map[string][]byte{"invocation-span": []byte(`{"recovered":true}`)}
	})
	source.callIDHook = func([]byte) string { return "native-call" }
	transcript.UpdateSessionID("native-session")
	original := []byte(`{"toolCallId":"native-call"}`)
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: original}, agent.SpanInfo{SpanID: "invocation-span", Closing: true}))
	require.NoError(t, transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
	row := sink.Messages()[0]
	assert.Equal(t, original, row.Content)
	assert.Equal(t, "invocation-span", row.SpanID)
	assert.JSONEq(t, `{"recovered":true}`, string(row.SupplementalContent))
}

// newTestToolTranscript builds a transcript whose supplement reader a test controls.
func newTestToolTranscript(t *testing.T, sink agent.ProviderServices, read func(map[string]agent.MessageContent, bool) map[string][]byte) (*Transcript, *testToolSource) {
	t.Helper()
	source := &testToolSource{
		locateHook: func(sessionID string) Location {
			return Location{SessionKey: sessionID, Ready: true}
		},
		readHook: func(_ context.Context, pending map[string]agent.MessageContent, final bool) map[string][]byte {
			return read(pending, final)
		},
	}
	return New(t.Context(), sink, source), source
}

// A provider whose records need no path still reads them. Pi returned os.TempDir()
// purely to pass a guard that tested the path, which said nothing true about Pi.
func TestToolTranscriptEnrichesALocationWithNoPath(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	transcript, _ := newTestToolTranscript(t, agent.NewProviderServices(sink), func(map[string]agent.MessageContent, bool) map[string][]byte {
		return map[string][]byte{"call": []byte(`{"recovered":true}`)}
	})
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"toolCallId":"call"}`)}, agent.SpanInfo{SpanID: "call", Closing: true}))
	require.NoError(t, transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
	require.NotEmpty(t, sink.Messages())
	assert.JSONEq(t, `{"recovered":true}`, string(sink.Messages()[0].SupplementalContent))
}

// A location that is not ready reads nothing, so a provider that reports one costs no
// deadline and no supplement.
func TestToolTranscriptSkipsALocationThatIsNotReady(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	var reads atomic.Int64
	transcript, source := newTestToolTranscript(t, agent.NewProviderServices(sink), func(map[string]agent.MessageContent, bool) map[string][]byte {
		reads.Add(1)
		return nil
	})
	source.locateHook = func(sessionID string) Location {
		return Location{SessionKey: sessionID, Path: "/a/store"}
	}
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"toolCallId":"call"}`)}, agent.SpanInfo{SpanID: "call", Closing: true}))
	require.NoError(t, transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
	assert.Zero(t, reads.Load())
}

// EnrichMessage reports whether a row took the supplement. A pass that discarded that
// answer deleted the entry anyway, and the turn end then cleared what was left, so the
// result was lost for good.
func TestToolTranscriptKeepsAPendingResultTheRowRefused(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("session-1")
	var passes atomic.Int64
	transcript, source := newTestToolTranscript(t, agent.NewProviderServices(sink), func(pending map[string]agent.MessageContent, _ bool) map[string][]byte {
		if len(pending) == 0 {
			return nil
		}
		passes.Add(1)
		return map[string][]byte{"call": []byte(`{"recovered":true}`)}
	})
	source.callIDHook = func([]byte) string { return "" }
	// The row does not exist yet, so the first pass cannot enrich it.
	span := agent.SpanInfo{SpanID: "other", Closing: true}
	content := transcript.CaptureMessage(agent.MessageContent{Original: []byte(`{"toolCallId":"call"}`), WriteReceipt: agent.NewTranscriptWriteReceipt()}, span)
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, span))
	transcript.mu.Lock()
	transcript.nextPendingEpoch++
	transcript.pending["call"] = pendingToolRow{content: content, epoch: transcript.nextPendingEpoch, sourceGeneration: transcript.sourceState.toolSourceGeneration}
	transcript.mu.Unlock()
	transcript.UpdateSessionID("session-1")
	assert.Equal(t, []string{"call"}, transcript.PendingSpanIDsForTest(),
		"a row that refused the supplement must stay pending for the next pass")
	assert.Equal(t, int64(1), passes.Load())
}

// CleanupChildAgent is part of the same interface, and the wrapper is the only holder
// of the child transcript. Without the override the map kept every dead child and each
// turn end re-ran that child's whole pass.
func TestToolTranscriptCleanupDropsTheChildTranscript(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	var childPasses atomic.Int64
	transcript, source := newTestToolTranscript(t, agent.NewProviderServices(sink), func(map[string]agent.MessageContent, bool) map[string][]byte { return nil })
	parentLocate := source.locateHook
	source.childHook = func(_ string, _ agent.ProviderServices) Source {
		child := &testToolSource{readHook: source.readHook}
		child.locateHook = func(sessionID string) Location {
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
	require.NoError(t, transcript.WaitForRetirementsForTest(testutil.DeadlineContext(t)))
	transcript.mu.Lock()
	remaining := len(transcript.children)
	retired := len(transcript.retired)
	transcript.mu.Unlock()
	assert.Zero(t, remaining)
	assert.Zero(t, retired, "completed native enrichment releases the retired child")
	assert.Equal(t, int64(1), childPasses.Swap(0), "the child queue performs one final native read")

	require.NoError(t, transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
	// EXACTLY one: the turn end owes the retired child its final pass, and a count
	// rather than a lower limit is what refuses a cleanup that re-enters or recurses.
	assert.Zero(t, childPasses.Swap(0), "the parent end must not repeat completed child enrichment")

	require.NoError(t, transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
	assert.Zero(t, childPasses.Load(), "a second turn end must not re-run a pass for a drained child")
}

// Nothing calls PersistChildTurnEnd anywhere in the worker, so CleanupChildAgent is
// the ONLY end a child transcript ever reaches. A cleanup that dropped the child
// without draining it lost every supplement the child still held -- and a subagent's
// LAST tool call is always among them, because PersistMessage asks for its pass
// before it adds the entry, so no running pass covers it.
func TestToolTranscriptCleanupFlushesWhatTheChildStillHolds(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	transcript, source := newTestToolTranscript(t, agent.NewProviderServices(sink), func(map[string]agent.MessageContent, bool) map[string][]byte { return nil })
	source.childHook = func(_ string, _ agent.ProviderServices) Source {
		child := &testToolSource{}
		child.locateHook = source.locateHook
		child.readHook = func(_ context.Context, pending map[string]agent.MessageContent, _ bool) map[string][]byte {
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
		agent.MessageContent{Original: []byte(`{"toolCallId":"call","result":"done"}`)},
		agent.SpanInfo{SpanID: "call", Closing: true},
	))

	transcript.CleanupChildAgent("child-1")
	require.NoError(t, transcript.WaitForRetirementsForTest(testutil.DeadlineContext(t)))

	childRecorder := sink.Child("child-1")
	rows := childRecorder.Messages()
	require.Len(t, rows, 1)
	assert.Contains(t, string(rows[0].SupplementalContent), "recovered",
		"the child's queue recovers its final result without waiting for the parent to end")

	require.NoError(t, transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))

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
		drop func(transcript *Transcript)
	}{
		{name: "reset", drop: func(transcript *Transcript) { transcript.Reset() }},
		{name: "session change", drop: func(transcript *Transcript) { transcript.UpdateSessionID("session-2") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			transcript, source := newTestToolTranscript(t, agent.NewProviderServices(sink), func(map[string]agent.MessageContent, bool) map[string][]byte { return nil })
			source.childHook = func(_ string, _ agent.ProviderServices) Source {
				child := &testToolSource{}
				child.locateHook = source.locateHook
				child.readHook = func(_ context.Context, pending map[string]agent.MessageContent, _ bool) map[string][]byte {
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
				agent.MessageContent{Original: []byte(`{"toolCallId":"call","result":"done"}`)},
				agent.SpanInfo{SpanID: "call", Closing: true},
			))
			child, ok := childSink.(*Transcript)
			require.True(t, ok)

			tc.drop(transcript)
			require.NoError(t, transcript.WaitForRetirementsForTest(testutil.DeadlineContext(t)))

			childRecorder := sink.Child("child-1")
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

// A child with no native reader still retains its lifetime and exact original bytes.
func TestToolTranscriptChildSinkWithoutAChildSourcePreservesItsLifetime(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	transcript, _ := newTestToolTranscript(t, agent.NewProviderServices(sink), func(map[string]agent.MessageContent, bool) map[string][]byte { return nil })
	// The lifecycle wrapper adds no native supplement or interim reader.
	row := []byte(`{"type":"text","text":"child row"}`)
	require.NoError(t, transcript.ChildSink("child-1").PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: row}, agent.SpanInfo{}))
	messages := sink.Child("child-1").Messages()
	require.Len(t, messages, 1)
	assert.JSONEq(t, string(row), string(messages[0].Content))
	assert.Empty(t, messages[0].SupplementalContent)
	transcript.mu.Lock()
	child := transcript.children["child-1"]
	transcript.mu.Unlock()
	require.NotNil(t, child)
	child.mu.Lock()
	assert.False(t, child.started)
	child.mu.Unlock()
	transcript.CleanupChildAgent("child-1")
	require.NoError(t, transcript.WaitForRetirementsForTest(testutil.DeadlineContext(t)))
}

// blockingToolSource holds one interim read until a test releases it, and records the
// order of every pass.
type blockingToolSource struct {
	SourceDefaults
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

func (b *blockingToolSource) ProviderName() string { return "Blocking" }

func (b *blockingToolSource) Locate(sessionID string) Location {
	return Location{SessionKey: sessionID, Ready: true}
}

func (b *blockingToolSource) ToolCallID([]byte) string { return "call" }

func (b *blockingToolSource) ReadSupplements(_ context.Context, _ Location, _ map[string]agent.MessageContent, final bool) (map[string][]byte, error) {
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

func (b *blockingToolSource) CaptureSupplement(Location, agent.MessageContent, agent.SpanInfo) SupplementReader {
	return nil
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

func persistToolResult(t *testing.T, transcript *Transcript, spanID string) {
	t.Helper()
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"toolCallId":"call"}`)}, agent.SpanInfo{SpanID: spanID, Closing: true}))
}

// The provider read runs on the supplement worker, so a store that answers slowly must
// not stop the goroutine that drains the provider's stdout. That goroutine used to
// perform the read itself, under the transcript's own mutex.
func TestToolTranscriptDoesNotBlockTheReaderOnASlowProviderRead(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	source := &blockingToolSource{release: make(chan struct{}), entered: make(chan struct{})}
	transcript := New(t.Context(), agent.NewProviderServices(sink), source)

	persistToolResult(t, transcript, "call")
	// The next agent message asks for a pass. The worker then blocks inside the read.
	persistToolResult(t, transcript, "call")
	<-source.entered

	persisted := make(chan error, 1)
	go func() {
		persisted <- transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
			agent.MessageContent{Original: []byte(`{"text":"more output"}`)}, agent.SpanInfo{})
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
	require.NoError(t, transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
}

// The turn end joins the supplement worker. Without that join the final pass and an
// interim pass would read the provider's store at the same time, and the turn end
// would clear the pending entries that the interim pass still holds.
func TestToolTranscriptTurnEndJoinsTheSupplementWorker(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	source := &blockingToolSource{release: make(chan struct{}), entered: make(chan struct{})}
	transcript := New(t.Context(), agent.NewProviderServices(sink), source)

	persistToolResult(t, transcript, "call")
	persistToolResult(t, transcript, "call")
	<-source.entered

	calling := make(chan struct{})
	ended := make(chan error, 1)
	go func() {
		close(calling)
		ended <- transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{})
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
	sink := &agenttest.Sink{}
	budgets := make(chan time.Duration, 2)
	source := &testToolSource{
		locateHook: func(sessionID string) Location {
			return Location{SessionKey: sessionID, Ready: true}
		},
		readHook: func(ctx context.Context, _ map[string]agent.MessageContent, _ bool) map[string][]byte {
			deadline, ok := ctx.Deadline()
			require.True(t, ok, "every pass runs under a deadline")
			budgets <- time.Until(deadline)
			return nil
		},
	}
	transcript := New(t.Context(), agent.NewProviderServices(sink), source)
	persistToolResult(t, transcript, "call")
	persistToolResult(t, transcript, "call")
	transcript.waitForSupplements()
	require.NoError(t, transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
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
	sink := &agenttest.Sink{}
	ctx, cancel := context.WithCancel(t.Context())
	transcript := New(ctx, agent.NewProviderServices(sink), &testToolSource{
		locateHook: func(sessionID string) Location {
			return Location{SessionKey: sessionID, Ready: true}
		},
		readHook: func(context.Context, map[string]agent.MessageContent, bool) map[string][]byte { return nil },
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
	require.NoError(t, transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
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

	decorated := &agenttest.Sink{}
	transcript, source := newTestToolTranscript(t, agent.NewProviderServices(decorated), func(map[string]agent.MessageContent, bool) map[string][]byte { return nil })
	source.childHook = func(_ string, _ agent.ProviderServices) Source { return &testToolSource{locateHook: source.locateHook} }
	require.NotNil(t, transcript.ChildSink("child-1"))

	bare := &agenttest.Sink{}
	require.NotNil(t, bare.ChildSink("child-1"))

	for _, write := range []struct {
		name string
		run  func(services agent.ChildServices) error
	}{
		{"prompt", func(services agent.ChildServices) error {
			return services.PersistChildPrompt("child-1", "do the thing")
		}},
		{"user message", func(services agent.ChildServices) error {
			return services.PersistChildUserMessage("child-1", "and this too")
		}},
	} {
		require.NoError(t, write.run(transcript), write.name)
		require.NoError(t, write.run(bare), write.name)
	}

	decoratedChild := decorated.Child("child-1")
	bareChild := bare.Child("child-1")
	require.Equal(t, bareChild.Messages(), decoratedChild.Messages(),
		"a decorated child user write must match the bare one; if it stops matching, the two writes need an override")
	for _, message := range decoratedChild.Messages() {
		assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, message.Source,
			"the bypass is only safe while these writes carry the USER source")
	}
}

// testToolSource is a Source whose answers one test controls. Set each
// hook before the first message, because the supplement worker also reads them.
type testToolSource struct {
	SourceDefaults
	locateHook  func(sessionID string) Location
	readHook    func(ctx context.Context, pending map[string]agent.MessageContent, final bool) map[string][]byte
	callIDHook  func(original []byte) string
	childHook   func(string, agent.ProviderServices) Source
	initialHook func([]byte) []byte
	observeHook func(agent.MessageContent)
	finishHook  func()
}

func (t *testToolSource) ProviderName() string { return "Test" }

func (source *testToolSource) InitialSupplement(_ context.Context, _ string, original []byte, _ agent.SpanInfo) ([]byte, error) {
	if source.initialHook == nil {
		return nil, nil
	}
	return source.initialHook(original), nil
}

func (source *testToolSource) ObserveMessage(content agent.MessageContent, _ agent.SpanInfo) {
	if source.observeHook != nil {
		source.observeHook(content)
	}
}

func (source *testToolSource) FinishTurn() {
	if source.finishHook != nil {
		source.finishHook()
	}
}

func (t *testToolSource) Locate(sessionID string) Location {
	return t.locateHook(sessionID)
}

func (t *testToolSource) ToolCallID(original []byte) string {
	if t.callIDHook == nil {
		return "call"
	}
	return t.callIDHook(original)
}

func (t *testToolSource) ReadSupplements(ctx context.Context, _ Location, pending map[string]agent.MessageContent, final bool) (map[string][]byte, error) {
	return t.readHook(ctx, pending, final), nil
}

func (t *testToolSource) CaptureSupplement(_ Location, content agent.MessageContent, span agent.SpanInfo) SupplementReader {
	hook := t.readHook
	if hook == nil {
		return nil
	}
	captured := content.Clone()
	return func(ctx context.Context, final bool) ([]byte, error) {
		return hook(ctx, map[string]agent.MessageContent{span.SpanID: captured}, final)[span.SpanID], nil
	}
}

func (t *testToolSource) NewChild(childID string, services agent.ProviderServices) Source {
	if t.childHook == nil {
		return nil
	}
	return t.childHook(childID, services)
}

func TestToolTranscriptOldReadCannotEnrichAReplacementPendingEntry(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	entered := make(chan struct{})
	release := make(chan struct{})
	var reads atomic.Int64
	source := &testToolSource{
		locateHook: func(sessionID string) Location { return Location{SessionKey: sessionID, Ready: true} },
		readHook: func(_ context.Context, pending map[string]agent.MessageContent, _ bool) map[string][]byte {
			if reads.Add(1) != 1 {
				return nil
			}
			assert.JSONEq(t, `{"record":"old"}`, string(pending["call"].Original))
			close(entered)
			<-release
			return map[string][]byte{"call": []byte(`{"stored_result":"old result"}`)}
		},
	}
	transcript := New(t.Context(), agent.NewProviderServices(sink), source)
	transcript.UpdateSessionID("native-session")
	span := agent.SpanInfo{SpanID: "call", Closing: true}
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"record":"old"}`)}, span))
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"record":"pass trigger"}`)}, agent.SpanInfo{}))
	<-entered
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"record":"replacement"}`)}, span))
	close(release)
	transcript.WaitForSupplementsForTest()
	rows := sink.Messages()
	require.Len(t, rows, 3)
	assert.Empty(t, rows[2].SupplementalContent, "an old provider read must not supply a replacement entry's result")
	assert.Equal(t, []string{"call"}, transcript.PendingSpanIDsForTest())
}

type rejectingTranscriptWrite struct {
	agent.ProviderServices
	messageAttempts int
	dividerAttempts int
}

func (writer *rejectingTranscriptWrite) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	writer.messageAttempts++
	if writer.messageAttempts == 1 {
		return errors.New("the message write failed")
	}
	return writer.ProviderServices.PersistMessage(source, content, span)
}

func (writer *rejectingTranscriptWrite) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	writer.dividerAttempts++
	if writer.dividerAttempts == 1 {
		return errors.New("the divider write failed")
	}
	return writer.ProviderServices.PersistTurnEnd(content, span)
}

func TestCapturedToolTranscriptFreezesTheInitialSupplementBeforeItsFirstAttempt(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	delegate := &rejectingTranscriptWrite{ProviderServices: agent.NewProviderServices(sink)}
	var reads, observations int
	source := &testToolSource{
		locateHook:  func(session string) Location { return Location{SessionKey: session, Ready: true} },
		readHook:    func(context.Context, map[string]agent.MessageContent, bool) map[string][]byte { return nil },
		initialHook: func([]byte) []byte { reads++; return []byte(`{"initial":1}`) },
		observeHook: func(agent.MessageContent) { observations++ },
	}
	transcript := New(t.Context(), delegate, source)
	transcript.UpdateSessionID("native")
	record := agent.CaptureTranscript(transcript, agent.MessageContent{Original: []byte(`{"record":"result"}`)}, agent.SpanInfo{SpanID: "call", Closing: true})
	assert.Equal(t, 1, reads)
	require.ErrorContains(t, record.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT), "message write failed")
	require.NoError(t, record.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT))
	assert.Equal(t, 1, reads)
	assert.Equal(t, 1, observations)
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Equal(t, []byte(`{"initial":1}`), rows[0].SupplementalContent)
}

func TestCapturedToolTranscriptOldMessageCannotReplaceCurrentPendingState(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	source := &testToolSource{
		locateHook: func(session string) Location { return Location{SessionKey: session, Ready: true} },
		readHook:   func(context.Context, map[string]agent.MessageContent, bool) map[string][]byte { return nil },
	}
	transcript := New(t.Context(), agent.NewProviderServices(sink), source)
	transcript.UpdateSessionID("native")
	transcript.SetTurnState(agent.TurnState{Active: true}, 1)
	span := agent.SpanInfo{SpanID: "call", Closing: true}
	old := agent.CaptureTranscript(transcript, agent.MessageContent{Original: []byte(`{"record":"old"}`)}, span)
	transcript.SetTurnState(agent.TurnState{}, 2)
	transcript.SetTurnState(agent.TurnState{Active: true}, 3)
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"record":"current"}`)}, span))
	require.NoError(t, old.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT))
	transcript.mu.Lock()
	assert.Equal(t, []byte(`{"record":"current"}`), transcript.pending["call"].content.Original)
	transcript.mu.Unlock()
}

func TestCapturedToolTranscriptRejectedDividerKeepsItsSourceAndPendingEntry(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	delegate := &rejectingTranscriptWrite{ProviderServices: agent.NewProviderServices(sink), messageAttempts: 1}
	var finished int
	source := &testToolSource{
		locateHook: func(session string) Location { return Location{SessionKey: session, Ready: true} },
		readHook:   func(context.Context, map[string]agent.MessageContent, bool) map[string][]byte { return nil },
		finishHook: func() { finished++ },
	}
	transcript := New(t.Context(), delegate, source)
	transcript.UpdateSessionID("native")
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"record":"result"}`)}, agent.SpanInfo{SpanID: "call", Closing: true}))
	record := agent.CaptureTranscript(transcript, agent.MessageContent{Original: []byte(`{"record":"divider"}`)}, agent.SpanInfo{})
	require.ErrorContains(t, record.PersistTurnEnd(), "divider write failed")
	assert.Equal(t, []string{"call"}, transcript.PendingSpanIDsForTest())
	assert.Zero(t, finished)
	require.NoError(t, record.PersistTurnEnd())
	assert.Empty(t, transcript.PendingSpanIDsForTest())
	assert.Equal(t, 1, finished)
}

func TestToolTranscriptChildSourceReadsItsOwnDelegateForAReusedSpan(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	parentServices := agent.NewProviderServices(sink)
	transcript, source := newTestToolTranscript(t, parentServices, func(map[string]agent.MessageContent, bool) map[string][]byte { return nil })
	source.childHook = func(childID string, delegate agent.ProviderServices) Source {
		assert.Equal(t, "child-1", childID, "the source factory receives the existing Worker child identity")
		return &testToolSource{
			locateHook: func(string) Location { return Location{SessionKey: "native-parent", Ready: true} },
			readHook: func(_ context.Context, pending map[string]agent.MessageContent, _ bool) map[string][]byte {
				if delegate == nil {
					return nil
				}
				result := make(map[string][]byte)
				for spanID := range pending {
					request, err := delegate.ReadToolRequest(spanID)
					if !assert.NoError(t, err) || !assert.NotNil(t, request) {
						return nil
					}
					var original map[string]any
					if !assert.NoError(t, json.Unmarshal(request.Content.Original, &original)) {
						return nil
					}
					encoded, err := json.Marshal(map[string]any{"lookupOwner": original["owner"]})
					if !assert.NoError(t, err) {
						return nil
					}
					result[spanID] = encoded
				}
				return result
			},
		}
	}
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"toolCallId":"call","owner":"root"}`)}, agent.SpanInfo{SpanID: "call"}))
	child := transcript.ChildSink("child-1")
	require.NotNil(t, child)
	require.NoError(t, child.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"toolCallId":"call","owner":"child"}`)}, agent.SpanInfo{SpanID: "call"}))
	require.NoError(t, child.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"toolCallId":"call","status":"completed"}`)}, agent.SpanInfo{SpanID: "call", Closing: true}))
	require.NoError(t, transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{}`)}, agent.SpanInfo{}))
	rows := sink.Child("child-1").Messages()
	require.Len(t, rows, 2)
	require.True(t, len(rows[1].SupplementalContent) > 0, "the child delegate must supply its actual stored opener")
	var extra map[string]any
	require.NoError(t, json.Unmarshal(rows[1].SupplementalContent, &extra))
	assert.Equal(t, "child", extra["lookupOwner"], "the child source must read the child span when the root holds the same native call ID")
	assert.Empty(t, sink.Messages()[0].SupplementalContent, "the child pass must not enrich the root row")
}

type reentrantToolWriter struct {
	agent.ProviderServices
	messageHook func()
	enrichHook  func()
	messageOnce sync.Once
	enrichOnce  sync.Once
}

func (writer *reentrantToolWriter) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	err := writer.ProviderServices.PersistMessage(source, content, span)
	if err == nil && writer.messageHook != nil {
		// Do not hold sync.Once through a handler that enters the same writer.
		invoke := false
		writer.messageOnce.Do(func() { invoke = true })
		if invoke {
			writer.messageHook()
		}
	}
	return err
}

func (writer *reentrantToolWriter) EnrichMessage(change agent.MessageEnrichment) (bool, error) {
	written, err := writer.ProviderServices.EnrichMessage(change)
	if written && err == nil && writer.enrichHook != nil {
		invoke := false
		writer.enrichOnce.Do(func() { invoke = true })
		if invoke {
			writer.enrichHook()
		}
	}
	return written, err
}

func TestCapturedToolTranscriptMessageObserverCanStartAReplacementTurn(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	writer := &reentrantToolWriter{ProviderServices: agent.NewProviderServices(sink)}
	transcript, _ := newTestToolTranscript(t, writer, func(map[string]agent.MessageContent, bool) map[string][]byte { return nil })
	transcript.SetTurnState(agent.TurnState{Active: true}, 1)
	span := agent.SpanInfo{SpanID: "call", Closing: true}
	callbackResult := make(chan error, 1)
	nestedFinished := make(chan struct{})
	writer.messageHook = func() {
		transcript.SetTurnState(agent.TurnState{}, 2)
		transcript.SetTurnState(agent.TurnState{Active: true}, 3)
		nestedResult := make(chan error, 1)
		go func() {
			nestedResult <- transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
				agent.MessageContent{Original: []byte(`{"record":"replacement"}`)}, span)
			close(nestedFinished)
		}()
		select {
		case err := <-nestedResult:
			callbackResult <- err
		case <-time.After(30 * time.Second):
			callbackResult <- errors.New("the message observer holds the transcript state lock across reentry")
		}
	}
	done := make(chan error, 1)
	go func() {
		done <- transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
			agent.MessageContent{Original: []byte(`{"record":"old"}`)}, span)
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(60 * time.Second):
		t.Fatal("the message observer blocked when it started a replacement turn")
	}
	select {
	case <-nestedFinished:
	case <-time.After(30 * time.Second):
		t.Fatal("the nested message did not finish after the observer returned")
	}
	assert.ErrorIs(t, <-callbackResult, agent.ErrOutputInFlight, "reentrant output is admitted behind its original observer")
	transcript.mu.Lock()
	current := transcript.pending["call"]
	transcript.mu.Unlock()
	assert.Equal(t, []byte(`{"record":"replacement"}`), current.content.Original)
	assert.Len(t, sink.Messages(), 2)
}

func TestCapturedToolTranscriptEnrichmentObserverCanFinishTheSameTurn(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	writer := &reentrantToolWriter{ProviderServices: agent.NewProviderServices(sink)}
	transcript, _ := newTestToolTranscript(t, writer, func(pending map[string]agent.MessageContent, final bool) map[string][]byte {
		result := make(map[string][]byte, len(pending))
		for id := range pending {
			if final {
				result[id] = []byte(`{"final":true}`)
			} else {
				result[id] = []byte(`{"interim":true}`)
			}
		}
		return result
	})
	callbackResult := make(chan error, 1)
	nestedFinished := make(chan struct{})
	writer.enrichHook = func() {
		nestedResult := make(chan error, 1)
		go func() {
			nestedResult <- transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{})
			close(nestedFinished)
		}()
		select {
		case err := <-nestedResult:
			callbackResult <- err
		case <-time.After(30 * time.Second):
			callbackResult <- errors.New("the enrichment observer waits for its own supplement pass")
		}
	}
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"toolCallId":"call"}`)}, agent.SpanInfo{SpanID: "call", Closing: true}))
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"trigger":true}`)}, agent.SpanInfo{}))
	var callbackErr error
	select {
	case callbackErr = <-callbackResult:
	case <-time.After(60 * time.Second):
		t.Fatal("the enrichment observer blocked when it finished the same turn")
	}
	select {
	case <-nestedFinished:
	case <-time.After(30 * time.Second):
		t.Fatal("the nested divider did not finish after the observer returned")
	}
	assert.NoError(t, callbackErr)
	transcript.WaitForSupplementsForTest()
	rows := sink.Messages()
	require.Len(t, rows, 3)
	assert.JSONEq(t, `{"interim":true,"final":true}`, string(rows[0].SupplementalContent))
	assert.Empty(t, transcript.PendingSpanIDsForTest())
}

type observationEnrichmentWriter struct {
	agent.ProviderServices
	before     func(agent.MessageEnrichment)
	after      func(agent.MessageEnrichment, bool, error) (bool, error)
	beforeOnce atomic.Bool
	afterOnce  atomic.Bool
	fail       atomic.Bool
}

func (writer *observationEnrichmentWriter) EnrichMessage(change agent.MessageEnrichment) (bool, error) {
	if writer.before != nil && writer.beforeOnce.CompareAndSwap(false, true) {
		writer.before(change)
	}
	if writer.fail.Load() {
		return false, errors.New("the observation enrichment failed")
	}
	written, err := writer.ProviderServices.EnrichMessage(change)
	if writer.after != nil && writer.afterOnce.CompareAndSwap(false, true) {
		return writer.after(change, written, err)
	}
	return written, err
}

func newObservationTranscript(t *testing.T) (*Transcript, *agenttest.Sink, *observationEnrichmentWriter) {
	t.Helper()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("native")
	writer := &observationEnrichmentWriter{ProviderServices: agent.NewProviderServices(sink)}
	transcript, _ := newTestToolTranscript(t, writer, func(map[string]agent.MessageContent, bool) map[string][]byte { return nil })
	return transcript, sink, writer
}

func TestToolTranscriptDistinctObservationWinsAnUncommittedStoreAttempt(t *testing.T) {
	t.Parallel()
	transcript, sink := newHookedToolTranscript(t, []byte(`{"store":"record"}`))
	var built int
	sink.hook = func() {
		written, err := transcript.EnrichToolSpan("call", func([]byte) ([]byte, error) {
			built++
			return []byte(`{"frame":"once"}`), nil
		})
		assert.NoError(t, err)
		assert.True(t, written)
	}
	persistClosingToolRow(t, transcript, []byte(`{"toolCallId":"call"}`))
	transcript.UpdateSessionID("session-1")
	transcript.WaitForSupplementsForTest()
	assert.Equal(t, 1, built)
	assert.JSONEq(t, `{"frame":"once"}`, string(sink.Messages()[0].SupplementalContent))
	assert.Equal(t, []string{"call"}, transcript.PendingSpanIDsForTest())
}

func TestToolTranscriptIdenticalInFlightAttemptCanRetryAfterFailure(t *testing.T) {
	t.Parallel()
	transcript, sink, writer := newObservationTranscript(t)
	persistClosingToolRow(t, transcript, []byte(`{"toolCallId":"call"}`))
	var built int
	build := func([]byte) ([]byte, error) { built++; return []byte(`{"frame":"once"}`), nil }
	writer.fail.Store(true)
	writer.before = func(agent.MessageEnrichment) {
		written, err := transcript.EnrichToolSpan("call", build)
		assert.NoError(t, err)
		assert.False(t, written)
	}
	written, err := transcript.EnrichToolSpan("call", build)
	require.ErrorContains(t, err, "observation enrichment failed")
	assert.False(t, written)
	assert.Equal(t, 2, built, "each native observation builds its bytes before identical-attempt admission")
	writer.fail.Store(false)
	written, err = transcript.EnrichToolSpan("call", build)
	require.NoError(t, err)
	assert.True(t, written)
	assert.JSONEq(t, `{"frame":"once"}`, string(sink.Messages()[0].SupplementalContent))
}

func loseExternalObservationCAS(t *testing.T, sink *agenttest.Sink, writer *observationEnrichmentWriter) {
	t.Helper()
	writer.before = func(change agent.MessageEnrichment) {
		change.SupplementalContent = []byte(`{"winner":"stored"}`)
		change.WriteReceipt = nil
		written, err := sink.EnrichMessage(change)
		assert.NoError(t, err)
		assert.True(t, written, "the competing stored revision must commit")
	}
}

func TestToolTranscriptRetainsAnExternalObservationAfterCASLoss(t *testing.T) {
	t.Parallel()
	transcript, sink, writer := newObservationTranscript(t)
	persistClosingToolRow(t, transcript, []byte(`{"toolCallId":"call"}`))
	loseExternalObservationCAS(t, sink, writer)
	written, err := transcript.EnrichToolSpan("call", func([]byte) ([]byte, error) { return []byte(`{"frame":"acknowledged"}`), nil })
	require.NoError(t, err)
	assert.False(t, written)
	assert.JSONEq(t, `{"winner":"stored"}`, string(sink.Messages()[0].SupplementalContent))
	require.NoError(t, transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
	assert.JSONEq(t, `{"winner":"stored","frame":"acknowledged"}`, string(sink.Messages()[0].SupplementalContent))
	assert.Empty(t, transcript.PendingSpanIDsForTest())
}

func TestToolTranscriptAcceptedInnerEnrichmentSurvivesOuterFailure(t *testing.T) {
	t.Parallel()
	transcript, sink, writer := newObservationTranscript(t)
	persistClosingToolRow(t, transcript, []byte(`{"toolCallId":"call"}`))
	writer.after = func(_ agent.MessageEnrichment, written bool, err error) (bool, error) {
		assert.True(t, written)
		assert.NoError(t, err)
		return false, errors.New("the outer enrichment failed")
	}
	written, err := transcript.EnrichToolSpan("call", func([]byte) ([]byte, error) { return []byte(`{"first":true}`), nil })
	require.ErrorContains(t, err, "outer enrichment failed")
	assert.False(t, written)
	assert.Equal(t, int64(1), sink.Messages()[0].SupplementalRevision)
	written, err = transcript.EnrichToolSpan("call", func([]byte) ([]byte, error) { return []byte(`{"second":true}`), nil })
	require.NoError(t, err)
	assert.True(t, written)
	assert.Equal(t, int64(2), sink.Messages()[0].SupplementalRevision)
	assert.JSONEq(t, `{"first":true,"second":true}`, string(sink.Messages()[0].SupplementalContent))
}

func TestToolTranscriptRetainedObservationUsesItsOriginalStoredSequence(t *testing.T) {
	t.Parallel()
	transcript, sink, writer := newObservationTranscript(t)
	original := []byte(`{"toolCallId":"call","same":true}`)
	persistClosingToolRow(t, transcript, original)
	loseExternalObservationCAS(t, sink, writer)
	written, err := transcript.EnrichToolSpan("call", func([]byte) ([]byte, error) { return []byte(`{"old_frame":true}`), nil })
	require.NoError(t, err)
	assert.False(t, written)
	persistClosingToolRow(t, transcript, original)
	// The second closing row requests an interim pass whose flush settles the
	// retained observation. Wait for it, so the boundary below observes the
	// observation at its own sequence instead of racing the background pass.
	transcript.WaitForSupplementsForTest()
	require.NoError(t, transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
	rows := sink.Messages()
	require.Len(t, rows, 3)
	assert.JSONEq(t, `{"winner":"stored","old_frame":true}`, string(rows[0].SupplementalContent))
	assert.Empty(t, rows[1].SupplementalContent, "equal bytes do not identify the newer stored row")
}

func TestToolTranscriptEqualBytesDoNotReplaceCommittedRowIdentity(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	writer := &reentrantToolWriter{ProviderServices: agent.NewProviderServices(sink)}
	transcript, _ := newTestToolTranscript(t, writer, func(map[string]agent.MessageContent, bool) map[string][]byte { return nil })
	original := []byte(`{"toolCallId":"call","same":true}`)
	writer.messageHook = func() {
		err := transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
			agent.MessageContent{Original: original}, agent.SpanInfo{SpanID: "call", Closing: true})
		assert.True(t, agent.OutputRetained(err))
		assert.ErrorIs(t, err, agent.ErrOutputInFlight)
	}
	persistClosingToolRow(t, transcript, original)
	written, err := transcript.EnrichToolSpan("call", func([]byte) ([]byte, error) { return []byte(`{"new_frame":true}`), nil })
	require.NoError(t, err)
	assert.True(t, written)
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.Empty(t, rows[0].SupplementalContent)
	assert.JSONEq(t, `{"new_frame":true}`, string(rows[1].SupplementalContent))
	transcript.mu.Lock()
	sequence, committed := transcript.pending["call"].content.WriteReceipt.StoredMessageSequence()
	transcript.mu.Unlock()
	assert.True(t, committed)
	assert.Equal(t, int64(2), sequence, "the pending tool must identify the last committed row")
}

func TestToolTranscriptSettledObservationCapturesOwnershipBeforeBuild(t *testing.T) {
	t.Parallel()
	transcript, sink, _ := newObservationTranscript(t)
	original := []byte(`{"toolCallId":"call","same":true}`)
	persistClosingToolRow(t, transcript, original)
	require.NoError(t, transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
	written, err := transcript.EnrichToolSpan("call", func(received []byte) ([]byte, error) {
		assert.Equal(t, original, received)
		transcript.UpdateSessionID("replacement")
		persistClosingToolRow(t, transcript, original)
		return []byte(`{"old_frame":true}`), nil
	})
	require.NoError(t, err)
	assert.True(t, written)
	rows := sink.Messages()
	require.Len(t, rows, 3)
	assert.JSONEq(t, `{"old_frame":true}`, string(rows[0].SupplementalContent))
	assert.Empty(t, rows[2].SupplementalContent)
	assert.Equal(t, "replacement", rows[2].AgentSessionID)
}

func TestToolTranscriptLaterAcceptedRevisionControlsCleanup(t *testing.T) {
	t.Parallel()
	transcript, sink, writer := newObservationTranscript(t)
	persistClosingToolRow(t, transcript, []byte(`{"toolCallId":"call"}`))
	writer.after = func(_ agent.MessageEnrichment, written bool, err error) (bool, error) {
		assert.True(t, written)
		assert.NoError(t, err)
		newer, nextErr := transcript.EnrichToolSpan("call", func([]byte) ([]byte, error) { return []byte(`{"newer":true}`), nil })
		assert.NoError(t, nextErr)
		assert.True(t, newer)
		return written, err
	}
	written, err := transcript.EnrichToolSpan("call", func([]byte) ([]byte, error) { return []byte(`{"earlier":true}`), nil })
	require.NoError(t, err)
	assert.True(t, written)
	assert.Equal(t, int64(2), sink.Messages()[0].SupplementalRevision)
	assert.JSONEq(t, `{"earlier":true,"newer":true}`, string(sink.Messages()[0].SupplementalContent))
	require.NoError(t, transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
	assert.Empty(t, transcript.PendingSpanIDsForTest())
	assert.Equal(t, int64(2), sink.Messages()[0].SupplementalRevision)
}

func TestToolTranscriptFinalBoundaryKeepsAnUnresolvedExternalObservation(t *testing.T) {
	t.Parallel()
	transcript, sink, writer := newObservationTranscript(t)
	persistClosingToolRow(t, transcript, []byte(`{"toolCallId":"call"}`))
	loseExternalObservationCAS(t, sink, writer)
	written, err := transcript.EnrichToolSpan("call", func([]byte) ([]byte, error) { return []byte(`{"retained":true}`), nil })
	require.NoError(t, err)
	assert.False(t, written)
	writer.fail.Store(true)
	boundary := agent.CaptureTranscript(transcript, agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{})
	assert.ErrorContains(t, boundary.PersistTurnEnd(), "observation enrichment failed")
	assert.Len(t, sink.Messages(), 1, "a refused preparation must not store the divider")
	writer.fail.Store(false)
	require.NoError(t, boundary.PersistTurnEnd())
	assert.JSONEq(t, `{"winner":"stored","retained":true}`, string(sink.Messages()[0].SupplementalContent))
	assert.Empty(t, transcript.PendingSpanIDsForTest())
}

type heldObservationReader struct {
	agent.ProviderServices
	entered chan struct{}
	release <-chan struct{}
	reads   atomic.Int64
}

func (reader *heldObservationReader) ReadToolResultBySeq(sequence int64) (*agent.StoredMessage, error) {
	if reader.reads.Add(1) == 1 {
		close(reader.entered)
		<-reader.release
	}
	return reader.ProviderServices.ReadToolResultBySeq(sequence)
}

func TestToolTranscriptConcurrentFlushClaimsEachObservationOnce(t *testing.T) {
	t.Parallel()
	transcript, sink, writer := newObservationTranscript(t)
	persistClosingToolRow(t, transcript, []byte(`{"toolCallId":"call"}`))
	loseExternalObservationCAS(t, sink, writer)
	written, err := transcript.EnrichToolSpan("call", func([]byte) ([]byte, error) { return []byte(`{"frame":"retained"}`), nil })
	require.NoError(t, err)
	assert.False(t, written)
	release := make(chan struct{})
	var once sync.Once
	finishRead := func() { once.Do(func() { close(release) }) }
	defer finishRead()
	reader := &heldObservationReader{ProviderServices: writer.ProviderServices, entered: make(chan struct{}), release: release}
	writer.ProviderServices = reader
	first := make(chan error, 1)
	go func() { first <- transcript.flushObservations() }()
	select {
	case <-reader.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the observation read did not start")
	}
	second := make(chan error, 1)
	go func() { second <- transcript.flushObservations() }()
	var secondError error
	select {
	case secondError = <-second:
	case <-time.After(30 * time.Second):
		finishRead()
		secondError = <-second
		t.Error("the second observation flush waited for the first read")
	}
	finishRead()
	select {
	case err = <-first:
	case <-time.After(30 * time.Second):
		t.Fatal("the first observation flush did not finish after release")
	}
	assert.NoError(t, err)
	assert.ErrorContains(t, secondError, "in flight")
	assert.Equal(t, int64(1), reader.reads.Load())
	assert.JSONEq(t, `{"winner":"stored","frame":"retained"}`, string(sink.Messages()[0].SupplementalContent))
}

func TestToolTranscriptTurnEndToleratesAConcurrentObservationFlush(t *testing.T) {
	t.Parallel()
	transcript, sink, writer := newObservationTranscript(t)
	persistClosingToolRow(t, transcript, []byte(`{"toolCallId":"call"}`))
	loseExternalObservationCAS(t, sink, writer)
	written, err := transcript.EnrichToolSpan("call", func([]byte) ([]byte, error) { return []byte(`{"frame":"retained"}`), nil })
	require.NoError(t, err)
	assert.False(t, written)
	release := make(chan struct{})
	var once sync.Once
	finishRead := func() { once.Do(func() { close(release) }) }
	defer finishRead()
	reader := &heldObservationReader{ProviderServices: writer.ProviderServices, entered: make(chan struct{}), release: release}
	writer.ProviderServices = reader
	first := make(chan error, 1)
	go func() { first <- transcript.flushObservations() }()
	select {
	case <-reader.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the observation read did not start")
	}
	// The interim pass owns the observation claim, exactly as a provider turn end
	// can land while the background supplement worker is mid-read. The boundary
	// must not fail the turn end for a claim it does not own: the observation
	// stays retained, and the flush that holds it settles the row.
	require.NoError(t, transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
	finishRead()
	select {
	case err = <-first:
	case <-time.After(30 * time.Second):
		t.Fatal("the observation flush did not finish after release")
	}
	assert.NoError(t, err)
	assert.JSONEq(t, `{"winner":"stored","frame":"retained"}`, string(sink.Messages()[0].SupplementalContent))
}

func TestToolTranscriptTurnEndStillFailsARealFlushError(t *testing.T) {
	t.Parallel()
	transcript, sink, writer := newObservationTranscript(t)
	persistClosingToolRow(t, transcript, []byte(`{"toolCallId":"call"}`))
	loseExternalObservationCAS(t, sink, writer)
	written, err := transcript.EnrichToolSpan("call", func([]byte) ([]byte, error) { return []byte(`{"retained":true}`), nil })
	require.NoError(t, err)
	assert.False(t, written)
	// A failing delegated read is a settlement failure the turn end must report;
	// only another flush's in-flight claim is tolerated at the boundary.
	writer.ProviderServices = &failingObservationReader{ProviderServices: writer.ProviderServices}
	err = transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{})
	require.Error(t, err)
	assert.NotErrorIs(t, err, errToolObservationInFlight)
	assert.ErrorContains(t, err, "the retained observation read failed")
}

type failingObservationReader struct {
	agent.ProviderServices
	failed atomic.Bool
}

func (reader *failingObservationReader) ReadToolResultBySeq(sequence int64) (*agent.StoredMessage, error) {
	if reader.failed.CompareAndSwap(false, true) {
		return nil, errors.New("the retained observation read failed")
	}
	return reader.ProviderServices.ReadToolResultBySeq(sequence)
}

func TestToolTranscriptObservationReadFailureReleasesItsClaim(t *testing.T) {
	t.Parallel()
	transcript, sink, writer := newObservationTranscript(t)
	persistClosingToolRow(t, transcript, []byte(`{"toolCallId":"call"}`))
	loseExternalObservationCAS(t, sink, writer)
	written, err := transcript.EnrichToolSpan("call", func([]byte) ([]byte, error) { return []byte(`{"retained":true}`), nil })
	require.NoError(t, err)
	assert.False(t, written)
	writer.ProviderServices = &failingObservationReader{ProviderServices: writer.ProviderServices}
	require.ErrorContains(t, transcript.flushObservations(), "retained observation read failed")
	assert.JSONEq(t, `{"winner":"stored"}`, string(sink.Messages()[0].SupplementalContent))
	require.NoError(t, transcript.flushObservations())
	assert.JSONEq(t, `{"winner":"stored","retained":true}`, string(sink.Messages()[0].SupplementalContent))
	transcript.mu.Lock()
	assert.Empty(t, transcript.observations)
	transcript.mu.Unlock()
}

func TestToolTranscriptRetainedObservationReceiptAllowsFinalBoundaryReentry(t *testing.T) {
	t.Parallel()
	transcript, sink, writer := newObservationTranscript(t)
	persistClosingToolRow(t, transcript, []byte(`{"toolCallId":"call"}`))
	loseExternalObservationCAS(t, sink, writer)
	written, err := transcript.EnrichToolSpan("call", func([]byte) ([]byte, error) { return []byte(`{"retained":true}`), nil })
	require.NoError(t, err)
	assert.False(t, written)
	writer.after = func(_ agent.MessageEnrichment, written bool, err error) (bool, error) {
		assert.True(t, written)
		assert.NoError(t, err)
		assert.NoError(t, transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
		return written, err
	}
	require.NoError(t, transcript.flushObservations())
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.JSONEq(t, `{"winner":"stored","retained":true}`, string(rows[0].SupplementalContent))
	assert.Equal(t, int64(2), rows[0].SupplementalRevision)
	assert.Empty(t, transcript.PendingSpanIDsForTest())
}

type childCleanupAuthority struct {
	agent.ProviderServices
	child             *agenttest.Sink
	cleanupCount      atomic.Int64
	enrichedAtCleanup atomic.Bool
}

func (sink *childCleanupAuthority) AcquireChildSink(childID string) agent.ChildSinkLease {
	lease := sink.ProviderServices.AcquireChildSink(childID)
	cleanup := lease.Cleanup
	lease.Cleanup = func() bool {
		if cleanup == nil || !cleanup() {
			return false
		}
		rows := sink.child.Messages()
		sink.enrichedAtCleanup.Store(len(rows) == 1 && len(rows[0].SupplementalContent) > 0)
		sink.cleanupCount.Add(1)
		return true
	}
	return lease
}

func (sink *childCleanupAuthority) CleanupChildAgent(string) {
	rows := sink.child.Messages()
	sink.enrichedAtCleanup.Store(len(rows) == 1 && len(rows[0].SupplementalContent) > 0)
	sink.cleanupCount.Add(1)
	sink.child.UpdateSessionID("retired-child")
}

func TestToolTranscript_OrdersChildRetirementAfterItsRetainedResult(t *testing.T) {
	t.Parallel()
	raw := &agenttest.Sink{}
	rawChild := raw.Child("child")
	rawChild.UpdateSessionID("native-child")
	authority := &childCleanupAuthority{ProviderServices: agent.NewProviderServices(raw), child: rawChild}
	transcript, source := newTestToolTranscript(t, authority, func(map[string]agent.MessageContent, bool) map[string][]byte { return nil })
	source.childHook = func(string, agent.ProviderServices) Source {
		return &testToolSource{locateHook: source.locateHook, readHook: func(_ context.Context, pending map[string]agent.MessageContent, _ bool) map[string][]byte {
			out := make(map[string][]byte)
			for span := range pending {
				out[span] = []byte(`{"recovered":true}`)
			}
			return out
		}}
	}
	child := transcript.ChildSink("child").(*Transcript)
	// Only the final native read can recover this result.
	child.closeSupplementWorker()
	queue := agent.OutputQueueFor(child)
	var released atomic.Bool
	blocker := queue.Append(agent.OutputWrite{Label: "hold the child result", Run: func() error {
		if !released.Load() {
			return agent.ErrOutputInFlight
		}
		return nil
	}})[0]
	require.ErrorIs(t, queue.Flush(), agent.ErrOutputInFlight)
	err := child.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"toolCallId":"call","result":"done"}`)}, agent.SpanInfo{SpanID: "call", Closing: true})
	require.True(t, agent.OutputRetained(err))
	transcript.CleanupChildAgent("child")
	assert.Zero(t, authority.cleanupCount.Load(), "physical cleanup must preserve the queued row's authority")
	require.NoError(t, transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
	assert.Equal(t, 1, raw.MessageCount(), "the root queue must continue while child storage is retained")
	assert.Zero(t, authority.cleanupCount.Load())
	released.Store(true)
	err = queue.FlushAfterReady(blocker)
	assert.True(t, err == nil || errors.Is(err, agent.ErrOutputInFlight))
	require.NoError(t, transcript.WaitForRetirementsForTest(testutil.DeadlineContext(t)))
	require.Len(t, rawChild.Messages(), 1)
	assert.JSONEq(t, `{"recovered":true}`, string(rawChild.Messages()[0].SupplementalContent))
	assert.Equal(t, int64(1), authority.cleanupCount.Load())
	assert.True(t, authority.enrichedAtCleanup.Load(), "native enrichment must finish before cleanup retires the child")
	assert.Empty(t, transcript.retired)
}

type resumedChildToolSource struct {
	*testToolSource
	running      *atomic.Bool
	observed     chan struct{}
	observedOnce sync.Once
}

func (source *resumedChildToolSource) CanFinalizeTurn() bool { return !source.running.Load() }
func (source *resumedChildToolSource) ObserveMessage(content agent.MessageContent, _ agent.SpanInfo) {
	var payload struct {
		Record string `json:"record"`
	}
	if json.Unmarshal(content.Original, &payload) == nil && payload.Record == "new" {
		source.observedOnce.Do(func() { close(source.observed) })
	}
}

func TestToolTranscript_ResumesAChildWhileItsOldResultIsRetained(t *testing.T) {
	t.Parallel()
	raw := &agenttest.Sink{}
	rawChild := raw.Child("child")
	rawChild.UpdateSessionID("native-child")
	authority := &childCleanupAuthority{ProviderServices: agent.NewProviderServices(raw), child: rawChild}
	root, source := newTestToolTranscript(t, authority, func(map[string]agent.MessageContent, bool) map[string][]byte { return nil })
	var running, released atomic.Bool
	observed := make(chan struct{})
	source.childHook = func(string, agent.ProviderServices) Source {
		return &resumedChildToolSource{testToolSource: &testToolSource{locateHook: source.locateHook, readHook: func(_ context.Context, pending map[string]agent.MessageContent, _ bool) map[string][]byte {
			out := make(map[string][]byte)
			for span, content := range pending {
				var record struct {
					Record string `json:"record"`
				}
				if json.Unmarshal(content.Original, &record) == nil {
					out[span] = []byte(`{"native":"` + record.Record + `"}`)
				}
			}
			return out
		}}, running: &running, observed: observed}
	}
	old := root.ChildSink("child").(*Transcript)
	old.closeSupplementWorker()
	queue := agent.OutputQueueFor(old)
	blocker := queue.Append(agent.OutputWrite{Label: "hold the old invocation", Run: func() error {
		if !released.Load() {
			return agent.ErrOutputInFlight
		}
		return nil
	}})[0]
	require.ErrorIs(t, queue.Flush(), agent.ErrOutputInFlight)
	err := old.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"toolCallId":"same","record":"old"}`)}, agent.SpanInfo{SpanID: "old-span", Closing: true})
	require.True(t, agent.OutputRetained(err))
	root.CleanupChildAgent("child")
	running.Store(true)
	resumed := root.ChildSink("child")
	require.NotNil(t, resumed)
	resumed.SetTurnState(agent.TurnState{Active: true}, 1)
	owner := resumed.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{}).Publication.Owner()
	err = resumed.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"toolCallId":"same","record":"new"}`)}, agent.SpanInfo{SpanID: "new-span", Closing: true})
	require.True(t, agent.OutputRetained(err))
	released.Store(true)
	err = queue.FlushAfterReady(blocker)
	assert.True(t, err == nil || errors.Is(err, agent.ErrOutputInFlight))
	ctx := testutil.DeadlineContext(t)
	select {
	case <-observed:
	case <-ctx.Done():
		t.Error("the new invocation's output waits for that invocation to end")
	}
	assert.True(t, owner.IsCurrent(), "old cleanup must preserve the resumed invocation's publication")
	assert.Zero(t, authority.cleanupCount.Load(), "the old lifetime must not clean up the resumed lifetime")
	rows := rawChild.Messages()
	if assert.Len(t, rows, 2) {
		assert.JSONEq(t, `{"native":"old"}`, string(rows[0].SupplementalContent))
	}
	// Release the old implementation's blocked policy after recording the regression.
	running.Store(false)
	_ = queue.FlushAfterReady(blocker)
	require.NoError(t, root.WaitForRetirementsForTest(testutil.DeadlineContext(t)))
}

type countedChildAcquisitions struct {
	agent.ProviderServices
	count atomic.Int64
}

func (services *countedChildAcquisitions) AcquireChildSink(id string) agent.ChildSinkLease {
	services.count.Add(1)
	return services.ProviderServices.AcquireChildSink(id)
}

func TestToolTranscript_ConcurrentChildLookupsPublishOneLease(t *testing.T) {
	t.Parallel()
	raw := &agenttest.Sink{}
	services := &countedChildAcquisitions{ProviderServices: agent.NewProviderServices(raw)}
	root, _ := newTestToolTranscript(t, services, func(map[string]agent.MessageContent, bool) map[string][]byte { return nil })
	var callers sync.WaitGroup
	results := make(chan agent.ProviderServices, 20)
	for index := 0; index < cap(results); index++ {
		callers.Go(func() { results <- root.ChildSink("child") })
	}
	callers.Wait()
	close(results)
	first := <-results
	require.NotNil(t, first)
	for result := range results {
		assert.Same(t, first, result)
	}
	assert.Equal(t, int64(1), services.count.Load(), "only the published wrapper may acquire a lifetime")
	root.CleanupChildAgent("child")
	require.NoError(t, root.WaitForRetirementsForTest(testutil.DeadlineContext(t)))
}

type pausedChildLeaseCapture struct {
	agent.ProviderServices
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (services *pausedChildLeaseCapture) AcquireChildSink(id string) agent.ChildSinkLease {
	lease := services.ProviderServices.AcquireChildSink(id)
	capture := lease.CaptureMessage
	lease.CaptureMessage = func(content agent.MessageContent, span agent.SpanInfo) agent.MessageContent {
		frozen := capture(content, span)
		if services.armed.CompareAndSwap(true, false) {
			close(services.entered)
			<-services.release
		}
		return frozen
	}
	return lease
}

func TestToolTranscript_ChildAdmittedDuringRetirementDelaysAncestorCleanup(t *testing.T) {
	t.Parallel()
	raw := &agenttest.Sink{}
	rawParent := raw.Child("parent")
	rawParent.UpdateSessionID("native-parent")
	services := &pausedChildLeaseCapture{ProviderServices: agent.NewProviderServices(raw), entered: make(chan struct{}), release: make(chan struct{})}
	root, source := newTestToolTranscript(t, services, func(map[string]agent.MessageContent, bool) map[string][]byte { return nil })
	source.childHook = func(string, agent.ProviderServices) Source {
		parentSource := &testToolSource{locateHook: source.locateHook, readHook: source.readHook}
		parentSource.childHook = func(string, agent.ProviderServices) Source {
			return &testToolSource{locateHook: source.locateHook, readHook: func(_ context.Context, pending map[string]agent.MessageContent, _ bool) map[string][]byte {
				result := make(map[string][]byte)
				for id := range pending {
					result[id] = []byte(`{"recovered":true}`)
				}
				return result
			}}
		}
		return parentSource
	}
	parent := root.ChildSink("parent").(*Transcript)
	publication := parent.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{}).Publication.Owner()
	services.armed.Store(true)
	cleaned := make(chan struct{})
	go func() { root.CleanupChildAgent("parent"); close(cleaned) }()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-services.entered:
	case <-ctx.Done():
		t.Fatal("retirement did not capture its lifetime")
	}
	grandchild := parent.ChildSink("grandchild")
	require.NotNil(t, grandchild)
	grandchild.UpdateSessionID("native-grandchild")
	queue := agent.OutputQueueFor(grandchild)
	var released atomic.Bool
	blocker := queue.Append(agent.OutputWrite{Label: "hold the grandchild result", Run: func() error {
		if !released.Load() {
			return agent.ErrOutputInFlight
		}
		return nil
	}})[0]
	require.ErrorIs(t, queue.Flush(), agent.ErrOutputInFlight)
	err := grandchild.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"toolCallId":"call","result":"done"}`)}, agent.SpanInfo{SpanID: "call", Closing: true})
	require.True(t, agent.OutputRetained(err))
	close(services.release)
	select {
	case <-cleaned:
	case <-ctx.Done():
		t.Fatal("retirement construction did not finish")
	}
	parent.mu.Lock()
	retirement := parent.retirement
	parent.mu.Unlock()
	err = parent.OutputQueue().FlushAfterReady(retirement.ticket.Load())
	assert.True(t, err == nil || errors.Is(err, agent.ErrOutputInFlight))
	require.NoError(t, root.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
	assert.True(t, publication.IsSessionCurrent(), "ancestor authority must survive its grandchild's retained output")
	released.Store(true)
	err = queue.FlushAfterReady(blocker)
	assert.True(t, err == nil || errors.Is(err, agent.ErrOutputInFlight))
	require.NoError(t, root.WaitForRetirementsForTest(testutil.DeadlineContext(t)))
	rows := rawParent.Child("grandchild").Messages()
	require.Len(t, rows, 1)
	assert.JSONEq(t, `{"recovered":true}`, string(rows[0].SupplementalContent))
	assert.False(t, publication.IsSessionCurrent())
	assert.Nil(t, parent.ChildSink("too-late"), "a committed cleanup cannot admit another descendant")
}

func TestToolTranscript_RetirementRejectsFreshCallbackRows(t *testing.T) {
	t.Parallel()
	raw := &agenttest.Sink{}
	writer := &reentrantToolWriter{ProviderServices: agent.NewProviderServices(raw)}
	transcript, _ := newTestToolTranscript(t, writer, func(map[string]agent.MessageContent, bool) map[string][]byte { return nil })
	callbackError := make(chan error, 1)
	writer.messageHook = func() {
		transcript.startRetirement()
		callbackError <- transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"record":"too-late"}`)}, agent.SpanInfo{})
	}
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"record":"accepted"}`)}, agent.SpanInfo{}))
	assert.ErrorIs(t, <-callbackError, errTranscriptRetired)
	require.Len(t, raw.Messages(), 1)
	assert.JSONEq(t, `{"record":"accepted"}`, string(raw.Messages()[0].Content))
	retirement := transcript.enqueueRetirement()
	select {
	case <-retirement.done:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the accepted row did not release retirement")
	}
}

func TestToolTranscript_RetiredWrappersRejectFreshReservations(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"batch", "generation"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			raw := &agenttest.Sink{}
			transcript, _ := newTestToolTranscript(t, agent.NewProviderServices(raw), func(map[string]agent.MessageContent, bool) map[string][]byte { return nil })
			retirement := transcript.enqueueRetirement()
			retirement.wake()
			select {
			case <-retirement.done:
			case <-testutil.DeadlineContext(t).Done():
				t.Fatal("retirement did not complete")
			}
			if kind == "batch" {
				err := agent.PersistTranscriptBatch(transcript, []agent.TranscriptMessage{{Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, Content: agent.MessageContent{Original: []byte(`{"record":"late"}`)}}})
				assert.ErrorIs(t, err, errTranscriptRetired)
				assert.False(t, agent.OutputRetained(err))
			} else {
				var buffer providerkit.GenerationBuffer
				buffer.Append("late", agent.AssembledMessageKindText, "late text", providerkit.JoinVerbatim)
				err := providerkit.FlushGeneration(&buffer, transcript, agent.MessageCompletionComplete, false)
				assert.ErrorIs(t, err, errTranscriptRetired)
				_, text, exists := buffer.ScopeSnapshot("late")
				assert.True(t, exists)
				assert.Equal(t, "late text", text)
			}
			assert.Empty(t, raw.Messages())
			assert.False(t, transcript.OutputQueue().HasPending(), "rejected output must not prevent another lease from writing")
		})
	}
}

type heldOrderedToolWriter struct {
	agent.TranscriptServices
	entered chan struct{}
	release chan struct{}
}

func (writer heldOrderedToolWriter) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	close(writer.entered)
	<-writer.release
	return writer.TranscriptServices.PersistMessage(source, content, span)
}

func TestToolTranscript_RefusesADirectWriteWhenRetirementWinsReservation(t *testing.T) {
	t.Parallel()
	raw := &agenttest.Sink{}
	transcript, _ := newTestToolTranscript(t, agent.NewProviderServices(raw), func(pending map[string]agent.MessageContent, final bool) map[string][]byte {
		if final && len(pending) > 0 {
			return map[string][]byte{"call": []byte(`{"final":true}`)}
		}
		return nil
	})
	var cleaned atomic.Bool
	transcript.setRetireCleanup(func() { cleaned.Store(true) })
	held := heldOrderedToolWriter{TranscriptServices: transcript.orderedWriter, entered: make(chan struct{}), release: make(chan struct{})}
	transcript.orderedWriter = held
	done := make(chan error, 1)
	go func() {
		done <- transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"toolCallId":"call"}`)}, agent.SpanInfo{SpanID: "call", Closing: true})
	}()
	<-held.entered
	retirement := transcript.enqueueRetirement()
	retirement.wake()
	assert.True(t, cleaned.Load(), "a call paused before reservation owns no queue position")
	close(held.release)
	require.ErrorIs(t, <-done, errTranscriptRetired)
	retirement.wake()
	select {
	case <-retirement.done:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("retirement did not finish")
	}
	assert.Empty(t, raw.Messages())
	assert.False(t, transcript.OutputQueue().HasPending())
}

func (writer heldOrderedToolWriter) OutputQueue() *agent.OutputQueue {
	return agent.OutputQueueFor(writer.TranscriptServices)
}

func TestToolTranscript_NewLeaseCannotAdmitARetiredWritersCapture(t *testing.T) {
	t.Parallel()
	raw := &agenttest.Sink{}
	raw.Child("child").UpdateSessionID("native-child")
	root, _ := newTestToolTranscript(t, agent.NewProviderServices(raw), func(map[string]agent.MessageContent, bool) map[string][]byte { return nil })
	old := root.ChildSink("child")
	captured := agent.CaptureTranscript(old, agent.MessageContent{Original: []byte(`{"record":"expired"}`)}, agent.SpanInfo{}).MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
	queue := agent.OutputQueueFor(old)
	var released atomic.Bool
	blocker := queue.Append(agent.OutputWrite{Label: "hold retirement", Run: func() error {
		if !released.Load() {
			return agent.ErrOutputInFlight
		}
		return nil
	}})[0]
	require.ErrorIs(t, queue.Flush(), agent.ErrOutputInFlight)
	root.CleanupChildAgent("child")
	newer := root.ChildSink("child")
	require.Same(t, queue, agent.OutputQueueFor(newer))
	released.Store(true)
	err := queue.FlushAfterReady(blocker)
	require.True(t, err == nil || errors.Is(err, agent.ErrOutputInFlight))
	require.NoError(t, root.WaitForRetirementsForTest(testutil.DeadlineContext(t)))
	err = agent.PersistCapturedTranscriptBatch(newer, func() []agent.CapturedTranscriptOperation { return []agent.CapturedTranscriptOperation{captured} })
	assert.ErrorIs(t, err, errTranscriptRetired)
	assert.False(t, agent.OutputRetained(err))
	assert.False(t, queue.HasPending())
	require.NoError(t, newer.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"record":"current"}`)}, agent.SpanInfo{}))
	rows := raw.Child("child").Messages()
	require.Len(t, rows, 1)
	assert.JSONEq(t, `{"record":"current"}`, string(rows[0].Content))
}

type heldToolCapture struct {
	agent.ProviderServices
	entered, release chan struct{}
	held             atomic.Bool
}

func (writer *heldToolCapture) CaptureMessage(content agent.MessageContent, span agent.SpanInfo) agent.MessageContent {
	if span.Closing && span.SpanID == "call" && writer.held.CompareAndSwap(false, true) {
		close(writer.entered)
		<-writer.release
	}
	return writer.ProviderServices.CaptureMessage(content, span)
}

func TestToolTranscript_AcceptedReservationDelaysRetirementThroughCapture(t *testing.T) {
	t.Parallel()
	raw := &agenttest.Sink{}
	writer := &heldToolCapture{ProviderServices: agent.NewProviderServices(raw), entered: make(chan struct{}), release: make(chan struct{})}
	transcript, _ := newTestToolTranscript(t, writer, func(pending map[string]agent.MessageContent, final bool) map[string][]byte {
		if final && len(pending) > 0 {
			return map[string][]byte{"call": []byte(`{"final":true}`)}
		}
		return nil
	})
	var cleaned atomic.Bool
	transcript.setRetireCleanup(func() { cleaned.Store(true) })
	done := make(chan error, 1)
	go func() {
		done <- transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"toolCallId":"call"}`)}, agent.SpanInfo{SpanID: "call", Closing: true})
	}()
	<-writer.entered
	retirement := transcript.enqueueRetirement()
	retirement.wake()
	assert.False(t, cleaned.Load())
	close(writer.release)
	require.NoError(t, <-done)
	retirement.wake()
	select {
	case <-retirement.done:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("retirement did not finish")
	}
	rows := raw.Messages()
	require.Len(t, rows, 1)
	assert.JSONEq(t, `{"final":true}`, string(rows[0].SupplementalContent))
}

type historicalSupplementSource struct {
	*testToolSource
	failure error
}

func (source *historicalSupplementSource) CaptureSupplement(_ Location, _ agent.MessageContent, _ agent.SpanInfo) SupplementReader {
	failure := source.failure
	return func(context.Context, bool) ([]byte, error) { return []byte(`{"native":"retained"}`), failure }
}

func newHistoricalObservationTranscript(t *testing.T, failure error) (*Transcript, *agenttest.Sink, *observationEnrichmentWriter) {
	t.Helper()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("native")
	writer := &observationEnrichmentWriter{ProviderServices: agent.NewProviderServices(sink)}
	source := &historicalSupplementSource{testToolSource: &testToolSource{
		locateHook: func(sessionID string) Location { return Location{SessionKey: sessionID, Ready: true} },
		readHook:   func(context.Context, map[string]agent.MessageContent, bool) map[string][]byte { return nil },
	}, failure: failure}
	transcript := New(t.Context(), writer, source)
	persistClosingToolRow(t, transcript, []byte(`{"toolCallId":"call"}`))
	return transcript, sink, writer
}

func TestToolTranscriptResetSchedulesHistoricalEnrichment(t *testing.T) {
	t.Parallel()
	transcript, sink, _ := newHistoricalObservationTranscript(t, nil)
	transcript.Reset()
	transcript.WaitForSupplementsForTest()
	assert.JSONEq(t, `{"native":"retained"}`, string(sink.Messages()[0].SupplementalContent))
}

func TestToolTranscriptHistoricalReadKeepsPartialNativeData(t *testing.T) {
	t.Parallel()
	transcript, sink, _ := newHistoricalObservationTranscript(t, errors.New("one output file is unavailable"))
	transcript.Reset()
	transcript.WaitForSupplementsForTest()
	require.NoError(t, transcript.FinalizePending())
	assert.JSONEq(t, `{"native":"retained"}`, string(sink.Messages()[0].SupplementalContent))
}

func TestToolTranscriptHistoricalNativeReadSurvivesAnExtensionObservation(t *testing.T) {
	t.Parallel()
	transcript, sink, writer := newHistoricalObservationTranscript(t, nil)
	loseExternalObservationCAS(t, sink, writer)
	written, err := transcript.EnrichToolSpan("call", func([]byte) ([]byte, error) { return []byte(`{"extension":true}`), nil })
	require.NoError(t, err)
	assert.False(t, written)
	transcript.Reset()
	transcript.WaitForSupplementsForTest()
	require.NoError(t, transcript.FinalizePending())
	assert.JSONEq(t, `{"winner":"stored","extension":true,"native":"retained"}`, string(sink.Messages()[0].SupplementalContent))
}

func TestToolTranscriptClosedWorkerFinishesAnAcceptedHistoricalResult(t *testing.T) {
	t.Parallel()
	transcript, sink, _ := newHistoricalObservationTranscript(t, nil)
	queue := transcript.OutputQueue()
	var released atomic.Bool
	blocker := queue.Append(agent.OutputWrite{Label: "hold an accepted result", Run: func() error {
		if !released.Load() {
			return agent.ErrOutputInFlight
		}
		return nil
	}})[0]
	require.ErrorIs(t, queue.Flush(), agent.ErrOutputInFlight)
	err := transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"toolCallId":"second"}`)}, agent.SpanInfo{SpanID: "second", Closing: true})
	require.True(t, agent.OutputRetained(err))
	transcript.closeSupplementWorker()
	transcript.AdoptNativeSession("replacement")
	transcript.Reset()
	released.Store(true)
	require.NoError(t, queue.FlushAfterReady(blocker))
	transcript.WaitForSupplementsForTest()
	rows := sink.Messages()
	require.NotEmpty(t, rows)
	assert.Contains(t, string(rows[len(rows)-1].SupplementalContent), `"native":"retained"`)
}

func TestToolTranscriptHistoricalReadDoesNotWaitForAReentrantNativePass(t *testing.T) {
	t.Parallel()
	transcript, sink, _ := newHistoricalObservationTranscript(t, nil)
	source := transcript.SourceForTest().(*historicalSupplementSource)
	entered := make(chan struct{})
	releaseRead := make(chan struct{})
	finalDone := make(chan error, 1)
	source.readHook = func(context.Context, map[string]agent.MessageContent, bool) map[string][]byte {
		transcript.Reset()
		go func() { finalDone <- transcript.FinalizePending() }()
		close(entered)
		<-releaseRead
		return nil
	}
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"next":true}`)}, agent.SpanInfo{}))
	<-entered
	ctx := testutil.DeadlineContext(t)
	joined := false
	select {
	case err := <-finalDone:
		joined = true
		assert.True(t, err == nil || errors.Is(err, agent.ErrOutputInFlight))
	case <-ctx.Done():
		assert.Fail(t, "finalization waited for the native read that invoked it")
	}
	close(releaseRead)
	if !joined {
		<-finalDone
	}
	transcript.WaitForSupplementsForTest()
	require.NoError(t, transcript.FinalizePending())
	assert.Contains(t, string(sink.Messages()[0].SupplementalContent), `"native":"retained"`)
}

func TestToolTranscriptFinalNativePassFlushesItsMigratedObservations(t *testing.T) {
	t.Parallel()
	transcript, sink, _ := newHistoricalObservationTranscript(t, nil)
	source := transcript.SourceForTest().(*historicalSupplementSource)
	source.readHook = func(context.Context, map[string]agent.MessageContent, bool) map[string][]byte {
		transcript.Reset()
		// The worker must release its refused read before this native observer returns.
		transcript.WaitForSupplementsForTest()
		return nil
	}
	require.NoError(t, transcript.FinalizePending())
	assert.Contains(t, string(sink.Messages()[0].SupplementalContent), `"native":"retained"`)
}

func TestToolTranscriptRetirementRefusesFreshNotifications(t *testing.T) {
	t.Parallel()
	for _, worker := range []bool{false, true} {
		t.Run(fmt.Sprintf("worker envelope %t", worker), func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			transcript, _ := newTestToolTranscript(t, agent.NewProviderServices(sink), func(map[string]agent.MessageContent, bool) map[string][]byte { return nil })
			child := transcript.ChildSink("child")
			recorder := sink.Child("child")
			transcript.CleanupChildAgent("child")
			require.NoError(t, transcript.WaitForRetirementsForTest(testutil.DeadlineContext(t)))
			content := agent.MessageContent{Original: []byte(`{"type":"late","text":"Unadmitted"}`)}
			var err error
			if worker {
				_, err = child.PersistWorkerNotification(content)
			} else {
				_, err = child.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, content)
			}
			require.ErrorIs(t, err, errTranscriptRetired)
			assert.Empty(t, recorder.PersistedNotifications())
			assert.Empty(t, recorder.LeapMuxNotifications())
		})
	}
}

func TestToolTranscript_ExpiredStreamDoesNotBlockTheNewLease(t *testing.T) {
	t.Parallel()
	raw := &agenttest.Sink{}
	child := raw.Child("child")
	child.UpdateSessionID("native-child")
	services := agent.NewProviderServices(raw)
	newChild := func() *Transcript {
		lease := services.AcquireChildSink("child")
		require.NotNil(t, lease.Sink)
		transcript := New(t.Context(), lease.Sink, unobservedChildSource{providerName: "test"})
		transcript.childLease = lease
		transcript.activateChildLease()
		return transcript
	}
	operation := func(writer *Transcript, record string) agent.CapturedTranscriptOperation {
		return agent.CaptureTranscript(writer, agent.MessageContent{Original: []byte(`{"record":"` + record + `"}`)}, agent.SpanInfo{}).MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
	}
	old := newChild()
	expired := operation(old, "expired")
	old.setRetireCleanup(func() { old.childLease.Cleanup() })
	retirement := old.enqueueRetirement()
	newer := newChild()
	queue := newer.OutputQueue()
	require.Same(t, old.OutputQueue(), queue)
	retirement.wake()
	require.True(t, old.retirementComplete())
	require.False(t, queue.HasPending())
	newer.ReportProgress(agent.ModelTextProgress("current", "the new invocation still has unfinished output"))
	progress := child.ProgressSnapshot()
	require.Positive(t, progress.ThinkingTokens)
	batch, err := agent.ReserveCapturedTranscriptBatch(newer)
	require.NoError(t, err)
	err = newer.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"record":"following"}`)}, agent.SpanInfo{})
	require.ErrorIs(t, err, agent.ErrOutputInFlight)
	require.True(t, agent.OutputRetained(err))
	effects := 0
	following := queue.Append(agent.OutputWrite{Owner: agent.NewOutputOwner(), Label: "preserve the following independent effect", Run: func() error { effects++; return nil }})[0]
	operations := []agent.CapturedTranscriptOperation{operation(newer, "prefix"), expired, operation(newer, "suffix")}
	calls, completions := 0, 0
	err = batch.PrepareStream(func() (agent.CapturedTranscriptOperation, bool, error) {
		if calls == len(operations) {
			completions++
			return agent.CapturedTranscriptOperation{}, true, nil
		}
		next := operations[calls]
		calls++
		return next, false, nil
	})
	require.ErrorIs(t, err, errTranscriptRetired)
	assert.False(t, agent.OutputRetained(err))
	assert.Equal(t, 2, calls)
	assert.Zero(t, completions)
	assert.True(t, following.Done())
	assert.Equal(t, 1, effects)
	assert.False(t, queue.HasPending())
	assert.Equal(t, progress, child.ProgressSnapshot())
	rows := child.Messages()
	require.Len(t, rows, 2)
	assert.JSONEq(t, `{"record":"prefix"}`, string(rows[0].Content))
	assert.JSONEq(t, `{"record":"following"}`, string(rows[1].Content))
	require.NoError(t, queue.Flush())
	assert.Equal(t, 2, calls, "the rejected iterator must not resume")
	assert.Equal(t, 2, child.MessageCount(), "the prefix must not repeat")
	assert.Equal(t, 1, effects)
}

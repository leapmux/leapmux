package tooltranscript

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
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
	var passes atomic.Int64
	transcript, _ := newTestToolTranscript(t, agent.NewProviderServices(sink), func(pending map[string]agent.MessageContent, _ bool) map[string][]byte {
		if len(pending) == 0 {
			return nil
		}
		passes.Add(1)
		return map[string][]byte{"call": []byte(`{"recovered":true}`)}
	})
	// The row does not exist yet, so the first pass cannot enrich it.
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"toolCallId":"call"}`)}, agent.SpanInfo{SpanID: "other", Closing: true}))
	transcript.mu.Lock()
	transcript.pending["call"] = pendingToolRow{content: agent.MessageContent{Original: []byte(`{"toolCallId":"call"}`)}}
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
	source.childHook = func() Source {
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
	transcript.mu.Lock()
	remaining := len(transcript.children)
	retired := len(transcript.retired)
	transcript.mu.Unlock()
	assert.Zero(t, remaining)
	assert.Equal(t, 1, retired, "the cleanup retires the child rather than dropping it")
	// The cleanup runs on the goroutine that drains the provider's stdout, and it runs
	// once per finished subagent, so it must read the provider's store ZERO times.
	assert.Zero(t, childPasses.Load(), "the cleanup must not read the provider's store")

	require.NoError(t, transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
	// EXACTLY one: the turn end owes the retired child its final pass, and a count
	// rather than a lower limit is what refuses a cleanup that re-enters or recurses.
	assert.Equal(t, int64(1), childPasses.Swap(0), "the turn end drains the retired child exactly once")

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
	source.childHook = func() Source {
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

	childRecorder := sink.Child("child-1")
	rows := childRecorder.Messages()
	require.Len(t, rows, 1)
	assert.NotContains(t, string(rows[0].SupplementalContent), "recovered",
		"the cleanup itself must not read the provider's store, because it runs on the stdout reader")

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
			source.childHook = func() Source {
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

// A source with no child hook has no child transcript, so the wrapped sink serves the
// child directly and no goroutine opens for it.
func TestToolTranscriptChildSinkWithoutAChildSourceDelegates(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	transcript, _ := newTestToolTranscript(t, agent.NewProviderServices(sink), func(map[string]agent.MessageContent, bool) map[string][]byte { return nil })
	// A row written through the transcript's child sink lands in the wrapped sink's
	// child, so the transcript added no layer of its own.
	row := []byte(`{"type":"text","text":"child row"}`)
	require.NoError(t, transcript.ChildSink("child-1").PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: row}, agent.SpanInfo{}))
	messages := sink.Child("child-1").Messages()
	require.Len(t, messages, 1)
	assert.JSONEq(t, string(row), string(messages[0].Content))
	transcript.mu.Lock()
	defer transcript.mu.Unlock()
	assert.Empty(t, transcript.children)
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

func (b *blockingToolSource) ReadSupplements(_ context.Context, _ string, _ map[string]agent.MessageContent, final bool) (map[string][]byte, error) {
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
	source.childHook = func() Source { return &testToolSource{locateHook: source.locateHook} }
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
	locateHook func(sessionID string) Location
	readHook   func(ctx context.Context, pending map[string]agent.MessageContent, final bool) map[string][]byte
	callIDHook func(original []byte) string
	childHook  func() Source
}

func (t *testToolSource) ProviderName() string { return "Test" }

func (t *testToolSource) Locate(sessionID string) Location {
	return t.locateHook(sessionID)
}

func (t *testToolSource) ToolCallID(original []byte) string {
	if t.callIDHook == nil {
		return "call"
	}
	return t.callIDHook(original)
}

func (t *testToolSource) ReadSupplements(ctx context.Context, _ string, pending map[string]agent.MessageContent, final bool) (map[string][]byte, error) {
	return t.readHook(ctx, pending, final), nil
}

func (t *testToolSource) NewChild() Source {
	if t.childHook == nil {
		return nil
	}
	return t.childHook()
}

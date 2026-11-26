package readseq

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

// testInput is one input of a testFeed. The zero value is a change with an
// empty text.
type testInput struct {
	// change is the text of a change to forward.
	change string
	// request asks for a read.
	request bool
	// abandon drops the read in flight.
	abandon bool
	// end ends the feed.
	end bool
}

func change(text string) testInput { return testInput{change: text} }
func request() testInput           { return testInput{request: true} }
func abandon() testInput           { return testInput{abandon: true} }

// testResult is the result of one read of a testFeed.
type testResult struct {
	value string
	err   error
}

// testFeed is a feed whose loop the test runs itself, one step at a time. So
// the test decides which input or result the loop takes, and when.
type testFeed struct {
	t      *testing.T
	ctx    context.Context
	cancel context.CancelFunc
	seq    *Sequencer[testInput, string, testResult]
	// log records, in order, each change that the feed sent, each result that
	// it applied as "read:<value>", and each failed read as
	// "failed:<error>".
	log []string
	// answers holds the channel that answers each read, by the number of the
	// read from 1.
	answers []chan testResult
	// started receives the number of each read when it starts. The read
	// starts on the test's goroutine, so the number is there when the step
	// that started it returns.
	started chan int
	// noRead makes NextRead ask for no read, and nextReadEnds makes it end
	// the feed.
	noRead, nextReadEnds bool
}

func newTestFeed(t *testing.T) *testFeed {
	t.Helper()
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	f := &testFeed{t: t, ctx: ctx, cancel: cancel, started: make(chan int, 64)}
	f.seq = New(Hooks[testInput, string, testResult]{
		Handle:   f.handle,
		NextRead: f.nextRead,
		Apply:    f.apply,
		Send:     f.send,
	})
	t.Cleanup(func() {
		cancel()
		f.seq.Wait()
	})
	return f
}

func (f *testFeed) handle(ctx context.Context, in testInput) bool {
	switch {
	case in.end:
		return false
	case in.request:
		return f.seq.RequestRead(ctx)
	case in.abandon:
		return f.seq.Abandon(ctx)
	default:
		return f.seq.Forward(ctx, in.change)
	}
}

func (f *testFeed) nextRead(context.Context) (Read[testResult], bool) {
	if f.nextReadEnds {
		return nil, false
	}
	if f.noRead {
		return nil, true
	}
	answer := make(chan testResult, 1)
	f.answers = append(f.answers, answer)
	f.started <- len(f.answers)
	return func(ctx context.Context) testResult {
		select {
		case r := <-answer:
			return r
		case <-ctx.Done():
			return testResult{err: ctx.Err()}
		}
	}, true
}

// apply ends the feed on a failed read, as `agent wait` does.
func (f *testFeed) apply(ctx context.Context, r testResult) bool {
	if ctx.Err() != nil {
		return false
	}
	if r.err != nil {
		f.log = append(f.log, "failed:"+r.err.Error())
		return false
	}
	f.log = append(f.log, "read:"+r.value)
	return true
}

func (f *testFeed) send(ctx context.Context, text string) bool {
	if ctx.Err() != nil {
		return false
	}
	f.log = append(f.log, text)
	return true
}

// take takes one input, as the loop does when it receives one.
func (f *testFeed) take(in testInput) bool { return f.handle(f.ctx, in) }

// queue pushes inputs that the loop did not take yet.
func (f *testFeed) queue(ins ...testInput) {
	f.t.Helper()
	for _, in := range ins {
		require.True(f.t, f.seq.Push(f.ctx, in))
	}
}

// answer ends the read n with value.
func (f *testFeed) answer(n int, value string) { f.answers[n-1] <- testResult{value: value} }

// nextResult waits for the next result that the reads deliver.
func (f *testFeed) nextResult() Result[testResult] {
	f.t.Helper()
	select {
	case r := <-f.seq.Results():
		return r
	case <-f.ctx.Done():
		f.t.Fatal("timed out waiting for the result of a read")
		return Result[testResult]{}
	}
}

// complete waits for the next result and hands it to Complete, as the loop
// does when it receives one.
func (f *testFeed) complete() bool {
	f.t.Helper()
	return f.seq.Complete(f.ctx, f.nextResult())
}

// requireStarted requires that read n started.
func (f *testFeed) requireStarted(n int) {
	f.t.Helper()
	select {
	case got := <-f.started:
		require.Equal(f.t, n, got, "the number of the read that started")
	default:
		f.t.Fatalf("read %d did not start", n)
	}
}

// requireNoRead requires that no read started since the last check.
func (f *testFeed) requireNoRead() {
	f.t.Helper()
	select {
	case n := <-f.started:
		f.t.Fatalf("read %d started", n)
	default:
	}
}

// A change with no read in flight passes at once.
func TestSequencer_SendsAChangeWithoutAReadAtOnce(t *testing.T) {
	f := newTestFeed(t)
	require.True(t, f.take(change("a")))
	assert.Equal(t, []string{"a"}, f.log)
	f.requireNoRead()
}

// A change that arrives during a read can be newer than the read's result,
// so it follows the result. A change after the result passes at once.
func TestSequencer_HoldsTheChangesThatArriveDuringARead(t *testing.T) {
	f := newTestFeed(t)
	require.True(t, f.take(change("before")))
	require.True(t, f.take(request()))
	f.requireStarted(1)
	require.True(t, f.take(change("a")))
	require.True(t, f.take(change("b")))
	assert.Equal(t, []string{"before"}, f.log, "a change during the read waits for its result")

	f.answer(1, "s1")
	require.True(t, f.complete())
	assert.Equal(t, []string{"before", "read:s1", "a", "b"}, f.log, "the held changes follow the result, in their order")
	require.True(t, f.take(change("after")))
	assert.Equal(t, []string{"before", "read:s1", "a", "b", "after"}, f.log)
	f.requireNoRead()
}

// However many requests arrive during a read, one read follows it: a read
// that starts after all of them covers each one. That read holds the changes
// that arrive during it too.
func TestSequencer_ReadsOnceMoreForTheRequestsDuringARead(t *testing.T) {
	f := newTestFeed(t)
	require.True(t, f.take(request()))
	f.requireStarted(1)
	for range 3 {
		require.True(t, f.take(request()))
	}
	f.requireNoRead()
	require.True(t, f.take(change("a")))

	f.answer(1, "s1")
	require.True(t, f.complete())
	f.requireStarted(2)
	require.True(t, f.take(change("b")))
	assert.Equal(t, []string{"read:s1", "a"}, f.log)
	f.answer(2, "s2")
	require.True(t, f.complete())
	assert.Equal(t, []string{"read:s1", "a", "read:s2", "b"}, f.log)
	f.requireNoRead()

	require.True(t, f.take(request()))
	f.requireStarted(3)
}

// An input that is queued when a result arrives arrived before the loop acts
// on the result, so it goes first. Two requests among it arrived during the
// read: they ask for one read more, not two.
func TestSequencer_TakesTheInputQueuedBesideAResultFirst(t *testing.T) {
	f := newTestFeed(t)
	require.True(t, f.take(request()))
	f.requireStarted(1)
	f.answer(1, "s1")
	result := f.nextResult()
	f.queue(request(), change("a"), request())

	require.True(t, f.seq.Complete(f.ctx, result))
	assert.Len(t, f.seq.Inputs(), 0, "Complete took every queued input")
	assert.Equal(t, []string{"read:s1", "a"}, f.log)
	f.requireStarted(2)

	f.answer(2, "s2")
	require.True(t, f.complete())
	assert.Equal(t, []string{"read:s1", "a", "read:s2"}, f.log)
	f.requireNoRead()
}

// An Abandon that is queued when a result arrives makes the result stale, so
// the result never goes out.
func TestSequencer_AnAbandonQueuedBesideAResultDropsTheResult(t *testing.T) {
	f := newTestFeed(t)
	require.True(t, f.take(request()))
	f.requireStarted(1)
	require.True(t, f.take(change("a")))
	f.answer(1, "s1")
	result := f.nextResult()
	f.queue(abandon())

	require.True(t, f.seq.Complete(f.ctx, result))
	assert.Equal(t, []string{"a"}, f.log, "the abandon sent the held change, and the stale result never went out")
	require.True(t, f.take(change("b")))
	assert.Equal(t, []string{"a", "b"}, f.log, "no read holds a change any more")
}

// An input that is queued when a result arrives and that ends the feed ends
// it before the result goes out.
func TestSequencer_AnInputQueuedBesideAResultCanEndTheFeed(t *testing.T) {
	f := newTestFeed(t)
	require.True(t, f.take(request()))
	f.requireStarted(1)
	f.answer(1, "s1")
	result := f.nextResult()
	f.queue(testInput{end: true})

	assert.False(t, f.seq.Complete(f.ctx, result))
	assert.Empty(t, f.log)
}

// Abandon drops the read in flight: the changes that it held go out at once,
// its result never goes out, and a request during it starts no read.
func TestSequencer_AbandonDropsTheReadInFlight(t *testing.T) {
	f := newTestFeed(t)
	require.True(t, f.take(request()))
	f.requireStarted(1)
	require.True(t, f.take(change("a")))
	require.True(t, f.take(request()))

	require.True(t, f.take(abandon()))
	assert.Equal(t, []string{"a"}, f.log, "the held change is real, so it goes out")
	require.True(t, f.take(change("b")))
	assert.Equal(t, []string{"a", "b"}, f.log, "no read holds a change any more")

	f.answer(1, "stale")
	require.True(t, f.complete())
	assert.Equal(t, []string{"a", "b"}, f.log, "the result of the abandoned read never goes out")
	f.requireNoRead()

	require.True(t, f.take(request()))
	f.requireStarted(2)
	f.answer(2, "s2")
	require.True(t, f.complete())
	assert.Equal(t, []string{"a", "b", "read:s2"}, f.log)
	// The request during the abandoned read asks for no read after this one
	// either.
	f.requireNoRead()
}

// A stale result that arrives during a newer read leaves the newer read in
// flight: its changes stay held until its own result.
func TestSequencer_AStaleResultLeavesTheNewerReadInFlight(t *testing.T) {
	f := newTestFeed(t)
	require.True(t, f.take(request()))
	f.requireStarted(1)
	require.True(t, f.take(abandon()))
	require.True(t, f.take(request()))
	f.requireStarted(2)
	require.True(t, f.take(change("a")))

	f.answer(1, "stale")
	require.True(t, f.complete())
	assert.Empty(t, f.log, "read 2 still holds the change")

	f.answer(2, "s2")
	require.True(t, f.complete())
	assert.Equal(t, []string{"read:s2", "a"}, f.log)
}

// A failed read that ends the feed sends nothing that it held, and starts no
// read that a request during it asked for.
func TestSequencer_AFailedReadThatEndsTheFeedSendsNothingMore(t *testing.T) {
	f := newTestFeed(t)
	require.True(t, f.take(request()))
	f.requireStarted(1)
	require.True(t, f.take(change("a")))
	require.True(t, f.take(request()))

	f.answers[0] <- testResult{err: errors.New("the source cannot read")}
	assert.False(t, f.complete())
	assert.Equal(t, []string{"failed:the source cannot read"}, f.log)
	f.requireNoRead()
}

// A request that NextRead answers with no read holds nothing, and a NextRead
// that ends the feed ends it.
func TestSequencer_NextReadDecidesWhetherARequestReads(t *testing.T) {
	f := newTestFeed(t)
	f.noRead = true
	require.True(t, f.take(request()))
	require.True(t, f.take(change("a")))
	assert.Equal(t, []string{"a"}, f.log, "no read is in flight, so the change passes")
	f.requireNoRead()

	f.noRead, f.nextReadEnds = false, true
	assert.False(t, f.take(request()))
}

// Each method reports a send that failed, because the context ended.
func TestSequencer_ReportsASendThatTheEndOfTheContextStopped(t *testing.T) {
	f := newTestFeed(t)
	require.True(t, f.take(request()))
	f.requireStarted(1)
	require.True(t, f.take(change("a")))
	f.cancel()
	assert.False(t, f.take(abandon()), "the held change cannot go out")
	assert.False(t, f.take(change("b")), "the change cannot go out")
	assert.Empty(t, f.log)
}

// Push waits while the loop takes no input, until the context ends.
func TestSequencer_PushReturnsFalseOnceTheContextEnds(t *testing.T) {
	f := newTestFeed(t)
	for range inputBuffer {
		require.True(t, f.seq.Push(f.ctx, change("queued")))
	}
	f.cancel()
	assert.False(t, f.seq.Push(f.ctx, change("one too many")))
}

// Wait returns only after each read returned. A read returns once its
// context ends.
func TestSequencer_WaitReturnsAfterEveryRead(t *testing.T) {
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	defer cancel()
	var returned atomic.Int32
	read := func(ctx context.Context) int {
		<-ctx.Done()
		returned.Add(1)
		return 0
	}
	seq := New(Hooks[int, int, int]{
		Handle:   func(context.Context, int) bool { return true },
		NextRead: func(context.Context) (Read[int], bool) { return read, true },
		Apply:    func(context.Context, int) bool { return true },
		Send:     func(context.Context, int) bool { return true },
	})
	require.True(t, seq.RequestRead(ctx))
	require.True(t, seq.Abandon(ctx))
	require.True(t, seq.RequestRead(ctx))

	cancel()
	seq.Wait()
	assert.Equal(t, int32(2), returned.Load(), "both reads returned before Wait did")
}

// Wait must not deadlock when the feed exits early while an unread result fills
// results (cap 1), another read goroutine is trying to deliver its result, and
// the shared context stays live.
func TestSequencer_WaitDoesNotDeadlockWithUnreadResultAndLiveContext(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	firstDone := make(chan struct{})
	secondInFlight := make(chan struct{})
	releaseSecond := make(chan struct{})
	readCount := 0
	seq := New(Hooks[int, int, int]{
		Handle: func(context.Context, int) bool { return true },
		NextRead: func(context.Context) (Read[int], bool) {
			readCount++
			n := readCount
			return func(context.Context) int {
				if n == 1 {
					defer close(firstDone)
					return 1
				}
				close(secondInFlight)
				<-releaseSecond
				return 2
			}, true
		},
		Apply: func(context.Context, int) bool { return true },
		Send:  func(context.Context, int) bool { return true },
	})

	require.True(t, seq.RequestRead(ctx))
	<-firstDone
	// Abandon read 1 without draining its buffered result from seq.Results(),
	// then start read 2 so its delivery blocks on the full results channel.
	require.True(t, seq.Abandon(ctx))
	require.True(t, seq.RequestRead(ctx))
	<-secondInFlight
	close(releaseSecond)

	waitDone := make(chan struct{})
	go func() {
		seq.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-ctx.Done():
		t.Fatal("Wait deadlocked while results buffer was full and context stayed live")
	}
}

// Complete must drain only the inputs already queued when it begins, so
// continuous Push traffic cannot starve Apply or grow held changes without limit.
func TestSequencer_CompleteDrainsOnlyAlreadyQueuedInputs(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	var seq *Sequencer[testInput, string, testResult]
	handled := 0
	applied := false
	var sent []string

	seq = New(Hooks[testInput, string, testResult]{
		Handle: func(ctx context.Context, in testInput) bool {
			handled++
			// Simulate continuous producer traffic arriving while Complete drains.
			if handled < 100 {
				seq.Push(ctx, change(fmt.Sprintf("extra-%d", handled)))
			}
			return seq.Forward(ctx, in.change)
		},
		NextRead: func(context.Context) (Read[testResult], bool) {
			return func(context.Context) testResult { return testResult{value: "snap"} }, true
		},
		Apply: func(context.Context, testResult) bool {
			applied = true
			return true
		},
		Send: func(_ context.Context, c string) bool {
			sent = append(sent, c)
			return true
		},
	})
	t.Cleanup(seq.Wait)

	require.True(t, seq.RequestRead(ctx))
	var res Result[testResult]
	select {
	case res = <-seq.Results():
	case <-ctx.Done():
		t.Fatal("timed out waiting for read result")
	}
	require.True(t, seq.Push(ctx, change("queued-1")))
	require.True(t, seq.Push(ctx, change("queued-2")))

	require.True(t, seq.Complete(ctx, res))
	assert.True(t, applied)
	assert.Equal(t, 2, handled, "Complete must drain only the 2 inputs already queued at entry")
	assert.Equal(t, []string{"queued-1", "queued-2"}, sent)
}

func TestSequencer_WaitIsIdempotentAndCancelsBlockedReadWithLiveContext(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	started := make(chan struct{})
	var canceled atomic.Int32
	seq := New(Hooks[int, int, int]{
		Handle: func(context.Context, int) bool { return true },
		NextRead: func(context.Context) (Read[int], bool) {
			return func(readCtx context.Context) int {
				select {
				case <-started:
				default:
					close(started)
				}
				<-readCtx.Done()
				canceled.Add(1)
				return 0
			}, true
		},
		Apply: func(context.Context, int) bool { return true },
		Send:  func(context.Context, int) bool { return true },
	})

	// Wait on an idle sequencer with no reads started is a no-op, and calling it
	// twice must not block or panic.
	idle := New(Hooks[int, int, int]{
		Handle:   func(context.Context, int) bool { return true },
		NextRead: func(context.Context) (Read[int], bool) { return nil, true },
		Apply:    func(context.Context, int) bool { return true },
		Send:     func(context.Context, int) bool { return true },
	})
	idle.Wait()
	idle.Wait()

	require.True(t, seq.RequestRead(ctx))
	<-started

	// Caller context stays live; Wait must cancel readCtx directly.
	waitDone := make(chan struct{})
	go func() {
		seq.Wait()
		seq.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-ctx.Done():
		t.Fatal("Wait blocked while a read waited on readCtx.Done()")
	}
	assert.Equal(t, int32(1), canceled.Load(), "Wait must cancel the in-flight read even when caller ctx is live")

	// Starting a read after Wait receives an already-canceled context and Wait returns cleanly.
	require.True(t, seq.Abandon(ctx))
	require.True(t, seq.RequestRead(ctx))
	seq.Wait()
	assert.Equal(t, int32(2), canceled.Load())
}

func TestSequencer_AbandonWhenIdleThenRequestRead(t *testing.T) {
	f := newTestFeed(t)
	// Abandon with no read in flight and no held changes must succeed and leave
	// the sequencer ready to start a new read.
	require.True(t, f.take(abandon()))
	assert.Empty(t, f.log)

	require.True(t, f.take(request()))
	f.requireStarted(1)
	f.answer(1, "after-idle-abandon")
	require.True(t, f.complete())
	assert.Equal(t, []string{"read:after-idle-abandon"}, f.log)
	f.requireNoRead()
}

func TestSequencer_CompleteWithEmptyQueueAndStaleEpoch(t *testing.T) {
	f := newTestFeed(t)
	require.True(t, f.take(request()))
	f.requireStarted(1)
	f.answer(1, "empty-queue")

	// Complete with empty Inputs and empty held slice applies the result cleanly.
	assert.Len(t, f.seq.Inputs(), 0)
	require.True(t, f.complete())
	assert.Equal(t, []string{"read:empty-queue"}, f.log)

	// Stale epoch must return true immediately without draining queued inputs.
	require.True(t, f.take(request()))
	f.requireStarted(2)
	f.answer(2, "stale")
	staleResult := f.nextResult()
	require.True(t, f.take(abandon()))
	f.queue(change("untouched"))

	require.True(t, f.seq.Complete(f.ctx, staleResult))
	assert.Len(t, f.seq.Inputs(), 1, "stale Complete must not drain queued inputs")
	assert.Equal(t, []string{"read:empty-queue"}, f.log)
}

func TestSequencer_CompleteStopsWhenFlushFails(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	var seq *Sequencer[testInput, string, testResult]
	reads := 0
	applied := 0
	seq = New(Hooks[testInput, string, testResult]{
		Handle: func(ctx context.Context, in testInput) bool {
			if in.request {
				return seq.RequestRead(ctx)
			}
			return seq.Forward(ctx, in.change)
		},
		NextRead: func(context.Context) (Read[testResult], bool) {
			reads++
			return func(context.Context) testResult { return testResult{value: "snap"} }, true
		},
		Apply: func(context.Context, testResult) bool {
			applied++
			return true
		},
		Send: func(context.Context, string) bool {
			return false
		},
	})
	t.Cleanup(seq.Wait)

	require.True(t, seq.RequestRead(ctx))
	require.True(t, seq.Forward(ctx, "held-change"))
	require.True(t, seq.RequestRead(ctx)) // sets readAgain

	var res Result[testResult]
	select {
	case res = <-seq.Results():
	case <-ctx.Done():
		t.Fatal("timed out waiting for read result")
	}

	assert.False(t, seq.Complete(ctx, res), "Complete must return false when flushing a held change fails")
	assert.Equal(t, 1, applied)
	assert.Equal(t, 1, reads, "a failed flush must not start the follow-up read")
}

func TestNew_PanicsWithoutEveryHook(t *testing.T) {
	full := Hooks[int, int, int]{
		Handle:   func(context.Context, int) bool { return true },
		NextRead: func(context.Context) (Read[int], bool) { return nil, true },
		Apply:    func(context.Context, int) bool { return true },
		Send:     func(context.Context, int) bool { return true },
	}
	assert.NotPanics(t, func() { New(full) })
	for name, drop := range map[string]func(*Hooks[int, int, int]){
		"Handle":   func(h *Hooks[int, int, int]) { h.Handle = nil },
		"NextRead": func(h *Hooks[int, int, int]) { h.NextRead = nil },
		"Apply":    func(h *Hooks[int, int, int]) { h.Apply = nil },
		"Send":     func(h *Hooks[int, int, int]) { h.Send = nil },
	} {
		hooks := full
		drop(&hooks)
		assert.PanicsWithValue(t, "readseq: New needs every hook", func() { New(hooks) }, "without %s", name)
	}
}

// stressResult is the result of one read of the stress test: how many
// changes and requests the loop took before the read began.
type stressResult struct {
	changes, requests int
}

// stressEntry is one item that the stress feed sent: a change, or the result
// of a read.
type stressEntry struct {
	change string
	result *stressResult
}

// stressInput is one input of the stress feed.
type stressInput struct {
	testInput
	// finish marks the last input.
	finish bool
}

// The rule holds on every schedule. Several goroutines push changes,
// requests and abandons at once, and each read ends at a moment of its own.
// Then every change goes out once, in the order that the loop took it, and
// each result follows exactly the changes that the loop took before its
// read began. A request after the last abandon gets a read that covers it.
func TestSequencer_KeepsTheRuleUnderConcurrentInput(t *testing.T) {
	const producers, inputsEach = 4, 250
	seed := rand.Uint64()
	t.Logf("seed %d", seed)
	rng := rand.New(rand.NewPCG(seed, 0))
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))

	var (
		taken                       []string
		requests                    int
		sent                        []stressEntry
		begun, applied, abandonedIn int
		finished                    bool
		seq                         *Sequencer[stressInput, string, stressResult]
	)
	changesSent := func() int {
		n := 0
		for _, e := range sent {
			if e.result == nil {
				n++
			}
		}
		return n
	}
	// handle is the Handle hook, which the loop below calls too.
	handle := func(ctx context.Context, in stressInput) bool {
		switch {
		case in.finish:
			finished = true
			return true
		case in.request:
			requests++
			return seq.RequestRead(ctx)
		case in.abandon:
			if begun > applied+abandonedIn {
				abandonedIn++
			}
			return seq.Abandon(ctx)
		default:
			taken = append(taken, in.change)
			return seq.Forward(ctx, in.change)
		}
	}
	seq = New(Hooks[stressInput, string, stressResult]{
		Handle: handle,
		NextRead: func(context.Context) (Read[stressResult], bool) {
			begun++
			result := stressResult{changes: len(taken), requests: requests}
			yields := rng.IntN(4)
			return func(context.Context) stressResult {
				for range yields {
					runtime.Gosched()
				}
				return result
			}, true
		},
		Apply: func(_ context.Context, r stressResult) bool {
			applied++
			assert.Equal(t, r.changes, changesSent(), "a result follows exactly the changes before its read")
			sent = append(sent, stressEntry{result: &r})
			return true
		},
		Send: func(_ context.Context, text string) bool {
			sent = append(sent, stressEntry{change: text})
			return true
		},
	})
	// pushing counts the producers, and finishing the goroutine that pushes
	// the last inputs after them.
	var pushing, finishing sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		finishing.Wait()
		seq.Wait()
	})

	for p := range producers {
		prng := rand.New(rand.NewPCG(seed, uint64(p)+1))
		pushing.Go(func() {
			for i := range inputsEach {
				in := stressInput{testInput: change(fmt.Sprintf("%d-%d", p, i))}
				switch roll := prng.IntN(100); {
				case roll < 25:
					in = stressInput{testInput: request()}
				case roll < 30:
					in = stressInput{testInput: abandon()}
				}
				if !seq.Push(ctx, in) {
					return
				}
			}
		})
	}
	finishing.Go(func() {
		pushing.Wait()
		if seq.Push(ctx, stressInput{testInput: request()}) {
			seq.Push(ctx, stressInput{finish: true})
		}
	})

	for !finished || begun > applied+abandonedIn {
		require.LessOrEqual(t, begun-applied-abandonedIn, 1, "one read at most is in flight")
		select {
		case in := <-seq.Inputs():
			require.True(t, handle(ctx, in))
		case result := <-seq.Results():
			require.True(t, seq.Complete(ctx, result))
		case <-ctx.Done():
			t.Fatal("the loop did not settle")
		}
	}

	var changes []string
	last := map[int]int{}
	for _, e := range sent {
		if e.result != nil {
			continue
		}
		changes = append(changes, e.change)
		var p, i int
		_, err := fmt.Sscanf(e.change, "%d-%d", &p, &i)
		require.NoError(t, err)
		prev, seen := last[p]
		require.True(t, !seen || i > prev, "the changes of producer %d keep their order", p)
		last[p] = i
	}
	assert.Equal(t, taken, changes, "every change goes out once, in the order that the loop took it")
	var final *stressResult
	for _, e := range sent {
		if e.result != nil {
			final = e.result
		}
	}
	require.NotNil(t, final)
	assert.Equal(t, stressResult{changes: len(taken), requests: requests}, *final,
		"the last read began after every input")
}

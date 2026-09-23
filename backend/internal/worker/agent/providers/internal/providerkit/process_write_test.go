package providerkit

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type partialStdinWriter struct {
	written int
	err     error
}

func (w partialStdinWriter) Write([]byte) (int, error) { return w.written, w.err }
func (partialStdinWriter) Close() error                { return nil }

func TestRawInputReportsPartialDelivery(t *testing.T) {
	cause := errors.New("the pipe closed")
	for _, test := range []struct {
		name      string
		written   int
		err       error
		want      error
		uncertain bool
	}{
		{name: "complete", written: 4},
		{name: "no bytes", err: cause, want: cause},
		{name: "partial error", written: 2, err: cause, want: cause, uncertain: true},
		{name: "short write", written: 2, want: io.ErrShortWrite, uncertain: true},
		{name: "empty write", want: io.ErrShortWrite},
		{name: "full write with error", written: 4, err: cause, want: cause, uncertain: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			process := &Process{stdin: partialStdinWriter{written: test.written, err: test.err}}
			err := process.SendRawInput([]byte("abc\n"))
			if test.want == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, test.want)
			}
			require.Equal(t, test.uncertain, errors.Is(err, agent.ErrDeliveryUncertain))
		})
	}
}

// blockingWriter holds every write until a test releases it, and records the order
// the writes arrived in.
type blockingWriter struct {
	release chan struct{}
	mu      sync.Mutex
	frames  []string
}

func (w *blockingWriter) Write(data []byte) (int, error) {
	<-w.release
	w.mu.Lock()
	defer w.mu.Unlock()
	w.frames = append(w.frames, strings.TrimSpace(string(data)))
	return len(data), nil
}

func (w *blockingWriter) Frames() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.frames...)
}

// A reply from the read loop must cost ONE goroutine however many frames wait.
//
// The refusal used to spawn a goroutine for each unanswered request, so a runtime
// that streamed unrecognized requests while it stopped reading its own stdin held an
// 8 KiB stack per frame with no limit. It must also not BLOCK the caller: that
// goroutine drains the child's stdout, and a child that is not reading its stdin
// blocks the write, its unread stdout backs up against it, and neither side moves.
func TestDetachedStdinWritesCostOneGoroutineAndKeepTheirOrder(t *testing.T) {
	t.Parallel()

	writer := &blockingWriter{release: make(chan struct{})}
	p := &Process{agentID: "agent", stdin: agenttest.NopStdin(writer)}

	p.stdinMu.Lock()
	first, _ := p.stdinWriterLocked()
	p.stdinMu.Unlock()
	for i := range 32 {
		// Returns at once: the writer holds every write, and this must not wait.
		p.writeStdinDetached([]byte(fmt.Sprintf("frame-%02d\n", i)), "probe")
	}
	// ONE writer, stated structurally rather than by counting goroutines: the
	// process-wide count moves with every parallel sibling, and a per-name count
	// over the stack dump counts their writers too. The queue is what the writer is
	// started for, so a queue that never changes identity is a writer that was
	// started once -- however many frames go through it.
	p.stdinMu.Lock()
	queue, _ := p.stdinWriterLocked()
	p.stdinMu.Unlock()
	assert.True(t, first == queue, "32 queued frames must cost ONE writer goroutine")

	close(writer.release)
	require.Eventually(t, func() bool { return len(writer.Frames()) == 32 }, 2*time.Second, 5*time.Millisecond)

	// FIFO, which is what lets a cancel follow the answer it must not overtake:
	// Base.Interrupt and codex.Agent.Interrupt both send their answers first.
	want := make([]string, 0, 32)
	for i := range 32 {
		want = append(want, fmt.Sprintf("frame-%02d", i))
	}
	assert.Equal(t, want, writer.Frames(), "one writer keeps the frames in the order they were queued")
}

// A waiting write and a detached one share the one writer, so they share its order.
func TestWaitingAndDetachedStdinWritesShareOneOrder(t *testing.T) {
	t.Parallel()

	writer := &blockingWriter{release: make(chan struct{})}
	p := &Process{agentID: "agent", stdin: agenttest.NopStdin(writer)}

	p.writeStdinDetached([]byte("first\n"), "probe")
	waited := make(chan error, 1)
	go func() { waited <- p.WriteStdin([]byte("second\n")) }()
	// The detached frame is already queued, so releasing the writer drains both in
	// the order they were queued.
	close(writer.release)
	require.NoError(t, <-waited)
	assert.Equal(t, []string{"first", "second"}, writer.Frames())
}

// A write asked for after Stop still reaches the writer inline, so a request that
// arrives during tear-down draws its refusal instead of vanishing.
func TestStdinWriteAfterStopStillReportsItsOwnError(t *testing.T) {
	t.Parallel()

	writer := &blockingWriter{release: make(chan struct{})}
	close(writer.release)
	p := &Process{agentID: "agent", stdin: agenttest.NopStdin(writer), processDone: make(chan struct{})}
	close(p.processDone)
	p.Stop()

	require.NoError(t, p.WriteStdin([]byte("after-stop\n")))
	p.writeStdinDetached([]byte("detached-after-stop\n"), "probe")
	assert.Equal(t, []string{"after-stop", "detached-after-stop"}, writer.Frames())
}

// A child that stops reading its stdin must not stall the goroutine that drains
// its stdout, and must not grow the queue without limit either.
//
// The detached path refuses past stdinQueueDepth rather than blocking. That loses a
// reply to a child which has not read 256 frames and would not have read this one,
// and it is the whole reason the read loop can hand off a reply at all: blocking
// here would put that loop back where the queue exists to take it out of.
func TestDetachedStdinWriteRefusesRatherThanBlockAnUnresponsiveChild(t *testing.T) {
	t.Parallel()

	writer := &blockingWriter{release: make(chan struct{})}
	p := &Process{agentID: "agent", stdin: agenttest.NopStdin(writer)}
	t.Cleanup(func() { close(writer.release) })

	// One frame reaches the writer and blocks there; the rest fill the queue.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range stdinQueueDepth * 2 {
			p.writeStdinDetached([]byte(fmt.Sprintf("frame-%03d\n", i)), "probe")
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a detached write blocked on a child that is not reading its stdin")
	}
}

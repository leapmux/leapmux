package pi

import (
	"bytes"
	"sync"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const piPendingOutputFrames = 128

type piQueuedOutput struct {
	raw        []byte
	observedAt time.Time
}

// piInterruptOutput retains events in a first in, first out (FIFO) queue.
// It waits for each abort write outcome. Remote procedure call (RPC) replies bypass it.
// The stdout reader supplies backpressure at both limits.
type piInterruptOutput struct {
	mu           sync.Mutex
	ready        *sync.Cond
	frames       []piQueuedOutput
	bytes        int
	writes       int
	draining     bool
	handling     bool
	stopsWaiting int
	stopsClosed  bool
	waiters      int
	maxFrames    int
	maxBytes     int
}

func (q *piInterruptOutput) initLocked() {
	if q.ready == nil {
		q.ready = sync.NewCond(&q.mu)
	}
	if q.maxFrames <= 0 {
		q.maxFrames = piPendingOutputFrames
	}
	if q.maxBytes <= 0 {
		q.maxBytes = agent.ConfiguredMaxMessageSize()
	}
}

// begin waits for the previous handler before it records a new write.
// The callback changes only local state. It must not write to the process.
func (q *piInterruptOutput) begin(record func() bool) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.initLocked()
	if q.handling && !q.stopsClosed {
		q.stopsWaiting++
		for q.handling && !q.stopsClosed {
			q.ready.Wait()
		}
		q.stopsWaiting--
		q.ready.Broadcast()
	}
	if q.stopsClosed {
		return false
	}
	if record() {
		q.writes++
	}
	return true
}

func (q *piInterruptOutput) canRetainLocked(size int) bool {
	// A single frame can exceed a changed budget after ReadOutput admitted it.
	// Retain that frame alone. Do not restrict the scanner's accepted vocabulary.
	return len(q.frames) == 0 || len(q.frames) < q.maxFrames && size <= q.maxBytes-q.bytes
}

func (q *piInterruptOutput) enqueue(line *providerkit.ParsedLine, observedAt time.Time, dispatch func(*providerkit.ParsedLine, time.Time)) {
	q.mu.Lock()
	q.initLocked()
	for !q.canRetainLocked(len(line.Raw)) {
		q.waiters++
		q.ready.Wait()
		q.waiters--
	}
	if q.writes == 0 && !q.draining && len(q.frames) == 0 {
		q.draining = true
		q.handling = true
		q.mu.Unlock()
		dispatch(line, observedAt)
		q.drain(dispatch)
		return
	}
	q.frames = append(q.frames, piQueuedOutput{raw: bytes.Clone(line.Raw), observedAt: observedAt})
	q.bytes += len(line.Raw)
	start := q.writes == 0 && !q.draining
	if start {
		q.draining = true
	}
	q.mu.Unlock()
	if start {
		q.drain(dispatch)
	}
}

// waitForDrain joins the sole drainer after the process's output reader finishes.
func (q *piInterruptOutput) waitForDrain() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.initLocked()
	for q.draining {
		q.ready.Wait()
	}
}

// finish resolves the write before it replays events or waits for an acknowledgement.
func (q *piInterruptOutput) finish(resolve func(), dispatch func(*providerkit.ParsedLine, time.Time)) {
	q.mu.Lock()
	q.initLocked()
	resolve()
	if q.writes > 0 {
		q.writes--
	}
	start := q.writes == 0 && !q.draining && len(q.frames) > 0
	if start {
		q.draining = true
	}
	q.ready.Broadcast()
	q.mu.Unlock()
	if start {
		q.drain(dispatch)
	}
}

func (q *piInterruptOutput) drain(dispatch func(*providerkit.ParsedLine, time.Time)) {
	for {
		q.mu.Lock()
		// Retain drain ownership while a waiting stop takes this handler boundary.
		// A continuing stdout producer cannot postpone that stop with another frame.
		q.handling = false
		q.ready.Broadcast()
		for q.stopsWaiting > 0 && !q.stopsClosed {
			q.ready.Wait()
		}
		if q.writes > 0 || len(q.frames) == 0 {
			q.draining = false
			q.ready.Broadcast()
			q.mu.Unlock()
			return
		}
		frame := q.frames[0]
		q.frames[0] = piQueuedOutput{}
		q.frames = q.frames[1:]
		q.bytes -= len(frame.raw)
		q.handling = true
		q.ready.Broadcast()
		q.mu.Unlock()
		// Only deferred frames require another decode. The FIFO retains raw bytes,
		// rather than a second copy of the decoded native message.
		dispatch(providerkit.ParseLine(frame.raw), frame.observedAt)
	}
}

// dispose rejects new stops during shutdown or after process completion.
// It resolves pending writes and releases producers. Native output keeps its order.
func (q *piInterruptOutput) dispose(resolve func(), dispatch func(*providerkit.ParsedLine, time.Time)) {
	q.mu.Lock()
	q.initLocked()
	q.stopsClosed = true
	q.writes = 0
	resolve()
	start := !q.draining && len(q.frames) > 0
	if start {
		q.draining = true
	}
	q.ready.Broadcast()
	q.mu.Unlock()
	if start {
		go q.drain(dispatch)
	}
}

func (a *Agent) dispatchPiOutput(line *providerkit.ParsedLine, observedAt time.Time) {
	handlePiOutputObserved(a, line, observedAt)
}

func (a *Agent) disposePiInterruptOutput() {
	a.interruptOutput.dispose(func() {
		a.Mu.Lock()
		for id, state := range a.interruptRequests {
			if state == piInterruptPending {
				delete(a.interruptRequests, id)
			}
		}
		a.Mu.Unlock()
	}, a.dispatchPiOutput)
}

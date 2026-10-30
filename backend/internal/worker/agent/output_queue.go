package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// ErrOutputInFlight reports an active drainer or a reservation whose data is not ready.
// A reentrant caller must leave its submitted writes for that caller.
var ErrOutputInFlight = errors.New("an output write is in flight")

// OutputOwner identifies writes that one stream buffer can discard or transfer.
// The nonzero size gives each allocated owner a distinct address.
type OutputOwner struct{ _ byte }

// NewOutputOwner creates an identity for one stream buffer's pending rows.
func NewOutputOwner() *OutputOwner { return &OutputOwner{} }

// OutputWrite retains one immutable write or one dependent effect.
// Run must retain its original destination and copy mutable inputs before submission.
// ScopeID and RetainedBytes describe buffered generation for diagnostics.
type OutputWrite struct {
	Owner         *OutputOwner
	ScopeID       string
	RetainedBytes int
	Label         string
	Run           func() error
}

// OutputTicket records whether one submitted write completed successfully.
type OutputTicket struct {
	write           OutputWrite
	done            atomic.Bool
	queue           atomic.Pointer[OutputQueue]
	synchronization *outputSynchronization
}

// Done reports whether this submitted operation completed successfully.
func (ticket *OutputTicket) Done() bool { return ticket != nil && ticket.done.Load() }

// Queue returns the current ordering owner, including after a buffer transfer.
func (ticket *OutputTicket) Queue() *OutputQueue { return ticket.queue.Load() }

func (queue *OutputQueue) executes(ticket *OutputTicket) bool {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return queue.flushState.inFlight() && len(queue.pending) != 0 && queue.pending[0] == ticket
}

// OutputQueue keeps native writes, generated rows and dependent effects in submission order.
// Failed writes remain at the head. No callback runs with mu held.
type OutputQueue struct {
	mu                sync.Mutex
	pending           []*OutputTicket
	readyWake         *OutputTicket
	retry             *outputRetry
	flushState        *outputFlushState
	readinessChanged  chan struct{}
	readinessRevision uint64
}

type outputFlushState struct {
	done              chan struct{}
	err               error
	readinessRevision uint64
}

// The queue mutex resolves each synchronization outcome exactly once.
type outputSynchronization struct {
	done chan struct{}
	err  error
}

func (result *outputSynchronization) resolveLocked(err error) {
	select {
	case <-result.done:
		return
	default:
		result.err = err
		close(result.done)
	}
}

func (queue *OutputQueue) resolveSynchronization(result *outputSynchronization, err error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	result.resolveLocked(err)
}

func (state *outputFlushState) inFlight() bool {
	if state == nil {
		return false
	}
	select {
	case <-state.done:
		return false
	default:
		return true
	}
}

// Append admits a complete batch before any of its callbacks can run.
func (queue *OutputQueue) Append(writes ...OutputWrite) []*OutputTicket {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return queue.appendLocked(writes...)
}

func (queue *OutputQueue) appendLocked(writes ...OutputWrite) []*OutputTicket {
	tickets := make([]*OutputTicket, len(writes))
	for i, write := range writes {
		tickets[i] = &OutputTicket{write: write}
		tickets[i].queue.Store(queue)
	}
	if len(tickets) != 0 {
		queue.pending = append(queue.pending, tickets...)
		queue.notifyReadinessLocked()
	}
	return tickets
}

// Synchronize waits until a private marker reaches the head of this queue.
// It does not wait for later writes. Cancellation removes only that marker.
// Call it outside provider locks and outside callbacks draining this queue.
func (queue *OutputQueue) Synchronize(ctx context.Context) error {
	if ctx == nil {
		return errors.New("output synchronization requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	owner := NewOutputOwner()
	result := &outputSynchronization{done: make(chan struct{})}
	queue.mu.Lock()
	if err := ctx.Err(); err != nil {
		queue.mu.Unlock()
		return err
	}
	ticket := queue.appendLocked(OutputWrite{Owner: owner, Label: "synchronize output", Run: func() error { queue.resolveSynchronization(result, nil); return nil }})[0]
	ticket.synchronization = result
	state, claimed := queue.claimFlushLocked()
	queue.mu.Unlock()
	defer queue.ClearOwner(owner)
	for {
		if claimed {
			go func(current *outputFlushState) { _ = queue.drain(current) }(state)
		}
		select {
		case <-result.done:
			return result.err
		case <-ctx.Done():
			select {
			case <-result.done:
				return result.err
			default:
				queue.resolveSynchronization(result, ctx.Err())
				return result.err
			}
		case <-state.done:
		}
		select {
		case <-result.done:
			return result.err
		default:
		}

		queue.mu.Lock()
		// A readiness publisher can finish another drain before this waiter runs.
		// Observe its result rather than silently retrying a retained real failure.
		if latest := queue.flushState; latest != nil && latest != state {
			state, claimed = latest, false
			queue.mu.Unlock()
			continue
		}
		if errors.Is(state.err, ErrOutputInFlight) && queue.readinessRevision == state.readinessRevision {
			changed := queue.watchReadinessLocked()
			queue.mu.Unlock()
			select {
			case <-result.done:
				return result.err
			case <-ctx.Done():
				queue.resolveSynchronization(result, ctx.Err())
				return result.err
			case <-changed:
			}
			queue.mu.Lock()
			if latest := queue.flushState; latest != nil && latest != state {
				state, claimed = latest, false
				queue.mu.Unlock()
				continue
			}
		}
		select {
		case <-result.done:
			queue.mu.Unlock()
			return result.err
		default:
		}
		state, claimed = queue.claimFlushLocked()
		queue.mu.Unlock()
	}
}

func (queue *OutputQueue) claimFlushLocked() (*outputFlushState, bool) {
	if queue.flushState.inFlight() {
		return queue.flushState, false
	}
	if len(queue.pending) == 0 {
		return nil, false
	}
	state := &outputFlushState{done: make(chan struct{}), readinessRevision: queue.readinessRevision}
	queue.flushState = state
	queue.notifyChangeLocked()
	return state, true
}

func (queue *OutputQueue) notifyReadinessLocked() {
	queue.readinessRevision++
	queue.notifyChangeLocked()
}

func (queue *OutputQueue) notifyChangeLocked() {
	if queue.readinessChanged != nil {
		close(queue.readinessChanged)
		queue.readinessChanged = nil
	}
}

func (queue *OutputQueue) watchReadinessLocked() <-chan struct{} {
	if queue.readinessChanged == nil {
		queue.readinessChanged = make(chan struct{})
	}
	return queue.readinessChanged
}

// FlushAfterReady publishes one readiness transition before attempting a flush.
// Call it once after publishing each ticket's prepared data, without holding provider locks.
// Only the current head can wake an existing drainer that observed unready data.
func (queue *OutputQueue) FlushAfterReady(tickets ...*OutputTicket) error {
	queue.mu.Lock()
	if queue.flushState.inFlight() && len(queue.pending) != 0 {
		for _, ticket := range tickets {
			if queue.pending[0] == ticket {
				queue.readyWake = ticket
				break
			}
		}
	}
	queue.notifyReadinessLocked()
	queue.mu.Unlock()
	return queue.Flush()
}

// Flush attempts pending writes until the queue is empty or a write fails.
// A concurrent or reentrant flush returns ErrOutputInFlight without waiting for the callback.
func (queue *OutputQueue) Flush() error {
	queue.mu.Lock()
	state, claimed := queue.claimFlushLocked()
	queue.mu.Unlock()
	if state == nil {
		return nil
	}
	if !claimed {
		return ErrOutputInFlight
	}
	return queue.drain(state)
}

func (queue *OutputQueue) drain(state *outputFlushState) (err error) {
	finished := false
	defer func() {
		if !finished {
			queue.mu.Lock()
			queue.finishLocked(state, err)
			queue.mu.Unlock()
		}
		if err != nil && !errors.Is(err, ErrOutputInFlight) {
			queue.requestRetry()
		}
	}()
	for {
		queue.mu.Lock()
		if len(queue.pending) == 0 {
			queue.pending = nil
			queue.finishLocked(state, nil)
			finished = true
			queue.mu.Unlock()
			return nil
		}
		ticket := queue.pending[0]
		run, label := ticket.write.Run, ticket.write.Label
		// Prepared data published before this attempt is already visible to
		// its callback. Only a later readiness transition can require another attempt.
		queue.readyWake = nil
		queue.mu.Unlock()
		var writeErr error
		if run == nil {
			writeErr = errors.New("the pending output has no write function")
		} else if err := run(); err != nil {
			writeErr = fmt.Errorf("%s: %w", label, err)
		}
		if writeErr != nil {
			queue.mu.Lock()
			headChanged := len(queue.pending) == 0 || queue.pending[0] != ticket
			if errors.Is(writeErr, ErrOutputInFlight) && (headChanged || queue.readyWake == ticket) {
				queue.readyWake = nil
				queue.mu.Unlock()
				continue
			}
			// Finish under the same lock as the readiness check. A publisher
			// then either wakes this drainer or starts the next one itself.
			queue.finishLocked(state, writeErr)
			finished = true
			queue.mu.Unlock()
			return writeErr
		}
		ticket.done.Store(true)
		queue.mu.Lock()
		if len(queue.pending) > 0 && queue.pending[0] == ticket {
			queue.pending[0] = nil
			queue.pending = queue.pending[1:]
		}
		queue.mu.Unlock()
	}
}

func (queue *OutputQueue) finishLocked(state *outputFlushState, err error) {
	queue.readyWake = nil
	if err != nil && !errors.Is(err, ErrOutputInFlight) {
		for _, ticket := range queue.pending {
			if ticket.synchronization != nil {
				ticket.synchronization.resolveLocked(err)
			}
		}
	}
	state.err = err
	state.readinessRevision = queue.readinessRevision
	close(state.done)
	queue.forgetFinishedFlushLocked()
	queue.notifyChangeLocked()
}

func (queue *OutputQueue) forgetFinishedFlushLocked() {
	if len(queue.pending) == 0 && !queue.flushState.inFlight() {
		queue.flushState = nil
	}
}

// HasPending reports queued writes or a callback that still runs.
func (queue *OutputQueue) HasPending() bool {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return queue.flushState.inFlight() || len(queue.pending) != 0
}

// HasOwner reports whether this buffer still owns a pending write.
func (queue *OutputQueue) HasOwner(owner *OutputOwner) bool {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	for _, ticket := range queue.pending {
		if ticket.write.Owner == owner {
			return true
		}
	}
	return false
}

// HasScope reports whether an owner has a pending write for this scope.
func (queue *OutputQueue) HasScope(owner *OutputOwner, scopeID string) bool {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	for _, ticket := range queue.pending {
		if ticket.write.Owner == owner && ticket.write.ScopeID == scopeID {
			return true
		}
	}
	return false
}

// RetainedBytes sums the retained generation bytes for one owner's scope.
func (queue *OutputQueue) RetainedBytes(owner *OutputOwner, scopeID string) int {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	size := 0
	for _, ticket := range queue.pending {
		if ticket.write.Owner == owner && ticket.write.ScopeID == scopeID {
			size += ticket.write.RetainedBytes
		}
	}
	return size
}

// ClearOwner removes this owner's writes. An in-flight completion cannot remove a replacement write.
func (queue *OutputQueue) ClearOwner(owner *OutputOwner) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	kept := queue.pending[:0]
	for _, ticket := range queue.pending {
		if ticket.write.Owner != owner {
			kept = append(kept, ticket)
		}
	}
	if len(kept) != len(queue.pending) {
		queue.notifyReadinessLocked()
	}
	clear(queue.pending[len(kept):])
	queue.pending = kept
	queue.forgetFinishedFlushLocked()
}

// MoveOwnerTo transfers this owner's pending writes after the destination's existing writes.
// A callback must not transfer its own queue. Other submissions can continue during the transfer.
func (queue *OutputQueue) MoveOwnerTo(owner *OutputOwner, destination *OutputQueue, destinationOwner *OutputOwner) {
	if destination == nil {
		return
	}
	queue.mu.Lock()
	for queue.flushState.inFlight() {
		idle := queue.flushState.done
		queue.mu.Unlock()
		<-idle
		queue.mu.Lock()
	}
	if destination == queue {
		for _, ticket := range queue.pending {
			if ticket.write.Owner == owner {
				ticket.write.Owner = destinationOwner
			}
		}
		queue.notifyReadinessLocked()
		queue.forgetFinishedFlushLocked()
		queue.mu.Unlock()
		return
	}
	var moved []*OutputTicket
	kept := queue.pending[:0]
	for _, ticket := range queue.pending {
		if ticket.write.Owner == owner {
			ticket.write.Owner = destinationOwner
			ticket.queue.Store(destination)
			moved = append(moved, ticket)
		} else {
			kept = append(kept, ticket)
		}
	}
	if len(moved) != 0 {
		queue.notifyReadinessLocked()
	}
	clear(queue.pending[len(kept):])
	queue.pending = kept
	queue.forgetFinishedFlushLocked()
	queue.mu.Unlock()
	destination.mu.Lock()
	destination.pending = append(destination.pending, moved...)
	if len(moved) != 0 {
		destination.notifyReadinessLocked()
	}
	destination.mu.Unlock()
}

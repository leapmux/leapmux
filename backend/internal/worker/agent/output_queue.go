package agent

import (
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
	write OutputWrite
	done  atomic.Bool
	queue atomic.Pointer[OutputQueue]
}

// Done reports whether this submitted operation completed successfully.
func (ticket *OutputTicket) Done() bool { return ticket != nil && ticket.done.Load() }

// Queue returns the current ordering owner, including after a buffer transfer.
func (ticket *OutputTicket) Queue() *OutputQueue { return ticket.queue.Load() }

func (queue *OutputQueue) executes(ticket *OutputTicket) bool {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return queue.running && len(queue.pending) != 0 && queue.pending[0] == ticket
}

// OutputQueue keeps native writes, generated rows and dependent effects in submission order.
// Failed writes remain at the head. No callback runs with mu held.
type OutputQueue struct {
	running   bool
	idle      chan struct{}
	mu        sync.Mutex
	pending   []*OutputTicket
	readyWake *OutputTicket
	retry     *outputRetry
}

// Append admits a complete batch before any of its callbacks can run.
func (queue *OutputQueue) Append(writes ...OutputWrite) []*OutputTicket {
	tickets := make([]*OutputTicket, len(writes))
	for i, write := range writes {
		tickets[i] = &OutputTicket{write: write}
		tickets[i].queue.Store(queue)
	}
	queue.mu.Lock()
	queue.pending = append(queue.pending, tickets...)
	queue.mu.Unlock()
	return tickets
}

// FlushAfterReady publishes one readiness transition before attempting a flush.
// Call it once after publishing each ticket's prepared data, without holding provider locks.
// Only the current head can wake an existing drainer that observed unready data.
func (queue *OutputQueue) FlushAfterReady(tickets ...*OutputTicket) error {
	queue.mu.Lock()
	if queue.running && len(queue.pending) != 0 {
		for _, ticket := range tickets {
			if queue.pending[0] == ticket {
				queue.readyWake = ticket
				break
			}
		}
	}
	queue.mu.Unlock()
	return queue.Flush()
}

// Flush attempts pending writes until the queue is empty or a write fails.
// A concurrent or reentrant flush returns ErrOutputInFlight without waiting for the callback.
func (queue *OutputQueue) Flush() (err error) {
	queue.mu.Lock()
	if queue.running {
		queue.mu.Unlock()
		return ErrOutputInFlight
	}
	if len(queue.pending) == 0 {
		queue.mu.Unlock()
		return nil
	}
	queue.running = true
	queue.idle = make(chan struct{})
	queue.mu.Unlock()
	finished := false
	defer func() {
		if !finished {
			queue.mu.Lock()
			queue.finishLocked()
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
			queue.finishLocked()
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
			queue.finishLocked()
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

func (queue *OutputQueue) finishLocked() {
	queue.running = false
	queue.readyWake = nil
	close(queue.idle)
}

// HasPending reports queued writes or a callback that still runs.
func (queue *OutputQueue) HasPending() bool {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return queue.running || len(queue.pending) != 0
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
	clear(queue.pending[len(kept):])
	queue.pending = kept
}

// MoveOwnerTo transfers this owner's pending writes after the destination's existing writes.
// A callback must not transfer its own queue. Other submissions can continue during the transfer.
func (queue *OutputQueue) MoveOwnerTo(owner *OutputOwner, destination *OutputQueue, destinationOwner *OutputOwner) {
	if destination == nil {
		return
	}
	queue.mu.Lock()
	for queue.running {
		idle := queue.idle
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
	clear(queue.pending[len(kept):])
	queue.pending = kept
	queue.mu.Unlock()
	destination.mu.Lock()
	destination.pending = append(destination.pending, moved...)
	destination.mu.Unlock()
}

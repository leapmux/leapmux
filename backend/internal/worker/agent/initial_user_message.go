package agent

import (
	"errors"
	"sync"
	"sync/atomic"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// InitialUserMessage owns one pending opening message for a transcript.
// Repeated calls join the first reservation. After it completes, a later call
// consults stored emptiness again. Keep one instance on each raw transcript sink.
type InitialUserMessage struct {
	mu      sync.Mutex
	pending *initialUserMessageWrite
}

type initialUserMessageWrite struct {
	queue    *OutputQueue
	ticket   *OutputTicket
	owner    *OutputOwner
	ready    atomic.Bool
	captured CapturedTranscript
	checked  bool
	empty    bool
}

// Persist reserves the opening position before capture or the emptiness read.
// isEmpty runs at that position and retries only failed reads. A successful
// decision remains fixed while its write retries. No sink runs under mu.
func (initial *InitialUserMessage) Persist(writer TranscriptServices, content MessageContent, isEmpty func() (bool, error)) error {
	queue := OutputQueueFor(writer)
	if queue == nil {
		return errors.New("the initial user message has no output queue")
	}
	if isEmpty == nil {
		return errors.New("the initial user message has no emptiness check")
	}
	if len(content.Original) == 0 {
		return errors.New("the initial user message has no content")
	}
	if err := validateOrderedContent(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, content); err != nil {
		return err
	}
	initial.mu.Lock()
	write := initial.pending
	created := write == nil
	if created {
		write = &initialUserMessageWrite{queue: queue, owner: NewOutputOwner()}
		write.ticket = queue.Append(OutputWrite{Owner: write.owner, ScopeID: "initial-user-message", RetainedBytes: len(content.Original) + len(content.Supplemental) + len(content.Metadata), Label: "persist the initial user message", Run: func() error {
			if !write.ready.Load() {
				return ErrOutputInFlight
			}
			if !write.checked {
				empty, err := isEmpty()
				if err != nil {
					return err
				}
				write.empty, write.checked = empty, true
			}
			if write.empty {
				if err := write.captured.PersistQueuedMessage(write.ticket, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER); err != nil {
					return err
				}
			}
			initial.release(write)
			return nil
		}})[0]
		initial.pending = write
	}
	initial.mu.Unlock()
	if write.queue != queue {
		return errors.New("the pending initial user message belongs to another output queue")
	}
	var err error
	if created {
		initialized := false
		defer func() {
			if !initialized {
				queue.ClearOwner(write.owner)
				initial.release(write)
			}
		}()
		write.captured = CaptureTranscript(writer, content, SpanInfo{})
		write.ready.Store(true)
		initialized = true
		err = queue.FlushAfterReady(write.ticket)
	} else {
		err = queue.Flush()
	}
	if write.ticket.Done() {
		return nil
	}
	if err == nil {
		err = ErrOutputInFlight
	}
	return &PendingOutputError{Err: err}
}

func (initial *InitialUserMessage) release(write *initialUserMessageWrite) {
	initial.mu.Lock()
	if initial.pending == write {
		initial.pending = nil
	}
	initial.mu.Unlock()
}

package channel

import (
	"log/slog"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// StreamController receives client frames and cancellation for one server stream.
// Both callbacks must return promptly and must tolerate concurrent cancellation.
type StreamController interface {
	OnClientFrame(payload []byte)
	OnCancel()
}

// streamRegistry reserves inboxes before dispatch and removes them on release or cancellation.
type streamRegistry struct {
	mu     sync.Mutex
	byID   map[uint64]*StreamInbox
	closed bool
}

func (r *streamRegistry) reserve(id uint64) (*StreamInbox, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.byID[id] != nil {
		return nil, false
	}
	if r.byID == nil {
		r.byID = make(map[uint64]*StreamInbox)
	}
	inbox := &StreamInbox{}
	r.byID[id] = inbox
	return inbox, true
}

func (r *streamRegistry) remove(id uint64, inbox *StreamInbox) {
	r.mu.Lock()
	if r.byID[id] == inbox {
		delete(r.byID, id)
	}
	r.mu.Unlock()
}

func (r *streamRegistry) bindReserved(id uint64, inbox *StreamInbox, controller StreamController) (func(), bool) {
	if !inbox.Bind(controller) {
		if !inbox.IsBound() {
			r.remove(id, inbox)
		}
		return nil, false
	}
	return func() { r.remove(id, inbox); inbox.Release() }, true
}

// bind supports direct writers whose handler runs synchronously.
func (r *streamRegistry) bind(id uint64, controller StreamController) func() {
	inbox, ok := r.reserve(id)
	if !ok {
		return func() {}
	}
	release, ok := r.bindReserved(id, inbox, controller)
	if !ok {
		return func() {}
	}
	return release
}

// deliver returns cleanup on failure. The caller must send the error before cleanup can emit a clean End.
func (r *streamRegistry) deliver(id uint64, frame *leapmuxv1.InnerStreamRequest) (func(), error) {
	r.mu.Lock()
	inbox := r.byID[id]
	r.mu.Unlock()
	if inbox == nil {
		slog.Debug("dropping a frame for an unknown stream", "correlation_id", id)
		return nil, nil
	}
	if frame.GetCancel() {
		inbox.Cancel()
		if inbox.IsBound() {
			r.remove(id, inbox)
		}
		return nil, nil
	}
	if err := inbox.Deliver(frame.GetPayload()); err != nil {
		return func() { r.remove(id, inbox); inbox.Cancel() }, err
	}
	return nil, nil
}

func (r *streamRegistry) releaseAll() {
	r.mu.Lock()
	r.closed = true
	inboxes := r.byID
	r.byID = nil
	r.mu.Unlock()
	for _, inbox := range inboxes {
		inbox.Cancel()
	}
}

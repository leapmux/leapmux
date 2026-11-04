package channel

import (
	"errors"
	"log/slog"
	"sync"

	"google.golang.org/grpc/codes"

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

// errStreamFrameNoAction is the error of a client frame that selects neither a
// revision nor a cancel. Such a frame states nothing that the worker could act
// on, so the worker fails the stream instead of guessing.
var errStreamFrameNoAction = errors.New("a stream frame selects no action")

// deliver returns cleanup on failure. The caller must send the error before cleanup can emit a clean End.
func (r *streamRegistry) deliver(id uint64, frame *leapmuxv1.InnerStreamRequest) (func(), error) {
	r.mu.Lock()
	inbox := r.byID[id]
	r.mu.Unlock()
	if inbox == nil {
		slog.Debug("dropping a frame for an unknown stream", "correlation_id", id)
		return nil, nil
	}
	failStream := func() { r.remove(id, inbox); inbox.Cancel() }
	switch action := frame.GetAction().(type) {
	case *leapmuxv1.InnerStreamRequest_Cancel:
		inbox.Cancel()
		if inbox.IsBound() {
			r.remove(id, inbox)
		}
		return nil, nil
	case *leapmuxv1.InnerStreamRequest_Payload:
		if err := inbox.Deliver(action.Payload); err != nil {
			return failStream, err
		}
		return nil, nil
	default:
		return failStream, errStreamFrameNoAction
	}
}

// streamFrameErrorCode is the code of the stream error frame that answers a
// client frame that deliver refused.
func streamFrameErrorCode(err error) codes.Code {
	if errors.Is(err, errStreamFrameNoAction) {
		return codes.InvalidArgument
	}
	return codes.ResourceExhausted
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

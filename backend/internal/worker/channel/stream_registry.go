package channel

import (
	"log/slog"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// StreamController is what a streaming handler installs so the transport can
// deliver client->worker frames on its correlation id.
//
// Both methods run on the transport's RECEIVE goroutine -- the one goroutine
// that decrypts every frame for every channel on this worker -- so neither may
// block. Implementations hand the work to their own loop.
type StreamController interface {
	// OnClientFrame delivers one InnerStreamRequest.payload.
	OnClientFrame(payload []byte)
	// OnCancel retires the stream. Called at most once per controller, by an
	// explicit cancel frame or by transport teardown.
	OnCancel()
}

// streamRegistry maps a session's live server streams by correlation id.
type streamRegistry struct {
	mu   sync.Mutex
	byID map[uint64]StreamController
}

func (r *streamRegistry) bind(id uint64, ctrl StreamController) (release func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byID == nil {
		r.byID = make(map[uint64]StreamController)
	}
	r.byID[id] = ctrl
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		// Remove without calling OnCancel -- the handler is already unwinding.
		delete(r.byID, id)
	}
}

func (r *streamRegistry) deliver(id uint64, frame *leapmuxv1.InnerStreamRequest) {
	r.mu.Lock()
	ctrl, ok := r.byID[id]
	if !ok {
		r.mu.Unlock()
		// A frame racing teardown is normal.
		slog.Debug("dropping stream_request for unbound correlation id",
			"correlation_id", id,
		)
		return
	}
	if frame.GetCancel() {
		// Remove before OnCancel so a double cancel is a no-op structurally.
		delete(r.byID, id)
		r.mu.Unlock()
		ctrl.OnCancel()
		return
	}
	r.mu.Unlock()
	ctrl.OnClientFrame(frame.GetPayload())
}

func (r *streamRegistry) releaseAll() {
	r.mu.Lock()
	ctrls := make([]StreamController, 0, len(r.byID))
	for _, c := range r.byID {
		ctrls = append(ctrls, c)
	}
	r.byID = make(map[uint64]StreamController)
	r.mu.Unlock()
	for _, c := range ctrls {
		c.OnCancel()
	}
}

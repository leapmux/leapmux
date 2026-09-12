package channel

import (
	"errors"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
)

var ErrStreamInboxFull = errors.New("the stream receive buffer is full")

const maxPendingStreamFrames = 64

// StreamInbox retains early frames and serializes delivery after the controller binds.
// Callbacks run without its mutex. A callback can therefore cancel or release its stream.
type StreamInbox struct {
	mu           sync.Mutex
	controller   StreamController
	bound        bool
	closed       bool
	failed       bool
	draining     bool
	pending      [][]byte
	pendingBytes int
}

func (inbox *StreamInbox) Bind(controller StreamController) bool {
	inbox.mu.Lock()
	if inbox.closed || inbox.failed || inbox.bound || controller == nil {
		inbox.mu.Unlock()
		return false
	}
	inbox.controller = controller
	inbox.bound = true
	inbox.draining = true
	inbox.mu.Unlock()
	inbox.drain()
	return true
}

func (inbox *StreamInbox) IsBound() bool {
	inbox.mu.Lock()
	defer inbox.mu.Unlock()
	return inbox.bound
}

// Deliver stops accepting frames on overflow. The caller must report the error before calling Cancel.
func (inbox *StreamInbox) Deliver(payload []byte) error {
	inbox.mu.Lock()
	if inbox.closed || inbox.failed {
		inbox.mu.Unlock()
		return nil
	}
	if len(inbox.pending) >= maxPendingStreamFrames || len(payload) > contracts.MaxMessageSize-inbox.pendingBytes {
		inbox.failed = true
		inbox.pending = nil
		inbox.pendingBytes = 0
		inbox.mu.Unlock()
		return ErrStreamInboxFull
	}
	inbox.pending = append(inbox.pending, append([]byte(nil), payload...))
	inbox.pendingBytes += len(payload)
	start := inbox.bound && !inbox.draining
	if start {
		inbox.draining = true
	}
	inbox.mu.Unlock()
	if start {
		inbox.drain()
	}
	return nil
}

func (inbox *StreamInbox) drain() {
	for {
		inbox.mu.Lock()
		if inbox.closed || inbox.failed || len(inbox.pending) == 0 {
			inbox.draining = false
			inbox.mu.Unlock()
			return
		}
		payload := inbox.pending[0]
		inbox.pending[0] = nil
		inbox.pending = inbox.pending[1:]
		inbox.pendingBytes -= len(payload)
		controller := inbox.controller
		inbox.mu.Unlock()
		controller.OnClientFrame(payload)
	}
}

func (inbox *StreamInbox) Cancel() { inbox.close(true) }

// Release discards queued data after the handler finishes, without another cancellation callback.
func (inbox *StreamInbox) Release() { inbox.close(false) }

func (inbox *StreamInbox) close(notify bool) {
	inbox.mu.Lock()
	if inbox.closed {
		inbox.mu.Unlock()
		return
	}
	inbox.closed = true
	controller := inbox.controller
	inbox.controller = nil
	inbox.pending = nil
	inbox.pendingBytes = 0
	inbox.mu.Unlock()
	if notify && controller != nil {
		controller.OnCancel()
	}
}

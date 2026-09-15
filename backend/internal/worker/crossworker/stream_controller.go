package crossworker

import (
	"context"
	"log/slog"
	"sync"
)

type streamChannel interface {
	Context() context.Context
	SendStreamRequest(context.Context, uint64, []byte, bool) error
	CancelStream(context.Context, uint64) error
}

// crossWorkerStreamCtrl forwards revisions and cancellation through one sender loop.
// Both callbacks return without waiting for transport writes. The loop preserves send order.
type crossWorkerStreamCtrl struct {
	ch    streamChannel
	reqID uint64

	mu         sync.Mutex
	pending    []byte
	pendingSet bool
	wake       chan struct{}
	closed     bool
	done       chan struct{}
}

func newCrossWorkerStreamCtrl(ch streamChannel, reqID uint64) *crossWorkerStreamCtrl {
	controller := &crossWorkerStreamCtrl{
		ch: ch, reqID: reqID, wake: make(chan struct{}, 1), done: make(chan struct{}),
	}
	go controller.loop()
	return controller
}

func (controller *crossWorkerStreamCtrl) loop() {
	defer close(controller.done)
	defer func() {
		controller.mu.Lock()
		controller.closed = true
		controller.pending, controller.pendingSet = nil, false
		controller.mu.Unlock()
	}()
	ctx := controller.ch.Context()
	for {
		controller.mu.Lock()
		payload, pending, closed := controller.pending, controller.pendingSet, controller.closed
		controller.pending, controller.pendingSet = nil, false
		controller.mu.Unlock()

		if ctx.Err() != nil {
			return
		}
		if pending {
			if err := controller.ch.SendStreamRequest(ctx, controller.reqID, payload, false); err != nil && ctx.Err() == nil {
				slog.Warn("cross-worker stream update failed", "correlation_id", controller.reqID, "error", err)
			}
		}
		if closed {
			if ctx.Err() == nil {
				if err := controller.ch.CancelStream(ctx, controller.reqID); err != nil {
					slog.Debug("cross-worker stream cancel failed", "correlation_id", controller.reqID, "error", err)
				}
			}
			return
		}
		select {
		case <-controller.wake:
		case <-ctx.Done():
			return
		}
	}
}

// OnClientFrame retains the newest revision. Each revision describes the complete interest set.
func (controller *crossWorkerStreamCtrl) OnClientFrame(payload []byte) {
	controller.mu.Lock()
	if controller.closed || controller.ch.Context().Err() != nil {
		controller.mu.Unlock()
		return
	}
	controller.pending = append([]byte(nil), payload...)
	controller.pendingSet = true
	controller.mu.Unlock()
	controller.notify()
}

func (controller *crossWorkerStreamCtrl) OnCancel() {
	controller.mu.Lock()
	controller.closed = true
	controller.mu.Unlock()
	controller.notify()
}

func (controller *crossWorkerStreamCtrl) notify() {
	select {
	case controller.wake <- struct{}{}:
	default:
	}
}

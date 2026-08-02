package workermgr

import (
	"context"
	"fmt"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
)

// PendingRequests tracks in-flight request/response pairs for worker
// communication. Used when Hub sends a request to a worker and waits
// for a matching response.
type PendingRequests struct {
	mu             sync.Mutex
	pending        map[string]chan *leapmuxv1.ConnectRequest // requestID -> response channel
	defaultTimeout func() time.Duration
}

// NewPendingRequests creates a new PendingRequests tracker.
// The defaultTimeout function is called when a context has no deadline
// to determine the send timeout.
func NewPendingRequests(defaultTimeout func() time.Duration) *PendingRequests {
	return &PendingRequests{
		pending:        make(map[string]chan *leapmuxv1.ConnectRequest),
		defaultTimeout: defaultTimeout,
	}
}

// SendAndWait sends a message to a worker and waits for a response with the
// matching request ID. Returns an error if the context is cancelled, the
// worker is not connected, or the default send timeout (10s) is exceeded.
func (p *PendingRequests) SendAndWait(
	ctx context.Context,
	conn *Conn,
	msg *leapmuxv1.ConnectResponse,
) (*leapmuxv1.ConnectRequest, error) {
	if conn == nil {
		return nil, fmt.Errorf("worker not connected")
	}

	// Enforce a default timeout so callers never hang indefinitely on a
	// stale connection where the worker has died but hasn't been unregistered yet.
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.defaultTimeout())
		defer cancel()
	}

	requestID := id.Generate()
	msg.RequestId = requestID

	ch := make(chan *leapmuxv1.ConnectRequest, 1)

	p.mu.Lock()
	p.pending[requestID] = ch
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		delete(p.pending, requestID)
		p.mu.Unlock()
	}()

	// Request/response RPCs (ChannelOpen, notifications) ride the waitable
	// data path: they must not consume the tiny-control reserve, and they
	// can park under backpressure instead of soft-failing.
	if err := conn.SendWait(ctx, msg); err != nil {
		return nil, fmt.Errorf("send to worker: %w", err)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		// Prefer an already-buffered response over timeout/cancel when both
		// are ready (Go select is random among ready cases).
		select {
		case resp := <-ch:
			return resp, nil
		default:
			return nil, ctx.Err()
		}
	case <-conn.Done():
		select {
		case resp := <-ch:
			return resp, nil
		default:
			return nil, ErrConnectionClosed
		}
	}
}

// Complete delivers a response message to the waiting goroutine.
// Returns true if a pending request was found and completed.
func (p *PendingRequests) Complete(requestID string, msg *leapmuxv1.ConnectRequest) bool {
	p.mu.Lock()
	ch, ok := p.pending[requestID]
	p.mu.Unlock()

	if !ok {
		return false
	}

	select {
	case ch <- msg:
		return true
	default:
		return false
	}
}

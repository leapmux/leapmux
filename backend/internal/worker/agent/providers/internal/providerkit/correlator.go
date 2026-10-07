package providerkit

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Correlator sends raw replies to callers by request ID.
// Providers retain their own envelope formats and decoders.
type Correlator[ID comparable] struct {
	pending sync.Map // ID -> *pendingResponse
}

type pendingResponse struct {
	channel chan json.RawMessage
	observe func(json.RawMessage)
}

// Register reserves an unused reply ID and returns its channel and cleanup function.
// Defer cleanup after registration succeeds, even when the later request fails.
// The channel holds one reply, so delivery does not wait for the caller.
func (c *Correlator[ID]) Register(id ID) (<-chan json.RawMessage, func(), error) {
	return c.RegisterObserved(id, nil)
}

// RegisterObserved calls observe before it sends the reply to the channel.
// The observer runs on the reader. It must not wait for another reply.
// An absent or removed request cannot call the observer.
// A duplicate in-flight ID returns an error and retains the original registration.
func (c *Correlator[ID]) RegisterObserved(id ID, observe func(json.RawMessage)) (<-chan json.RawMessage, func(), error) {
	ch := make(chan json.RawMessage, 1)
	response := &pendingResponse{channel: ch, observe: observe}
	if _, occupied := c.pending.LoadOrStore(id, response); occupied {
		return nil, nil, fmt.Errorf("request ID already has a pending reply: %v", id)
	}
	return ch, func() { c.pending.CompareAndDelete(id, response) }, nil
}

// Deliver claims one pending request and sends its unchanged reply.
// It returns false when no request waits for the ID.
// The atomic lookup prevents duplicate observer calls and duplicate delivery.
func (c *Correlator[ID]) Deliver(id ID, raw json.RawMessage) bool {
	stored, ok := c.pending.LoadAndDelete(id)
	if !ok {
		return false
	}
	response := stored.(*pendingResponse)
	if response.observe != nil {
		response.observe(raw)
	}
	response.channel <- raw
	return true
}

// AwaitResponseTimerTag tags the timer of each AwaitResponse on the process
// clock, beside the label. A test traps it to end the wait.
const AwaitResponseTimerTag = "await-response"

// AwaitResponse waits for a reply, process exit, context cancellation, or timeout.
// Process.Clock supplies the timer. The error identifies the request label.
// A nonpositive timeout waits without a timer.
// Use it for a turn that ends through a reply or cancellation.
func (p *Process) AwaitResponse(
	ch <-chan json.RawMessage,
	label string,
	timeout time.Duration,
) (json.RawMessage, error) {
	if timeout <= 0 {
		select {
		case raw := <-ch:
			return raw, nil
		case <-p.processDone:
			return nil, p.ProcessExitError()
		case <-p.ctx.Done():
			return nil, p.contextEndError()
		}
	}
	timer := p.Clock().NewTimer(timeout, AwaitResponseTimerTag, label)
	defer timer.Stop(AwaitResponseTimerTag, label)
	select {
	case raw := <-ch:
		return raw, nil
	case <-p.processDone:
		return nil, p.ProcessExitError()
	case <-p.ctx.Done():
		return nil, p.contextEndError()
	case <-timer.C:
		return nil, fmt.Errorf("timeout waiting for %s response", label)
	}
}

// contextEndError reports the process exit when exit also cancels the context.
// A select can choose either ready channel. Both paths must report the same cause.
func (p *Process) contextEndError() error {
	select {
	case <-p.processDone:
		return p.ProcessExitError()
	default:
		return p.ctx.Err()
	}
}

// IsPendingForTest reports whether a caller waits for the response to id.
func (c *Correlator[ID]) IsPendingForTest(id ID) bool {
	_, ok := c.pending.Load(id)
	return ok
}

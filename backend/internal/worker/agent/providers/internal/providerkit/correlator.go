package providerkit

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Correlator routes raw response bytes back to pending callers
// keyed by id. Generic over the id type so JSON-RPC 2.0 (int64) and Pi
// (opaque string) share the same plumbing without converging on a single
// envelope shape — the marshal/decode is left to each provider, only the
// pending-map mechanics live here.
type Correlator[ID comparable] struct {
	pending sync.Map // ID -> chan json.RawMessage
}

// Register allocates a delivery channel for `id` and returns it along
// with a cleanup function the caller MUST defer to release the slot
// regardless of whether the response arrived. The channel is buffered
// (capacity 1) so a late delivery after timeout doesn't block the
// dispatcher.
func (c *Correlator[ID]) Register(id ID) (<-chan json.RawMessage, func()) {
	ch := make(chan json.RawMessage, 1)
	c.pending.Store(id, ch)
	return ch, func() { c.pending.Delete(id) }
}

// Deliver hands `raw` to the channel registered for `id`. Returns false
// when no caller was waiting, so the dispatcher can fall through to its
// default handling for unsolicited responses. The slot is removed
// atomically with the lookup.
func (c *Correlator[ID]) Deliver(id ID, raw json.RawMessage) bool {
	chAny, ok := c.pending.LoadAndDelete(id)
	if !ok {
		return false
	}
	chAny.(chan json.RawMessage) <- raw
	return true
}

// AwaitResponseTimerTag tags the timer of each AwaitResponse on the process
// clock, beside the label. A test traps it to end the wait.
const AwaitResponseTimerTag = "await-response"

// AwaitResponse blocks until raw bytes arrive on `ch` or a teardown
// signal fires (process exit, ctx cancel, timeout). The timeout runs on the
// process clock (Process.Clock). The label is
// interpolated into the timeout error so log messages name the stuck
// RPC. Lives on Process so any agent — JSON-RPC, Pi, or future —
// shares the same cancellation semantics.
//
// A timeout of 0 means "no timeout": the wait unblocks only on
// response, process exit, or ctx cancel. Use this for per-turn RPCs
// whose duration is bounded by the user's request, not by clock time.
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

// contextEndError states why the process context ended. The context also ends
// when the process exits (finishOutput), and a select that sees both channels
// ready picks one at random, so the exit wins here: it states why no answer
// comes, where the context alone states only "canceled".
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

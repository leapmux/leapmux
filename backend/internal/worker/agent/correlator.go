package agent

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// responseCorrelator routes raw response bytes back to pending callers
// keyed by id. Generic over the id type so JSON-RPC 2.0 (int64) and Pi
// (opaque string) share the same plumbing without converging on a single
// envelope shape — the marshal/decode is left to each provider, only the
// pending-map mechanics live here.
type responseCorrelator[ID comparable] struct {
	pending sync.Map // ID -> chan json.RawMessage
}

// register allocates a delivery channel for `id` and returns it along
// with a cleanup function the caller MUST defer to release the slot
// regardless of whether the response arrived. The channel is buffered
// (capacity 1) so a late delivery after timeout doesn't block the
// dispatcher.
func (c *responseCorrelator[ID]) register(id ID) (<-chan json.RawMessage, func()) {
	ch := make(chan json.RawMessage, 1)
	c.pending.Store(id, ch)
	return ch, func() { c.pending.Delete(id) }
}

// deliver hands `raw` to the channel registered for `id`. Returns false
// when no caller was waiting, so the dispatcher can fall through to its
// default handling for unsolicited responses. The slot is removed
// atomically with the lookup.
func (c *responseCorrelator[ID]) deliver(id ID, raw json.RawMessage) bool {
	chAny, ok := c.pending.LoadAndDelete(id)
	if !ok {
		return false
	}
	chAny.(chan json.RawMessage) <- raw
	return true
}

// awaitResponse blocks until raw bytes arrive on `ch` or a teardown
// signal fires (process exit, ctx cancel, timeout). The label is
// interpolated into the timeout error so log messages name the stuck
// RPC. Lives on processBase so any agent — JSON-RPC, Pi, or future —
// shares the same cancellation semantics.
//
// A timeout of 0 means "no timeout": the wait unblocks only on
// response, process exit, or ctx cancel. Use this for per-turn RPCs
// whose duration is bounded by the user's request, not by clock time.
func (p *processBase) awaitResponse(
	ch <-chan json.RawMessage,
	label string,
	timeout time.Duration,
) (json.RawMessage, error) {
	if timeout <= 0 {
		select {
		case raw := <-ch:
			return raw, nil
		case <-p.processDone:
			return nil, p.processExitError()
		case <-p.ctx.Done():
			return nil, p.ctx.Err()
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case raw := <-ch:
		return raw, nil
	case <-p.processDone:
		return nil, p.processExitError()
	case <-p.ctx.Done():
		return nil, p.ctx.Err()
	case <-timer.C:
		return nil, fmt.Errorf("timeout waiting for %s response", label)
	}
}

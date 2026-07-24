package tunnel

import (
	"context"
	"sync"
)

// readCredit batches a tunnel conn's read-credit grants and sends them off the read
// path.
//
// It owns the whole "never block Read, coalesce grants, end with the conn
// lifetime" contract, which was previously five Conn fields, a spawned goroutine,
// and a teardown call that had to be remembered at three exit sites. As a type,
// that contract can be read (and tested) in one place: consume accrues, the loop
// sends, the conn lifetime ends it.
//
// The batching is a 16:1 RPC reduction (ReadCreditBatch frames per grant) AND the
// reason the type exists as a dedicated loop: a grant sent inline from Read would
// park the consumer on the shared send path -- contended by every other conn's
// writes on this E2EE channel, and bound to the CHANNEL's lifetime context, so it
// honours neither the read deadline nor Conn.Close. A stalled transport would then
// hang the read goroutine while it still held bytes it had already dequeued, until
// the whole channel tore down. Handing the send to a dedicated goroutine keeps the
// download path off the shared write path.
type readCredit struct {
	mu sync.Mutex
	// pending is the count of consumed frames not yet granted back to the worker.
	pending uint64
	// batch is how many consumed frames accrue before a grant is sent.
	batch uint64
	// signal wakes the loop. Buffered depth 1: credit is additive, so one queued
	// signal subsumes every grant accrued before it is serviced -- which is what
	// lets consume poke it without ever blocking.
	signal chan struct{}
	// ctx bounds the loop and its in-flight grant. It is the conn's lifetime
	// (see connLifetime), so a grant parked on the shared send path unwinds when
	// the conn closes instead of lingering until the whole channel tears down.
	ctx context.Context
	// send delivers one accumulated grant. It may block on the shared send path,
	// which is exactly why only the loop calls it.
	send func(ctx context.Context, credit uint64)
}

// newReadCredit starts the grant loop, bounded by ctx (the conn's lifetime).
func newReadCredit(ctx context.Context, batch uint64, send func(context.Context, uint64)) *readCredit {
	c := &readCredit{
		batch:  batch,
		signal: make(chan struct{}, 1),
		ctx:    ctx,
		send:   send,
	}
	go c.loop()
	return c
}

// consume accounts for frames drained from the read buffer and, once a batch has
// accrued, wakes the loop to replenish the worker's read-send window.
//
// It never blocks. Read calls it under readMu, having already copied bytes into the
// caller's buffer; anything that parked here would withhold bytes the consumer
// already owns.
func (c *readCredit) consume(frames uint64) {
	c.mu.Lock()
	c.pending += frames
	ready := c.pending >= c.batch
	c.mu.Unlock()
	if !ready {
		return
	}
	// Credit is additive, so a signal already queued picks up this frame's
	// contribution too -- a non-blocking poke is enough and never stalls Read.
	select {
	case c.signal <- struct{}{}:
	default:
	}
}

// loop sends accumulated grants for the conn's lifetime. It is the only caller of
// send, so a grant blocked on the shared send path delays nothing but later
// grants -- which coalesce into it anyway.
func (c *readCredit) loop() {
	for {
		select {
		case <-c.signal:
		case <-c.ctx.Done():
			return
		}
		c.mu.Lock()
		grant := c.pending
		c.pending = 0
		c.mu.Unlock()
		if grant > 0 {
			c.send(c.ctx, grant)
		}
	}
}

// done is closed when the conn lifetime ends. For tests asserting that a teardown
// path actually released it, rather than leaving a goroutine alive until the whole
// channel dies.
func (c *readCredit) done() <-chan struct{} { return c.ctx.Done() }

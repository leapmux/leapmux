package main

import (
	"context"
	"errors"
	"time"

	"github.com/leapmux/leapmux/util/drain"
)

const (
	handlerDrainTimeout   = 5 * time.Second
	handlerInterruptGrace = 250 * time.Millisecond
)

// rpcHandlerDrain keeps the deadline and wait operations on one session.
type rpcHandlerDrain struct {
	timeout        time.Duration
	interruptGrace time.Duration
	now            func() time.Time
	wait           func(<-chan struct{}, time.Duration, string) bool
	cleanupDone    <-chan struct{}
	peerDone       <-chan struct{}
	waitCleanup    func(<-chan struct{}, <-chan struct{}) bool
}

func newRPCHandlerDrain() rpcHandlerDrain {
	return rpcHandlerDrain{
		timeout:        handlerDrainTimeout,
		interruptGrace: handlerInterruptGrace,
		now:            time.Now,
		wait:           drain.WaitBounded,
		waitCleanup:    waitShutdownCleanup,
	}
}

// run shares one deadline across the waits before and after writer interruption.
func (d rpcHandlerDrain) run(handlers *drain.Counter, cause error, interrupt func()) {
	deadline := d.now().Add(d.timeout)
	if cause == nil || errors.Is(cause, context.Canceled) {
		if d.wait(handlers.DoneChan(), deadline.Sub(d.now()), "") {
			return
		}
		if d.cleanupDone != nil {
			// App.Shutdown already waits for owned cleanup before process exit.
			// Retain its reply writer until that cleanup ends or the peer disconnects.
			if d.waitCleanup(d.cleanupDone, d.peerDone) {
				// Cleanup completion precedes response writing.
				// Allow a response grace even when cleanup finished at the first deadline.
				if d.wait(handlers.DoneChan(), d.interruptGrace, "") {
					return
				}
			}
		}
	}
	interrupt()
	d.wait(handlers.DoneChan(), max(deadline.Sub(d.now()), d.interruptGrace),
		"rpc session: the handler drain timed out; abandon in-flight handlers")
}

func waitShutdownCleanup(cleanupDone, peerDone <-chan struct{}) bool {
	select {
	case <-peerDone:
		return false
	default:
	}
	select {
	case <-cleanupDone:
		return true
	case <-peerDone:
		return false
	}
}

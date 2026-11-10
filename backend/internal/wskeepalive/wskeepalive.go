// Package wskeepalive probes long-lived WebSockets.
package wskeepalive

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/coder/websocket"
)

const (
	// Interval is how often an idle long-lived socket is probed.
	//
	// Every other bound on these connections fires on a WRITE -- the per-write
	// watchdog, the stall clock, the byte budget -- so a peer that stops
	// receiving without sending a close frame (a laptop suspending, a mobile
	// link dropping, a middlebox forgetting the flow) is invisible to all of
	// them. Nothing notices until the OS TCP stack gives up, which with Go's
	// default keepalives is on the order of ten minutes and behind a proxy that
	// holds the connection open may be never.
	//
	// That became a user-visible cost when connections started being counted:
	// each dead tab holds two leases against max_connections_per_user, and the
	// message a refused user gets tells them to close a tab -- which does
	// nothing for sockets that are already gone. Ten seconds is well inside
	// any plausible cap-refill window and far below the idle timeouts of common
	// proxies, which this doubles as protection against.
	Interval = 10 * time.Second
	// Timeout limits one probe. Generous relative to a round trip on
	// any real link, because the cost of being wrong is disconnecting a healthy
	// client.
	//
	// It is not the whole story, and cannot be: coder/websocket wraps every
	// control write in its OWN five-second deadline (write.go, writeControl),
	// so a probe fails at five seconds however long this is. Worse, that
	// deadline covers acquiring the connection's write mutex, which a data
	// frame holds for as long as it is in flight -- up to relayWriteTimeout,
	// which is longer. A single failed probe therefore does not mean the peer
	// is gone; see FailuresBeforeDrop.
	Timeout = 10 * time.Second
	// FailuresBeforeDrop is how many CONSECUTIVE probes must fail
	// before the connection is dropped.
	//
	// One is not enough, because a failed probe has two causes and only one of
	// them is a dead peer. The other is a data frame holding the write mutex
	// past the library's five-second control deadline -- a tab whose receive
	// window is full during a terminal burst, which is exactly the condition
	// ws_relay_writer budgets ten seconds and thirty seconds of stall to ride
	// out. Condemning on one sample let the keepalive pre-empt those bounds and
	// report a dead peer for a client that was alive and about to finish.
	//
	// At a ten-second interval, two failed probes can disconnect a slow peer
	// before its writer's stall limit. The same policy applies to every route.
	FailuresBeforeDrop = 2
)

// Probe holds a connection and its liveness callbacks. OnAnswer runs
// synchronously after each answered probe, before the loop takes another tick.
type Probe struct {
	Conn     *websocket.Conn
	Cancel   func()
	OnAnswer func()
	Attrs    []any
}

// A probe loop is only safe once something is READING the socket.
// coder/websocket says so outright (conn.go): "Ping must be called concurrently
// with Reader as it does not read from the connection but instead waits for a
// Reader call to read the pong." A probe armed before there is a reader cannot
// see its answer, so it times out and cancels a perfectly healthy connection --
// on /ws/userevents that was the whole pre-bootstrap window (the ACL resolve,
// the resume scan or baseline walk, the snapshot marshal and the bootstrap
// write), which is precisely the reconnect-storm case this endpoint has to
// survive.
//
// So the reader is part of ARMING rather than a precondition in prose. The two
// shapes a long-lived endpoint can have are the two methods below, and a third
// endpoint has to pick one of them by name: neither can be called without
// saying where its reads come from.

// Production is the pace a real connection is probed at. A function
// rather than a package variable, so no handler and no test can mutate the one
// the others read.
func Production() Pace {
	return Pace{Interval: Interval, Timeout: Timeout}
}

// Pace is how fast the probe loop runs. A value each handler carries
// rather than a package variable a test overrides: these handlers run in
// parallel with each other and with every other test in this package, so a
// mutable global would be a data race between a test setting its pace and a
// handler reading it.
type Pace struct {
	Interval time.Duration
	Timeout  time.Duration
	// Ticks, when non-nil, replaces the interval ticker. A test drives one
	// probe at a time through this channel. The test controls when the loop
	// starts probing, without relying on a duration a loaded CI machine can
	// exceed. Nil uses the production ticker.
	Ticks <-chan time.Time
}

// StartDrainingReads probes Probe.Conn and owns the goroutine that drains its reads,
// starting the reader FIRST so it is live before any probe can fire. For a
// handler with no use for what the peer sends: it cannot arm a probe loop
// without the reader that answers it, because this one call starts both.
//
// The reads are discarded. /ws/userevents clients send nothing after the URL
// query, and the loop is there so the library observes a close frame promptly
// when the peer disconnects -- and so a pong is processed at all.
//
// Both goroutines cancel on the way out: a read that fails is how a peer that
// DID send a close frame is noticed, and a probe that fails is how one that
// vanished without one is.
func (k Pace) StartDrainingReads(ctx context.Context, probe Probe) {
	go func() {
		defer probe.Cancel()
		for {
			if _, _, err := probe.Conn.Read(ctx); err != nil {
				return
			}
		}
	}()
	k.StartBesideAReadLoop(ctx, probe)
}

// StartBesideAReadLoop probes Probe.Conn while the caller owns its reader.
// Start that reader before this call, or enter its read loop immediately after
// this call. Pong processing requires an active reader, so no other work may
// delay a reader that has not started yet.
func (k Pace) StartBesideAReadLoop(ctx context.Context, probe Probe) {
	ticks, stop := k.tick()
	go func() {
		defer probe.Cancel()
		defer stop()
		failures := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticks:
			}
			probeCtx, done := context.WithTimeout(ctx, k.Timeout)
			err := probe.Conn.Ping(probeCtx)
			done()
			if err == nil {
				// Consecutive, so a probe lost to a long write does not count
				// against one lost later to a peer that really did vanish.
				failures = 0
				if probe.OnAnswer != nil {
					probe.OnAnswer()
				}
				continue
			}
			// A cancelled parent means the handler is already unwinding, which
			// is not a dead peer and not worth a line.
			if ctx.Err() != nil {
				return
			}
			failures++
			if failures < FailuresBeforeDrop {
				slog.Debug("websocket keepalive probe failed; waiting for the next one",
					append(slices.Clone(probe.Attrs), "failures", failures, "error", err)...)
				continue
			}
			slog.Debug("websocket keepalive failed; dropping the connection",
				append(slices.Clone(probe.Attrs), "failures", failures, "error", err)...)
			return
		}
	}()
}

// tick is where probes come from: the interval ticker, or the channel a test
// injected. Built on the CALLER's goroutine so the interval is measured from
// the moment the loop is armed rather than from whenever its goroutine first
// runs.
//
// A zero pace panics rather than defaulting. time.NewTicker would panic anyway,
// with a message about a non-positive interval and no clue whose; a handler
// composed without a pace is a wiring bug, and the one it produces -- a socket
// with no liveness probe at all -- is invisible from outside.
func (k Pace) tick() (<-chan time.Time, func()) {
	if k.Ticks != nil {
		return k.Ticks, func() {}
	}
	if k.Interval <= 0 {
		panic("wskeepalive: a probe requires a keepalive pace")
	}
	ticker := time.NewTicker(k.Interval)
	return ticker.C, ticker.Stop
}

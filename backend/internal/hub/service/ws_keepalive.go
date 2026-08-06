package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/coder/websocket"
)

const (
	// wsKeepaliveInterval is how often an idle long-lived socket is probed.
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
	// nothing for sockets that are already gone. Thirty seconds is well inside
	// any plausible cap-refill window and far below the idle timeouts of common
	// proxies, which this doubles as protection against.
	wsKeepaliveInterval = 30 * time.Second
	// wsKeepaliveTimeout bounds ONE probe. Generous relative to a round trip on
	// any real link, because the cost of being wrong is disconnecting a healthy
	// client.
	//
	// It is not the whole story, and cannot be: coder/websocket wraps every
	// control write in its OWN five-second deadline (write.go, writeControl),
	// so a probe fails at five seconds however long this is. Worse, that
	// deadline covers acquiring the connection's write mutex, which a data
	// frame holds for as long as it is in flight -- up to relayWriteTimeout,
	// which is longer. A single failed probe therefore does not mean the peer
	// is gone; see wsKeepaliveFailuresBeforeDrop.
	wsKeepaliveTimeout = 10 * time.Second
	// wsKeepaliveFailuresBeforeDrop is how many CONSECUTIVE probes must fail
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
	// Two probes span a whole interval, so anything the WRITER considers fatal
	// has already fired by the second one, with its own timeout and its own
	// reason. What is left for the keepalive is what only it can see: a socket
	// with nothing to write whose peer is gone.
	wsKeepaliveFailuresBeforeDrop = 2
)

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

// wsKeepaliveProduction is the pace a real connection is probed at. A function
// rather than a package variable, so no handler and no test can mutate the one
// the others read.
func wsKeepaliveProduction() wsKeepalivePace {
	return wsKeepalivePace{interval: wsKeepaliveInterval, timeout: wsKeepaliveTimeout}
}

// wsKeepalivePace is how fast the probe loop runs. A value each handler carries
// rather than a package variable a test overrides: these handlers run in
// parallel with each other and with every other test in this package, so a
// mutable global would be a data race between a test setting its pace and a
// handler reading it.
type wsKeepalivePace struct {
	interval time.Duration
	timeout  time.Duration
	// ticks, when non-nil, replaces the interval ticker. A test drives probes
	// through it one at a time, so a case about WHEN the loop is armed is bounded
	// by an event it sends rather than by a duration a loaded CI box overshoots.
	// Nil in production, where the ticker is the only thing that fires it.
	ticks <-chan time.Time
}

// startDrainingReads probes conn and OWNS the goroutine that drains its reads,
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
func (k wsKeepalivePace) startDrainingReads(ctx context.Context, conn *websocket.Conn, cancel context.CancelFunc, endpoint, userID string) {
	go func() {
		defer cancel()
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	}()
	k.startBesideAReadLoop(ctx, conn, cancel, endpoint, userID)
}

// startBesideAReadLoop probes conn for a handler that reads the socket ITSELF,
// from the statement after this one. The /ws/channel relay is that shape: its
// read loop is what routes frontend frames, so the keepalive cannot own it.
//
// Call it immediately before entering that loop and nowhere else. Every
// statement between the two is a window in which a probe has no reader.
func (k wsKeepalivePace) startBesideAReadLoop(ctx context.Context, conn *websocket.Conn, cancel context.CancelFunc, endpoint, userID string) {
	ticks, stop := k.tick()
	go func() {
		defer cancel()
		defer stop()
		failures := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticks:
			}
			probeCtx, done := context.WithTimeout(ctx, k.timeout)
			err := conn.Ping(probeCtx)
			done()
			if err == nil {
				// Consecutive, so a probe lost to a long write does not count
				// against one lost later to a peer that really did vanish.
				failures = 0
				continue
			}
			// A cancelled parent means the handler is already unwinding, which
			// is not a dead peer and not worth a line.
			if ctx.Err() != nil {
				return
			}
			failures++
			if failures < wsKeepaliveFailuresBeforeDrop {
				slog.Debug("websocket keepalive probe failed; waiting for the next one",
					"endpoint", endpoint, "user_id", userID,
					"failures", failures, "error", err)
				continue
			}
			slog.Debug("websocket keepalive failed; dropping the connection",
				"endpoint", endpoint, "user_id", userID,
				"failures", failures, "error", err)
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
func (k wsKeepalivePace) tick() (<-chan time.Time, func()) {
	if k.ticks != nil {
		return k.ticks, func() {}
	}
	if k.interval <= 0 {
		panic("service: WebSocket handler composed without a keepalive pace")
	}
	ticker := time.NewTicker(k.interval)
	return ticker.C, ticker.Stop
}

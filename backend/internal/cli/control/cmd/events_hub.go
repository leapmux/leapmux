package cmd

import (
	"context"
	"errors"
	"io"
	"sync/atomic"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
)

// hubSessionFunc runs one subscription to the hub's user events, and hands
// each event to onEvent until the stream ends. It returns nil when the hub
// ended the stream, the error of onEvent when onEvent failed, and the failure
// of the stream otherwise.
type hubSessionFunc func(ctx context.Context, onEvent func(*leapmuxv1.WatchUserEvent) error) error

// hubUserEvents subscribes to the user events of workspaceIDs, or of every
// workspace when it is empty, over the transport of c: the hub's
// `/ws/userevents` WebSocket, or the socket of the tab that runs the CLI.
func hubUserEvents(c *control.Client, workspaceIDs []string) hubSessionFunc {
	if c.IsWorkerIPC() {
		return func(ctx context.Context, onEvent func(*leapmuxv1.WatchUserEvent) error) error {
			return streamWatchUserLocal(ctx, c, workspaceIDs, func(evt *leapmuxv1.WatchUserEvent) (bool, error) {
				return false, onEvent(evt)
			})
		}
	}
	return func(ctx context.Context, onEvent func(*leapmuxv1.WatchUserEvent) error) error {
		stream, err := c.OpenUserEvents(ctx, workspaceIDs)
		if err != nil {
			return err
		}
		defer func() { _ = stream.Close() }()
		for {
			evt, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			if err := onEvent(evt); err != nil {
				return err
			}
		}
	}
}

// hubInput is one input of the hub source: an event, the end of a session,
// or the failure that ends the stream.
type hubInput struct {
	event *leapmuxv1.WatchUserEvent
	// down reports that a session ended, and err is why. A nil err is a
	// stream that the hub ended.
	down bool
	err  error
	// fatal ends the stream: the first session failed before its first
	// event, or the hub refused the stream for good.
	fatal error
}

// hubReconnectTimerTag identifies the reconnect timer of the hub source to a
// test clock.
const hubReconnectTimerTag = "events-hub-reconnect"

// errHubEndedFirst is the failure of a first session that ended before its
// first event.
var errHubEndedFirst = cli.Newf(cli.CodeUnavailable, "the hub ended the stream before its first event")

// runHubSource keeps a subscription to the hub's user events open until ctx
// ends, and sends what it receives to out.
//
// The first session must deliver: a failure before its first event ends the
// stream, so a command that cannot reach the hub fails at once. After that,
// a session that ends reports down and subscribes again after the backoff of
// every CLI watch, unless the hub refused the stream for good.
func runHubSource(ctx context.Context, session hubSessionFunc, clock quartz.Clock, out chan<- hubInput) {
	send := func(in hubInput) bool {
		select {
		case out <- in:
			return true
		case <-ctx.Done():
			return false
		}
	}
	backoff := newReconnectBackoff(activityReconnectInitial, activityReconnectMax)
	everDelivered := false
	for {
		var delivered atomic.Bool
		err := session(ctx, func(evt *leapmuxv1.WatchUserEvent) error {
			delivered.Store(true)
			if !send(hubInput{event: evt}) {
				return ctx.Err()
			}
			return nil
		})
		if ctx.Err() != nil {
			return
		}
		if !everDelivered && !delivered.Load() {
			if err == nil {
				err = errHubEndedFirst
			}
			send(hubInput{fatal: err})
			return
		}
		everDelivered = true
		if refusedForGood(err) {
			send(hubInput{fatal: err})
			return
		}
		if !send(hubInput{down: true, err: err}) {
			return
		}
		timer := clock.NewTimer(backoff.afterSession(delivered.Load()), hubReconnectTimerTag)
		select {
		case <-ctx.Done():
			timer.Stop(hubReconnectTimerTag)
			return
		case <-timer.C:
		}
	}
}

// refusedForGood reports whether err is a refusal that a retry with the same
// credential cannot change: no valid credential, no permission, a peer of
// another version, a request that the peer rejects, or a target that does
// not exist. Every other failure can pass, so a watch retries it.
func refusedForGood(err error) bool {
	if err == nil {
		return false
	}
	switch cli.FromError(inBandError(err)).Code {
	case cli.CodeNotLoggedIn, cli.CodePermissionDenied, cli.CodeVersionMismatch,
		cli.CodeInvalidArgument, cli.CodeUsage, cli.CodeNotFound:
		return true
	default:
		return false
	}
}

package cmd

import (
	"context"
	"errors"
	"sync"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// startHubSource runs runHubSource over a fakeHub and returns what it sends.
func startHubSource(t *testing.T, clock *quartz.Mock) (context.Context, *fakeHub, <-chan hubInput, <-chan struct{}) {
	return startHubSourceWithPolicy(t, clock, true)
}

func startHubSourceWithPolicy(t *testing.T, clock *quartz.Mock, reconnect bool) (context.Context, *fakeHub, <-chan hubInput, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	hub := newFakeHub()
	out := make(chan hubInput, 16)
	stopped := make(chan struct{})
	var once sync.Once
	go func() {
		runHubSource(ctx, hub.session, clock, reconnect, out)
		once.Do(func() { close(stopped) })
	}()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
	return ctx, hub, out, stopped
}

// The first session must deliver: a hub that cannot be reached fails the
// command at once, with the code of its failure.
func TestHubSource_TheFirstFailureIsFatal(t *testing.T) {
	refused := connect.NewError(connect.CodeUnauthenticated, "the token expired")
	ctx, hub, out, stopped := startHubSource(t, testutil.NewQuartzMock(t))
	receive(t, ctx, hub.sessions).end(refused)
	in := receive(t, ctx, out)
	require.ErrorIs(t, in.fatal, refused)
	receive(t, ctx, stopped)
}

func TestHubSource_AFirstSessionThatEndsBeforeItsFirstEventIsFatal(t *testing.T) {
	ctx, hub, out, stopped := startHubSource(t, testutil.NewQuartzMock(t))
	receive(t, ctx, hub.sessions).end(nil)
	in := receive(t, ctx, out)
	require.ErrorIs(t, in.fatal, errHubEndedFirst)
	assert.Equal(t, cli.CodeUnavailable, cli.FromError(in.fatal).Code)
	receive(t, ctx, stopped)
}

// After the first event, a session that ends reports down and subscribes
// again: after the shortest backoff when it delivered, and after a longer
// one each time a session delivers nothing.
func TestHubSource_ASessionThatDropsSubscribesAgain(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	reconnect := trapNewTimer(t, clock, hubReconnectTimerTag)
	ctx, hub, out, _ := startHubSource(t, clock)

	first := receive(t, ctx, hub.sessions)
	first.send(snapshotEvent([]string{"ws-1"}))
	assert.NotNil(t, receive(t, ctx, out).event.GetInitial())
	reset := errors.New("connection reset by peer")
	first.end(reset)
	down := receive(t, ctx, out)
	assert.True(t, down.down)
	require.ErrorIs(t, down.err, reset)
	assert.Equal(t, activityReconnectInitial, testutil.WaitForTimer(t, ctx, reconnect))
	clock.Advance(activityReconnectInitial).MustWait(ctx)

	second := receive(t, ctx, hub.sessions)
	second.end(errors.New("dial: connection refused"))
	assert.True(t, receive(t, ctx, out).down, "a failed session after the first event is a gap, not the end")
	assert.Equal(t, 2*activityReconnectInitial, testutil.WaitForTimer(t, ctx, reconnect), "a session that delivered nothing grows the backoff")
	clock.Advance(2 * activityReconnectInitial).MustWait(ctx)

	third := receive(t, ctx, hub.sessions)
	third.end(nil)
	down = receive(t, ctx, out)
	assert.True(t, down.down)
	assert.NoError(t, down.err, "a session that the hub ended has no error")
	assert.Equal(t, 4*activityReconnectInitial, testutil.WaitForTimer(t, ctx, reconnect))
}

// A refusal for good after the first event ends the stream: a retry with
// the same credential cannot pass.
func TestHubSource_ARefusalForGoodIsFatal(t *testing.T) {
	ctx, hub, out, stopped := startHubSource(t, testutil.NewQuartzMock(t))
	session := receive(t, ctx, hub.sessions)
	session.send(snapshotEvent([]string{"ws-1"}))
	receive(t, ctx, out)
	revoked := connect.NewError(connect.CodePermissionDenied, "the credential was revoked")
	session.end(revoked)
	in := receive(t, ctx, out)
	require.ErrorIs(t, in.fatal, revoked)
	receive(t, ctx, stopped)
}

func TestHubSource_NoReconnectEndsOnDisconnect(t *testing.T) {
	ctx, hub, out, stopped := startHubSourceWithPolicy(t, testutil.NewQuartzMock(t), false)
	session := receive(t, ctx, hub.sessions)
	session.send(snapshotEvent([]string{"ws-1"}))
	receive(t, ctx, out)
	drop := errors.New("connection reset by peer")
	session.end(drop)
	in := receive(t, ctx, out)
	require.ErrorIs(t, in.fatal, drop)
	assert.False(t, in.down)
	receive(t, ctx, stopped)
}

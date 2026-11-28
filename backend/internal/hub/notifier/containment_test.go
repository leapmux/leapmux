package notifier

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/hub/workermgr/workermgrtest"
)

func TestDeregisterAcknowledgementKeepsTheConnectionContained(t *testing.T) {
	const workerID = "worker-contained"
	entered, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	registry := workermgr.New(containmentReach(func(ctx context.Context, _ *auth.UserInfo, _ string) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	connection := workermgrtest.NewConn(t, workerID)
	_, err := registry.Register(connection)
	require.NoError(t, err)
	t.Cleanup(func() { registry.Unregister(workerID, connection) })
	type reachResult struct {
		conn *workermgr.Conn
		err  error
	}
	done := make(chan reachResult, 1)
	go func() {
		conn, err := registry.ConnForUser(ctx, &auth.UserInfo{}, workerID)
		done <- reachResult{conn: conn, err: err}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	n := New(registry, ackSender(), func() time.Duration { return time.Second })
	n.SendDeregister(ctx, workerID)
	assert.True(t, registry.IsDeregistering(workerID), "acknowledgement must not restore reach to a published connection")
	close(release)
	select {
	case result := <-done:
		require.NoError(t, result.err)
		assert.Nil(t, result.conn)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.True(t, registry.Unregister(workerID, connection))
	assert.False(t, registry.IsDeregistering(workerID))
}

type containmentReach func(context.Context, *auth.UserInfo, string) error

func (f containmentReach) AuthorizeWorkerReach(ctx context.Context, user *auth.UserInfo, workerID string) error {
	return f(ctx, user, workerID)
}

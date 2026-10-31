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
	for _, alreadyDeleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "deregistering", true: "deleted"}[alreadyDeleted], func(t *testing.T) {
			e := newEnv(t)
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
			connection, _ := workermgrtest.NewRecordedConn(t, e.worker)
			_, err := registry.Register(connection)
			require.NoError(t, err)
			t.Cleanup(func() { registry.Unregister(e.worker, connection) })
			type reachResult struct {
				conn *workermgr.Conn
				err  error
			}
			done := make(chan reachResult, 1)
			go func() {
				conn, err := registry.ConnForUser(ctx, &auth.UserInfo{}, e.worker)
				done <- reachResult{conn: conn, err: err}
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if alreadyDeleted {
				require.NoError(t, e.st.Workers().MarkDeleted(ctx, e.worker))
			}
			n := New(e.st, registry, ackSender(), func() time.Duration { return time.Second }, nil)
			require.NoError(t, n.SendDeregister(ctx, e.worker))
			assert.True(t, registry.IsDeregistering(e.worker), "deletion must not restore reach to a published connection")
			close(release)
			select {
			case result := <-done:
				require.NoError(t, result.err)
				assert.Nil(t, result.conn)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			require.True(t, registry.Unregister(e.worker, connection))
			assert.False(t, registry.IsDeregistering(e.worker))
		})
	}
}

type containmentReach func(context.Context, *auth.UserInfo, string) error

func (f containmentReach) AuthorizeWorkerReach(ctx context.Context, user *auth.UserInfo, workerID string) error {
	return f(ctx, user, workerID)
}

package workermgr

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
)

func TestDeregisteringMarkersAreLimitedToLiveConnections(t *testing.T) {
	m := New(DenyAllReach())
	for i := range 100 {
		m.MarkDeregistering(fmt.Sprintf("offline-%d", i))
	}
	assert.Zero(t, len(m.deregistering), "offline deregistrations must not retain registry entries")
	first, _ := newTestConn(t, "worker", nil, nil)
	_, err := m.Register(first)
	require.NoError(t, err)
	m.MarkDeregistering("worker")
	require.True(t, m.IsDeregistering("worker"))
	replacement, _ := newTestConn(t, "worker", nil, nil)
	_, err = m.Register(replacement)
	require.NoError(t, err)
	assert.False(t, m.Unregister("worker", first))
	assert.True(t, m.IsDeregistering("worker"), "stale teardown must preserve replacement containment")
	assert.True(t, m.Unregister("worker", replacement))
	assert.False(t, m.IsDeregistering("worker"))
	m.MarkDeregistering("worker")
	assert.False(t, m.IsDeregistering("worker"), "late marking must not recreate an offline entry")
}

func TestUserReachRequiresTheConnectionObservedBeforeAuthorization(t *testing.T) {
	for _, initiallyConnected := range []bool{false, true} {
		t.Run(map[bool]string{false: "offline admission", true: "connection replacement"}[initiallyConnected], func(t *testing.T) {
			authorizer := &blockedReachAuthorizer{entered: make(chan struct{}), release: make(chan struct{})}
			t.Cleanup(authorizer.unblock)
			m := New(authorizer)
			var first *Conn
			if initiallyConnected {
				first, _ = newTestConn(t, "worker", nil, nil)
				_, err := m.Register(first)
				require.NoError(t, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			type reachResult struct {
				conn *Conn
				err  error
			}
			done := make(chan reachResult, 1)
			go func() {
				conn, err := m.ConnForUser(ctx, &auth.UserInfo{}, "worker")
				done <- reachResult{conn: conn, err: err}
			}()
			select {
			case <-authorizer.entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if initiallyConnected {
				require.True(t, m.Unregister("worker", first))
			}
			replacement, _ := newTestConn(t, "worker", nil, nil)
			_, err := m.Register(replacement)
			require.NoError(t, err)
			t.Cleanup(func() { m.Unregister("worker", replacement) })
			authorizer.unblock()
			select {
			case result := <-done:
				require.NoError(t, result.err)
				assert.Nil(t, result.conn, "authorization must not transfer to a different connection")
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			assert.Same(t, replacement, m.ConnForTrustedPath("worker"))
		})
	}
}

type blockedReachAuthorizer struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (a *blockedReachAuthorizer) AuthorizeWorkerReach(ctx context.Context, _ *auth.UserInfo, _ string) error {
	close(a.entered)
	select {
	case <-a.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *blockedReachAuthorizer) unblock() { a.once.Do(func() { close(a.release) }) }

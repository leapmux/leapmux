package hub

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/testutil"
	workerhub "github.com/leapmux/leapmux/internal/worker/hub"
	"github.com/leapmux/leapmux/internal/workerkeys/workerkeystest"
)

// workerKeysClient builds a worker hub client aimed at srv's local listener.
func workerKeysClient(t *testing.T, srv *Server, opts ...workerhub.ClientOption) *workerhub.Client {
	t.Helper()
	require.NotEmpty(t, srv.localListenURLs, "startTestServer always binds a local listener")
	endpoint, err := hubtransport.New(srv.localListenURLs[0])
	require.NoError(t, err)
	t.Cleanup(endpoint.CloseIdleConnections)
	client := workerhub.New(endpoint, opts...)
	t.Cleanup(client.Stop)
	// An owner is seeded, so BuildTabSync would produce an inventory; an empty
	// one is enough to make Connect write a second frame after the hello, which is
	// what the give-up path needs to be reachable.
	client.TabSyncProvider = func() *leapmuxv1.WorkerTabInventory { return &leapmuxv1.WorkerTabInventory{} }
	return client
}

// A Worker that presents keys the Hub did not pin is refused for good: the loop
// stops with ErrKeysRefused, keeps the state (no OnDeregister), and the error
// states the fact without the re-registration advice the CLI adds.
func TestServer_AWorkerWithOtherKeysStopsReconnecting(t *testing.T) {
	t.Parallel()
	srv := startTestServer(t, &config.Config{})
	ctx := testutil.DeadlineContext(t)
	owner := storetest.SeedUser(t, srv.store, "keys-owner")
	credentials, err := srv.RegisterWorker(ctx, owner.ID, workerkeystest.Keys(0x31))
	require.NoError(t, err)

	var deregisters atomic.Int32
	client := workerKeysClient(t, srv, workerhub.WithClock(testutil.NewQuartzMock(t)))
	client.PublicKeys = workerkeystest.Keys(0x77)
	client.EncryptionMode = leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM
	client.OnDeregister = func() { deregisters.Add(1) }

	err = client.ConnectWithReconnect(ctx, credentials.AuthToken)
	require.Error(t, err)
	assert.ErrorIs(t, err, workerhub.ErrKeysRefused,
		"the refusal must reach the reconnect loop through the real receive path")
	assert.Contains(t, err.Error(), "differ from the keys it registered with",
		"the error states the Hub's fact")
	assert.NotContains(t, err.Error(), "register it again",
		"the remedy belongs to reregistrationAdvice, not to the Hub's fact")
	assert.NotContains(t, err.Error(), "deregister")
	assert.Zero(t, deregisters.Load(), "a key refusal must never clear the worker's state")
}

// A Worker the Hub has DELETED is told so for good: OnDeregister runs and the
// loop stops. The Hub refuses before it reads the hello, so the status arrives
// through either Send or Receive; only the outcome is asserted.
func TestServer_ADeletedWorkerIsToldToStop(t *testing.T) {
	t.Parallel()
	srv := startTestServer(t, &config.Config{})
	ctx := testutil.DeadlineContext(t)
	owner := storetest.SeedUser(t, srv.store, "deleted-owner")
	credentials, err := srv.RegisterWorker(ctx, owner.ID, workerkeystest.Keys(0x31))
	require.NoError(t, err)
	require.NoError(t, srv.store.Workers().MarkDeleted(ctx, credentials.WorkerID))

	var deregisters atomic.Int32
	client := workerKeysClient(t, srv, workerhub.WithClock(testutil.NewQuartzMock(t)))
	client.PublicKeys = workerkeystest.Keys(0x31)
	client.EncryptionMode = leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM
	client.OnDeregister = func() { deregisters.Add(1) }

	require.NoError(t, client.ConnectWithReconnect(ctx, credentials.AuthToken))
	assert.Equal(t, int32(1), deregisters.Load(),
		"a deleted worker is told to stop and clear its state")
}

// A token the Hub has no row for is retried for ever rather than treated as a
// deletion: a Worker pointed at the wrong Hub, or a Hub restored from an older
// backup, must keep its identity.
func TestServer_AWorkerTheHubDoesNotKnowKeepsRetrying(t *testing.T) {
	t.Parallel()
	srv := startTestServer(t, &config.Config{})
	testCtx := testutil.DeadlineContext(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	clock := testutil.NewQuartzMock(t)

	var deregisters atomic.Int32
	client := workerKeysClient(t, srv, workerhub.WithClock(clock))
	client.PublicKeys = workerkeystest.Keys(0x31)
	client.EncryptionMode = leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM
	client.OnDeregister = func() { deregisters.Add(1) }

	// The constant is unexported in the worker package, so the tag is its literal.
	trap := clock.Trap().NewTimer("hub-reconnect")
	t.Cleanup(trap.Close)

	done := make(chan error, 1)
	go func() { done <- client.ConnectWithReconnect(ctx, id.Generate()) }()

	// The first refusal arms the retry; advance exactly to it, because quartz
	// fails a test that advances past the next timer and the wait is jittered.
	first := trap.MustWait(testCtx)
	first.MustRelease(testCtx)
	clock.Advance(first.Duration).MustWait(testCtx)

	// A second timer proves the second attempt happened. Release it so the client
	// is not parked inside NewTimer when the trap closes.
	second := trap.MustWait(testCtx)
	second.MustRelease(testCtx)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "cancelling the context ends the loop cleanly")
	case <-testCtx.Done():
		require.FailNow(t, "the reconnect loop ignored cancellation")
	}
	assert.Zero(t, deregisters.Load(), "an unknown token must never clear the worker's state")
}

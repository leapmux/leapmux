package service_test

import (
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

func TestRecoveryRefusesAnOldTokenHelloAfterTheRecoveredStreamPublishes(t *testing.T) {
	env := setupRegKeyEnv(t)
	fixture := newRecoveryFixture(t, env)
	ctx := testutil.DeadlineContext(t)
	old := dialWorker(t, ctx, env.server, fixture.original.GetAuthToken())
	replacement := recoverThroughChallenge(t, ctx, env, fixture.key, fixture.attempt)
	current := dialWorker(t, ctx, env.server, replacement.GetAuthToken())
	hello := &leapmuxv1.WorkerHello{Keys: fixture.attempt.keys, EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM}
	require.NoError(t, current.Send(helloRequest(hello)))
	greeting, err := current.Receive()
	require.NoError(t, err)
	require.NotNil(t, greeting.GetWorkerIdentity())
	published := env.wMgr.ConnForTrustedPath(replacement.GetWorkerId())
	require.NotNil(t, published)
	require.NoError(t, old.Send(helloRequest(hello)))
	_, err = old.Receive()
	require.Error(t, err, "an old-token hello must not receive a new greeting after rotation")
	requireWorkerClose(t, err, websocket.StatusPolicyViolation, "worker_credential_replaced")
	assert.Same(t, published, env.wMgr.ConnForTrustedPath(replacement.GetWorkerId()))
	select {
	case <-published.Done():
		t.Fatal("the old pending hello displaced the recovered stream")
	default:
	}
}

func TestRecoveryEndsAnAlreadyPublishedStream(t *testing.T) {
	env := setupRegKeyEnv(t)
	fixture := newRecoveryFixture(t, env)
	ctx := testutil.DeadlineContext(t)
	peer := dialWorker(t, ctx, env.server, fixture.original.GetAuthToken())
	hello := &leapmuxv1.WorkerHello{Keys: fixture.attempt.keys, EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM}
	require.NoError(t, peer.Send(helloRequest(hello)))
	greeting, err := peer.Receive()
	require.NoError(t, err)
	require.NotNil(t, greeting.GetWorkerIdentity())
	established := env.wMgr.ConnForTrustedPath(fixture.original.GetWorkerId())
	require.NotNil(t, established)
	recovered := recoverThroughChallenge(t, ctx, env, fixture.key, fixture.attempt)
	require.Equal(t, fixture.original.GetWorkerId(), recovered.GetWorkerId())
	select {
	case <-established.Done():
	default:
		t.Fatal("successful recovery must end the replaced credential's established stream")
	}
}

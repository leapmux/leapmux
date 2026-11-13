package service_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// TestRecoveryRejectsChangedInputs changes ONE input of an otherwise
// valid recovery -- the attempt identity, the registration key, the
// pinned key set, or the signature -- and requires a refusal with no
// credentials, no token replacement, and no extra row.
func TestRecoveryRejectsChangedInputs(t *testing.T) {
	env := setupRegKeyEnv(t)
	ctx := context.Background()
	admin := userid.MustNew(env.adminID(t))
	key := id.Generate()
	require.NoError(t, env.store.RegistrationKeys().Create(ctx, store.CreateRegistrationKeyParams{KeyHash: hubtestutil.TestSecretHasher().Hash(key),
		ID: id.Generate(), CreatedBy: admin, ExpiresAt: time.Now().Add(time.Hour),
	}))
	attempt := newRegisterAttempt(t)
	_, err := env.registerCall(t, ctx, key, attempt, true)
	require.Error(t, err, "the first response is lost after commit")
	worker, rowErr := env.store.Workers().GetByRegistrationAttemptID(ctx, attempt.attemptID)
	require.NoError(t, rowErr)

	issue := func(t *testing.T, attemptID, bearerKey string) *leapmuxv1.GetRegistrationChallengeResponse {
		t.Helper()
		requestCtx, info := connect.NewClientContext(ctx)
		info.RequestHeader().Set("Authorization", "Bearer "+bearerKey)
		challenge, err := env.connectorClient.GetRegistrationChallenge(requestCtx,
			&leapmuxv1.GetRegistrationChallengeRequest{AttemptId: attemptID})
		require.NoError(t, err)
		return challenge
	}
	recoverWith := func(t *testing.T, challenge *leapmuxv1.GetRegistrationChallengeResponse, signer registerAttempt, attemptID, bearerKey string) (*leapmuxv1.RegisterResponse, error) {
		t.Helper()
		message := recoveryMessageBytes(attemptID, challenge.GetChallengeId(), challenge.GetNonce(), bearerKey)
		signature, signErr := signer.composite.SlhdsaPrivateKey.Sign(rand.Reader, message, nil)
		require.NoError(t, signErr)
		requestCtx, info := connect.NewClientContext(ctx)
		info.RequestHeader().Set("Authorization", "Bearer "+bearerKey)
		return env.connectorClient.RecoverRegistration(requestCtx, &leapmuxv1.RecoverRegistrationRequest{
			AttemptId:   attemptID,
			ChallengeId: challenge.GetChallengeId(),
			Signature:   signature,
		})
	}

	t.Run("a changed attempt identity is unknown", func(t *testing.T) {
		foreign := newRegisterAttempt(t)
		requestCtx, info := connect.NewClientContext(ctx)
		info.RequestHeader().Set("Authorization", "Bearer "+key)
		_, err := env.connectorClient.GetRegistrationChallenge(requestCtx,
			&leapmuxv1.GetRegistrationChallengeRequest{AttemptId: foreign.attemptID})
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})

	t.Run("a changed registration key is refused", func(t *testing.T) {
		requestCtx, info := connect.NewClientContext(ctx)
		info.RequestHeader().Set("Authorization", "Bearer another-registration-key")
		_, err := env.connectorClient.GetRegistrationChallenge(requestCtx,
			&leapmuxv1.GetRegistrationChallengeRequest{AttemptId: attempt.attemptID})
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err),
			"a consumed key alone recovers nothing without its digest match")
	})

	t.Run("a signature from other keys is refused", func(t *testing.T) {
		challenge := issue(t, attempt.attemptID, key)
		otherKeys := newRegisterAttempt(t)
		_, err := recoverWith(t, challenge, otherKeys, attempt.attemptID, key)
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})
	t.Run("a corrupted signature from the pinned key is refused", func(t *testing.T) {
		challenge := issue(t, attempt.attemptID, key)
		message := recoveryMessageBytes(attempt.attemptID, challenge.GetChallengeId(), challenge.GetNonce(), key)
		signature, err := attempt.composite.SlhdsaPrivateKey.Sign(rand.Reader, message, nil)
		require.NoError(t, err)
		require.NotEmpty(t, signature)
		signature[0] ^= 0xff
		requestCtx, info := connect.NewClientContext(ctx)
		info.RequestHeader().Set("Authorization", "Bearer "+key)
		response, err := env.connectorClient.RecoverRegistration(requestCtx, &leapmuxv1.RecoverRegistrationRequest{
			AttemptId: attempt.attemptID, ChallengeId: challenge.GetChallengeId(), Signature: signature,
		})
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
		assert.Nil(t, response, "a rejected proof must not return credentials")
	})

	var recoveredToken string
	t.Run("the honest recovery still works after the refusals", func(t *testing.T) {
		_, err := env.registerCall(t, ctx, key, attempt, false)
		require.Error(t, err)
		assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))

		challenge := issue(t, attempt.attemptID, key)
		resp, err := recoverWith(t, challenge, attempt, attempt.attemptID, key)
		require.NoError(t, err)
		assert.Equal(t, worker.ID, resp.GetWorkerId())
		recoveredToken = resp.GetAuthToken()
		hubtestutil.RequireWorkerToken(t, env.store, worker.ID, recoveredToken)
	})
	t.Run("a replaced challenge requests a fresh proof", func(t *testing.T) {
		stale := issue(t, attempt.attemptID, key)
		_ = issue(t, attempt.attemptID, key)
		response, err := recoverWith(t, stale, attempt, attempt.attemptID, key)
		require.Error(t, err)
		assert.Equal(t, connect.CodeAborted, connect.CodeOf(err))
		assert.Nil(t, response)
	})

	// The later refusal preserves the recovered token and the existing Worker.
	fresh, err := env.store.Workers().GetByRegistrationAttemptID(ctx, attempt.attemptID)
	require.NoError(t, err)
	hubtestutil.RequireWorkerToken(t, env.store, fresh.ID, recoveredToken)
	page, listErr := env.store.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
		RegisteredBy: admin, PageParams: store.PageParams{Limit: 10},
	})
	require.NoError(t, listErr)
	require.Len(t, page.Rows, 1, "no refusal path minted an extra row")
}

type recoveryFixture struct {
	key      string
	attempt  registerAttempt
	original *leapmuxv1.RegisterResponse
}

func newRecoveryFixture(t *testing.T, env *regKeyEnv) recoveryFixture {
	t.Helper()
	token := env.login(t, hubtestutil.TestAdminUsername, hubtestutil.TestAdminPassword)
	key, err := env.mgmtClient.CreateRegistrationKey(authedReq(t.Context(), &leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	attempt := newRegisterAttempt(t)
	registered, err := env.registerCall(t, t.Context(), key.GetRegistrationKey(), attempt, false)
	require.NoError(t, err)
	hubtestutil.RequireWorkerToken(t, env.store, registered.GetWorkerId(), registered.GetAuthToken())
	return recoveryFixture{key: key.GetRegistrationKey(), attempt: attempt, original: registered}
}

func TestRecoveryRotatesTheTokenOnEverySuccessfulProof(t *testing.T) {
	env := setupRegKeyEnv(t)
	fixture := newRecoveryFixture(t, env)
	first := recoverThroughChallenge(t, t.Context(), env, fixture.key, fixture.attempt)
	assert.Equal(t, fixture.original.GetWorkerId(), first.GetWorkerId())
	assert.NotEqual(t, fixture.original.GetAuthToken(), first.GetAuthToken())
	hubtestutil.RequireWorkerToken(t, env.store, first.GetWorkerId(), first.GetAuthToken())
	_, err := auth.AuthenticateWorkerBearer(t.Context(), env.store, hubtestutil.TestSecretHasher(), "Bearer "+fixture.original.GetAuthToken())
	require.ErrorIs(t, err, auth.ErrInvalidWorkerToken)
	second := recoverThroughChallenge(t, t.Context(), env, fixture.key, fixture.attempt)
	assert.Equal(t, first.GetWorkerId(), second.GetWorkerId())
	assert.NotEqual(t, first.GetAuthToken(), second.GetAuthToken())
	hubtestutil.RequireWorkerToken(t, env.store, second.GetWorkerId(), second.GetAuthToken())
	_, err = auth.AuthenticateWorkerBearer(t.Context(), env.store, hubtestutil.TestSecretHasher(), "Bearer "+first.GetAuthToken())
	require.ErrorIs(t, err, auth.ErrInvalidWorkerToken)
}

type deregisterAfterRecoveryRead struct {
	store.Store
	once *sync.Once
}
type deregisterAfterRecoveryReadWorkers struct {
	store.WorkerStore
	base store.Store
	once *sync.Once
}

func (s deregisterAfterRecoveryRead) Workers() store.WorkerStore {
	return deregisterAfterRecoveryReadWorkers{WorkerStore: s.Store.Workers(), base: s.Store, once: s.once}
}

func (s deregisterAfterRecoveryReadWorkers) GetByIDIncludeDeleted(ctx context.Context, id string) (*store.Worker, error) {
	row, err := s.WorkerStore.GetByIDIncludeDeleted(ctx, id)
	if err != nil {
		return nil, err
	}
	var writeErr error
	s.once.Do(func() {
		rows, err := s.base.Workers().ForceDeregister(ctx, id)
		if err != nil {
			writeErr = err
		} else if rows != 1 {
			writeErr = fmt.Errorf("expected to deregister one fixture Worker, got %d", rows)
		}
	})
	if writeErr != nil {
		return nil, writeErr
	}
	return row, nil
}

func TestRecoveryRefusesADeregistrationAfterItsLivenessRead(t *testing.T) {
	env := newRegKeyEnv(t, testConfig(), nil, regKeyEnvOptions{connectorStore: func(st store.Store) store.Store {
		return deregisterAfterRecoveryRead{Store: st, once: &sync.Once{}}
	}})
	fixture := newRecoveryFixture(t, env)
	challengeCtx, challengeInfo := connect.NewClientContext(t.Context())
	challengeInfo.RequestHeader().Set("Authorization", "Bearer "+fixture.key)
	challenge, err := env.connectorClient.GetRegistrationChallenge(challengeCtx, &leapmuxv1.GetRegistrationChallengeRequest{AttemptId: fixture.attempt.attemptID})
	require.NoError(t, err)
	message := recoveryMessageBytes(fixture.attempt.attemptID, challenge.GetChallengeId(), challenge.GetNonce(), fixture.key)
	signature, err := fixture.attempt.composite.SlhdsaPrivateKey.Sign(rand.Reader, message, nil)
	require.NoError(t, err)
	recoveryCtx, info := connect.NewClientContext(t.Context())
	info.RequestHeader().Set("Authorization", "Bearer "+fixture.key)
	recovered, err := env.connectorClient.RecoverRegistration(recoveryCtx, &leapmuxv1.RecoverRegistrationRequest{AttemptId: fixture.attempt.attemptID, ChallengeId: challenge.GetChallengeId(), Signature: signature})
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "this worker can no longer be recovered")
	assert.Nil(t, recovered)
	unchanged, err := env.store.Workers().GetByAuthTokenHashIncludeDeleted(t.Context(), hubtestutil.TestSecretHasher().Hash(fixture.original.GetAuthToken()))
	require.NoError(t, err)
	assert.Equal(t, fixture.original.GetWorkerId(), unchanged.ID)
	assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING, unchanged.Status)
}

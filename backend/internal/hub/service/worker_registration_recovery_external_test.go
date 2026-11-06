package service_test

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
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
	require.NoError(t, env.store.RegistrationKeys().Create(ctx, store.CreateRegistrationKeyParams{
		ID: key, CreatedBy: admin, ExpiresAt: time.Now().Add(time.Hour),
	}))
	attempt := newRegisterAttempt(t, 0x51)
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
		foreign := newRegisterAttempt(t, 0x52)
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
		otherKeys := newRegisterAttempt(t, 0x53)
		_, err := recoverWith(t, challenge, otherKeys, attempt.attemptID, key)
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})

	t.Run("the honest recovery still works after the refusals", func(t *testing.T) {
		_, err := env.registerCall(t, ctx, key, attempt, false)
		require.Error(t, err)
		assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))

		challenge := issue(t, attempt.attemptID, key)
		resp, err := recoverWith(t, challenge, attempt, attempt.attemptID, key)
		require.NoError(t, err)
		assert.Equal(t, worker.ID, resp.GetWorkerId())
		assert.Equal(t, worker.AuthToken, resp.GetAuthToken())
	})

	// No refusal above replaced the token or minted a row.
	fresh, err := env.store.Workers().GetByRegistrationAttemptID(ctx, attempt.attemptID)
	require.NoError(t, err)
	assert.Equal(t, worker.AuthToken, fresh.AuthToken, "the original token was never replaced")
	page, listErr := env.store.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
		RegisteredBy: admin, PageParams: store.PageParams{Limit: 10},
	})
	require.NoError(t, listErr)
	require.Len(t, page.Rows, 1, "no refusal path minted an extra row")
}

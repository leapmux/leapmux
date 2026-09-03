package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// writeBeforeUserAuthLock runs one write at the instant the user-auth
// transaction opens, which is the window a concurrent request really commits
// in: DeletePasskey reads the passkey count OUTSIDE the lock to decide
// whether it must set a replacement password, and reads it again INSIDE.
//
// A store wrapper rather than two goroutines. An Argon2 hash and a lock
// acquisition separate the two reads, so a racing goroutine would land in
// that window only sometimes, and a test that passes sometimes proves
// nothing.
type writeBeforeUserAuthLock struct {
	store.Store
	once  sync.Once
	write func()
}

func (s *writeBeforeUserAuthLock) RunInUserAuthTransaction(ctx context.Context, userID userid.UserID, fn func(tx store.Store) error) error {
	s.once.Do(s.write)
	return s.Store.RunInUserAuthTransaction(ctx, userID, fn)
}

func seedRacePasskey(t *testing.T, st store.Store, userID, friendlyName string) string {
	t.Helper()
	pkID := id.Generate()
	require.NoError(t, st.PasskeyCredentials().Create(context.Background(), store.CreatePasskeyCredentialParams{
		ID:           pkID,
		UserID:       userID,
		CredentialID: []byte("cred-" + pkID),
		PublicKey:    []byte("pubkey"),
		Transports:   "[]",
		FriendlyName: friendlyName,
		KeyVersion:   1,
		CreatedAt:    time.Now().UTC(),
	}))
	return pkID
}

// TestDeletePasskey_RefusesWhenTheLockedCountNoLongerNeedsAPassword pins the
// half of the count guard that used to be missing.
//
// commitPasskeyDeactivation catches a FALLING count: it refuses an empty
// hash. Nothing caught a RISING one. prepare hashed the replacement password
// for the last-passkey branch, a registration committed before the lock, the
// locked count then read two, and the plain-delete branch ran -- which has
// nowhere to put the hash. The delete committed, the RPC answered success,
// and the account stayed passwordless while the user believed they set a
// password.
func TestDeletePasskey_RefusesWhenTheLockedCountNoLongerNeedsAPassword(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := newStepUpTestStore(t)
	user := stepUpUser(t, st, false)
	require.False(t, user.FirstCredentialExempt, "precondition: the account holds no password")

	doomed := seedRacePasskey(t, st, user.ID, "Only One")
	sessionID, _, err := auth.CreateSession(ctx, st, userid.MustNew(user.ID), auth.DefaultSessionDuration)
	require.NoError(t, err)
	now := time.Now().UTC()
	n, err := st.Sessions().Elevate(ctx, store.ElevateSessionParams{
		SessionID:          sessionID,
		UserID:             userid.MustNew(user.ID),
		ElevationProvenAt:  now,
		ElevationExpiresAt: now.Add(auth.ElevationWindow),
	}, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	racing := &writeBeforeUserAuthLock{Store: st, write: func() {
		seedRacePasskey(t, st, user.ID, "Registered while this request queued")
	}}
	svc := &UserService{
		store:     racing,
		cfg:       &config.Config{},
		lifecycle: auth.NewCredentialLifecycleEffects(nil, nil, nil),
	}
	acting := auth.WithUser(ctx, &auth.UserInfo{
		ID:                 userid.MustNew(user.ID),
		Credential:         auth.SessionCredential(sessionID),
		AuthenticatedAt:    now,
		Elevation:          auth.NewElevation(&now, ptrTime(now.Add(auth.ElevationWindow))),
		UserAuthGeneration: user.AuthGeneration,
	})

	_, err = svc.DeletePasskey(acting, connect.NewRequest(&leapmuxv1.DeletePasskeyRequest{
		Id:          doomed,
		NewPassword: "newpass123",
	}))
	require.Error(t, err, "prepare hashed for one branch, and a different branch now runs")
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "please retry")

	// NOTHING committed. The retry recomputes the branch against the settled
	// state, where two passkeys make the plain delete correct and the account
	// needs no replacement password at all.
	after, err := st.Users().GetByID(ctx, user.ID)
	require.NoError(t, err)
	assert.False(t, after.FirstCredentialExempt, "the refused request must not leave a half-applied state")
	_, err = st.PasskeyCredentials().GetByID(ctx, doomed)
	assert.NoError(t, err, "the passkey must survive a refused delete")
}

func ptrTime(t time.Time) *time.Time { return &t }

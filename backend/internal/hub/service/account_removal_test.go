package service_test

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openAccountRemovalTestStore(t *testing.T) (store.Store, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hub.db")
	st, err := sqlite.Open(path, sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	db, err := sqlite.OpenDB(path, sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return st, func() {
		var count int
		err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'account_lifecycle_lock'").Scan(&count)
		require.NoError(t, err)
		require.Zero(t, count)
	}
}

func TestOrdinaryAccountDeletionDoesNotAccessTheLifecycleTable(t *testing.T) {
	st, assertNoLifecycleTable := openAccountRemovalTestStore(t)
	env := setupAdminUserTestUnelevatedWithStore(t, st)
	env.elevateAdminSession(t)
	created, err := env.client.CreateUser(authedReq(t.Context(), &leapmuxv1.CreateUserRequest{
		Username: "ordinary-removal", Password: "ordinary-password",
	}, env.token))
	require.NoError(t, err)
	userID := created.GetUser().GetId()
	worker := storetest.SeedWorker(t, st, userID)
	assertNoLifecycleTable()
	_, err = env.client.DeleteUser(authedReq(t.Context(), &leapmuxv1.DeleteUserRequest{
		User: service.SelectUserByID(userID),
	}, env.token))
	require.NoError(t, err)
	_, err = st.Users().GetByID(t.Context(), userID)
	require.ErrorIs(t, err, store.ErrNotFound)
	storedWorker, err := st.Workers().GetByIDIncludeDeleted(t.Context(), worker.ID)
	require.NoError(t, err)
	assert.NotNil(t, storedWorker.DeletedAt)
	assert.Len(t, env.revocations.recorded(), 1)
	admin, err := st.Users().GetFirstAdmin(t.Context())
	require.NoError(t, err)
	assert.Equal(t, env.adminID, admin.ID)
}

func TestOrdinarySignupRollbackDoesNotAccessTheLifecycleTable(t *testing.T) {
	for _, factor := range []string{"password", "passkey"} {
		t.Run(factor, func(t *testing.T) {
			st, assertNoLifecycleTable := openAccountRemovalTestStore(t)
			createRetainedAdministrator(t, st)
			sender := &firstSignupDeliveryFailure{entered: make(chan struct{}), release: make(chan struct{})}
			env := setupEmptyPasskeyAuthTestServerWithStore(t, st, func(t *testing.T, set *settings.Manager) {
				enableSignup(t, set)
				enableEmailVerification(t, set)
			}, sender)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(sender.release) }) }
			t.Cleanup(release)
			create := func() error {
				_, err := env.client.SignUp(ctx, &leapmuxv1.SignUpRequest{
					Username: "ordinary-rollback", Email: "ordinary@example.com", Password: "ordinary-password",
				})
				return err
			}
			if factor == "passkey" {
				begin := beginPasskeySignUp(t, env.client, "ordinary-rollback", "ordinary@example.com")
				ceremony := newPasskeyCeremony()
				credential, err := ceremony.registrationResponse(begin.GetChallenge().GetOptionsJson())
				require.NoError(t, err)
				create = func() error {
					_, err := env.client.FinishPasskeySignUp(passkeyCtx(ctx), &leapmuxv1.FinishPasskeySignUpRequest{
						Credential: &leapmuxv1.WebAuthnCredential{SessionId: begin.GetChallenge().GetSessionId(), CredentialJson: credential},
					})
					return err
				}
			}
			assertNoLifecycleTable()
			result := make(chan error, 1)
			go func() { result <- create() }()
			select {
			case <-sender.entered:
			case err := <-result:
				t.Fatalf("signup ended before verification delivery: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			user, err := st.Users().GetByUsername(ctx, "ordinary-rollback")
			require.NoError(t, err)
			release()
			select {
			case err := <-result:
				require.Error(t, err)
				assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			_, err = st.Users().GetByID(ctx, user.ID)
			require.ErrorIs(t, err, store.ErrNotFound, "verification failure must remove the ordinary account without the singleton")
			credentials, err := st.PasskeyCredentials().ListByUser(ctx, user.ID)
			require.NoError(t, err)
			assert.Empty(t, credentials)
		})
	}
}

type promoteBeforeRemovalStore struct {
	store.Store
	armed  atomic.Bool
	userID string
}

func (s *promoteBeforeRemovalStore) RunInTransaction(ctx context.Context, fn func(store.Store) error) error {
	if err := s.promote(ctx); err != nil {
		return err
	}
	return s.Store.RunInTransaction(ctx, fn)
}

func (s *promoteBeforeRemovalStore) RunInUserAuthTransaction(ctx context.Context, userID userid.UserID, fn func(store.Store) error) error {
	if err := s.promote(ctx); err != nil {
		return err
	}
	return s.Store.RunInUserAuthTransaction(ctx, userID, fn)
}

func (s *promoteBeforeRemovalStore) promote(ctx context.Context) error {
	if s.armed.CompareAndSwap(true, false) {
		return s.Store.Users().UpdateAdmin(ctx, store.UpdateUserAdminParams{ID: s.userID, IsAdmin: true})
	}
	return nil
}

func TestAccountDeletionRechecksAdministratorConfirmationAfterPromotion(t *testing.T) {
	st, _ := openAccountRemovalTestStore(t)
	promoted := &promoteBeforeRemovalStore{Store: st}
	env := setupAdminUserTestUnelevatedWithStore(t, promoted)
	env.elevateAdminSession(t)
	user := storetest.SeedUser(t, st, "promoted-before-removal")
	promoted.userID = user.ID
	promoted.armed.Store(true)
	_, err := env.client.DeleteUser(authedReq(t.Context(), &leapmuxv1.DeleteUserRequest{
		User: service.SelectUserByID(user.ID),
	}, env.token))
	require.Error(t, err, "a promoted target requires administrator deletion confirmation")
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	stored, err := st.Users().GetByID(t.Context(), user.ID)
	require.NoError(t, err)
	assert.True(t, stored.IsAdmin)
	assert.Empty(t, env.revocations.recorded())
	_, err = env.client.DeleteUser(authedReq(t.Context(), &leapmuxv1.DeleteUserRequest{
		User: service.SelectUserByID(user.ID), Force: true,
	}, env.token))
	require.NoError(t, err, "a confirmed administrator deletion remains allowed when another administrator exists")
	_, err = st.Users().GetByID(t.Context(), user.ID)
	require.ErrorIs(t, err, store.ErrNotFound)
	assert.Len(t, env.revocations.recorded(), 1)
}

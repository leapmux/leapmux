package service_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type observedAdmissionStore struct {
	store.Store
	reads atomic.Int64
}

type failAccountTransactionOnce struct {
	store.Store
	fail       atomic.Bool
	afterWrite bool
}

func (s *failAccountTransactionOnce) RunInTransaction(ctx context.Context, fn func(store.Store) error) error {
	if s.fail.CompareAndSwap(true, false) {
		if s.afterWrite {
			return s.Store.RunInTransaction(ctx, func(tx store.Store) error {
				if err := fn(tx); err != nil {
					return err
				}
				return errors.New("controlled failure after account writes")
			})
		}
		return errors.New("controlled account transaction failure")
	}
	return s.Store.RunInTransaction(ctx, fn)
}

func TestFailedSetupReleasesClaimWithoutPublishingPresence(t *testing.T) {
	for _, afterWrite := range []bool{false, true} {
		for _, factor := range []string{"password", "passkey"} {
			stage := "before writes"
			if afterWrite {
				stage = "after writes"
			}
			t.Run(factor+"/"+stage, func(t *testing.T) {
				base := hubtestutil.OpenTestStore(t)
				fault := &failAccountTransactionOnce{Store: base, afterWrite: afterWrite}
				env := setupEmptyPasskeyAuthTestServerWithStore(t, fault, nil, nil)
				create := func() (*leapmuxv1.SessionReport, error) {
					if factor == "password" {
						response, err := env.client.SignUp(t.Context(), &leapmuxv1.SignUpRequest{Username: "setup-retry", Password: "setup-retry-password"})
						return response.GetSessionReport(), err
					}
					begin := beginPasskeySignUp(t, env.client, "setup-retry", "")
					ceremony := newPasskeyCeremony()
					credential, err := ceremony.registrationResponse(begin.GetChallenge().GetOptionsJson())
					require.NoError(t, err)
					response, err := finishPasskeySignUp(t, env.client, begin.GetChallenge().GetSessionId(), credential)
					return response.GetSessionReport(), err
				}
				fault.fail.Store(true)
				_, err := create()
				require.Error(t, err)
				assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
				count, err := base.Users().Count(t.Context())
				require.NoError(t, err)
				assert.Zero(t, count)
				info, err := env.client.GetSystemInfo(passkeyCtx(t.Context()), &leapmuxv1.GetSystemInfoRequest{})
				require.NoError(t, err)
				assert.True(t, info.GetSetupRequired())
				report, err := create()
				require.NoError(t, err, "a failed transaction must release the setup claim")
				require.NotNil(t, report.GetUser())
				assert.True(t, report.GetUser().GetIsAdmin())
				count, err = base.Users().Count(t.Context())
				require.NoError(t, err)
				assert.EqualValues(t, 1, count)
			})
		}
	}
}

func TestOrdinaryAccountAdmissionAllowsConcurrentTransactions(t *testing.T) {
	base := hubtestutil.OpenTestStore(t)
	createRetainedAdministrator(t, base)
	observed := &observedAdmissionStore{Store: base}
	barrier := &accountCreationBarrier{Store: observed, holdCount: 2, arrived: make(chan struct{}, 2), release: make(chan struct{})}
	env := setupEmptyPasskeyAuthTestServerWithStore(t, barrier, enableSignup, nil)
	_, err := env.client.GetSystemInfo(passkeyCtx(t.Context()), &leapmuxv1.GetSystemInfoRequest{})
	require.NoError(t, err)
	observed.reads.Store(0)
	barrier.armed.Store(true)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	results := make(chan error, 2)
	released := false
	t.Cleanup(func() {
		if !released {
			close(barrier.release)
		}
	})
	for _, username := range []string{"concurrent-one", "concurrent-two"} {
		go func() {
			_, err := env.client.SignUp(ctx, &leapmuxv1.SignUpRequest{Username: username, Password: "ordinary-password"})
			results <- err
		}()
	}
	for range 2 {
		select {
		case <-barrier.arrived:
		case <-ctx.Done():
			t.Fatal("ordinary account admission serialized the transactions:", ctx.Err())
		}
	}
	close(barrier.release)
	released = true
	for range 2 {
		select {
		case err := <-results:
			require.NoError(t, err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	for _, username := range []string{"concurrent-one", "concurrent-two"} {
		user, err := base.Users().GetByUsername(t.Context(), username)
		require.NoError(t, err)
		assert.False(t, user.IsAdmin)
	}
	assert.Zero(t, observed.reads.Load())
}

func (s *observedAdmissionStore) Users() store.UserStore {
	return observedAdmissionUsers{UserStore: s.Store.Users(), owner: s}
}

func (s *observedAdmissionStore) RunInTransaction(ctx context.Context, fn func(store.Store) error) error {
	return s.Store.RunInTransaction(ctx, func(tx store.Store) error {
		return fn(&observedAdmissionTransaction{Store: tx, owner: s})
	})
}

type observedAdmissionTransaction struct {
	store.Store
	owner *observedAdmissionStore
}

func (s *observedAdmissionTransaction) Users() store.UserStore {
	return observedAdmissionUsers{UserStore: s.Store.Users(), owner: s.owner}
}

type observedAdmissionUsers struct {
	store.UserStore
	owner *observedAdmissionStore
}

func (s observedAdmissionUsers) HasAny(ctx context.Context) (bool, error) {
	s.owner.reads.Add(1)
	return s.UserStore.HasAny(ctx)
}

func TestRuntimeAccountAdmissionDoesNotReadPresenceForOrdinarySignup(t *testing.T) {
	base, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = base.Close() })
	require.NoError(t, base.Migrator().Migrate(t.Context()))
	_, err = service.CreateUser(t.Context(), base, service.CreateUserParams{
		Username: "administrator", PasswordHash: "unused", IsAdmin: true,
	})
	require.NoError(t, err)
	observed := &observedAdmissionStore{Store: base}
	env := setupEmptyPasskeyAuthTestServerWithStore(t, observed, enableSignup, nil)
	info, err := env.client.GetSystemInfo(passkeyCtx(t.Context()), &leapmuxv1.GetSystemInfoRequest{})
	require.NoError(t, err)
	require.False(t, info.GetSetupRequired())
	require.Positive(t, observed.reads.Load(), "the runtime initially reads authoritative presence")
	observed.reads.Store(0)
	_, err = env.client.SignUp(t.Context(), &leapmuxv1.SignUpRequest{
		Username: "ordinary-runtime-user", Password: "ordinary-password",
	})
	require.NoError(t, err)
	created, err := base.Users().GetByUsername(t.Context(), "ordinary-runtime-user")
	require.NoError(t, err)
	assert.False(t, created.IsAdmin)
	info, err = env.client.GetSystemInfo(passkeyCtx(t.Context()), &leapmuxv1.GetSystemInfoRequest{})
	require.NoError(t, err)
	assert.False(t, info.GetSetupRequired())
	assert.Zero(t, observed.reads.Load(), "populated runtime presence must not query the database again")
}

func TestRuntimeAccountAdmissionDoesNotReadPresenceForPasskeySignup(t *testing.T) {
	base := hubtestutil.OpenTestStore(t)
	createRetainedAdministrator(t, base)
	observed := &observedAdmissionStore{Store: base}
	env := setupEmptyPasskeyAuthTestServerWithStore(t, observed, enableSignup, nil)
	info, err := env.client.GetSystemInfo(passkeyCtx(t.Context()), &leapmuxv1.GetSystemInfoRequest{})
	require.NoError(t, err)
	require.False(t, info.GetSetupRequired())
	observed.reads.Store(0)
	begin := beginPasskeySignUp(t, env.client, "ordinary-passkey", "")
	ceremony := newPasskeyCeremony()
	credential, err := ceremony.registrationResponse(begin.GetChallenge().GetOptionsJson())
	require.NoError(t, err)
	response, err := finishPasskeySignUp(t, env.client, begin.GetChallenge().GetSessionId(), credential)
	require.NoError(t, err)
	assert.False(t, response.GetSessionReport().GetUser().GetIsAdmin())
	assert.EqualValues(t, 1, response.GetSessionReport().GetUser().GetPasskeyCount())
	assert.Zero(t, observed.reads.Load())
}

func TestEmptyRuntimeAccountPresenceIsInitializedOnce(t *testing.T) {
	base := hubtestutil.OpenTestStore(t)
	observed := &observedAdmissionStore{Store: base}
	env := setupEmptyPasskeyAuthTestServerWithStore(t, observed, nil, nil)
	info, err := env.client.GetSystemInfo(passkeyCtx(t.Context()), &leapmuxv1.GetSystemInfoRequest{})
	require.NoError(t, err)
	require.True(t, info.GetSetupRequired())
	observed.reads.Store(0)
	for range 2 {
		info, err = env.client.GetSystemInfo(passkeyCtx(t.Context()), &leapmuxv1.GetSystemInfoRequest{})
		require.NoError(t, err)
		assert.True(t, info.GetSetupRequired())
	}
	created, err := env.client.SignUp(t.Context(), &leapmuxv1.SignUpRequest{
		Username: "initial-admin", Password: "initial-admin-password",
	})
	require.NoError(t, err)
	assert.True(t, created.GetSessionReport().GetUser().GetIsAdmin())
	info, err = env.client.GetSystemInfo(passkeyCtx(t.Context()), &leapmuxv1.GetSystemInfoRequest{})
	require.NoError(t, err)
	assert.False(t, info.GetSetupRequired())
	assert.Zero(t, observed.reads.Load(), "empty presence must also remain in memory after initialization")
}

type uncertainSetupCommit struct{}

func (uncertainSetupCommit) Error() string              { return "lost setup commit acknowledgement" }
func (uncertainSetupCommit) CommitOutcomeUnknown() bool { return true }

type uncertainSetupStore struct {
	store.Store
	armed    atomic.Bool
	username string
}

func (s *uncertainSetupStore) RunInTransaction(ctx context.Context, fn func(store.Store) error) error {
	if err := s.Store.RunInTransaction(ctx, fn); err != nil {
		return err
	}
	if s.armed.Load() {
		if _, err := s.Store.Users().GetByUsername(ctx, s.username); err == nil && s.armed.CompareAndSwap(true, false) {
			return uncertainSetupCommit{}
		}
	}
	return nil
}

func TestUncertainSetupCommitCannotAdmitAnotherAdministrator(t *testing.T) {
	base := hubtestutil.OpenTestStore(t)
	uncertain := &uncertainSetupStore{Store: base, username: "uncertain-first"}
	env := setupEmptyPasskeyAuthTestServerWithStore(t, uncertain, nil, nil)
	uncertain.armed.Store(true)
	_, err := env.client.SignUp(t.Context(), &leapmuxv1.SignUpRequest{
		Username: "uncertain-first", Password: "first-admin-password",
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	user, err := base.Users().GetByUsername(t.Context(), "uncertain-first")
	require.NoError(t, err, "the database committed even though its acknowledgement was lost")
	assert.True(t, user.IsAdmin)
	_, err = env.client.SignUp(t.Context(), &leapmuxv1.SignUpRequest{
		Username: "uncertain-second", Password: "second-admin-password",
	})
	assert.Error(t, err, "an uncertain commit must not leave initial setup open")
	if err != nil {
		assert.Contains(t, err.Error(), "restart")
	}
	count, err := base.Users().Count(t.Context())
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)
}

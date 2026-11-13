package service_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/service"

	"connectrpc.com/connect/v2"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Both handlers finish their preliminary policy reads before either creation
// transaction begins. The real database still serializes every transaction.
type accountCreationBarrier struct {
	store.Store
	armed     atomic.Bool
	calls     atomic.Int32
	holdCount int32
	arrived   chan struct{}
	release   chan struct{}
}

func (s *accountCreationBarrier) RunInTransaction(ctx context.Context, fn func(store.Store) error) error {
	if s.armed.Load() && s.calls.Add(1) <= s.holdCount {
		s.arrived <- struct{}{}
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.Store.RunInTransaction(ctx, fn)
}

func TestSetupCreationSerializesPasswordAndPasskeyAdmission(t *testing.T) {
	for _, factors := range [][2]string{{"password", "password"}, {"passkey", "passkey"}, {"password", "passkey"}} {
		t.Run(factors[0]+" and "+factors[1], func(t *testing.T) {
			base := hubtestutil.OpenTestStore(t)
			barrier := &accountCreationBarrier{Store: base, holdCount: 2, arrived: make(chan struct{}, 2), release: make(chan struct{})}
			env := setupEmptyPasskeyAuthTestServerWithStore(t, barrier, func(t *testing.T, set *settings.Manager) {
				require.NoError(t, settings.KeySignupEnabled.Set(t.Context(), set, false))
			}, nil)
			ctx := testutil.DeadlineContext(t)
			type outcome struct {
				userID string
				admin  bool
				err    error
			}
			var calls [2]func() outcome
			usernames := [2]string{"setupfirst", "setupsecond"}
			for index, factor := range factors {
				if factor == "password" {
					calls[index] = func() outcome {
						response, err := env.client.SignUp(ctx, &leapmuxv1.SignUpRequest{Username: usernames[index], Password: "correct-horse-battery-staple"})
						return outcome{response.GetSessionReport().GetUser().GetId(), response.GetSessionReport().GetUser().GetIsAdmin(), err}
					}
				} else {
					begin := beginPasskeySignUp(t, env.client, usernames[index], "")
					ceremony := newPasskeyCeremony()
					credential, err := ceremony.registrationResponse(begin.GetChallenge().GetOptionsJson())
					require.NoError(t, err)
					calls[index] = func() outcome {
						response, err := env.client.FinishPasskeySignUp(passkeyCtx(ctx), &leapmuxv1.FinishPasskeySignUpRequest{Credential: &leapmuxv1.WebAuthnCredential{SessionId: begin.GetChallenge().GetSessionId(), CredentialJson: credential}})
						return outcome{response.GetSessionReport().GetUser().GetId(), response.GetSessionReport().GetUser().GetIsAdmin(), err}
					}
				}
			}
			barrier.armed.Store(true)
			results := make(chan outcome, 2)
			released := false
			t.Cleanup(func() {
				if !released {
					close(barrier.release)
				}
			})
			for _, call := range calls {
				go func() { results <- call() }()
			}
			for range 2 {
				select {
				case <-barrier.arrived:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			close(barrier.release)
			released = true
			successes, refusals := 0, 0
			for range 2 {
				select {
				case result := <-results:
					if result.err == nil {
						successes++
						assert.True(t, result.admin)
						assert.NotEmpty(t, result.userID)
					} else {
						refusals++
						assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(result.err))
						assert.Empty(t, result.userID)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			assert.Equal(t, 1, successes, "only one setup request may create an administrator")
			assert.Equal(t, 1, refusals, "a stale setup request must not become ordinary signup while signup is disabled")
			count, err := base.Users().Count(ctx)
			require.NoError(t, err)
			assert.EqualValues(t, 1, count)
			for _, username := range usernames {
				user, err := base.Users().GetByUsername(ctx, username)
				if err != nil {
					require.ErrorIs(t, err, store.ErrNotFound)
					continue
				}
				assert.True(t, user.IsAdmin)
				assert.Equal(t, 1, countSessions(t, base, user.ID))
			}
		})
	}
}

func TestFinalUserDeletionOrdersOrdinarySignupAdmission(t *testing.T) {
	admin := setupAdminUserTest(t)
	barrier := &accountCreationBarrier{Store: admin.st, holdCount: 1, arrived: make(chan struct{}, 2), release: make(chan struct{})}
	env := setupEmptyPasskeyAuthTestServerWithStore(t, barrier, enableSignup, nil)
	ctx := testutil.DeadlineContext(t)
	barrier.armed.Store(true)
	result := make(chan error, 1)
	released := false
	t.Cleanup(func() {
		if !released {
			close(barrier.release)
		}
	})
	go func() {
		_, err := env.client.SignUp(ctx, &leapmuxv1.SignUpRequest{Username: "ordinaryduringdeletion", Password: "correct-horse-battery-staple"})
		result <- err
	}()
	select {
	case <-barrier.arrived:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, err := admin.client.DeleteUser(authedReq(ctx, &leapmuxv1.DeleteUserRequest{User: service.SelectUserByID(admin.adminID), Force: true}, admin.token))
	require.NoError(t, err)
	close(barrier.release)
	released = true
	select {
	case err := <-result:
		require.Error(t, err)
		assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	count, err := admin.st.Users().Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, count)
	barrier.armed.Store(false)
	created, err := env.client.SignUp(ctx, &leapmuxv1.SignUpRequest{Username: "setupafterdeletion", Password: "correct-horse-battery-staple"})
	require.NoError(t, err)
	assert.True(t, created.GetSessionReport().GetUser().GetIsAdmin())
}

type firstSignupDeliveryFailure struct {
	calls            atomic.Int32
	entered, release chan struct{}
}

func (s *firstSignupDeliveryFailure) Send(ctx context.Context, _ mail.Message) error {
	if s.calls.Add(1) != 1 {
		return nil
	}
	close(s.entered)
	select {
	case <-s.release:
		return errors.New("first verification delivery failed")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestVerificationRollbackOrdersOrdinaryCreationInBothDirections(t *testing.T) {
	for _, creationWins := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback commits first", true: "creation commits first"}[creationWins], func(t *testing.T) {
			admin := setupAdminUserTest(t)
			barrier := &accountCreationBarrier{Store: admin.st, holdCount: 1, arrived: make(chan struct{}, 2), release: make(chan struct{})}
			sender := &firstSignupDeliveryFailure{entered: make(chan struct{}), release: make(chan struct{})}
			env := setupEmptyPasskeyAuthTestServerWithStore(t, barrier, func(t *testing.T, set *settings.Manager) { enableSignup(t, set); enableEmailVerification(t, set) }, sender)
			ctx := testutil.DeadlineContext(t)
			firstResult := make(chan error, 1)
			senderReleased, creationReleased := false, false
			t.Cleanup(func() {
				if !senderReleased {
					close(sender.release)
				}
				if !creationReleased {
					close(barrier.release)
				}
			})
			go func() {
				_, err := env.client.SignUp(ctx, &leapmuxv1.SignUpRequest{Username: "deliveryrollback", Email: "deliveryrollback@example.com", Password: "correct-horse-battery-staple"})
				firstResult <- err
			}()
			select {
			case <-sender.entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			_, err := admin.client.DeleteUser(authedReq(ctx, &leapmuxv1.DeleteUserRequest{User: service.SelectUserByID(admin.adminID), Force: true}, admin.token))
			require.NoError(t, err)
			secondResult := make(chan error, 1)
			if !creationWins {
				barrier.armed.Store(true)
			}
			go func() {
				_, err := env.client.SignUp(ctx, &leapmuxv1.SignUpRequest{Username: "survivingsignup", Email: "surviving@example.com", Password: "correct-horse-battery-staple"})
				secondResult <- err
			}()
			if creationWins {
				select {
				case err := <-secondResult:
					require.NoError(t, err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			} else {
				select {
				case <-barrier.arrived:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			close(sender.release)
			senderReleased = true
			select {
			case err := <-firstResult:
				require.Error(t, err)
				assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if !creationWins {
				close(barrier.release)
				creationReleased = true
				select {
				case err := <-secondResult:
					require.Error(t, err)
					assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			_, err = admin.st.Users().GetByUsername(ctx, "deliveryrollback")
			require.ErrorIs(t, err, store.ErrNotFound)
			hasUser, err := admin.st.Users().HasAny(ctx)
			require.NoError(t, err)
			assert.Equal(t, creationWins, hasUser)
			if !creationWins {
				barrier.armed.Store(false)
				setup, err := env.client.SignUp(ctx, &leapmuxv1.SignUpRequest{Username: "setupafterrollback", Email: "setupafterrollback@example.com", Password: "correct-horse-battery-staple"})
				require.NoError(t, err)
				assert.True(t, setup.GetSessionReport().GetUser().GetIsAdmin())
			}
		})
	}
}

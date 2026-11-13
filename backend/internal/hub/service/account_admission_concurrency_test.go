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

// Hold selected account transactions before BEGIN. A setup loser must refuse
// while the winner holds its runtime claim, without entering another transaction.
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
			barrier := &accountCreationBarrier{Store: base, holdCount: 1, arrived: make(chan struct{}, 2), release: make(chan struct{})}
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
			select {
			case <-barrier.arrived:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			info, err := env.client.GetSystemInfo(passkeyCtx(ctx), &leapmuxv1.GetSystemInfoRequest{})
			require.NoError(t, err)
			assert.True(t, info.GetSetupRequired(), "a claimed setup must not publish uncommitted presence")
			var loser outcome
			select {
			case loser = <-results:
				require.Error(t, loser.err, "the losing setup must finish while the winner is still blocked")
				assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(loser.err))
				assert.Empty(t, loser.userID)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			close(barrier.release)
			released = true
			select {
			case winner := <-results:
				require.NoError(t, winner.err)
				assert.True(t, winner.admin)
				assert.NotEmpty(t, winner.userID)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
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

func TestLastAdministratorDeletionCannotInterruptOrdinarySignup(t *testing.T) {
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
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	close(barrier.release)
	released = true
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	count, err := admin.st.Users().Count(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 2, count)
	barrier.armed.Store(false)
	created, err := env.client.SignUp(ctx, &leapmuxv1.SignUpRequest{Username: "ordinaryafterrefusal", Password: "correct-horse-battery-staple"})
	require.NoError(t, err)
	assert.False(t, created.GetSessionReport().GetUser().GetIsAdmin())
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
					require.NoError(t, err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			_, err := admin.st.Users().GetByUsername(ctx, "deliveryrollback")
			require.ErrorIs(t, err, store.ErrNotFound)
			hasUser, err := admin.st.Users().HasAny(ctx)
			require.NoError(t, err)
			assert.True(t, hasUser)
			if !creationWins {
				barrier.armed.Store(false)
				setup, err := env.client.SignUp(ctx, &leapmuxv1.SignUpRequest{Username: "ordinaryafterrollback", Email: "setupafterrollback@example.com", Password: "correct-horse-battery-staple"})
				require.NoError(t, err)
				assert.False(t, setup.GetSessionReport().GetUser().GetIsAdmin())
			}
		})
	}
}

func TestVerificationRollbackPreservesAPromotedLastAdministratorAndPasskey(t *testing.T) {
	admin := setupAdminUserTest(t)
	sender := &firstSignupDeliveryFailure{entered: make(chan struct{}), release: make(chan struct{})}
	env := setupEmptyPasskeyAuthTestServerWithStore(t, admin.st, func(t *testing.T, set *settings.Manager) {
		enableSignup(t, set)
		enableEmailVerification(t, set)
	}, sender)
	begin := beginPasskeySignUp(t, env.client, "promoted-during-delivery", "promoted@example.com")
	ceremony := newPasskeyCeremony()
	credential, err := ceremony.registrationResponse(begin.GetChallenge().GetOptionsJson())
	require.NoError(t, err)
	ctx := testutil.DeadlineContext(t)
	result := make(chan error, 1)
	released := false
	t.Cleanup(func() {
		if !released {
			close(sender.release)
		}
	})
	go func() {
		_, err := env.client.FinishPasskeySignUp(passkeyCtx(ctx), &leapmuxv1.FinishPasskeySignUpRequest{
			Credential: &leapmuxv1.WebAuthnCredential{SessionId: begin.GetChallenge().GetSessionId(), CredentialJson: credential},
		})
		result <- err
	}()
	select {
	case <-sender.entered:
	case <-ctx.Done():
		t.Fatal("verification delivery did not begin:", ctx.Err())
	}
	user, err := admin.st.Users().GetByUsername(ctx, "promoted-during-delivery")
	require.NoError(t, err)
	_, err = admin.client.SetUserAdmin(authedReq(ctx, &leapmuxv1.SetUserAdminRequest{
		User: service.SelectUserByID(user.ID), IsAdmin: true,
	}, admin.token))
	require.NoError(t, err)
	require.NoError(t, removeAdministrator(t, admin, admin.adminID, "delete"))
	close(sender.release)
	released = true
	select {
	case err := <-result:
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	case <-ctx.Done():
		t.Fatal("failed signup did not finish:", ctx.Err())
	}
	remaining, err := admin.st.Users().GetFirstAdmin(ctx)
	require.NoError(t, err)
	assert.Equal(t, user.ID, remaining.ID)
	credentials, err := admin.st.PasskeyCredentials().ListByUser(ctx, user.ID)
	require.NoError(t, err)
	assert.Len(t, credentials, 1, "refused compensation must roll back passkey deletion also")
	info, err := env.client.GetSystemInfo(passkeyCtx(ctx), &leapmuxv1.GetSystemInfoRequest{})
	require.NoError(t, err)
	assert.False(t, info.GetSetupRequired())
}

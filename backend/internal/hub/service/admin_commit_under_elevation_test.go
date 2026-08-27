package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// The other two thirds of the elevation rule, on the six verbs that commit
// under it.
//
// The GATE alone is a third: it reads a CACHED UserInfo, so "elevated" stays
// true of a session an administrator already took away for the auth cache's
// whole TTL. commitUnderElevation adds the other two -- a re-read of the
// acting authority immediately BEFORE the write, and a slide of the window
// AFTER it -- and each handler used to apply them by hand and unevenly. Of
// the four durable-authority verbs, one re-read and one slid; the other two
// did neither.
//
// Both halves take the whole set of verbs, because one helper is what stops
// the next verb in the class from getting half the rule.

// adminCommitVerb is one verb that commits under the elevation window: the
// call, and the probe that reports whether its own write reached the store.
//
// The probe is what separates "refused" from "refused for the right reason".
// A test that only requires an error passes when the interceptor refuses the
// request before the handler runs, which is exactly what happens when the
// acting session row is simply gone -- and that says nothing about the
// re-read.
type adminCommitVerb struct {
	call func(ctx context.Context, env *adminUserEnv, subjectID, subjectUsername, token string) error
	// landed reports whether this verb's own write is in the store.
	landed func(t *testing.T, env *adminUserEnv, subjectID, subjectUsername string) bool
}

// adminCommitVerbs is every verb on AdminUserService that runs its write
// through commitUnderElevation. A verb added to that helper without an entry
// here keeps its re-read and its slide untested.
func adminCommitVerbs() map[string]adminCommitVerb {
	return map[string]adminCommitVerb{
		"CreateUser": {
			call: func(ctx context.Context, env *adminUserEnv, _, _, token string) error {
				_, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
					Username: "planted", Password: "password123",
				}, token))
				return err
			},
			landed: func(t *testing.T, env *adminUserEnv, _, _ string) bool {
				t.Helper()
				_, err := env.st.Users().GetByUsername(context.Background(), "planted")
				return err == nil
			},
		},
		"SetUserAdmin": {
			call: func(ctx context.Context, env *adminUserEnv, subjectID, _, token string) error {
				_, err := env.client.SetUserAdmin(ctx, authedReq(&leapmuxv1.SetUserAdminRequest{
					Id: subjectID, IsAdmin: true,
				}, token))
				return err
			},
			landed: func(t *testing.T, env *adminUserEnv, subjectID, _ string) bool {
				t.Helper()
				user, err := env.st.Users().GetByID(context.Background(), subjectID)
				require.NoError(t, err)
				return user.IsAdmin
			},
		},
		"ResetPassword": {
			call: func(ctx context.Context, env *adminUserEnv, subjectID, _, token string) error {
				_, err := env.client.ResetPassword(ctx, authedReq(&leapmuxv1.ResetPasswordRequest{
					Id: subjectID, Password: adminCommitResetPassword,
				}, token))
				return err
			},
			landed: func(t *testing.T, env *adminUserEnv, _, subjectUsername string) bool {
				t.Helper()
				_, _, _, err := auth.Login(context.Background(), env.st,
					subjectUsername, adminCommitResetPassword, auth.DefaultSessionDuration)
				return err == nil
			},
		},
		"DeleteUser": {
			call: func(ctx context.Context, env *adminUserEnv, subjectID, _, token string) error {
				_, err := env.client.DeleteUser(ctx, authedReq(&leapmuxv1.DeleteUserRequest{
					Id: subjectID,
				}, token))
				return err
			},
			landed: func(t *testing.T, env *adminUserEnv, subjectID, _ string) bool {
				t.Helper()
				// ErrNotFound exactly, never any error: a store failure must
				// not read as a completed deletion.
				_, err := env.st.Users().GetByID(context.Background(), subjectID)
				if err != nil && !errors.Is(err, store.ErrNotFound) {
					require.NoError(t, err)
				}
				return errors.Is(err, store.ErrNotFound)
			},
		},
		"UpdateUser": {
			call: func(ctx context.Context, env *adminUserEnv, subjectID, _, token string) error {
				_, err := env.client.UpdateUser(ctx, authedReq(&leapmuxv1.UpdateUserRequest{
					Id: subjectID, DisplayName: proto.String("Renamed"),
				}, token))
				return err
			},
			landed: func(t *testing.T, env *adminUserEnv, subjectID, _ string) bool {
				t.Helper()
				user, err := env.st.Users().GetByID(context.Background(), subjectID)
				require.NoError(t, err)
				return user.DisplayName == "Renamed"
			},
		},
		"IssueAPIToken": {
			call: func(ctx context.Context, env *adminUserEnv, subjectID, _, token string) error {
				_, err := env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
					UserId: subjectID, InstallationName: "ci-bot",
				}, token))
				return err
			},
			landed: func(t *testing.T, env *adminUserEnv, subjectID, _ string) bool {
				t.Helper()
				page, err := env.st.APITokens().ListAll(context.Background(), store.ListAllAPITokensParams{
					UserID: &subjectID, IncludeRevoked: true, PageParams: store.PageParams{Limit: 10},
				})
				require.NoError(t, err)
				return len(page.Rows) > 0
			},
		},
	}
}

// adminCommitResetPassword is the password ResetPassword writes above. A
// constant, because the call states it and the probe logs in with it.
const adminCommitResetPassword = "brand-new-subject-pass"

// seedCommitSubject creates the account each verb above acts on, through the
// RPC an administrator uses, and returns its id and username.
//
// A SECOND account, never the acting administrator. ResetPassword and
// DeleteUser aimed at the actor's own account revoke the acting session
// inside their own transaction, so the slide that follows correctly writes
// nothing -- and a test that used the actor could not tell that from a slide
// the handler forgot.
func seedCommitSubject(t *testing.T, env *adminUserEnv) (id, username string) {
	t.Helper()
	created, err := env.client.CreateUser(context.Background(), authedReq(&leapmuxv1.CreateUserRequest{
		Username: "subject", Password: "subjectpass123", DisplayName: "Subject",
	}, env.token))
	require.NoError(t, err)
	return created.Msg.GetUser().GetId(), "subject"
}

// TestAdminUserService_CommitRefusesAWithdrawnActingSession pins the re-read.
//
// The auth interceptor caches a validated UserInfo for its TTL, and a revoke
// raised on another hub reaches this process only on the revocation watcher's
// next sweep, so the gate can admit a request on a session an administrator
// already took away. Every verb here moves a credential or an identity, so a
// commit on authority that no longer exists is exactly the outcome the window
// exists to prevent.
//
// The cache is PRIMED first, with a read on the same cookie. Without that the
// interceptor refuses the request itself -- Revoke deletes the row -- and the
// case would pass with the re-read deleted.
func TestAdminUserService_CommitRefusesAWithdrawnActingSession(t *testing.T) {
	ctx := context.Background()

	for name, verb := range adminCommitVerbs() {
		t.Run(name, func(t *testing.T) {
			env := setupAdminUserTest(t)
			subjectID, subjectUsername := seedCommitSubject(t, env)
			require.False(t, verb.landed(t, env, subjectID, subjectUsername),
				"the fixture must not already hold what the verb writes")

			// One read on the acting cookie, so the interceptor holds a
			// validated UserInfo. That entry is the staleness the re-read
			// exists to answer, and it is what makes the handler -- rather
			// than the interceptor -- the thing under test.
			_, err := env.client.GetUser(ctx, authedReq(&leapmuxv1.GetUserRequest{Id: env.adminID}, env.token))
			require.NoError(t, err)

			// An administrator takes the acting session away. This writes the
			// distinct session_revoked event, which is what separates it from
			// the owner's own sign-out.
			revoked, err := env.st.Sessions().Revoke(ctx, env.token)
			require.NoError(t, err)
			require.EqualValues(t, 1, revoked)

			err = verb.call(ctx, env, subjectID, subjectUsername, env.token)
			require.Error(t, err, "a commit must not land on a session an administrator took away")
			assert.False(t, verb.landed(t, env, subjectID, subjectUsername),
				"a refused commit must leave no write behind")
		})
	}
}

// TestAdminUserService_CommitSlidesTheElevationWindow pins the slide.
//
// The hub's standing rule is that a sensitive action slides the window that
// admitted it, so an administrator who spends two hours on this surface meets
// the prompt once rather than in the middle of the work. A verb that did not
// slide would be a verb the window does not count as use.
func TestAdminUserService_CommitSlidesTheElevationWindow(t *testing.T) {
	ctx := context.Background()

	for name, verb := range adminCommitVerbs() {
		t.Run(name, func(t *testing.T) {
			env := setupAdminUserTest(t)
			subjectID, subjectUsername := seedCommitSubject(t, env)

			// Re-elevate with a deadline close to lapsing, so a slide reads as
			// an increase rather than as the value the fixture already wrote.
			now := time.Now().UTC()
			nearly := now.Add(time.Minute)
			n, err := env.st.Sessions().Elevate(ctx, store.ElevateSessionParams{
				SessionID:          env.token,
				UserID:             userid.MustNew(env.adminID),
				ElevationProvenAt:  now,
				ElevationExpiresAt: nearly,
			}, now)
			require.NoError(t, err)
			require.EqualValues(t, 1, n)

			require.NoError(t, verb.call(ctx, env, subjectID, subjectUsername, env.token))
			require.True(t, verb.landed(t, env, subjectID, subjectUsername),
				"the write must land, or the slide is measured on a request the hub refused")

			session, err := env.st.Sessions().GetByID(ctx, env.token, time.Now().UTC())
			require.NoError(t, err)
			require.NotNil(t, session.ElevationExpiresAt)
			assert.True(t, session.ElevationExpiresAt.After(nearly),
				"a successful commit must extend the window that admitted it: had %s, want later than %s",
				session.ElevationExpiresAt, nearly)
		})
	}
}

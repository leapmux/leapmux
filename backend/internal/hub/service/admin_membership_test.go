package service_test

import (
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createRetainedAdministrator(t *testing.T, st store.Store) *store.User {
	t.Helper()
	hash, err := password.Hash("retained-admin-password")
	require.NoError(t, err)
	user, err := service.CreateUser(t.Context(), st, service.CreateUserParams{
		Username: "retained-admin", PasswordHash: hash, IsAdmin: true,
		FirstCredentialExempt: true,
	})
	require.NoError(t, err)
	return user
}

func removeAdministrator(t *testing.T, env *adminUserEnv, userID, operation string) error {
	t.Helper()
	if operation == "delete" {
		_, err := env.client.DeleteUser(authedReq(t.Context(), &leapmuxv1.DeleteUserRequest{
			User: service.SelectUserByID(userID), Force: true,
		}, env.token))
		return err
	}
	_, err := env.client.SetUserAdmin(authedReq(t.Context(), &leapmuxv1.SetUserAdminRequest{
		User: service.SelectUserByID(userID), IsAdmin: false, Force: true,
	}, env.token))
	return err
}

func TestLastAdministratorCannotBeRemoved(t *testing.T) {
	for _, operation := range []string{"delete", "demote"} {
		t.Run(operation, func(t *testing.T) {
			env := setupAdminUserTest(t)
			// An ordinary user does not replace administrator authority.
			_, err := env.client.CreateUser(authedReq(t.Context(), &leapmuxv1.CreateUserRequest{
				Username: "ordinary", Password: "password1",
			}, env.token))
			require.NoError(t, err)
			worker := storetest.SeedWorker(t, env.st, env.adminID)
			before, err := env.st.Users().GetByID(t.Context(), env.adminID)
			require.NoError(t, err)
			err = removeAdministrator(t, env, env.adminID, operation)
			require.Error(t, err, "force must not remove the only administrator")
			assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			assert.Contains(t, err.Error(), "last administrator")
			after, err := env.st.Users().GetByID(t.Context(), env.adminID)
			require.NoError(t, err)
			assert.True(t, after.IsAdmin)
			assert.Equal(t, before.AuthGeneration, after.AuthGeneration)
			storedWorker, err := env.st.Workers().GetByIDIncludeDeleted(t.Context(), worker.ID)
			require.NoError(t, err)
			assert.Nil(t, storedWorker.DeletedAt)
			sessions, err := env.st.Sessions().ListByUserID(t.Context(), store.ListUserSessionsParams{
				UserID: userid.MustNew(env.adminID), PageParams: store.PageParams{Limit: 10},
			}, time.Now().UTC())
			require.NoError(t, err)
			require.NotEmpty(t, sessions.Rows)
			assert.Empty(t, env.revocations.recorded(), "a refused mutation must not tear down credentials")
		})
	}
}

func TestAdministratorRemovalPreservesAnotherAdministrator(t *testing.T) {
	for _, operation := range []string{"delete", "demote"} {
		t.Run(operation, func(t *testing.T) {
			env := setupAdminUserTest(t)
			other, err := env.client.CreateUser(authedReq(t.Context(), &leapmuxv1.CreateUserRequest{
				Username: "second-admin", Password: "password1", IsAdmin: true,
			}, env.token))
			require.NoError(t, err)
			require.NoError(t, removeAdministrator(t, env, other.GetUser().GetId(), operation))
			remaining, err := env.st.Users().GetFirstAdmin(t.Context())
			require.NoError(t, err)
			assert.Equal(t, env.adminID, remaining.ID)
		})
	}
}

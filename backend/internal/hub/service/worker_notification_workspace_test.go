package service_test

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

func TestCheckNotificationWorkspace_UsesTheAuthenticatedWorkerOwner(t *testing.T) {
	t.Parallel()
	st := hubtestutil.OpenTestStore(t)
	owner := storetest.SeedUser(t, st, "notify-owner")
	stranger := storetest.SeedUser(t, st, "notify-stranger")
	_, token := hubtestutil.CreateTestWorker(t, st, owner.ID)
	owned := storetest.SeedWorkspace(t, st, owner.ID, "Destination")
	foreign := storetest.SeedWorkspace(t, st, stranger.ID, "Other account")
	deleted := storetest.SeedWorkspace(t, st, owner.ID, "Deleted destination")
	_, err := st.Workspaces().SoftDelete(t.Context(), store.SoftDeleteWorkspaceParams{ID: deleted, OwnerUserID: userid.MustNew(owner.ID)})
	require.NoError(t, err)
	svc := newOwnedTabsService(st)
	check := func(bearer, workspaceID string) error {
		_, err := callUnderServerInfo(t.Context(), func(ctx context.Context) error {
			if bearer != "" {
				info, _ := connect.CallInfoForServerContext(ctx)
				info.RequestHeader().Set("Authorization", "Bearer "+bearer)
			}
			_, err := svc.CheckNotificationWorkspace(ctx, &leapmuxv1.CheckNotificationWorkspaceRequest{WorkspaceId: workspaceID})
			return err
		})
		return err
	}
	require.NoError(t, check(token, owned))
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(check("", owned)))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(check(token, "")))
	for _, workspaceID := range []string{foreign, deleted, "missing"} {
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(check(token, workspaceID)))
	}
}

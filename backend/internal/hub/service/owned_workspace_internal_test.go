package service

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// loadOwnedWorkspaceOr403 is the single door onto workspace access, and its
// ownership test is auth.IsOwner -- which has no zero-id prologue, so the
// comparison inside Matches is the only thing standing between a zero caller
// and a workspace.
//
// The blank-OWNER half of this case is gone by construction:
// CreateUserParams.Validate refuses a blank users.id, and owner_user_id is
// `NOT NULL REFERENCES users(id)`, so no blank-owner workspace can exist to be
// matched. What remains reachable -- and is what this pins -- is a zero CALLER
// against a real owner's row. That stays non-vacuous: if the predicate stopped
// comparing, the zero caller would load realWS and the assertion would fail.
func TestZeroCallerCannotLoadBlankOwnedWorkspace(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()

	owner := storetest.SeedUser(t, st, "owned-ws-owner")
	realWS := storetest.SeedWorkspace(t, st, owner.ID, "real")

	// The seam itself, asserted rather than assumed: if this ever stopped
	// refusing, a blank-owner workspace would become storable again and the
	// empty-vs-empty pairing would be back in play.
	require.ErrorIs(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: "", Username: "owned-ws-blank-user", PasswordHash: "h",
		DisplayName: "Blank", PasswordSet: true,
	}), store.ErrInvalidArgument)
	require.Error(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: "ws-blank-owner-loader", OwnerUserID: userid.UserID{}, Title: "blank-owner",
	}), "with no blank-id parent, a blank-owner workspace has nothing to reference")

	// Control: the real owner passes, so the denial below is about the id and
	// not about an unreachable fixture.
	got, err := loadOwnedWorkspaceOr403(ctx, st, realWS, userid.MustNew(owner.ID), "denied")
	require.NoError(t, err)
	require.Equal(t, realWS, got.ID)

	_, err = loadOwnedWorkspaceOr403(ctx, st, realWS, userid.UserID{}, "denied")
	require.Error(t, err, "a zero caller owns nothing, including someone else's workspace")
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

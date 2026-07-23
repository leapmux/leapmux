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
// empty-vs-empty refusal inside Matches is the ONLY thing standing between a
// zero caller and a blank-owner workspace row.
//
// That row is representable: owner_user_id is `NOT NULL REFERENCES users(id)`,
// but a blank-id user inserts cleanly, so a bad migration or a hand-seeded row
// is all it takes. The control below proves the row IS reachable by its real
// owner, so the denial cannot be passing because the fixture is inaccessible.
func TestZeroCallerCannotLoadBlankOwnedWorkspace(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()

	orgID := storetest.SeedOrg(t, st, "owned-ws-org")
	owner := storetest.SeedUser(t, st, orgID, "owned-ws-owner")
	realWS := storetest.SeedWorkspace(t, st, orgID, owner.ID, "real")

	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: "", OrgID: orgID, Username: "owned-ws-blank-user",
		PasswordHash: "h", DisplayName: "Blank", PasswordSet: true,
	}))
	blankWS := "ws-blank-owner-loader"
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: blankWS, OrgID: orgID, OwnerUserID: userid.UserID{}, Title: "blank-owner",
	}))

	// Control: the real owner passes, so the denials below are about the id.
	got, err := loadOwnedWorkspaceOr403(ctx, st, realWS, userid.MustNew(owner.ID), "denied")
	require.NoError(t, err)
	require.Equal(t, realWS, got.ID)

	// The pairing that bites: two empty strings must not read as one principal.
	_, err = loadOwnedWorkspaceOr403(ctx, st, blankWS, userid.UserID{}, "denied")
	require.Error(t, err, "a zero caller must not own a blank-owner workspace")
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	// And a zero caller is not the owner of a REAL workspace either.
	_, err = loadOwnedWorkspaceOr403(ctx, st, realWS, userid.UserID{}, "denied")
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

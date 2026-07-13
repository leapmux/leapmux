package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
)

// TestCRDTAuthChecker_CanUseWorker_RequiresActive pins the fix that the CRDT
// tab-register gate rejects a worker that is registered to the caller but is no
// longer ACTIVE -- the SAME bar ChannelService.verifyWorkerAccess holds for
// opening a channel. Deregistering a compromised worker is the operator's one
// containment action; because SubmitOps is a delegation-allowed procedure, a
// gate that accepted a deregistering minter (deleted_at still NULL) would let a
// leaked delegation token bind a tab to it after the channel path already
// refuses it.
func TestCRDTAuthChecker_CanUseWorker_RequiresActive(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	orgID := storetest.SeedOrg(t, st, "org")
	owner := storetest.SeedUser(t, st, orgID, "owner")
	worker := storetest.SeedWorker(t, st, owner.ID)
	checker := service.NewCRDTAuthChecker(st)
	ctx := context.Background()

	// ACTIVE worker owned by the caller: allowed.
	ok, err := checker.CanUseWorker(ctx, "", worker.ID, owner.ID)
	require.NoError(t, err)
	assert.True(t, ok, "owner may bind a tab to their ACTIVE worker")

	// Deregister transitions the worker to DEREGISTERING (status 2) but leaves
	// deleted_at NULL, so GetByID still returns it -- the exact state the old
	// DeletedAt-only check let slip through.
	n, err := st.Workers().Deregister(ctx, store.DeregisterWorkerParams{
		ID:           worker.ID,
		RegisteredBy: owner.ID,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), n)

	ok, err = checker.CanUseWorker(ctx, "", worker.ID, owner.ID)
	require.NoError(t, err)
	assert.False(t, ok, "a deregistering worker must be refused, matching the channel path")
}

// TestCRDTAuthChecker_CanUseWorker_EmptyWorkerID confirms an empty worker id
// short-circuits to allowed so clearing a tab's worker reference needs no store
// round-trip.
func TestCRDTAuthChecker_CanUseWorker_EmptyWorkerID(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	checker := service.NewCRDTAuthChecker(st)

	ok, err := checker.CanUseWorker(context.Background(), "", "", "user")
	require.NoError(t, err)
	assert.True(t, ok)
}

// TestCRDTAuthChecker_CanUseWorker_NonOwner confirms a worker registered to a
// different user is refused.
func TestCRDTAuthChecker_CanUseWorker_NonOwner(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	orgID := storetest.SeedOrg(t, st, "org")
	owner := storetest.SeedUser(t, st, orgID, "owner")
	other := storetest.SeedUser(t, st, orgID, "other")
	worker := storetest.SeedWorker(t, st, owner.ID)
	checker := service.NewCRDTAuthChecker(st)

	ok, err := checker.CanUseWorker(context.Background(), "", worker.ID, other.ID)
	require.NoError(t, err)
	assert.False(t, ok)
}

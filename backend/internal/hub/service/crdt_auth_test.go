package service_test

import (
	"context"
	"testing"

	"github.com/leapmux/leapmux/internal/util/userid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
)

// TestCRDTAuthChecker_CanUseWorker_RequiresActive pins the fix that the CRDT
// tab-register gate rejects a worker that is registered to the caller but is no
// longer ACTIVE -- the SAME bar service.WorkerReachAuthorizer holds for
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
		RegisteredBy: userid.MustNew(owner.ID),
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

// TestCRDTAuthChecker_EmptyPrincipalDenies pins the CRDT wire boundary:
// crdt.AuthChecker's principal arrives as a bare string off a SubmitOps op, so
// this is where it becomes a userid.UserID. A blank one is a malformed op, not
// an anonymous caller -- every arm must deny rather than hand the auth
// predicates a zero id.
//
// CanUseWorker is checked with a REAL worker id: an empty worker id
// short-circuits to allowed before the principal is ever minted (see
// TestCRDTAuthChecker_CanUseWorker_EmptyWorkerID), so passing "" there would
// pass for the wrong reason.
func TestCRDTAuthChecker_EmptyPrincipalDenies(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()
	orgID := storetest.SeedOrg(t, st, "org")
	owner := storetest.SeedUser(t, st, orgID, "owner")
	workspaceID := storetest.SeedWorkspace(t, st, orgID, owner.ID, "ws")
	worker := storetest.SeedWorker(t, st, owner.ID)
	checker := service.NewCRDTAuthChecker(st)

	// Control: the real owner is allowed on every arm, so the denials below
	// cannot be passing because the fixture itself is inaccessible.
	access, err := checker.CanAccessWorkspace(ctx, orgID, workspaceID, owner.ID)
	require.NoError(t, err)
	require.True(t, access, "the owner must be allowed, or the denials below prove nothing")

	access, err = checker.CanAccessWorkspace(ctx, orgID, workspaceID, "")
	require.NoError(t, err)
	assert.False(t, access, "a blank principal must not read a workspace")

	ok, err := checker.CanUseWorker(ctx, "", worker.ID, "")
	require.NoError(t, err)
	assert.False(t, ok, "a blank principal must not bind a tab to a worker")
}

// TestCRDTAuthChecker_CanAccessWorkspaceForUsers_DropsBlankPrincipals pins the
// batch arm of the same boundary: a blank entry is dropped from the expansion
// rather than short-circuiting the whole call, so a real subscriber in the same
// batch still resolves. The returned map is keyed by the raw wire id (the CRDT
// actor format), which is why the owner's key is a plain string.
func TestCRDTAuthChecker_CanAccessWorkspaceForUsers_DropsBlankPrincipals(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()
	orgID := storetest.SeedOrg(t, st, "org")
	owner := storetest.SeedUser(t, st, orgID, "owner")
	stranger := storetest.SeedUser(t, st, orgID, "stranger")
	workspaceID := storetest.SeedWorkspace(t, st, orgID, owner.ID, "ws")
	// CanAccessWorkspaceForUsers is an OPTIONAL capability the crdt package
	// discovers by assertion (its interface is unexported there), so assert it
	// here too: a rename or drop would otherwise silently downgrade subscriber
	// expansion to the per-op form with nothing failing.
	batch, ok := service.NewCRDTAuthChecker(st).(interface {
		CanAccessWorkspaceForUsers(ctx context.Context, orgID, workspaceID string, userIDs []string) (map[string]bool, error)
	})
	require.True(t, ok, "crdtAuthChecker must implement the batch read capability")

	readable, err := batch.CanAccessWorkspaceForUsers(
		ctx, orgID, workspaceID, []string{"", owner.ID, "", stranger.ID})
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{owner.ID: true}, readable,
		"blank principals drop out; the owner still resolves and a non-owner stays absent")

	// An all-blank batch is the empty set, never a blanket allow.
	readable, err = batch.CanAccessWorkspaceForUsers(ctx, orgID, workspaceID, []string{"", ""})
	require.NoError(t, err)
	assert.Empty(t, readable)
}

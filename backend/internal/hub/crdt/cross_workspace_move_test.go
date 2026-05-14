package crdt_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// TestCrossWorkspaceMove_SetTabRegisterTileID_ResolvesNewWorkspace
// covers the v1 cross-workspace move primitive: a single
// SetTabRegister(tile_id=newTile) on a tab whose new tile resolves to
// a different workspace_id via the parent_id chain. Both
// `_owned.workspace_id` and `_rendered.workspace_id` must reflect the
// new workspace immediately.
func TestCrossWorkspaceMove_SetTabRegisterTileID_ResolvesNewWorkspace(t *testing.T) {
	mgr, j, _ := runManager(t, "org", allowAll{}, 100_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Add tab on w1.
	_, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "init", "tA", "root1", "wkr1", "p1")},
	})
	require.NoError(t, err)

	// Check initial index.
	owned, rendered := j.snapshotIndex()
	require.Equal(t, "w1", owned["tA"].WorkspaceID)
	require.Equal(t, "w1", rendered["tA"].WorkspaceID)

	// Move tab to w2 via SetTabRegister(tile_id=root2). One LWW write.
	moveBatch := &leapmuxv1.OpBatch{
		BatchId: "move",
		Ops: []*leapmuxv1.OrgOp{{OpId: "op-move", Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
			Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root2"},
		}}}},
	}
	_, err = mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{moveBatch},
	})
	require.NoError(t, err)

	owned, rendered = j.snapshotIndex()
	require.Contains(t, owned, "tA")
	assert.Equal(t, "w2", owned["tA"].WorkspaceID,
		"_owned must reflect the post-move workspace via tile ancestry")
	assert.Equal(t, "w2", rendered["tA"].WorkspaceID,
		"_rendered must reflect the post-move workspace")
	assert.Equal(t, "root2", owned["tA"].TileID)
}

// TestCrossWorkspaceMove_RequiresWritePermissionToBothWorkspaces
// asserts the auth rule: a move (post-tile resolves to a different
// workspace) requires write access to BOTH the source and destination.
func TestCrossWorkspaceMove_RequiresWritePermissionToBothWorkspaces(t *testing.T) {
	// Caller can only write w1 (not w2).
	mgr, _, _ := runManager(t, "org", onlyOwner{allowed: map[string]bool{"w1": true}}, 200_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Add tab on w1 (allowed — caller has w1 write).
	_, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "init", "tA", "root1", "wkr1", "p1")},
	})
	require.NoError(t, err)

	// Try to move to w2 — must fail with FORBIDDEN_WORKSPACE.
	moveBatch := &leapmuxv1.OpBatch{
		BatchId: "move",
		Ops: []*leapmuxv1.OrgOp{{OpId: "op-move", Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
			Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root2"},
		}}}},
	}
	r, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{moveBatch},
	})
	require.NoError(t, err)
	rejected := r[0].GetRejected()
	require.NotNil(t, rejected)
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_FORBIDDEN_WORKSPACE, rejected.GetReason())
}

// TestCrossWorkspaceMove_PureCreate_OnlyDestPermissionRequired
// asserts the create-exception in the auth rule: when an entity is
// being created (no pre-batch state), only the post-batch workspace
// permission is required.
func TestCrossWorkspaceMove_PureCreate_OnlyDestPermissionRequired(t *testing.T) {
	mgr, _, _ := runManager(t, "org", onlyOwner{allowed: map[string]bool{"w2": true}}, 300_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Caller has only w2 write; creating a tab on w2 should succeed
	// (no pre-workspace permission needed for creates).
	r, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "create", "tNew", "root2", "wkr1", "p1")},
	})
	require.NoError(t, err)
	require.NotNil(t, r[0].GetCommitted(), "pure-create on w2 should succeed without w1 permission")
}

// TestCrossWorkspaceMove_TabIndexBothViewsConsistent verifies that
// after a move, both _owned and _rendered carry the same workspace_id
// and tile_id (no stale rows from the source workspace).
func TestCrossWorkspaceMove_TabIndexBothViewsConsistent(t *testing.T) {
	mgr, j, _ := runManager(t, "org", allowAll{}, 400_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	_, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "init", "tA", "root1", "wkr1", "p1")},
	})
	require.NoError(t, err)

	// Move + reposition in one batch.
	mv := &leapmuxv1.OpBatch{
		BatchId: "move",
		Ops: []*leapmuxv1.OrgOp{
			{OpId: "op-move", Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
				Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root2"},
			}}},
			{OpId: "op-pos", Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
				Field: &leapmuxv1.SetTabRegisterOp_Position{Position: "newpos"},
			}}},
		},
	}
	_, err = mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{mv},
	})
	require.NoError(t, err)

	owned, rendered := j.snapshotIndex()
	require.Contains(t, owned, "tA")
	require.Contains(t, rendered, "tA")
	assert.Equal(t, "w2", owned["tA"].WorkspaceID)
	assert.Equal(t, "w2", rendered["tA"].WorkspaceID)
	assert.Equal(t, "root2", owned["tA"].TileID)
	assert.Equal(t, "root2", rendered["tA"].TileID)
	assert.Equal(t, "newpos", rendered["tA"].Position)

	// Only one row per tab id — no stale w1 row.
	assert.Len(t, owned, 1)
	assert.Len(t, rendered, 1)
}

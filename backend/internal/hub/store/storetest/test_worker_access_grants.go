package storetest

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testWorkerAccessGrants(t *testing.T) {
	t.Run("grant and has access", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wag-org", false)
		owner := SeedUser(t, st, orgID, "wag-owner")
		grantee := SeedUser(t, st, orgID, "wag-grantee")
		worker := SeedWorker(t, st, owner.ID)

		err := st.WorkerAccessGrants().Grant(ctx, store.GrantWorkerAccessParams{
			WorkerID:  worker.ID,
			UserID:    grantee.ID,
			GrantedBy: owner.ID,
		})
		require.NoError(t, err)

		has, err := st.WorkerAccessGrants().HasAccess(ctx, store.HasWorkerAccessParams{
			WorkerID: worker.ID,
			UserID:   grantee.ID,
		})
		require.NoError(t, err)
		assert.True(t, has)
	})

	t.Run("has access false when not granted", func(t *testing.T) {
		st := s.NewStore(t)

		has, err := st.WorkerAccessGrants().HasAccess(ctx, store.HasWorkerAccessParams{
			WorkerID: "no-worker",
			UserID:   "no-user",
		})
		require.NoError(t, err)
		assert.False(t, has)
	})

	t.Run("list", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wag-org", false)
		owner := SeedUser(t, st, orgID, "wag-list-owner")
		g1 := SeedUser(t, st, orgID, "wag-g1")
		g2 := SeedUser(t, st, orgID, "wag-g2")
		worker := SeedWorker(t, st, owner.ID)

		err := st.WorkerAccessGrants().Grant(ctx, store.GrantWorkerAccessParams{
			WorkerID: worker.ID, UserID: g1.ID, GrantedBy: owner.ID,
		})
		require.NoError(t, err)
		err = st.WorkerAccessGrants().Grant(ctx, store.GrantWorkerAccessParams{
			WorkerID: worker.ID, UserID: g2.ID, GrantedBy: owner.ID,
		})
		require.NoError(t, err)

		grants, err := st.WorkerAccessGrants().List(ctx, worker.ID)
		require.NoError(t, err)
		assert.Len(t, grants, 2)
	})

	t.Run("revoke", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wag-org", false)
		owner := SeedUser(t, st, orgID, "wag-revoke-owner")
		grantee := SeedUser(t, st, orgID, "wag-revoke-grantee")
		worker := SeedWorker(t, st, owner.ID)

		err := st.WorkerAccessGrants().Grant(ctx, store.GrantWorkerAccessParams{
			WorkerID: worker.ID, UserID: grantee.ID, GrantedBy: owner.ID,
		})
		require.NoError(t, err)

		err = st.WorkerAccessGrants().Revoke(ctx, store.RevokeWorkerAccessParams{
			WorkerID: worker.ID,
			UserID:   grantee.ID,
		})
		require.NoError(t, err)

		has, err := st.WorkerAccessGrants().HasAccess(ctx, store.HasWorkerAccessParams{
			WorkerID: worker.ID,
			UserID:   grantee.ID,
		})
		require.NoError(t, err)
		assert.False(t, has)
	})

	t.Run("delete by worker", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wag-org", false)
		owner := SeedUser(t, st, orgID, "wag-dw-owner")
		grantee := SeedUser(t, st, orgID, "wag-dw-grantee")
		worker := SeedWorker(t, st, owner.ID)

		err := st.WorkerAccessGrants().Grant(ctx, store.GrantWorkerAccessParams{
			WorkerID: worker.ID, UserID: grantee.ID, GrantedBy: owner.ID,
		})
		require.NoError(t, err)

		err = st.WorkerAccessGrants().DeleteByWorker(ctx, worker.ID)
		require.NoError(t, err)

		grants, err := st.WorkerAccessGrants().List(ctx, worker.ID)
		require.NoError(t, err)
		require.NotNil(t, grants)
		assert.Empty(t, grants)
	})

	t.Run("delete by user", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wag-org", false)
		owner := SeedUser(t, st, orgID, "wag-du-owner")
		grantee := SeedUser(t, st, orgID, "wag-du-grantee")
		w1 := SeedWorker(t, st, owner.ID)
		w2 := SeedWorker(t, st, owner.ID)

		for _, wID := range []string{w1.ID, w2.ID} {
			err := st.WorkerAccessGrants().Grant(ctx, store.GrantWorkerAccessParams{
				WorkerID: wID, UserID: grantee.ID, GrantedBy: owner.ID,
			})
			require.NoError(t, err)
		}

		err := st.WorkerAccessGrants().DeleteByUser(ctx, grantee.ID)
		require.NoError(t, err)

		for _, wID := range []string{w1.ID, w2.ID} {
			has, err := st.WorkerAccessGrants().HasAccess(ctx, store.HasWorkerAccessParams{
				WorkerID: wID, UserID: grantee.ID,
			})
			require.NoError(t, err)
			assert.False(t, has)
		}
	})

	t.Run("has access non existent worker", func(t *testing.T) {
		st := s.NewStore(t)

		has, err := st.WorkerAccessGrants().HasAccess(ctx, store.HasWorkerAccessParams{
			WorkerID: "nonexistent-worker",
			UserID:   "nonexistent-user",
		})
		require.NoError(t, err)
		assert.False(t, has)
	})

	t.Run("list empty", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wag-org", false)
		owner := SeedUser(t, st, orgID, "wag-listempty-owner")
		worker := SeedWorker(t, st, owner.ID)

		grants, err := st.WorkerAccessGrants().List(ctx, worker.ID)
		require.NoError(t, err)
		require.NotNil(t, grants)
		assert.Empty(t, grants)
	})

	t.Run("revoke non existent", func(t *testing.T) {
		st := s.NewStore(t)

		err := st.WorkerAccessGrants().Revoke(ctx, store.RevokeWorkerAccessParams{
			WorkerID: "nonexistent-worker",
			UserID:   "nonexistent-user",
		})
		require.NoError(t, err)
	})

	t.Run("delete by worker empty", func(t *testing.T) {
		st := s.NewStore(t)

		err := st.WorkerAccessGrants().DeleteByWorker(ctx, "nonexistent-worker")
		require.NoError(t, err)
	})

	t.Run("delete by user empty", func(t *testing.T) {
		st := s.NewStore(t)

		err := st.WorkerAccessGrants().DeleteByUser(ctx, "nonexistent-user")
		require.NoError(t, err)
	})

	t.Run("delete by user in org", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wag-org", false)
		owner := SeedUser(t, st, orgID, "wag-duo-owner")
		grantee := SeedUser(t, st, orgID, "wag-duo-grantee")
		SeedOrgMember(t, st, orgID, owner.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER)
		SeedOrgMember(t, st, orgID, grantee.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER)
		worker := SeedWorker(t, st, owner.ID)

		err := st.WorkerAccessGrants().Grant(ctx, store.GrantWorkerAccessParams{
			WorkerID: worker.ID, UserID: grantee.ID, GrantedBy: owner.ID,
		})
		require.NoError(t, err)

		err = st.WorkerAccessGrants().DeleteByUserInOrg(ctx, store.DeleteWorkerAccessGrantsByUserInOrgParams{
			UserID: grantee.ID,
			OrgID:  orgID,
		})
		require.NoError(t, err)

		has, err := st.WorkerAccessGrants().HasAccess(ctx, store.HasWorkerAccessParams{
			WorkerID: worker.ID, UserID: grantee.ID,
		})
		require.NoError(t, err)
		assert.False(t, has)
	})

	t.Run("delete by user in org does not affect grants in other orgs", func(t *testing.T) {
		st := s.NewStore(t)
		org1 := SeedOrg(t, st, "grant-org1", false)
		org2 := SeedOrg(t, st, "grant-org2", false)
		ownerInOrg1 := SeedUser(t, st, org1, "grant-owner1")
		ownerInOrg2 := SeedUser(t, st, org2, "grant-owner2")
		grantee := SeedUser(t, st, org1, "grant-cross-user")

		w1 := SeedWorker(t, st, ownerInOrg1.ID)
		w2 := SeedWorker(t, st, ownerInOrg2.ID)

		// Create org memberships for the owners.
		SeedOrgMember(t, st, org1, ownerInOrg1.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER)
		SeedOrgMember(t, st, org2, ownerInOrg2.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER)

		// Grant grantee access to both workers.
		for _, wID := range []string{w1.ID, w2.ID} {
			err := st.WorkerAccessGrants().Grant(ctx, store.GrantWorkerAccessParams{
				WorkerID: wID, UserID: grantee.ID, GrantedBy: ownerInOrg1.ID,
			})
			require.NoError(t, err)
		}

		// Delete grants for grantee in org1 only.
		err := st.WorkerAccessGrants().DeleteByUserInOrg(ctx, store.DeleteWorkerAccessGrantsByUserInOrgParams{
			UserID: grantee.ID, OrgID: org1,
		})
		require.NoError(t, err)

		// Grant for w1 (org1) should be gone.
		has, err := st.WorkerAccessGrants().HasAccess(ctx, store.HasWorkerAccessParams{
			WorkerID: w1.ID, UserID: grantee.ID,
		})
		require.NoError(t, err)
		assert.False(t, has)

		// Grant for w2 (org2) should still exist.
		has, err = st.WorkerAccessGrants().HasAccess(ctx, store.HasWorkerAccessParams{
			WorkerID: w2.ID, UserID: grantee.ID,
		})
		require.NoError(t, err)
		assert.True(t, has)
	})

	t.Run("duplicate grant is idempotent", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "grant-org", false)
		owner := SeedUser(t, st, orgID, "idem-grant-owner")
		grantee := SeedUser(t, st, orgID, "idem-grant-user")
		worker := SeedWorker(t, st, owner.ID)

		err := st.WorkerAccessGrants().Grant(ctx, store.GrantWorkerAccessParams{
			WorkerID: worker.ID, UserID: grantee.ID, GrantedBy: owner.ID,
		})
		require.NoError(t, err)

		// Second grant should not error (idempotent).
		err = st.WorkerAccessGrants().Grant(ctx, store.GrantWorkerAccessParams{
			WorkerID: worker.ID, UserID: grantee.ID, GrantedBy: owner.ID,
		})
		require.NoError(t, err)

		// Should still have exactly one grant.
		grants, err := st.WorkerAccessGrants().List(ctx, worker.ID)
		require.NoError(t, err)
		assert.Len(t, grants, 1)
	})
}

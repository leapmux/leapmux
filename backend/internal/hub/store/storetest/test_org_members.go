package storetest

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testOrgMembers(t *testing.T) {
	t.Run("create and get by org and user", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "member-org", false)
		user := SeedUser(t, st, orgID, "member-user")
		SeedOrgMember(t, st, orgID, user.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER)

		member, err := st.OrgMembers().GetByOrgAndUser(ctx, orgID, user.ID)
		require.NoError(t, err)
		assert.Equal(t, orgID, member.OrgID)
		assert.Equal(t, user.ID, member.UserID)
		assert.Equal(t, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER, member.Role)
		assert.False(t, member.JoinedAt.IsZero())
	})

	t.Run("get by org and user not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.OrgMembers().GetByOrgAndUser(ctx, "noorg", "nouser")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("list by org id", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "list-org", false)
		alice := SeedUser(t, st, orgID, "alice-member")
		bob := SeedUser(t, st, orgID, "bob-member")
		SeedOrgMember(t, st, orgID, alice.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_ADMIN)
		SeedOrgMember(t, st, orgID, bob.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER)

		members, err := st.OrgMembers().ListByOrgID(ctx, orgID)
		require.NoError(t, err)
		require.Len(t, members, 2)

		// Verify joined user fields are populated.
		usernames := make(map[string]bool)
		for _, m := range members {
			usernames[m.Username] = true
			assert.NotEmpty(t, m.DisplayName)
			assert.NotEmpty(t, m.Email)
		}
		assert.True(t, usernames["alice-member"])
		assert.True(t, usernames["bob-member"])
	})

	t.Run("list orgs by user id", func(t *testing.T) {
		st := s.NewStore(t)
		org1 := SeedOrg(t, st, "org-1", false)
		org2 := SeedOrg(t, st, "org-2", false)
		user := SeedUser(t, st, org1, "multi-org-user")
		SeedOrgMember(t, st, org1, user.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER)
		SeedOrgMember(t, st, org2, user.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_ADMIN)

		orgs, err := st.OrgMembers().ListOrgsByUserID(ctx, user.ID)
		require.NoError(t, err)
		assert.Len(t, orgs, 2)

		orgIDs := make(map[string]bool)
		for _, o := range orgs {
			orgIDs[o.ID] = true
		}
		assert.True(t, orgIDs[org1])
		assert.True(t, orgIDs[org2])
	})

	t.Run("update role", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "role-org", false)
		user := SeedUser(t, st, orgID, "role-user")
		SeedOrgMember(t, st, orgID, user.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER)

		err := st.OrgMembers().UpdateRole(ctx, store.UpdateOrgMemberRoleParams{
			OrgID:  orgID,
			UserID: user.ID,
			Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_ADMIN,
		})
		require.NoError(t, err)

		member, err := st.OrgMembers().GetByOrgAndUser(ctx, orgID, user.ID)
		require.NoError(t, err)
		assert.Equal(t, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_ADMIN, member.Role)
	})

	t.Run("delete", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "del-org", false)
		user := SeedUser(t, st, orgID, "del-member")
		SeedOrgMember(t, st, orgID, user.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER)

		err := st.OrgMembers().Delete(ctx, store.DeleteOrgMemberParams{
			OrgID:  orgID,
			UserID: user.ID,
		})
		require.NoError(t, err)

		_, err = st.OrgMembers().GetByOrgAndUser(ctx, orgID, user.ID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("count by role", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "count-org", false)
		u1 := SeedUser(t, st, orgID, "count-admin")
		u2 := SeedUser(t, st, orgID, "count-member1")
		u3 := SeedUser(t, st, orgID, "count-member2")
		SeedOrgMember(t, st, orgID, u1.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_ADMIN)
		SeedOrgMember(t, st, orgID, u2.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER)
		SeedOrgMember(t, st, orgID, u3.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER)

		count, err := st.OrgMembers().CountByRole(ctx, store.CountOrgMembersByRoleParams{
			OrgID: orgID,
			Role:  leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(2), count)

		count, err = st.OrgMembers().CountByRole(ctx, store.CountOrgMembersByRoleParams{
			OrgID: orgID,
			Role:  leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_ADMIN,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), count)
	})

	t.Run("is member", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ismember-org", false)
		user := SeedUser(t, st, orgID, "ismember-user")

		isMember, err := st.OrgMembers().IsMember(ctx, store.IsOrgMemberParams{
			OrgID:  orgID,
			UserID: user.ID,
		})
		require.NoError(t, err)
		assert.False(t, isMember)

		SeedOrgMember(t, st, orgID, user.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER)

		isMember, err = st.OrgMembers().IsMember(ctx, store.IsOrgMemberParams{
			OrgID:  orgID,
			UserID: user.ID,
		})
		require.NoError(t, err)
		assert.True(t, isMember)
	})

	t.Run("get non-existent", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.OrgMembers().GetByOrgAndUser(ctx, "no-org", "no-user")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("is member non-existent", func(t *testing.T) {
		st := s.NewStore(t)
		isMember, err := st.OrgMembers().IsMember(ctx, store.IsOrgMemberParams{
			OrgID:  "no-org",
			UserID: "no-user",
		})
		require.NoError(t, err)
		assert.False(t, isMember)
	})

	t.Run("list by org empty", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "empty-members-org", false)

		members, err := st.OrgMembers().ListByOrgID(ctx, orgID)
		require.NoError(t, err)
		require.NotNil(t, members)
		assert.Empty(t, members)
	})

	t.Run("list orgs by user empty", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "no-membership-org", false)
		user := SeedUser(t, st, orgID, "no-membership-user")

		orgs, err := st.OrgMembers().ListOrgsByUserID(ctx, user.ID)
		require.NoError(t, err)
		require.NotNil(t, orgs)
		assert.Empty(t, orgs)
	})

	t.Run("count by role zero", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "zero-role-org", false)

		count, err := st.OrgMembers().CountByRole(ctx, store.CountOrgMembersByRoleParams{
			OrgID: orgID,
			Role:  leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_ADMIN,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(0), count)
	})

	t.Run("list by org id excludes deleted users", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "del-user-org", false)
		alice := SeedUser(t, st, orgID, "alive-alice")
		bob := SeedUser(t, st, orgID, "deleted-bob")
		SeedOrgMember(t, st, orgID, alice.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER)
		SeedOrgMember(t, st, orgID, bob.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER)

		// Soft-delete bob's user account.
		err := st.Users().Delete(ctx, bob.ID)
		require.NoError(t, err)

		// ListByOrgID should only return alice (bob's user is deleted).
		members, err := st.OrgMembers().ListByOrgID(ctx, orgID)
		require.NoError(t, err)
		require.Len(t, members, 1)
		assert.Equal(t, "alive-alice", members[0].Username)
	})

	t.Run("is member after delete", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "del-member-org", false)
		user := SeedUser(t, st, orgID, "del-member-user")
		SeedOrgMember(t, st, orgID, user.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER)

		err := st.OrgMembers().Delete(ctx, store.DeleteOrgMemberParams{
			OrgID:  orgID,
			UserID: user.ID,
		})
		require.NoError(t, err)

		isMember, err := st.OrgMembers().IsMember(ctx, store.IsOrgMemberParams{
			OrgID:  orgID,
			UserID: user.ID,
		})
		require.NoError(t, err)
		assert.False(t, isMember)
	})

	t.Run("duplicate org membership returns conflict", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "dup-member-org", false)
		user := SeedUser(t, st, orgID, "dup-member-user")
		SeedOrgMember(t, st, orgID, user.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER)

		err := st.OrgMembers().Create(ctx, store.CreateOrgMemberParams{
			OrgID: orgID, UserID: user.ID, Role: leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_ADMIN,
		})
		assert.ErrorIs(t, err, store.ErrConflict)
	})

	t.Run("list orgs by user excludes soft-deleted orgs", func(t *testing.T) {
		st := s.NewStore(t)
		org1 := SeedOrg(t, st, "alive-org", false)
		org2 := SeedOrg(t, st, "dead-org", false)
		user := SeedUser(t, st, org1, "org-del-user")
		SeedOrgMember(t, st, org1, user.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER)
		SeedOrgMember(t, st, org2, user.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER)

		err := st.Orgs().SoftDelete(ctx, org2)
		require.NoError(t, err)

		orgs, err := st.OrgMembers().ListOrgsByUserID(ctx, user.ID)
		require.NoError(t, err)
		require.Len(t, orgs, 1)
		assert.Equal(t, org1, orgs[0].ID)
	})
}

package storetest

import (
	"fmt"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testUsers(t *testing.T) {
	t.Run("create and get by id", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		userID := id.Generate()

		err := st.Users().Create(ctx, store.CreateUserParams{
			ID:            userID,
			OrgID:         orgID,
			Username:      "alice",
			PasswordHash:  "hash123",
			DisplayName:   "Alice Smith",
			Email:         "alice@example.com",
			EmailVerified: true,
			PasswordSet:   true,
			IsAdmin:       false,
		})
		require.NoError(t, err)

		user, err := st.Users().GetByID(ctx, userID)
		require.NoError(t, err)
		assert.Equal(t, userID, user.ID)
		assert.Equal(t, orgID, user.OrgID)
		assert.Equal(t, "alice", user.Username)
		assert.Equal(t, "hash123", user.PasswordHash)
		assert.Equal(t, "Alice Smith", user.DisplayName)
		assert.Equal(t, "alice@example.com", user.Email)
		assert.True(t, user.EmailVerified)
		assert.True(t, user.PasswordSet)
		assert.False(t, user.IsAdmin)
		assert.False(t, user.CreatedAt.IsZero())
		assert.False(t, user.UpdatedAt.IsZero())
		assert.Nil(t, user.DeletedAt)
	})

	t.Run("get by username", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		user := SeedUser(t, st, orgID, "bob")

		found, err := st.Users().GetByUsername(ctx, "bob")
		require.NoError(t, err)
		assert.Equal(t, user.ID, found.ID)
	})

	t.Run("get by email", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		user := SeedUser(t, st, orgID, "carol")

		found, err := st.Users().GetByEmail(ctx, "carol@example.com")
		require.NoError(t, err)
		assert.Equal(t, user.ID, found.ID)
	})

	t.Run("get by id not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.Users().GetByID(ctx, "nonexistent")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("get first admin", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "admin-org", true)

		// No users: ErrNotFound.
		_, err := st.Users().GetFirstAdmin(ctx)
		assert.ErrorIs(t, err, store.ErrNotFound)

		// Non-admin user only: still ErrNotFound.
		SeedUser(t, st, orgID, "regular")
		_, err = st.Users().GetFirstAdmin(ctx)
		assert.ErrorIs(t, err, store.ErrNotFound)

		// Sleep between creates so created_at ordering is deterministic
		// (some backends only have millisecond precision).
		time.Sleep(5 * time.Millisecond)
		older := SeedUser(t, st, orgID, "older-admin")
		require.NoError(t, st.Users().UpdateAdmin(ctx, store.UpdateUserAdminParams{
			ID:      older.ID,
			IsAdmin: true,
		}))
		time.Sleep(5 * time.Millisecond)
		newer := SeedUser(t, st, orgID, "newer-admin")
		require.NoError(t, st.Users().UpdateAdmin(ctx, store.UpdateUserAdminParams{
			ID:      newer.ID,
			IsAdmin: true,
		}))

		// With two admins, returns the one with the oldest created_at.
		found, err := st.Users().GetFirstAdmin(ctx)
		require.NoError(t, err)
		assert.Equal(t, older.ID, found.ID)

		// Soft-deleted admins are ignored.
		require.NoError(t, st.Users().Delete(ctx, older.ID))
		found, err = st.Users().GetFirstAdmin(ctx)
		require.NoError(t, err)
		assert.Equal(t, newer.ID, found.ID)

		require.NoError(t, st.Users().Delete(ctx, newer.ID))
		_, err = st.Users().GetFirstAdmin(ctx)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("has any", func(t *testing.T) {
		st := s.NewStore(t)

		has, err := st.Users().HasAny(ctx)
		require.NoError(t, err)
		assert.False(t, has)

		orgID := SeedOrg(t, st, "user-org", true)
		SeedUser(t, st, orgID, "first")

		has, err = st.Users().HasAny(ctx)
		require.NoError(t, err)
		assert.True(t, has)
	})

	t.Run("count", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)

		count, err := st.Users().Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(0), count)

		SeedUser(t, st, orgID, "u1")
		SeedUser(t, st, orgID, "u2")

		count, err = st.Users().Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(2), count)
	})

	t.Run("count excludes soft-deleted users", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		user := SeedUser(t, st, orgID, "count-del-user")
		SeedUser(t, st, orgID, "count-alive-user")

		count, err := st.Users().Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(2), count)

		err = st.Users().Delete(ctx, user.ID)
		require.NoError(t, err)

		count, err = st.Users().Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(1), count)
	})

	t.Run("list by org id", func(t *testing.T) {
		st := s.NewStore(t)
		orgA := SeedOrg(t, st, "org-a", false)
		orgB := SeedOrg(t, st, "org-b", false)

		SeedUser(t, st, orgA, "in-a")
		SeedUser(t, st, orgB, "in-b")

		users, err := st.Users().ListByOrgID(ctx, orgA)
		require.NoError(t, err)
		require.Len(t, users, 1)
		assert.Equal(t, "in-a", users[0].Username)
	})

	t.Run("list all", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)

		// Sleep between creates to ensure distinct created_at timestamps
		// (some backends only have millisecond precision).
		SeedUser(t, st, orgID, "u1")
		time.Sleep(5 * time.Millisecond)
		SeedUser(t, st, orgID, "u2")
		time.Sleep(5 * time.Millisecond)
		SeedUser(t, st, orgID, "u3")

		// First page: limit 2.
		users, err := st.Users().ListAll(ctx, store.ListAllUsersParams{Limit: 2})
		require.NoError(t, err)
		assert.Len(t, users, 2)

		// Second page: use cursor from last item of first page.
		cursor := users[len(users)-1].CreatedAt.Format(time.RFC3339Nano)
		users, err = st.Users().ListAll(ctx, store.ListAllUsersParams{Cursor: cursor, Limit: 10})
		require.NoError(t, err)
		assert.Len(t, users, 1)
	})

	t.Run("search", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)

		SeedUser(t, st, orgID, "searchable-alice")
		SeedUser(t, st, orgID, "searchable-bob")
		SeedUser(t, st, orgID, "other-carol")

		q := "searchable"
		users, err := st.Users().Search(ctx, store.SearchUsersParams{
			Query: &q,
			Limit: 10,
		})
		require.NoError(t, err)
		assert.Len(t, users, 2)
	})

	t.Run("update profile", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		user := SeedUser(t, st, orgID, "profile-user")

		err := st.Users().UpdateProfile(ctx, store.UpdateUserProfileParams{
			ID:          user.ID,
			Username:    "new-username",
			DisplayName: "New Display",
		})
		require.NoError(t, err)

		updated, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, "new-username", updated.Username)
		assert.Equal(t, "New Display", updated.DisplayName)
	})

	t.Run("update password", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		user := SeedUser(t, st, orgID, "pw-user")

		err := st.Users().UpdatePassword(ctx, store.UpdateUserPasswordParams{
			ID:           user.ID,
			PasswordHash: "newhash",
		})
		require.NoError(t, err)

		updated, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, "newhash", updated.PasswordHash)
	})

	t.Run("update email", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		user := SeedUser(t, st, orgID, "email-user")

		err := st.Users().UpdateEmail(ctx, store.UpdateUserEmailParams{
			ID:            user.ID,
			Email:         "new@example.com",
			EmailVerified: false,
		})
		require.NoError(t, err)

		updated, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, "new@example.com", updated.Email)
		assert.False(t, updated.EmailVerified)
	})

	t.Run("update email verified", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)

		userID := id.Generate()
		err := st.Users().Create(ctx, store.CreateUserParams{
			ID:            userID,
			OrgID:         orgID,
			Username:      "unverified",
			PasswordHash:  "hash",
			DisplayName:   "Unverified",
			Email:         "unverified@example.com",
			EmailVerified: false,
			PasswordSet:   true,
			IsAdmin:       false,
		})
		require.NoError(t, err)

		err = st.Users().UpdateEmailVerified(ctx, store.UpdateUserEmailVerifiedParams{
			ID:            userID,
			EmailVerified: true,
		})
		require.NoError(t, err)

		updated, err := st.Users().GetByID(ctx, userID)
		require.NoError(t, err)
		assert.True(t, updated.EmailVerified)
	})

	t.Run("update admin", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		user := SeedUser(t, st, orgID, "admin-user")

		err := st.Users().UpdateAdmin(ctx, store.UpdateUserAdminParams{
			ID:      user.ID,
			IsAdmin: true,
		})
		require.NoError(t, err)

		updated, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.True(t, updated.IsAdmin)
	})

	t.Run("update prefs", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		user := SeedUser(t, st, orgID, "prefs-user")

		err := st.Users().UpdatePrefs(ctx, store.UpdateUserPrefsParams{
			ID:    user.ID,
			Prefs: `{"theme":"dark"}`,
		})
		require.NoError(t, err)

		prefs, err := st.Users().GetPrefs(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, `{"theme":"dark"}`, prefs)
	})

	t.Run("pending email lifecycle", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		user := SeedUser(t, st, orgID, "pending-email-user")

		token := id.Generate()
		expires := time.Now().Add(1 * time.Hour)
		err := st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
			ID:                    user.ID,
			PendingEmail:          "new-pending@example.com",
			PendingEmailToken:     token,
			PendingEmailExpiresAt: &expires,
		})
		require.NoError(t, err)

		// GetByPendingEmailToken should find the user.
		found, err := st.Users().GetByPendingEmailToken(ctx, token)
		require.NoError(t, err)
		assert.Equal(t, user.ID, found.ID)
		assert.Equal(t, "new-pending@example.com", found.PendingEmail)

		// PromotePendingEmail should update the email.
		err = st.Users().PromotePendingEmail(ctx, user.ID)
		require.NoError(t, err)

		promoted, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, "new-pending@example.com", promoted.Email)
		assert.Empty(t, promoted.PendingEmail)
		assert.Empty(t, promoted.PendingEmailToken)
	})

	t.Run("clear pending email", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		user := SeedUser(t, st, orgID, "clear-pending-user")

		token := id.Generate()
		expires := time.Now().Add(1 * time.Hour)
		err := st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
			ID:                    user.ID,
			PendingEmail:          "pending@example.com",
			PendingEmailToken:     token,
			PendingEmailExpiresAt: &expires,
		})
		require.NoError(t, err)

		err = st.Users().ClearPendingEmail(ctx, user.ID)
		require.NoError(t, err)

		updated, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.Empty(t, updated.PendingEmail)
		assert.Empty(t, updated.PendingEmailToken)
	})

	t.Run("clear competing pending emails", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		user1 := SeedUser(t, st, orgID, "compete1")
		user2 := SeedUser(t, st, orgID, "compete2")

		expires := time.Now().Add(1 * time.Hour)
		// Both users request the same pending email.
		for _, u := range []*store.User{user1, user2} {
			err := st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
				ID:                    u.ID,
				PendingEmail:          "contested@example.com",
				PendingEmailToken:     id.Generate(),
				PendingEmailExpiresAt: &expires,
			})
			require.NoError(t, err)
		}

		// Clear competing pending emails for user1 (keep user1, clear user2).
		err := st.Users().ClearCompetingPendingEmails(ctx, store.ClearCompetingPendingEmailsParams{
			PendingEmail: "contested@example.com",
			ExcludeID:    user1.ID,
		})
		require.NoError(t, err)

		u1, err := st.Users().GetByID(ctx, user1.ID)
		require.NoError(t, err)
		assert.Equal(t, "contested@example.com", u1.PendingEmail)

		u2, err := st.Users().GetByID(ctx, user2.ID)
		require.NoError(t, err)
		assert.Empty(t, u2.PendingEmail)
	})

	t.Run("delete (soft)", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		user := SeedUser(t, st, orgID, "deleteme")

		err := st.Users().Delete(ctx, user.ID)
		require.NoError(t, err)

		_, err = st.Users().GetByID(ctx, user.ID)
		assert.ErrorIs(t, err, store.ErrNotFound)

		// Include deleted should still find it.
		found, err := st.Users().GetByIDIncludeDeleted(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, user.ID, found.ID)
		assert.NotNil(t, found.DeletedAt)
	})

	t.Run("duplicate username returns conflict", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		SeedUser(t, st, orgID, "unique-name")

		err := st.Users().Create(ctx, store.CreateUserParams{
			ID:           id.Generate(),
			OrgID:        orgID,
			Username:     "unique-name",
			PasswordHash: "hash",
			DisplayName:  "Dup",
			Email:        "different@example.com",
			PasswordSet:  true,
		})
		assert.ErrorIs(t, err, store.ErrConflict)
	})

	t.Run("get by username not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.Users().GetByUsername(ctx, "nonexistent-user")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("get by email not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.Users().GetByEmail(ctx, "nonexistent@example.com")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("get prefs default", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		user := SeedUser(t, st, orgID, "default-prefs-user")

		prefs, err := st.Users().GetPrefs(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, "{}", prefs)
	})

	t.Run("deleted user excluded from get by username", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		user := SeedUser(t, st, orgID, "del-username-user")

		err := st.Users().Delete(ctx, user.ID)
		require.NoError(t, err)

		_, err = st.Users().GetByUsername(ctx, "del-username-user")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("deleted user excluded from get by email", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		user := SeedUser(t, st, orgID, "del-email-user")

		err := st.Users().Delete(ctx, user.ID)
		require.NoError(t, err)

		_, err = st.Users().GetByEmail(ctx, "del-email-user@example.com")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("deleted user excluded from list by org", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		alive := SeedUser(t, st, orgID, "alive-org-user")
		dead := SeedUser(t, st, orgID, "dead-org-user")

		err := st.Users().Delete(ctx, dead.ID)
		require.NoError(t, err)

		users, err := st.Users().ListByOrgID(ctx, orgID)
		require.NoError(t, err)
		require.Len(t, users, 1)
		assert.Equal(t, alive.ID, users[0].ID)
	})

	t.Run("deleted user excluded from list all", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		SeedUser(t, st, orgID, "alive-all-user")
		dead := SeedUser(t, st, orgID, "dead-all-user")

		err := st.Users().Delete(ctx, dead.ID)
		require.NoError(t, err)

		users, err := st.Users().ListAll(ctx, store.ListAllUsersParams{Limit: 100})
		require.NoError(t, err)
		require.Len(t, users, 1)
		assert.Equal(t, "alive-all-user", users[0].Username)
	})

	t.Run("deleted user excluded from search", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		SeedUser(t, st, orgID, "searchdel-alive")
		dead := SeedUser(t, st, orgID, "searchdel-dead")

		err := st.Users().Delete(ctx, dead.ID)
		require.NoError(t, err)

		q := "searchdel"
		users, err := st.Users().Search(ctx, store.SearchUsersParams{
			Query: &q,
			Limit: 100,
		})
		require.NoError(t, err)
		require.Len(t, users, 1)
		assert.Equal(t, "searchdel-alive", users[0].Username)
	})

	t.Run("duplicate email returns conflict", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		SeedUser(t, st, orgID, "email-orig")

		err := st.Users().Create(ctx, store.CreateUserParams{
			ID:           id.Generate(),
			OrgID:        orgID,
			Username:     "email-dup",
			PasswordHash: "hash",
			DisplayName:  "Dup Email",
			Email:        "email-orig@example.com", // Same email as email-orig
			PasswordSet:  true,
		})
		assert.ErrorIs(t, err, store.ErrConflict)
	})

	t.Run("search empty string query returns all", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		SeedUser(t, st, orgID, "sqall-a")
		SeedUser(t, st, orgID, "sqall-b")

		q := ""
		users, err := st.Users().Search(ctx, store.SearchUsersParams{
			Query: &q,
			Limit: 100,
		})
		require.NoError(t, err)
		assert.Len(t, users, 2)
	})

	t.Run("list all pagination", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		for i := 0; i < 5; i++ {
			if i > 0 {
				time.Sleep(5 * time.Millisecond)
			}
			SeedUser(t, st, orgID, "page-user-"+id.Generate()[:6])
		}

		// First page: limit 2.
		page1, err := st.Users().ListAll(ctx, store.ListAllUsersParams{Limit: 2})
		require.NoError(t, err)
		assert.Len(t, page1, 2)

		// Second page using cursor from last item.
		cursor := page1[len(page1)-1].CreatedAt.Format(time.RFC3339Nano)
		page2, err := st.Users().ListAll(ctx, store.ListAllUsersParams{Cursor: cursor, Limit: 2})
		require.NoError(t, err)
		assert.Len(t, page2, 2)
	})

	t.Run("list all cursor beyond total", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		SeedUser(t, st, orgID, "beyond-user")

		// Use a cursor far in the past to get no results.
		cursor := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
		users, err := st.Users().ListAll(ctx, store.ListAllUsersParams{Cursor: cursor, Limit: 10})
		require.NoError(t, err)
		require.NotNil(t, users)
		assert.Empty(t, users)
	})

	t.Run("has any after delete", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		user := SeedUser(t, st, orgID, "hasany-del-user")

		err := st.Users().Delete(ctx, user.ID)
		require.NoError(t, err)

		// HasAny should return false when all users are soft-deleted.
		has, err := st.Users().HasAny(ctx)
		require.NoError(t, err)
		assert.False(t, has)
	})

	t.Run("update profile username conflict", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		alice := SeedUser(t, st, orgID, "conflict-alice")
		SeedUser(t, st, orgID, "conflict-bob")

		err := st.Users().UpdateProfile(ctx, store.UpdateUserProfileParams{
			ID: alice.ID, Username: "conflict-bob", DisplayName: alice.DisplayName,
		})
		assert.ErrorIs(t, err, store.ErrConflict)
	})

	t.Run("update email conflict", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		alice := SeedUser(t, st, orgID, "email-conflict-alice")
		bob := SeedUser(t, st, orgID, "email-conflict-bob")

		err := st.Users().UpdateEmail(ctx, store.UpdateUserEmailParams{
			ID: alice.ID, Email: bob.Email, EmailVerified: true,
		})
		assert.ErrorIs(t, err, store.ErrConflict)
	})

	t.Run("reuse username after soft delete", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		user := SeedUser(t, st, orgID, "reuse-user")

		err := st.Users().Delete(ctx, user.ID)
		require.NoError(t, err)

		// Creating a new user with the same username should succeed.
		err = st.Users().Create(ctx, store.CreateUserParams{
			ID: id.Generate(), OrgID: orgID, Username: "reuse-user",
			PasswordHash: "hash", DisplayName: "Reuse", Email: "reuse-new@example.com",
			EmailVerified: true, PasswordSet: true, IsAdmin: false,
		})
		require.NoError(t, err)
	})

	t.Run("reuse email after soft delete", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		user := SeedUser(t, st, orgID, "reuse-email-user")

		err := st.Users().Delete(ctx, user.ID)
		require.NoError(t, err)

		// Creating a new user with the same email should succeed.
		err = st.Users().Create(ctx, store.CreateUserParams{
			ID: id.Generate(), OrgID: orgID, Username: "reuse-email-user2",
			PasswordHash: "hash", DisplayName: "Reuse", Email: user.Email,
			EmailVerified: true, PasswordSet: true, IsAdmin: false,
		})
		require.NoError(t, err)
	})

	t.Run("promote pending email conflicting with existing email", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		alice := SeedUser(t, st, orgID, "promote-conflict-alice")
		bob := SeedUser(t, st, orgID, "promote-conflict-bob")

		// Set bob's pending email to alice's email.
		expires := time.Now().Add(24 * time.Hour)
		err := st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
			ID:                    bob.ID,
			PendingEmail:          alice.Email,
			PendingEmailToken:     id.Generate(),
			PendingEmailExpiresAt: &expires,
		})
		require.NoError(t, err)

		// Promoting should fail with ErrConflict since alice already has that email.
		err = st.Users().PromotePendingEmail(ctx, bob.ID)
		assert.ErrorIs(t, err, store.ErrConflict)
	})

	t.Run("search finds user by exact username", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		SeedUser(t, st, orgID, "searchable-user")

		q := "searchable-user"
		users, err := st.Users().Search(ctx, store.SearchUsersParams{
			Query: &q, Limit: 10,
		})
		require.NoError(t, err)
		assert.Len(t, users, 1)
	})

	t.Run("delete already deleted user is no-op", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		user := SeedUser(t, st, orgID, "double-del-user")

		err := st.Users().Delete(ctx, user.ID)
		require.NoError(t, err)

		// Second delete should not error.
		err = st.Users().Delete(ctx, user.ID)
		require.NoError(t, err)
	})

	t.Run("search with nil query returns all", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		SeedUser(t, st, orgID, "nilq-user1")
		SeedUser(t, st, orgID, "nilq-user2")

		users, err := st.Users().Search(ctx, store.SearchUsersParams{
			Query: nil, Limit: 100,
		})
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(users), 2)
	})

	t.Run("update profile preserves email", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		user := SeedUser(t, st, orgID, "preserve-email-user")

		err := st.Users().UpdateProfile(ctx, store.UpdateUserProfileParams{
			ID: user.ID, Username: "new-username", DisplayName: "New Name",
		})
		require.NoError(t, err)

		found, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, "new-username", found.Username)
		assert.Equal(t, user.Email, found.Email, "email should be preserved")
	})

	t.Run("update email preserves username", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		user := SeedUser(t, st, orgID, "preserve-uname-user")

		err := st.Users().UpdateEmail(ctx, store.UpdateUserEmailParams{
			ID: user.ID, Email: "new-email@example.com", EmailVerified: true,
		})
		require.NoError(t, err)

		found, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, "new-email@example.com", found.Email)
		assert.Equal(t, user.Username, found.Username, "username should be preserved")
	})

	t.Run("search with cursor and limit", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		for i := 0; i < 5; i++ {
			if i > 0 {
				time.Sleep(5 * time.Millisecond)
			}
			SeedUser(t, st, orgID, fmt.Sprintf("pagesearch-%d", i))
		}

		q := "pagesearch"
		// First page.
		page1, err := st.Users().Search(ctx, store.SearchUsersParams{
			Query: &q, Limit: 2,
		})
		require.NoError(t, err)
		assert.Len(t, page1, 2)

		// Second page using cursor.
		cursor := page1[len(page1)-1].CreatedAt.Format(time.RFC3339Nano)
		page2, err := st.Users().Search(ctx, store.SearchUsersParams{
			Query: &q, Cursor: cursor, Limit: 2,
		})
		require.NoError(t, err)
		assert.Len(t, page2, 2)
	})

	t.Run("search is case insensitive", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		SeedUser(t, st, orgID, "MixedCaseSearchUser")

		q := "mixedcasesearchuser"
		users, err := st.Users().Search(ctx, store.SearchUsersParams{
			Query: &q, Limit: 10,
		})
		require.NoError(t, err)
		assert.Len(t, users, 1)
	})

	t.Run("search is prefix not substring", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		SeedUser(t, st, orgID, "alice")
		SeedUser(t, st, orgID, "superalice")

		// "alice" is a prefix of "alice" but NOT "superalice".
		q := "alice"
		users, err := st.Users().Search(ctx, store.SearchUsersParams{
			Query: &q, Limit: 10,
		})
		require.NoError(t, err)
		assert.Len(t, users, 1)
		assert.Equal(t, "alice", users[0].Username)
	})

	t.Run("username and email are case-normalized on create", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)

		userID := id.Generate()
		err := st.Users().Create(ctx, store.CreateUserParams{
			ID: userID, OrgID: orgID, Username: "MixedCase",
			PasswordHash: "hash", DisplayName: "Mixed", Email: "Mixed@Example.COM",
			EmailVerified: true, PasswordSet: true, IsAdmin: false,
		})
		require.NoError(t, err)

		// Stored username and email should be lowercase.
		user, err := st.Users().GetByID(ctx, userID)
		require.NoError(t, err)
		assert.Equal(t, "mixedcase", user.Username)
		assert.Equal(t, "mixed@example.com", user.Email)
	})

	t.Run("get by username is case-insensitive", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		SeedUser(t, st, orgID, "lookupuser")

		// Lookup with different case should find the user.
		user, err := st.Users().GetByUsername(ctx, "LookupUser")
		require.NoError(t, err)
		assert.Equal(t, "lookupuser", user.Username)
	})

	t.Run("get by email is case-insensitive", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		user := SeedUser(t, st, orgID, "emaillookup")

		// Lookup with different case should find the user.
		found, err := st.Users().GetByEmail(ctx, "EMAILLOOKUP@example.com")
		require.NoError(t, err)
		assert.Equal(t, user.ID, found.ID)
	})

	t.Run("mixed-case username conflicts with lowercase", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		SeedUser(t, st, orgID, "conflictcase")

		// Creating with different case should conflict.
		err := st.Users().Create(ctx, store.CreateUserParams{
			ID: id.Generate(), OrgID: orgID, Username: "ConflictCase",
			PasswordHash: "hash", DisplayName: "X", Email: "other@example.com",
			EmailVerified: true, PasswordSet: true, IsAdmin: false,
		})
		assert.ErrorIs(t, err, store.ErrConflict)
	})

	t.Run("search excludes deleted users without panic", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)

		// Create and immediately soft-delete users so the bucket table has
		// entries but the hydrated result set is empty.
		for i := 0; i < 3; i++ {
			u := SeedUser(t, st, orgID, fmt.Sprintf("del-search-%d", i))
			err := st.Users().Delete(ctx, u.ID)
			require.NoError(t, err)
		}

		q := "del-search"
		users, err := st.Users().Search(ctx, store.SearchUsersParams{
			Query: &q, Limit: 10,
		})
		require.NoError(t, err)
		assert.Empty(t, users)
	})

	t.Run("update profile conflict preserves original fields", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		alice := SeedUser(t, st, orgID, "rollback-profile-alice")
		SeedUser(t, st, orgID, "rollback-profile-bob")

		// Record alice's state before the conflicting update.
		before, err := st.Users().GetByID(ctx, alice.ID)
		require.NoError(t, err)

		// Try to rename alice to bob's username — should conflict.
		err = st.Users().UpdateProfile(ctx, store.UpdateUserProfileParams{
			ID: alice.ID, Username: "rollback-profile-bob", DisplayName: "Changed",
		})
		assert.ErrorIs(t, err, store.ErrConflict)

		// Verify alice's record is unchanged.
		after, err := st.Users().GetByID(ctx, alice.ID)
		require.NoError(t, err)
		assert.Equal(t, before.Username, after.Username, "username should be rolled back")
		assert.Equal(t, before.DisplayName, after.DisplayName, "display_name should be rolled back")
		assert.Equal(t, before.UpdatedAt.UnixMilli(), after.UpdatedAt.UnixMilli(), "updated_at should be rolled back")
	})

	t.Run("update email conflict preserves original fields", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		alice := SeedUser(t, st, orgID, "rollback-email-alice")
		bob := SeedUser(t, st, orgID, "rollback-email-bob")

		before, err := st.Users().GetByID(ctx, alice.ID)
		require.NoError(t, err)

		// Try to change alice's email to bob's — should conflict.
		err = st.Users().UpdateEmail(ctx, store.UpdateUserEmailParams{
			ID: alice.ID, Email: bob.Email, EmailVerified: false,
		})
		assert.ErrorIs(t, err, store.ErrConflict)

		after, err := st.Users().GetByID(ctx, alice.ID)
		require.NoError(t, err)
		assert.Equal(t, before.Email, after.Email, "email should be rolled back")
		assert.Equal(t, before.EmailVerified, after.EmailVerified, "email_verified should be rolled back")
		assert.Equal(t, before.UpdatedAt.UnixMilli(), after.UpdatedAt.UnixMilli(), "updated_at should be rolled back")
	})

	t.Run("promote pending email conflict preserves original fields", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "user-org", true)
		alice := SeedUser(t, st, orgID, "rollback-promote-alice")
		bob := SeedUser(t, st, orgID, "rollback-promote-bob")

		// Set bob's pending email to alice's email.
		token := id.Generate()
		expires := time.Now().Add(24 * time.Hour)
		err := st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
			ID:                    bob.ID,
			PendingEmail:          alice.Email,
			PendingEmailToken:     token,
			PendingEmailExpiresAt: &expires,
		})
		require.NoError(t, err)

		before, err := st.Users().GetByID(ctx, bob.ID)
		require.NoError(t, err)

		// Promoting should fail since alice already has that email.
		err = st.Users().PromotePendingEmail(ctx, bob.ID)
		assert.ErrorIs(t, err, store.ErrConflict)

		// Verify bob's record is unchanged — pending_email, token, and
		// updated_at should all be preserved.
		after, err := st.Users().GetByID(ctx, bob.ID)
		require.NoError(t, err)
		assert.Equal(t, before.Email, after.Email, "email should be rolled back")
		assert.Equal(t, before.EmailVerified, after.EmailVerified, "email_verified should be rolled back")
		assert.Equal(t, before.PendingEmail, after.PendingEmail, "pending_email should be preserved")
		assert.Equal(t, before.PendingEmailToken, after.PendingEmailToken, "pending_email_token should be preserved")
		assert.Equal(t, before.UpdatedAt.UnixMilli(), after.UpdatedAt.UnixMilli(), "updated_at should be rolled back")
	})
}

package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedMembershipAdministrator(t *testing.T, st store.Store, userID string) {
	t.Helper()
	require.NoError(t, st.Users().Create(t.Context(), store.CreateUserParams{
		ID: userID, Username: userID, PasswordHash: "hash", DisplayName: userID,
		FirstCredentialExempt: true, IsAdmin: true,
	}))
}

func mutateMembership(ctx context.Context, st store.Store, userID, operation string) error {
	if operation == "delete" {
		return st.Users().Delete(ctx, userID)
	}
	return st.Users().UpdateAdmin(ctx, store.UpdateUserAdminParams{ID: userID, IsAdmin: false})
}

func (s *Suite) testAdministratorMembership(t *testing.T) {
	t.Run("locking removal requires a transaction", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.Users().LockForRemoval(t.Context(), "absent")
		require.ErrorIs(t, err, store.ErrLockingReadOutsideTransaction)
	})
	for _, operation := range []string{"delete", "demote"} {
		t.Run(operation+" refuses the last live administrator", func(t *testing.T) {
			st := s.NewStore(t)
			seedMembershipAdministrator(t, st, "remaining-admin")
			seedMembershipAdministrator(t, st, "deleted-admin")
			require.NoError(t, st.Users().Delete(t.Context(), "deleted-admin"))
			before, err := st.Users().GetByID(t.Context(), "remaining-admin")
			require.NoError(t, err)
			require.ErrorIs(t, mutateMembership(t.Context(), st, "remaining-admin", operation), store.ErrLastAdministrator)
			after, err := st.Users().GetByID(t.Context(), "remaining-admin")
			require.NoError(t, err)
			assert.True(t, after.IsAdmin)
			assert.Equal(t, before.AuthGeneration, after.AuthGeneration)
		})
		t.Run(operation+" allows an ordinary user", func(t *testing.T) {
			st := s.NewStore(t)
			seedMembershipAdministrator(t, st, "remaining-admin")
			ordinary := SeedUser(t, st, "ordinary")
			require.False(t, ordinary.IsAdmin)
			require.NoError(t, mutateMembership(t.Context(), st, ordinary.ID, operation))
			remaining, err := st.Users().GetByIDIncludeDeleted(t.Context(), ordinary.ID)
			require.NoError(t, err)
			if operation == "delete" {
				assert.NotNil(t, remaining.DeletedAt)
			} else {
				assert.Nil(t, remaining.DeletedAt)
				assert.False(t, remaining.IsAdmin)
			}
			admin, err := st.Users().GetFirstAdmin(t.Context())
			require.NoError(t, err)
			assert.Equal(t, "remaining-admin", admin.ID)
		})
		t.Run(operation+" preserves missing-user behavior", func(t *testing.T) {
			st := s.NewStore(t)
			require.NoError(t, mutateMembership(t.Context(), st, "absent", operation))
		})
	}
	for _, operations := range [][2]string{{"delete", "delete"}, {"demote", "demote"}, {"delete", "demote"}, {"demote", "delete"}} {
		t.Run(operations[0]+" and "+operations[1]+" retain an administrator", func(t *testing.T) {
			st := s.NewStore(t)
			userIDs := [2]string{"first-admin", "second-admin"}
			for _, userID := range userIDs {
				seedMembershipAdministrator(t, st, userID)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			start := make(chan struct{})
			results := make(chan error, 2)
			for index, userID := range userIDs {
				go func() {
					<-start
					results <- mutateMembership(ctx, st, userID, operations[index])
				}()
			}
			close(start)
			succeeded, refused := 0, 0
			for range 2 {
				select {
				case err := <-results:
					switch {
					case err == nil:
						succeeded++
					case errors.Is(err, store.ErrLastAdministrator):
						refused++
					default:
						t.Errorf("administrator mutation failed: %v", err)
					}
				case <-ctx.Done():
					t.Fatal("administrator mutations did not finish:", ctx.Err())
				}
			}
			assert.Equal(t, 1, succeeded)
			assert.Equal(t, 1, refused)
			liveAdmins := 0
			for _, userID := range userIDs {
				user, err := st.Users().GetByIDIncludeDeleted(t.Context(), userID)
				require.NoError(t, err)
				if user.IsAdmin && user.DeletedAt == nil {
					liveAdmins++
				}
			}
			assert.Equal(t, 1, liveAdmins)
		})
	}
	for _, operation := range []string{"delete", "demote"} {
		t.Run(operation+" reads current administrator membership after an older snapshot", func(t *testing.T) {
			if !s.ConcurrentWriteTransactions {
				t.Skip("this backend cannot hold two write transactions open")
			}
			st := s.NewStore(t)
			seedMembershipAdministrator(t, st, "original-admin")
			target := SeedUser(t, st, "promoted-after-snapshot")
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			snapshot, release := make(chan struct{}), make(chan struct{})
			var snapshotOnce, releaseOnce sync.Once
			result := make(chan error, 1)
			go func() {
				result <- st.RunInTransaction(ctx, func(tx store.Store) error {
					if _, err := tx.Users().GetByID(ctx, target.ID); err != nil {
						return err
					}
					snapshotOnce.Do(func() { close(snapshot) })
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
					return mutateMembership(ctx, tx, target.ID, operation)
				})
			}()
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			select {
			case <-snapshot:
			case <-ctx.Done():
				t.Fatal("the transaction did not read its initial snapshot:", ctx.Err())
			}
			require.NoError(t, st.Users().UpdateAdmin(ctx, store.UpdateUserAdminParams{ID: target.ID, IsAdmin: true}))
			require.NoError(t, st.Users().Delete(ctx, "original-admin"))
			releaseOnce.Do(func() { close(release) })
			select {
			case err := <-result:
				require.ErrorIs(t, err, store.ErrLastAdministrator)
			case <-ctx.Done():
				t.Fatal("the administrator mutation did not finish:", ctx.Err())
			}
			remaining, err := st.Users().GetFirstAdmin(ctx)
			require.NoError(t, err)
			assert.Equal(t, target.ID, remaining.ID)
		})
	}
}

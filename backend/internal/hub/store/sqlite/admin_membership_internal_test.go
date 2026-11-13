package sqlite

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrdinaryUserRemovalDoesNotAccessTheLifecycleTable(t *testing.T) {
	for _, operation := range []string{"delete", "demote"} {
		for _, nested := range []bool{false, true} {
			t.Run(operation+map[bool]string{false: "/direct", true: "/nested"}[nested], func(t *testing.T) {
				st, err := Open(filepath.Join(t.TempDir(), "hub.db"), sqlitedb.Config{})
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, st.Close()) })
				admin := storetest.SeedUser(t, st, "administrator")
				require.NoError(t, st.Users().UpdateAdmin(t.Context(), store.UpdateUserAdminParams{ID: admin.ID, IsAdmin: true}))
				user := storetest.SeedUser(t, st, "ordinary")
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				mutex := &st.(*sqliteStore).conn.shared.adminMembership
				require.NoError(t, mutex.Lock(ctx))
				release := sync.OnceFunc(mutex.Unlock)
				t.Cleanup(release)
				remove := func(tx store.Store) error {
					if operation == "delete" {
						return tx.Users().Delete(ctx, user.ID)
					}
					return tx.Users().UpdateAdmin(ctx, store.UpdateUserAdminParams{ID: user.ID, IsAdmin: false})
				}
				if nested {
					err = st.RunInUserAuthTransaction(ctx, userid.MustNew(user.ID), remove)
				} else {
					err = remove(st)
				}
				require.NoError(t, err, "ordinary removal must not access the lifecycle table")
				stored, err := st.Users().GetByIDIncludeDeleted(t.Context(), user.ID)
				require.NoError(t, err)
				if operation == "delete" {
					assert.NotNil(t, stored.DeletedAt)
				} else {
					assert.Nil(t, stored.DeletedAt)
					assert.False(t, stored.IsAdmin)
				}
				remaining, err := st.Users().GetFirstAdmin(t.Context())
				require.NoError(t, err)
				assert.Equal(t, admin.ID, remaining.ID)
				require.NoError(t, st.Users().Delete(t.Context(), "absent"))
				if operation == "delete" {
					require.NoError(t, st.Users().Delete(t.Context(), user.ID))
				}
				release()
				require.ErrorIs(t, st.Users().Delete(t.Context(), admin.ID), store.ErrLastAdministrator)
				_, err = st.Users().GetByID(t.Context(), admin.ID)
				require.NoError(t, err)
			})
		}
	}
}

func openAdministratorRemovalTestStore(t *testing.T) (*sqliteStore, [2]string) {
	t.Helper()
	st, err := Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	var ids [2]string
	for index, name := range []string{"first-admin", "second-admin"} {
		user := storetest.SeedUser(t, st, name)
		ids[index] = user.ID
		require.NoError(t, st.Users().UpdateAdmin(t.Context(), store.UpdateUserAdminParams{ID: user.ID, IsAdmin: true}))
	}
	return st.(*sqliteStore), ids
}

func TestHubSchemaDoesNotCreateAnAccountLifecycleTable(t *testing.T) {
	st, _ := openAdministratorRemovalTestStore(t)
	var count int
	err := st.conn.exec.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'account_lifecycle_lock'").Scan(&count)
	require.NoError(t, err)
	assert.Zero(t, count, "administrator coordination must not require a SQL singleton table")
}

func TestAdministratorRemovalRestartsTheWholeTransaction(t *testing.T) {
	st, admins := openAdministratorRemovalTestStore(t)
	attempts := 0
	err := st.RunInTransaction(t.Context(), func(tx store.Store) error {
		attempts++
		if attempts == 1 {
			assert.False(t, tx.(*sqliteStore).conn.adminMembershipLocked)
		} else {
			assert.True(t, tx.(*sqliteStore).conn.adminMembershipLocked, "the administrator mutex must precede callback work on the retry")
			if st.conn.shared.adminMembership.TryLock() {
				st.conn.shared.adminMembership.Unlock()
				t.Error("the shared administrator mutex must be held during the retry")
			}
		}
		// A fixed unique row makes a partial retry fail. The outer transaction
		// must roll this write back before invoking the entire callback again.
		if err := tx.Users().Create(t.Context(), store.CreateUserParams{
			ID: "rollback-witness", Username: "rollback-witness", PasswordHash: "hash", FirstCredentialExempt: true,
		}); err != nil {
			return err
		}
		return tx.RunInUserAuthTransaction(t.Context(), userid.MustNew(admins[0]), func(locked store.Store) error {
			return locked.Users().Delete(t.Context(), admins[0])
		})
	})
	require.NoError(t, err)
	assert.Equal(t, 2, attempts)
	_, err = st.Users().GetByID(t.Context(), admins[0])
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.Users().GetByID(t.Context(), "rollback-witness")
	require.NoError(t, err)
	count, err := st.Users().Count(t.Context())
	require.NoError(t, err)
	assert.EqualValues(t, 2, count)

	require.True(t, st.conn.shared.adminMembership.TryLock(), "commit must release the administrator mutex")
	st.conn.shared.adminMembership.Unlock()
	require.NoError(t, st.RunInTransaction(t.Context(), func(tx store.Store) error {
		assert.False(t, tx.(*sqliteStore).conn.adminMembershipLocked, "a completed transaction cannot lend its mutex ownership to the next one")
		return nil
	}))
	require.ErrorIs(t, st.Users().Delete(t.Context(), admins[1]), store.ErrLastAdministrator)
	_, err = st.Users().GetByID(t.Context(), admins[1])
	require.NoError(t, err)
}

func TestAdministratorRemovalPanicReleasesTheMutexAndRollsBack(t *testing.T) {
	st, admins := openAdministratorRemovalTestStore(t)
	require.Panics(t, func() {
		_ = st.RunInTransaction(t.Context(), func(tx store.Store) error {
			if _, err := tx.Users().LockForRemoval(t.Context(), admins[0]); err != nil {
				return err
			}
			if err := tx.Users().Create(t.Context(), store.CreateUserParams{
				ID: "panic-witness", Username: "panic-witness", PasswordHash: "hash", FirstCredentialExempt: true,
			}); err != nil {
				return err
			}
			panic("controlled removal panic")
		})
	})
	_, err := st.Users().GetByID(t.Context(), "panic-witness")
	require.ErrorIs(t, err, store.ErrNotFound)
	require.True(t, st.conn.shared.adminMembership.TryLock(), "panic must release the administrator mutex")
	st.conn.shared.adminMembership.Unlock()
	require.NoError(t, st.Users().Delete(t.Context(), admins[0]))
}

func TestOrdinaryRemovalFailureDoesNotRestartOrAcquireTheAdministratorMutex(t *testing.T) {
	st, _ := openAdministratorRemovalTestStore(t)
	user := storetest.SeedUser(t, st, "failed-removal")
	_, err := st.conn.exec.ExecContext(t.Context(), `CREATE TRIGGER refuse_deletion BEFORE UPDATE OF deleted_at ON users BEGIN SELECT RAISE(ABORT, 'controlled deletion failure'); END`)
	require.NoError(t, err)
	attempts := 0
	err = st.RunInUserAuthTransaction(t.Context(), userid.MustNew(user.ID), func(tx store.Store) error {
		attempts++
		return tx.Users().Delete(t.Context(), user.ID)
	})
	require.ErrorContains(t, err, "controlled deletion failure")
	assert.Equal(t, 1, attempts)
	_, err = st.Users().GetByID(t.Context(), user.ID)
	require.NoError(t, err)
}

func TestCancelledRemovalCannotRestartWithTheAdministratorMutex(t *testing.T) {
	st, admins := openAdministratorRemovalTestStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	attempts := 0
	err := st.RunInTransaction(ctx, func(tx store.Store) error {
		attempts++
		_, err := tx.Users().LockForRemoval(ctx, admins[0])
		if err != nil {
			cancel()
		}
		return err
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, attempts)
	for _, id := range admins {
		user, err := st.Users().GetByID(t.Context(), id)
		require.NoError(t, err)
		assert.True(t, user.IsAdmin)
	}
}

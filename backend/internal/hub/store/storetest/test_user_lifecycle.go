package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testAccountLifecycle(t *testing.T) {
	t.Run("requires a transaction and survives truncation", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.Users().LockAccountLifecycle(t.Context())
		require.ErrorIs(t, err, store.ErrLockingReadOutsideTransaction)
		SeedUser(t, st, "before-lifecycle-reset")
		require.NoError(t, st.TestHelper().TruncateAll(t.Context()))
		require.NoError(t, st.RunInTransaction(t.Context(), func(tx store.Store) error {
			exists, err := tx.Users().LockAccountLifecycle(t.Context())
			require.NoError(t, err)
			require.False(t, exists)
			SeedUser(t, tx, "after-lifecycle-reset")
			exists, err = tx.Users().LockAccountLifecycle(t.Context())
			require.NoError(t, err)
			require.True(t, exists)
			return nil
		}))
	})
	t.Run("rollback releases the lock without publishing an account", func(t *testing.T) {
		st := s.NewStore(t)
		failure := errors.New("rollback lifecycle transaction")
		err := st.RunInTransaction(t.Context(), func(tx store.Store) error {
			_, err := tx.Users().LockAccountLifecycle(t.Context())
			if err != nil {
				return err
			}
			SeedUser(t, tx, "rolled-back-admission")
			return failure
		})
		require.ErrorIs(t, err, failure)
		require.NoError(t, st.RunInTransaction(t.Context(), func(tx store.Store) error {
			exists, err := tx.Users().LockAccountLifecycle(t.Context())
			require.NoError(t, err)
			assert.False(t, exists)
			return nil
		}))
	})
	t.Run("excludes an independent transaction until cancellation", func(t *testing.T) {
		st := s.NewStore(t)
		holding, release := make(chan struct{}), make(chan struct{})
		firstResult := make(chan error, 1)
		var releaseOnce sync.Once
		firstCtx, cancelFirst := context.WithCancel(context.WithoutCancel(t.Context()))
		t.Cleanup(cancelFirst)
		go func() {
			firstResult <- st.RunInTransaction(firstCtx, func(tx store.Store) error {
				if _, err := tx.Users().LockAccountLifecycle(firstCtx); err != nil {
					return err
				}
				close(holding)
				select {
				case <-release:
					return nil
				case <-firstCtx.Done():
					return firstCtx.Err()
				}
			})
		}()
		t.Cleanup(func() { releaseOnce.Do(func() { close(release) }); require.NoError(t, <-firstResult) })
		readyCtx := testutil.DeadlineContext(t)
		select {
		case <-holding:
		case <-readyCtx.Done():
			t.Fatal(readyCtx.Err())
		}
		// Keep the first real transaction open for the second operation's entire
		// deadline. A missing lock returns success and fails this assertion.
		waiting, cancelWaiting := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancelWaiting()
		err := st.RunInTransaction(waiting, func(tx store.Store) error {
			_, err := tx.Users().LockAccountLifecycle(waiting)
			return err
		})
		require.Error(t, err, "a second transaction must not acquire the held lifecycle lock")
		require.ErrorIs(t, waiting.Err(), context.DeadlineExceeded)
	})
	t.Run("reads current users after an older transaction snapshot", func(t *testing.T) {
		if !s.ConcurrentWriteTransactions {
			t.Skip("this backend cannot hold two write transactions open")
		}
		st := s.NewStore(t)
		snapshot, committed := make(chan struct{}), make(chan struct{})
		result := make(chan error, 1)
		ctx := testutil.DeadlineContext(t)
		var committedOnce sync.Once
		t.Cleanup(func() { committedOnce.Do(func() { close(committed) }) })
		go func() {
			result <- st.RunInTransaction(ctx, func(tx store.Store) error {
				exists, err := tx.Users().HasAny(ctx)
				if err != nil {
					return err
				}
				if exists {
					return errors.New("snapshot unexpectedly contains a user")
				}
				select {
				case snapshot <- struct{}{}:
				case <-ctx.Done():
					return ctx.Err()
				}
				select {
				case <-committed:
				case <-ctx.Done():
					return ctx.Err()
				}
				exists, err = tx.Users().LockAccountLifecycle(ctx)
				if err != nil {
					return err
				}
				if !exists {
					return errors.New("lifecycle admission used the stale transaction snapshot")
				}
				return nil
			})
		}()
		select {
		case <-snapshot:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
			_, err := tx.Users().LockAccountLifecycle(ctx)
			if err != nil {
				return err
			}
			SeedUser(t, tx, "committed-after-snapshot")
			return nil
		}))
		committedOnce.Do(func() { close(committed) })
		select {
		case err := <-result:
			require.NoError(t, err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	})
}

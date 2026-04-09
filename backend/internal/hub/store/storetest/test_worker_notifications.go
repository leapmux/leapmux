package storetest

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testWorkerNotifications(t *testing.T) {
	t.Run("create and list pending", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wn-org", true)
		user := SeedUser(t, st, orgID, "wn-user")
		worker := SeedWorker(t, st, user.ID)

		notifID := id.Generate()
		err := st.WorkerNotifications().Create(ctx, store.CreateWorkerNotificationParams{
			ID:       notifID,
			WorkerID: worker.ID,
			Type:     leapmuxv1.NotificationType_NOTIFICATION_TYPE_DEREGISTER,
			Payload:  `{"reason":"test"}`,
		})
		require.NoError(t, err)

		notifs, err := st.WorkerNotifications().ListPendingByWorker(ctx, worker.ID)
		require.NoError(t, err)
		require.Len(t, notifs, 1)
		assert.Equal(t, notifID, notifs[0].ID)
		assert.Equal(t, worker.ID, notifs[0].WorkerID)
		assert.Equal(t, leapmuxv1.NotificationType_NOTIFICATION_TYPE_DEREGISTER, notifs[0].Type)
		assert.Equal(t, `{"reason":"test"}`, notifs[0].Payload)
		assert.Equal(t, leapmuxv1.NotificationStatus_NOTIFICATION_STATUS_PENDING, notifs[0].Status)
	})

	t.Run("mark delivered", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wn-org", true)
		user := SeedUser(t, st, orgID, "wn-deliver-user")
		worker := SeedWorker(t, st, user.ID)

		notifID := id.Generate()
		err := st.WorkerNotifications().Create(ctx, store.CreateWorkerNotificationParams{
			ID:       notifID,
			WorkerID: worker.ID,
			Type:     leapmuxv1.NotificationType_NOTIFICATION_TYPE_DEREGISTER,
			Payload:  "{}",
		})
		require.NoError(t, err)

		err = st.WorkerNotifications().MarkDelivered(ctx, notifID)
		require.NoError(t, err)

		// After delivery, it should no longer appear in pending list.
		notifs, err := st.WorkerNotifications().ListPendingByWorker(ctx, worker.ID)
		require.NoError(t, err)
		require.NotNil(t, notifs)
		assert.Empty(t, notifs)
	})

	t.Run("mark failed", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wn-org", true)
		user := SeedUser(t, st, orgID, "wn-fail-user")
		worker := SeedWorker(t, st, user.ID)

		notifID := id.Generate()
		err := st.WorkerNotifications().Create(ctx, store.CreateWorkerNotificationParams{
			ID:       notifID,
			WorkerID: worker.ID,
			Type:     leapmuxv1.NotificationType_NOTIFICATION_TYPE_DEREGISTER,
			Payload:  "{}",
		})
		require.NoError(t, err)

		err = st.WorkerNotifications().MarkFailed(ctx, notifID)
		require.NoError(t, err)

		// Failed notifications should not appear in pending list.
		notifs, err := st.WorkerNotifications().ListPendingByWorker(ctx, worker.ID)
		require.NoError(t, err)
		require.NotNil(t, notifs)
		assert.Empty(t, notifs)
	})

	t.Run("increment attempts", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wn-org", true)
		user := SeedUser(t, st, orgID, "wn-attempts-user")
		worker := SeedWorker(t, st, user.ID)

		notifID := id.Generate()
		err := st.WorkerNotifications().Create(ctx, store.CreateWorkerNotificationParams{
			ID:       notifID,
			WorkerID: worker.ID,
			Type:     leapmuxv1.NotificationType_NOTIFICATION_TYPE_DEREGISTER,
			Payload:  "{}",
		})
		require.NoError(t, err)

		err = st.WorkerNotifications().IncrementAttempts(ctx, notifID)
		require.NoError(t, err)
		err = st.WorkerNotifications().IncrementAttempts(ctx, notifID)
		require.NoError(t, err)

		notifs, err := st.WorkerNotifications().ListPendingByWorker(ctx, worker.ID)
		require.NoError(t, err)
		require.Len(t, notifs, 1)
		assert.Equal(t, int64(2), notifs[0].Attempts)
	})

	t.Run("list pending empty", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wn-org", true)
		user := SeedUser(t, st, orgID, "wn-empty-user")
		worker := SeedWorker(t, st, user.ID)

		notifs, err := st.WorkerNotifications().ListPendingByWorker(ctx, worker.ID)
		require.NoError(t, err)
		require.NotNil(t, notifs)
		assert.Empty(t, notifs)
	})

	t.Run("mark delivered removes from pending", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wn-org", true)
		user := SeedUser(t, st, orgID, "wn-deliver2-user")
		worker := SeedWorker(t, st, user.ID)

		n1 := id.Generate()
		n2 := id.Generate()
		for _, nID := range []string{n1, n2} {
			err := st.WorkerNotifications().Create(ctx, store.CreateWorkerNotificationParams{
				ID:       nID,
				WorkerID: worker.ID,
				Type:     leapmuxv1.NotificationType_NOTIFICATION_TYPE_DEREGISTER,
				Payload:  "{}",
			})
			require.NoError(t, err)
		}

		// Deliver one notification.
		err := st.WorkerNotifications().MarkDelivered(ctx, n1)
		require.NoError(t, err)

		// Only the other should remain pending.
		notifs, err := st.WorkerNotifications().ListPendingByWorker(ctx, worker.ID)
		require.NoError(t, err)
		require.Len(t, notifs, 1)
		assert.Equal(t, n2, notifs[0].ID)
	})

	t.Run("duplicate notification id returns conflict", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "notif-org", true)
		user := SeedUser(t, st, orgID, "notif-dup-user")
		worker := SeedWorker(t, st, user.ID)

		notifID := id.Generate()
		err := st.WorkerNotifications().Create(ctx, store.CreateWorkerNotificationParams{
			ID:       notifID,
			WorkerID: worker.ID,
			Type:     leapmuxv1.NotificationType_NOTIFICATION_TYPE_DEREGISTER,
			Payload:  "{}",
		})
		require.NoError(t, err)

		err = st.WorkerNotifications().Create(ctx, store.CreateWorkerNotificationParams{
			ID:       notifID,
			WorkerID: worker.ID,
			Type:     leapmuxv1.NotificationType_NOTIFICATION_TYPE_DEREGISTER,
			Payload:  "{}",
		})
		assert.ErrorIs(t, err, store.ErrConflict)
	})

	t.Run("increment attempts multiple", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wn-org", true)
		user := SeedUser(t, st, orgID, "wn-incr-user")
		worker := SeedWorker(t, st, user.ID)

		notifID := id.Generate()
		err := st.WorkerNotifications().Create(ctx, store.CreateWorkerNotificationParams{
			ID:       notifID,
			WorkerID: worker.ID,
			Type:     leapmuxv1.NotificationType_NOTIFICATION_TYPE_DEREGISTER,
			Payload:  "{}",
		})
		require.NoError(t, err)

		for i := 0; i < 3; i++ {
			err = st.WorkerNotifications().IncrementAttempts(ctx, notifID)
			require.NoError(t, err)
		}

		notifs, err := st.WorkerNotifications().ListPendingByWorker(ctx, worker.ID)
		require.NoError(t, err)
		require.Len(t, notifs, 1)
		assert.Equal(t, int64(3), notifs[0].Attempts)
	})
}

package hub

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
)

func TestRegisterWorkerRequiresALiveOwner(t *testing.T) {
	t.Parallel()
	for _, deleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "live owner", true: "deleted owner"}[deleted], func(t *testing.T) {
			st := hubtestutil.OpenTestStore(t)
			ctx := context.Background()
			owner := storetest.SeedUser(t, st, "auto-worker-owner")
			if deleted {
				require.NoError(t, st.Users().Delete(ctx, owner.ID))
			}
			credentials, err := (&Server{store: st}).RegisterWorker(ctx, owner.ID)
			if deleted {
				assert.ErrorIs(t, err, store.ErrNotFound)
				assert.Nil(t, credentials)
			} else {
				require.NoError(t, err)
				worker, err := st.Workers().GetByID(ctx, credentials.WorkerID)
				require.NoError(t, err)
				assert.True(t, worker.AutoRegistered)
				assert.Equal(t, owner.ID, worker.RegisteredBy)
			}
		})
	}
}

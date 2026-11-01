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
				// The co-located worker connects with the token it was handed,
				// so that token must resolve to this row.
				byToken, err := st.Workers().GetByAuthToken(ctx, credentials.AuthToken)
				require.NoError(t, err)
				assert.Equal(t, credentials.WorkerID, byToken.ID)
			}
		})
	}
}

// A worker with a blank registrant would be owned by nobody, so its real
// owner's gate would refuse it for the life of the process.
func TestRegisterWorkerRefusesABlankRegistrant(t *testing.T) {
	t.Parallel()
	st := hubtestutil.OpenTestStore(t)

	credentials, err := (&Server{store: st}).RegisterWorker(context.Background(), "")
	require.ErrorContains(t, err, "registeredBy is required")
	assert.Nil(t, credentials)
}

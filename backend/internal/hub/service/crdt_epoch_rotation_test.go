package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// epochClock is a mutex-guarded test clock that every manager in a test reads.
type epochClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *epochClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *epochClock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// storedEpoch reads the persisted UserCrdtState blob and returns its epoch
// header, or fails the test when no state row exists.
func storedEpoch(t *testing.T, st store.Store, owner userid.UserID) (epoch int64, startedAt *time.Time) {
	t.Helper()
	row, err := st.UserState().Get(context.Background(), owner)
	require.NoError(t, err, "the user's state blob must be persisted")
	state := &leapmuxv1.UserCrdtState{}
	require.NoError(t, proto.Unmarshal(row.StatePayload, state))
	if ts := state.GetEpochStartedAt(); ts != nil {
		at := ts.AsTime()
		startedAt = &at
	}
	return state.GetCurrentEpoch(), startedAt
}

// TestEpochRotationSurvivesARestart is the regression test for an epoch that
// never advanced. Bootstrap left EpochStartedAt nil, the advance returned early
// on nil, and an advance would have been written only to user_state columns
// that LoadState never reads, so a restart lost it. The epoch start must be
// stamped once, persisted in the blob, kept across restarts, and rotated after
// EpochDuration -- by the hub that is running at the time, and visible to the
// next one.
//
// Each manager is used without Start: TickHousekeeping before Start runs the
// pass inline, and Stop would wait forever on a goroutine that never ran.
func TestEpochRotationSurvivesARestart(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	owner := userid.MustNew(storetest.SeedUser(t, st, "epoch-rotation").ID)
	ctx := context.Background()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &epochClock{now: t0}
	newManager := func() *crdt.Manager {
		return crdt.NewManager(owner, service.NewCRDTJournal(st), allowAllAuth{}, nil, clock.Now)
	}

	// 1. A fresh user: Bootstrap stamps the start, and the first pass persists
	// it even though the user committed nothing.
	first := newManager()
	require.NoError(t, first.Bootstrap(ctx))
	first.TickHousekeeping(ctx)
	epoch, startedAt := storedEpoch(t, st, owner)
	assert.Equal(t, int64(1), epoch)
	require.NotNil(t, startedAt, "the persisted blob must carry the epoch start")
	assert.True(t, t0.Equal(*startedAt), "the stored start is the bootstrap time: got %v, want %v", *startedAt, t0)

	// 2. A restart an hour later keeps the stored start rather than minting a
	// new one, and a pass writes nothing that would move it.
	clock.Set(t0.Add(time.Hour))
	second := newManager()
	require.NoError(t, second.Bootstrap(ctx))
	second.TickHousekeeping(ctx)
	epoch, startedAt = storedEpoch(t, st, owner)
	assert.Equal(t, int64(1), epoch)
	require.NotNil(t, startedAt)
	assert.True(t, t0.Equal(*startedAt), "a restart must keep the stored start: got %v, want %v", *startedAt, t0)

	// 3. Past EpochDuration, the running hub rotates, and the next hub sees the
	// rotation.
	clock.Set(t0.Add(crdt.EpochDuration + time.Minute))
	second.TickHousekeeping(ctx)
	third := newManager()
	require.NoError(t, third.Bootstrap(ctx))
	assert.Equal(t, int64(2), third.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch(),
		"the rotation must survive the restart")
	epoch, startedAt = storedEpoch(t, st, owner)
	assert.Equal(t, int64(2), epoch)
	require.NotNil(t, startedAt)
	assert.True(t, t0.Add(crdt.EpochDuration).Equal(*startedAt),
		"the new epoch starts where the old one ended: got %v, want %v", *startedAt, t0.Add(crdt.EpochDuration))
}

package crdt_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// epochT0 is where every test in this file starts its clock.
var epochT0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

var errCompactionInjected = errors.New("fake journal: injected compaction failure")

// epochTestClock is a settable clock. It does not move on its own, so a test
// decides exactly how much time has passed between two passes.
type epochTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *epochTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *epochTestClock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// bootstrapEpochManager builds and bootstraps a manager that is NOT started.
// TickHousekeeping runs inline on such a manager. The test must never call
// Stop on it: Stop waits for a goroutine that never ran.
func bootstrapEpochManager(t *testing.T, j *fakeJournal, clock *epochTestClock) *crdt.Manager {
	t.Helper()
	mgr := crdt.NewManager(userid.MustNew("user-1"), j, allowAll{}, nil, clock.Now)
	require.NoError(t, mgr.Bootstrap(context.Background()))
	return mgr
}

// startEpochManager builds, bootstraps and starts a manager on clock, with a
// workspace w1 whose root is root1 committed at the clock's current time.
func startEpochManager(t *testing.T, clock *epochTestClock) (*crdt.Manager, *fakeJournal) {
	t.Helper()
	j := newFakeJournal()
	mgr := crdt.NewManager(userid.MustNew("user-1"), j, allowAll{}, nil, clock.Now)
	require.NoError(t, mgr.Bootstrap(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()
	mgr.WaitForStartForTest()
	t.Cleanup(func() {
		cancel()
		mgr.Stop()
	})
	seedRootInternal(t, mgr, "w1", "root1")
	return mgr, j
}

// epochStateAt is a persisted state whose epoch began at startedAt.
func epochStateAt(epoch int64, startedAt time.Time) *leapmuxv1.UserCrdtState {
	state := crdt.NewState("user-1")
	state.CurrentEpoch = epoch
	state.EpochStartedAt = timestamppb.New(startedAt)
	return state
}

// rotateTo moves the clock to `at` and runs one housekeeping pass.
func rotateTo(t *testing.T, mgr *crdt.Manager, clock *epochTestClock, at time.Time) {
	t.Helper()
	clock.Set(at)
	mgr.TickHousekeeping(context.Background())
}

// positionSubmit submits one client batch, stamped with epoch, that moves
// root1's position.
func positionSubmit(t *testing.T, mgr *crdt.Manager, epoch int64, batchID, position string) *leapmuxv1.BatchResult {
	t.Helper()
	res, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{positionBatchAtEpoch(epoch, batchID, position)},
	})
	require.NoError(t, err)
	require.Len(t, res, 1)
	return res[0]
}

func positionBatchAtEpoch(epoch int64, batchID, position string) *leapmuxv1.OpBatch {
	return &leapmuxv1.OpBatch{Epoch: epoch, BatchId: batchID, Ops: []*leapmuxv1.CrdtOp{{
		OpId: "op-" + batchID,
		Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
			NodeId: "root1",
			Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: position},
		}},
	}}}
}

// resumeReason subscribes with (cursor, epoch) and reports how the hub
// answered: a delta (SubscribeDelta) or a snapshot, and why.
func resumeReason(t *testing.T, mgr *crdt.Manager, cursor *leapmuxv1.HLC, epoch int64) (crdt.SubscribeMode, crdt.SubscribeReason) {
	t.Helper()
	sub := &crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, crdt.HLCClone(cursor), epoch, resolveAll)
	require.NoError(t, err)
	defer out.Unsub()()
	return out.Mode(), out.Reason()
}

// ---- (b) the stamp, its persist, and the rotation ----

func TestBootstrap_StampsAFreshEpochStartAndTheFirstPassPersistsIt(t *testing.T) {
	j := newFakeJournal()
	clock := &epochTestClock{now: epochT0}
	mgr := bootstrapEpochManager(t, j, clock)

	assert.Zero(t, j.compactionCalls(), "Bootstrap must not write: it runs on Registry.Get's request path")
	require.NotNil(t, mgr.State().GetEpochStartedAt(), "a fresh state begins its epoch at Bootstrap")
	assert.True(t, epochT0.Equal(mgr.State().GetEpochStartedAt().AsTime()))

	clock.Set(epochT0.Add(time.Minute))
	mgr.TickHousekeeping(context.Background())
	require.Equal(t, 1, j.compactionCalls(), "the first pass persists the stamp even though nothing was committed")
	persisted := j.persistedState()
	require.NotNil(t, persisted)
	require.NotNil(t, persisted.GetEpochStartedAt())
	assert.True(t, epochT0.Equal(persisted.GetEpochStartedAt().AsTime()),
		"the persisted start is the Bootstrap time, not the time of the pass")
	assert.Equal(t, int64(1), persisted.GetCurrentEpoch())

	clock.Set(epochT0.Add(2 * time.Minute))
	mgr.TickHousekeeping(context.Background())
	assert.Equal(t, 1, j.compactionCalls(), "once persisted, an idle pass writes nothing")
}

func TestBootstrap_KeepsALoadedEpochStartAndWritesNothing(t *testing.T) {
	j := newFakeJournal()
	loadedStart := epochT0.Add(-time.Hour)
	j.seedState(epochStateAt(1, loadedStart))
	clock := &epochTestClock{now: epochT0}
	mgr := bootstrapEpochManager(t, j, clock)

	require.NotNil(t, mgr.State().GetEpochStartedAt())
	assert.True(t, loadedStart.Equal(mgr.State().GetEpochStartedAt().AsTime()), "a loaded start is kept, not re-stamped")

	mgr.TickHousekeeping(context.Background())
	assert.Zero(t, j.compactionCalls(), "a loaded start needs no persist, and the epoch is not due")
}

func TestCompactionPass_FailedWriteLeavesTheEpochUnadvanced(t *testing.T) {
	j := newFakeJournal()
	j.seedState(epochStateAt(1, epochT0))
	clock := &epochTestClock{now: epochT0}
	mgr := bootstrapEpochManager(t, j, clock)

	j.failCompaction(errCompactionInjected)
	rotateTo(t, mgr, clock, epochT0.Add(crdt.EpochDuration+time.Minute))
	assert.Equal(t, 1, j.compactionCalls(), "the due rotation must try to persist")
	assert.Equal(t, int64(1), mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch(),
		"a rotation that was not persisted must not be published")
	assert.Equal(t, int64(1), j.persistedState().GetCurrentEpoch())

	j.failCompaction(nil)
	mgr.TickHousekeeping(context.Background())
	assert.Equal(t, int64(2), mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch(), "the next pass retries the rotation")
	assert.Equal(t, int64(2), j.persistedState().GetCurrentEpoch())
}

func TestCompactionPass_FailedFirstPersistRetriesTheBootstrapStamp(t *testing.T) {
	j := newFakeJournal()
	clock := &epochTestClock{now: epochT0}
	mgr := bootstrapEpochManager(t, j, clock)

	j.failCompaction(errCompactionInjected)
	clock.Set(epochT0.Add(time.Minute))
	mgr.TickHousekeeping(context.Background())
	assert.Nil(t, j.persistedState(), "the failed write persisted nothing")

	j.failCompaction(nil)
	clock.Set(epochT0.Add(5 * time.Minute))
	mgr.TickHousekeeping(context.Background())
	persisted := j.persistedState()
	require.NotNil(t, persisted, "the next pass retries the persist")
	require.NotNil(t, persisted.GetEpochStartedAt())
	assert.True(t, epochT0.Equal(persisted.GetEpochStartedAt().AsTime()),
		"the retry persists the original Bootstrap stamp, not the time of the retry")
}

func TestCompactionPass_RotationCatchesUpEveryElapsedPeriod(t *testing.T) {
	j := newFakeJournal()
	j.seedState(epochStateAt(1, epochT0))
	clock := &epochTestClock{now: epochT0}
	mgr := bootstrapEpochManager(t, j, clock)

	rotateTo(t, mgr, clock, epochT0.Add(3*crdt.EpochDuration+time.Minute))
	assert.Equal(t, 1, j.compactionCalls(), "one pass, one write, however many periods elapsed")
	wantStart := epochT0.Add(3 * crdt.EpochDuration)
	for name, state := range map[string]*leapmuxv1.UserCrdtState{
		"in memory": mgr.State(),
		"persisted": j.persistedState(),
	} {
		assert.Equal(t, int64(4), state.GetCurrentEpoch(), "%s: three whole periods elapsed", name)
		require.NotNil(t, state.GetEpochStartedAt(), name)
		assert.True(t, wantStart.Equal(state.GetEpochStartedAt().AsTime()),
			"%s: the start moves by whole periods: got %v, want %v", name, state.GetEpochStartedAt().AsTime(), wantStart)
	}
}

func TestCompactionPass_RotationAndCompactionLandInOneWrite(t *testing.T) {
	j := newFakeJournal()
	state := epochStateAt(1, epochT0)
	// A head the compaction watermark has not reached yet.
	state.MaxHlc = &leapmuxv1.HLC{Physical: epochT0.UnixMilli(), Logical: 3, ClientId: crdt.CanonicalClockID}
	j.seedState(state)
	clock := &epochTestClock{now: epochT0}
	mgr := bootstrapEpochManager(t, j, clock)

	rotateTo(t, mgr, clock, epochT0.Add(crdt.EpochDuration+time.Minute))
	assert.Equal(t, 1, j.compactionCalls(), "the rotation and the watermark advance share one CompactBatch")
	persisted := j.persistedState()
	assert.Equal(t, int64(2), persisted.GetCurrentEpoch())
	assert.Zero(t, crdt.HLCCmp(state.GetMaxHlc(), persisted.GetCompactionWatermark()),
		"the same write advances the compaction watermark to the head")
}

func TestEpochRotation_Boundaries(t *testing.T) {
	start := epochT0
	for _, c := range []struct {
		name      string
		startedAt *time.Time
		now       time.Time
		wantDue   bool
		wantEpoch int64
		wantStart time.Time
	}{
		{name: "a nil start is never due", startedAt: nil, now: start.Add(10 * crdt.EpochDuration), wantDue: false, wantEpoch: 5},
		{name: "one millisecond short is not due", startedAt: &start, now: start.Add(crdt.EpochDuration - time.Millisecond), wantDue: false, wantEpoch: 5, wantStart: start},
		{name: "exactly one period is due by one", startedAt: &start, now: start.Add(crdt.EpochDuration), wantDue: true, wantEpoch: 6, wantStart: start.Add(crdt.EpochDuration)},
		{name: "a clock before the start is not due", startedAt: &start, now: start.Add(-time.Hour), wantDue: false, wantEpoch: 5, wantStart: start},
		{name: "just under two periods is due by one", startedAt: &start, now: start.Add(2*crdt.EpochDuration - time.Millisecond), wantDue: true, wantEpoch: 6, wantStart: start.Add(crdt.EpochDuration)},
	} {
		t.Run(c.name, func(t *testing.T) {
			state := crdt.NewState("user-1")
			state.CurrentEpoch = 5
			if c.startedAt != nil {
				state.EpochStartedAt = timestamppb.New(*c.startedAt)
			}
			epoch, startedAt, due := crdt.EpochRotationForTest(state, c.now)
			assert.Equal(t, c.wantDue, due)
			assert.Equal(t, c.wantEpoch, epoch)
			if c.startedAt == nil {
				assert.Nil(t, startedAt)
				return
			}
			require.NotNil(t, startedAt)
			assert.True(t, c.wantStart.Equal(startedAt.AsTime()), "got %v, want %v", startedAt.AsTime(), c.wantStart)
			assert.Equal(t, int64(5), state.GetCurrentEpoch(), "the decision must not change the state it reads")
			assert.True(t, c.startedAt.Equal(state.GetEpochStartedAt().AsTime()), "the decision must not change the state it reads")
		})
	}
}

// ---- (c) the protocol decisions the rotation turns on ----
//
// decideResume checks the retention floor BEFORE the epoch, and a clock moved
// a whole epoch puts every older cursor below that floor. So each resume test
// takes its cursor from an op committed AFTER the rotation, which sits above
// the floor; a StaleEpoch answer then also proves the floor check passed.

func TestEpochRotation_CurrentEpochResumeStillGetsADelta(t *testing.T) {
	clock := &epochTestClock{now: epochT0}
	mgr, _ := startEpochManager(t, clock)
	rotateTo(t, mgr, clock, epochT0.Add(crdt.EpochDuration+time.Minute))
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	require.Equal(t, int64(2), epoch, "precondition: the epoch rotated")

	submitNodePositionBatch(t, mgr, "after-rotation", "root1", "p-after")
	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()

	mode, reason := resumeReason(t, mgr, cursor, epoch)
	assert.Equal(t, crdt.SubscribeReasonResumed, reason)
	assert.Equal(t, crdt.SubscribeDelta, mode, "a resume in the current epoch above the floor gets a delta")
}

func TestEpochRotation_PreviousEpochReconnectGetsASnapshot(t *testing.T) {
	clock := &epochTestClock{now: epochT0}
	mgr, _ := startEpochManager(t, clock)
	previous := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	rotateTo(t, mgr, clock, epochT0.Add(crdt.EpochDuration+time.Minute))
	require.Equal(t, previous+1, mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch())

	submitNodePositionBatch(t, mgr, "after-rotation", "root1", "p-after")
	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()

	mode, reason := resumeReason(t, mgr, cursor, previous)
	assert.NotEqual(t, crdt.SubscribeReasonBelowRetentionFloor, reason,
		"the cursor sits above the floor, so the refusal must come from the epoch gate")
	assert.Equal(t, crdt.SubscribeReasonStaleEpoch, reason)
	assert.Equal(t, crdt.SubscribeInitial, mode, "a client from the previous epoch takes a full snapshot")
}

func TestEpochRotation_PreviousEpochSubmitIsAccepted(t *testing.T) {
	clock := &epochTestClock{now: epochT0}
	mgr, _ := startEpochManager(t, clock)
	previous := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	rotateTo(t, mgr, clock, epochT0.Add(crdt.EpochDuration+time.Minute))
	require.Equal(t, previous+1, mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch())

	res := positionSubmit(t, mgr, previous, "from-previous-epoch", "p1")
	assert.NotNil(t, res.GetCommitted(), "a submit one epoch behind is inside the window; got %v", res)
}

func TestEpochRotationRetainsDedupWhileTheOriginalEpochIsAccepted(t *testing.T) {
	for _, failedRotation := range []bool{false, true} {
		t.Run(map[bool]string{false: "successful rotation", true: "failed rotation"}[failedRotation], func(t *testing.T) {
			clock := &epochTestClock{now: epochT0}
			mgr, journal := startEpochManager(t, clock)
			epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
			first := positionSubmit(t, mgr, epoch, "lost-ack-position", "p1")
			require.NotNil(t, first.GetCommitted())
			originalHLC := crdt.HLCClone(first.GetCommitted().GetCommitted()[0].GetCanonicalHlc())
			require.NotNil(t, positionSubmit(t, mgr, epoch, "later-position", "p2").GetCommitted())
			if failedRotation {
				journal.failCompaction(errCompactionInjected)
			}
			rotateTo(t, mgr, clock, epochT0.Add(crdt.DedupTTL+time.Minute))

			retry := positionSubmit(t, mgr, epoch, "lost-ack-position", "p1")
			require.NotNil(t, retry.GetCommitted())
			gotHLC := retry.GetCommitted().GetCommitted()[0].GetCanonicalHlc()
			assert.Zero(t, crdt.HLCCmp(originalHLC, gotHLC), "a retry must return the original canonical identity")
			assert.Equal(t, "p2", mgr.Materialized(crdt.SubscriberFilter{}).GetNodes()["root1"].GetPosition().GetValue(),
				"a retry must not replace a later placement")
		})
	}
}

func TestEpochRotationRejectsAFutureSubmissionEpoch(t *testing.T) {
	clock := &epochTestClock{now: epochT0}
	mgr, _ := startEpochManager(t, clock)
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	before := crdt.HLCClone(mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc())
	result := positionSubmit(t, mgr, epoch+10, "future-epoch", "invalid")
	require.NotNil(t, result.GetRejected())
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, result.GetRejected().GetReason())
	assert.Zero(t, crdt.HLCCmp(before, mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()))
}

func TestSubmissionEpochsRejectBatchesIndependently(t *testing.T) {
	clock := &epochTestClock{now: epochT0}
	mgr, _ := startEpochManager(t, clock)
	rotateTo(t, mgr, clock, epochT0.Add(2*crdt.EpochDuration+time.Minute))
	current := mgr.State().GetCurrentEpoch()
	require.EqualValues(t, 3, current)
	batches := []*leapmuxv1.OpBatch{
		positionBatchAtEpoch(1, "stale-first", "stale-first"),
		positionBatchAtEpoch(2, "previous", "previous"),
		positionBatchAtEpoch(1, "stale-last", "stale-last"),
		positionBatchAtEpoch(3, "current", "current"),
		positionBatchAtEpoch(0, "unset", "unset"),
		positionBatchAtEpoch(-1, "negative", "negative"),
		positionBatchAtEpoch(4, "future", "future"),
		nil,
	}
	results, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		Batches: batches, PrincipalID: "user", OriginClient: "c1",
	})
	require.NoError(t, err)
	require.Len(t, results, len(batches))
	for i, expected := range []leapmuxv1.BatchRejectionReason{
		leapmuxv1.BatchRejectionReason_BATCH_REJECTION_STALE_EPOCH,
		leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED,
		leapmuxv1.BatchRejectionReason_BATCH_REJECTION_STALE_EPOCH,
		leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED,
		leapmuxv1.BatchRejectionReason_BATCH_REJECTION_EPOCH_REQUIRED,
		leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN,
		leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN,
		leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN,
	} {
		if expected == leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED {
			require.NotNil(t, results[i].GetCommitted())
		} else {
			require.NotNil(t, results[i].GetRejected())
			assert.Equal(t, expected, results[i].GetRejected().GetReason())
		}
	}
	assert.Equal(t, "current", mgr.State().GetNodes()["root1"].GetPosition().GetValue())
}

func TestClientAuthorizationFlagCannotBypassTheSubmissionEpoch(t *testing.T) {
	clock := &epochTestClock{now: epochT0}
	mgr, _ := startEpochManager(t, clock)
	input := crdt.SubmitInput{
		Internal: true, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{positionBatchAtEpoch(0, "internal-flag", "p1")},
	}
	results, err := mgr.Submit(context.Background(), input)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_EPOCH_REQUIRED, results[0].GetRejected().GetReason())
	results, err = mgr.SubmitInternal(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, results[0].GetCommitted())
}

func TestClosedTabCannotBeRecreatedByALostAcknowledgementRetry(t *testing.T) {
	clock := &epochTestClock{now: epochT0}
	mgr, journal := startEpochManager(t, clock)
	create := addTabBatch(t, "lost-ack-tab", "closed-tab", "root1", "worker", "p1")
	create.Epoch = 1
	input := crdt.SubmitInput{Batches: []*leapmuxv1.OpBatch{create}, PrincipalID: "user", OriginClient: "c1"}
	results, err := mgr.Submit(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, results[0].GetCommitted())
	original := crdt.HLCClone(results[0].GetCommitted().GetCommitted()[0].GetCanonicalHlc())
	_, err = mgr.SubmitInternal(context.Background(), crdt.SubmitInput{Batches: []*leapmuxv1.OpBatch{{
		BatchId: "close-tab", Ops: []*leapmuxv1.CrdtOp{{
			Body: &leapmuxv1.CrdtOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{
				Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "closed-tab"},
			}},
		}},
	}}})
	require.NoError(t, err)
	mgr.TickHousekeeping(context.Background())
	require.NotContains(t, mgr.State().GetTabs(), "closed-tab")
	rotateTo(t, mgr, clock, epochT0.Add(crdt.DedupTTL+time.Minute))
	results, err = mgr.Submit(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, results[0].GetCommitted())
	assert.Zero(t, crdt.HLCCmp(original, results[0].GetCommitted().GetCommitted()[0].GetCanonicalHlc()))
	assert.NotContains(t, mgr.State().GetTabs(), "closed-tab")
	rotateTo(t, mgr, clock, epochT0.Add(2*crdt.EpochDuration+time.Minute))
	results, err = mgr.Submit(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_STALE_EPOCH, results[0].GetRejected().GetReason())
	assert.NotContains(t, mgr.State().GetTabs(), "closed-tab")
	owned, _ := journal.snapshotIndex()
	assert.NotContains(t, owned, "closed-tab")
}

func TestEpochRotation_TwoEpochsOldSubmitIsStale(t *testing.T) {
	clock := &epochTestClock{now: epochT0}
	mgr, _ := startEpochManager(t, clock)
	first := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	rotateTo(t, mgr, clock, epochT0.Add(crdt.EpochDuration+time.Minute))
	rotateTo(t, mgr, clock, epochT0.Add(2*crdt.EpochDuration+time.Minute))
	require.Equal(t, first+2, mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch())

	res := positionSubmit(t, mgr, first, "from-first-epoch", "p1")
	require.NotNil(t, res.GetRejected(), "a submit two epochs behind must be rejected; got %v", res)
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_STALE_EPOCH, res.GetRejected().GetReason())
}

// The dedup check also rejects an old identity submitted under a new epoch.
// This fixture keeps the old row unexpired so cleanup does not remove it when
// its epoch leaves the accepted window.
func TestEpochRotation_StaleDedupRowIsRejected(t *testing.T) {
	clock := &epochTestClock{now: epochT0}
	mgr, j := startEpochManager(t, clock)
	first := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	j.putDedup(crdt.RecentBatchRecord{
		UserID:            "user-1",
		BatchID:           "b1",
		BodyHash:          []byte("any"),
		PrincipalID:       "user",
		CanonicalFirstHLC: &leapmuxv1.HLC{Physical: epochT0.UnixMilli(), ClientId: crdt.CanonicalClockID},
		OpCount:           1,
		Epoch:             first,
		ExpiresAt:         epochT0.Add(10 * crdt.EpochDuration),
	})
	rotateTo(t, mgr, clock, epochT0.Add(crdt.EpochDuration+time.Minute))
	rotateTo(t, mgr, clock, epochT0.Add(2*crdt.EpochDuration+time.Minute))
	current := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	require.Equal(t, first+2, current)

	res := positionSubmit(t, mgr, current, "b1", "p1")
	require.NotNil(t, res.GetRejected(), "a dedup row two epochs old must not be echoed; got %v", res)
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_STALE_EPOCH, res.GetRejected().GetReason())
}

func TestEpochRotation_FailedWriteChangesNoDecision(t *testing.T) {
	clock := &epochTestClock{now: epochT0}
	mgr, j := startEpochManager(t, clock)
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Move the clock first and commit, so the cursor sits above the retention
	// floor before AND after the tick under test.
	clock.Set(epochT0.Add(crdt.EpochDuration + time.Minute))
	submitNodePositionBatch(t, mgr, "before-tick", "root1", "p-before")
	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	beforeMode, beforeReason := resumeReason(t, mgr, cursor, epoch)
	require.Equal(t, crdt.SubscribeReasonResumed, beforeReason, "precondition: the cursor resumes before the tick")

	j.failCompaction(errCompactionInjected)
	calls := j.compactionCalls()
	mgr.TickHousekeeping(context.Background())
	require.Equal(t, calls+1, j.compactionCalls(), "the due rotation must try to persist")

	assert.Equal(t, epoch, mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch(),
		"an unpersisted rotation must not move the epoch")
	afterMode, afterReason := resumeReason(t, mgr, cursor, epoch)
	assert.Equal(t, beforeReason, afterReason, "the resume decision must not change")
	assert.Equal(t, beforeMode, afterMode)
	res := positionSubmit(t, mgr, epoch, "same-epoch", "p2")
	assert.NotNil(t, res.GetCommitted(), "the accepted epoch window must not change; got %v", res)
}

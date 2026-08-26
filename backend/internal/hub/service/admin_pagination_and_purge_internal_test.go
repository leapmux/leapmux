package service

// The two ceilings the administrator surface puts on ONE operation's row
// set: the per-page limit NormalizePageParams applies to every paginated
// handler, and the pass ceiling the registration-key purge drain applies to
// a backlog. They share a file because they answer one question -- how much
// work a single request may ask the hub for.
//
// The file is an INTERNAL test because the purge cases read
// maxRegistrationKeyPurgeBatches, which the package does not export. An
// external test would have to restate that number, and a copy of a ceiling
// beside the ceiling passes whatever the original becomes.

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// TestNormalizePageParams pins the normalization every paginated admin handler
// shares. Nine call sites read it, and none of them re-checks its result.
//
// Both ends carry a defect the function exists to prevent. A non-positive
// limit must NOT reach the store: store.ClampListLimit preserves 0, and the
// keyset queries read 0 as "return no rows", so a caller that simply omits
// `limit` -- the proto3 default -- would get an empty page it cannot tell
// apart from an empty table. An oversized limit must be CAPPED: the
// hand-rolled form WorkerManagementService.ListWorkers used had no ceiling
// at all, so `page.limit = 100000` returned the caller's whole worker row
// set in one response.
func TestNormalizePageParams(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		limit int64
		want  int64
	}{
		"omitted limit takes the default":  {limit: 0, want: DefaultPageLimit},
		"negative limit takes the default": {limit: -1, want: DefaultPageLimit},
		"the most negative limit does too": {limit: math.MinInt64, want: DefaultPageLimit},
		"one row is a legal page":          {limit: 1, want: 1},
		"a limit below the default stays":  {limit: DefaultPageLimit - 1, want: DefaultPageLimit - 1},
		"the default itself stays":         {limit: DefaultPageLimit, want: DefaultPageLimit},
		"the ceiling itself is allowed":    {limit: MaxPageLimit, want: MaxPageLimit},
		"one past the ceiling is capped":   {limit: MaxPageLimit + 1, want: MaxPageLimit},
		"the reported bug's limit":         {limit: 100000, want: MaxPageLimit},
		"the largest limit is capped":      {limit: math.MaxInt64, want: MaxPageLimit},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, NormalizePageParams("", tc.limit).Limit)
		})
	}

	// The ceiling is the point of the function, so state the relation the
	// table rests on rather than leaving it to the two literals.
	assert.Less(t, int64(DefaultPageLimit), int64(MaxPageLimit),
		"the default page must fit inside the ceiling")

	// The cursor is carried through untouched at every limit: normalizing it
	// is the store's job, and a handler that rewrote it would silently
	// restart a listing from the first page.
	assert.Equal(t, "opaque-cursor", NormalizePageParams("opaque-cursor", 0).Cursor)
	assert.Equal(t, "opaque-cursor", NormalizePageParams("opaque-cursor", math.MaxInt64).Cursor)
	assert.Empty(t, NormalizePageParams("", 10).Cursor)
}

// fakePurgeStore serves one fake CleanupStore and nothing else. The purge
// handler reads no other store surface, and the embedded nil interface
// makes that a compile-time-shaped contract rather than a comment: any
// other store call this handler grows would panic in this test rather than
// pass unnoticed.
type fakePurgeStore struct {
	store.Store
	cleanup *fakePurgeCleanup
}

func (s fakePurgeStore) Cleanup() store.CleanupStore { return s.cleanup }

// fakePurgeCleanup answers the purge query in fixed-size batches out of a
// simulated backlog.
//
// It is the seam that makes the BATCH BOUNDARY observable without restating
// the query's own LIMIT in Go. A case seeded through the real store cannot
// tell the drain's "stop when a pass deletes NOTHING" rule apart from the
// old "stop on a page shorter than 1000" one -- both report the same total
// for any backlog -- and the second rule is a copy of the SQL's LIMIT that
// silently stops the purge after one page as soon as the SQL changes.
type fakePurgeCleanup struct {
	store.CleanupStore
	// remaining is the backlog left to delete. A negative value means an
	// endless backlog, which is what the runaway ceiling has to survive.
	remaining int64
	batch     int64
	// failOnPass makes that pass (1-based) report an error; 0 never fails.
	failOnPass int
	passes     int
	cutoffs    []time.Time
}

var errPurgePassFailed = errors.New("registration key purge pass failed")

func (c *fakePurgeCleanup) HardDeleteExpiredRegistrationKeysBefore(_ context.Context, cutoff time.Time) (int64, error) {
	c.passes++
	c.cutoffs = append(c.cutoffs, cutoff)
	if c.passes == c.failOnPass {
		return 0, errPurgePassFailed
	}
	if c.remaining < 0 {
		return c.batch, nil
	}
	deleted := min(c.batch, c.remaining)
	c.remaining -= deleted
	return deleted, nil
}

func purgeRegistrationKeys(t *testing.T, cleanup *fakePurgeCleanup) (*connect.Response[leapmuxv1.PurgeExpiredRegistrationKeysResponse], error) {
	t.Helper()
	svc := NewAdminWorkerService(fakePurgeStore{cleanup: cleanup}, nil)
	return svc.PurgeExpiredRegistrationKeys(context.Background(),
		connect.NewRequest(&leapmuxv1.PurgeExpiredRegistrationKeysRequest{}))
}

// TestPurgeExpiredRegistrationKeysDrainsUntilAPassDeletesNothing pins the
// drain's stop rule and its accumulation across passes.
//
// A backlog of 7 over a batch of 3 needs four passes: 3, 3, 1, then the
// empty pass that ends the drain. The old rule broke on the FIRST short
// page, so it would report 3 and leave four expired keys behind with
// nothing to say they were missed.
func TestPurgeExpiredRegistrationKeysDrainsUntilAPassDeletesNothing(t *testing.T) {
	t.Parallel()

	cleanup := &fakePurgeCleanup{remaining: 7, batch: 3}
	got, err := purgeRegistrationKeys(t, cleanup)
	require.NoError(t, err)
	assert.Equal(t, int64(7), got.Msg.GetPurged(), "every pass adds to the reported total")
	assert.Equal(t, 4, cleanup.passes, "the drain ends on the pass that deletes nothing")

	// ONE cutoff for the whole drain, computed before the first pass. A
	// cutoff recomputed per pass would widen the purge as it ran, so what a
	// call deleted would depend on how long it took.
	require.Len(t, cleanup.cutoffs, 4)
	for _, cutoff := range cleanup.cutoffs {
		assert.Equal(t, cleanup.cutoffs[0], cutoff, "every pass purges against one cutoff")
	}
}

// TestPurgeExpiredRegistrationKeysStopsOnAnEmptyBacklog covers the boundary
// the drain meets on an idle hub: the first pass deletes nothing, so the
// call reports zero and makes exactly one query.
func TestPurgeExpiredRegistrationKeysStopsOnAnEmptyBacklog(t *testing.T) {
	t.Parallel()

	cleanup := &fakePurgeCleanup{remaining: 0, batch: 1000}
	got, err := purgeRegistrationKeys(t, cleanup)
	require.NoError(t, err)
	assert.Zero(t, got.Msg.GetPurged())
	assert.Equal(t, 1, cleanup.passes, "an empty backlog costs one query, not a loop")
}

// TestPurgeExpiredRegistrationKeysStopsAtTheRunawayCeiling pins the OTHER
// end of the drain. The stop rule is "a pass deleted nothing", which never
// arrives while rows keep appearing -- a hub minting registration keys
// faster than the purge deletes them, or a store that reports a delete it
// did not perform. The ceiling is what keeps one RPC from looping forever
// holding write locks.
func TestPurgeExpiredRegistrationKeysStopsAtTheRunawayCeiling(t *testing.T) {
	t.Parallel()

	// A negative backlog never empties, so only the ceiling can end this.
	cleanup := &fakePurgeCleanup{remaining: -1, batch: 1}
	got, err := purgeRegistrationKeys(t, cleanup)
	require.NoError(t, err)
	assert.Equal(t, maxRegistrationKeyPurgeBatches, cleanup.passes,
		"an endless backlog stops at the runaway ceiling")
	assert.Equal(t, int64(maxRegistrationKeyPurgeBatches), got.Msg.GetPurged(),
		"the total counts every pass that ran, ceiling included")
}

// TestPurgeExpiredRegistrationKeysReportsAFailedPass pins the error path. A
// store fault mid-drain must surface as Internal with the operation named,
// not as a short success that reports the rows deleted so far and invites
// the operator to believe the backlog is clear.
func TestPurgeExpiredRegistrationKeysReportsAFailedPass(t *testing.T) {
	t.Parallel()

	cleanup := &fakePurgeCleanup{remaining: -1, batch: 5, failOnPass: 2}
	_, err := purgeRegistrationKeys(t, cleanup)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	assert.ErrorIs(t, err, errPurgePassFailed, "the store fault is wrapped, not replaced")
	assert.Contains(t, err.Error(), "purge expired registration keys")
	assert.Equal(t, 2, cleanup.passes, "a failed pass ends the drain at once")
}

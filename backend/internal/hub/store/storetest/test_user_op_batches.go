package storetest

import (
	"fmt"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testUserOpBatches exercises the UserOpBatchesStore surface. The bulk of
// the journal logic is covered indirectly via the manager-integration
// suite; the cases here focus on the raw SQL contracts that the
// integration suite doesn't run against real SQL backends.
func (s *Suite) testUserOpBatches(t *testing.T) {
	// The retention sweep. It runs across ALL owners (a dormant account has no
	// session to scope it to, which is precisely the case it exists for), so it
	// must delete strictly by physical_ms and honour its LIMIT identically on
	// every backend -- a dialect that got the predicate or the batching wrong
	// would either leak rows forever or delete a live client's resume tail.
	//
	// The cutoff is an HLC physical, NOT the committed_at wall clock. That is
	// what keeps the sweep and Manager.decideResume in one time domain, and it
	// is also what makes these cases deterministic: the rows carry the exact
	// physical values under test rather than whatever the DB server stamped.
	insertBatch := func(t *testing.T, st store.Store, owner userid.UserID, batchID string, physical int64) {
		t.Helper()
		require.NoError(t, st.UserOpBatches().Insert(ctx, store.InsertUserOpBatchParams{
			UserID: owner, PhysicalMs: physical, Logical: 0, LastLogical: 0,
			OriginClient: "c1", PrincipalID: "p", BatchID: batchID,
			BodyHash: []byte("h"), BatchPayload: []byte("b"), TransitionsPayload: []byte("t"),
			OpCount: 1, Epoch: 1,
		}))
	}

	// How far this owner's state_payload has actually absorbed its op tail. The
	// sweep refuses to delete at or above it, so any case that wants the
	// retention cutoff to be the binding floor must raise this first.
	absorbedThrough := func(t *testing.T, st store.Store, owner userid.UserID, physical int64) {
		t.Helper()
		now := time.Now()
		require.NoError(t, st.UserState().Upsert(ctx, store.UpsertUserStateParams{
			UserID:               owner,
			StatePayload:         []byte("s"),
			CompactionPhysicalMs: physical,
			CurrentEpoch:         1,
			EpochStartedAt:       now,
			UpdatedAt:            now,
		}))
	}

	t.Run("retention sweep deletes by physical cutoff across owners", func(t *testing.T) {
		st := s.NewStore(t)
		a := userid.MustNew(SeedUser(t, st, "uob-sweep-a").ID)
		b := userid.MustNew(SeedUser(t, st, "uob-sweep-b").ID)

		insertBatch(t, st, a, "a-old", 100)
		insertBatch(t, st, a, "a-new", 300)
		insertBatch(t, st, b, "b-old", 100)
		// Both owners are fully compacted, leaving the retention cutoff as the
		// only floor in play for this case.
		absorbedThrough(t, st, a, 1_000)
		absorbedThrough(t, st, b, 1_000)

		countFor := func(owner userid.UserID) int64 {
			n, err := st.UserOpBatches().Count(ctx, owner)
			require.NoError(t, err)
			return n
		}
		require.Equal(t, int64(2), countFor(a))
		require.Equal(t, int64(1), countFor(b))

		// Cutoff 200: both owners' physical=100 rows go, a's physical=300 stays.
		// Owner-blindness is the point -- b has no session here at all.
		deleted, err := st.Cleanup().DeleteUserOpBatchesBeforePhysical(ctx, 200)
		require.NoError(t, err)
		assert.Equal(t, int64(2), deleted, "the sweep is owner-blind by design; every owner's aged rows must go")
		assert.Equal(t, int64(1), countFor(a), "a's row above the cutoff must survive")
		assert.Zero(t, countFor(b))
	})

	// The boundary itself. decideResume admits a cursor at exactly the cutoff
	// (its test is a strict `<`), so the sweep must NOT delete the row at that
	// physical -- otherwise a cursor the manager just accepted resumes onto a
	// row that is already gone, and ListAfter reports the hole as an ordinary
	// short tail rather than an error.
	t.Run("retention sweep keeps the row at exactly the cutoff", func(t *testing.T) {
		st := s.NewStore(t)
		owner := userid.MustNew(SeedUser(t, st, "uob-sweep-boundary").ID)
		insertBatch(t, st, owner, "below", 199)
		insertBatch(t, st, owner, "at", 200)
		insertBatch(t, st, owner, "above", 201)
		absorbedThrough(t, st, owner, 1_000)

		deleted, err := st.Cleanup().DeleteUserOpBatchesBeforePhysical(ctx, 200)
		require.NoError(t, err)
		assert.Equal(t, int64(1), deleted, "only the row strictly below the cutoff is eligible")

		n, err := st.UserOpBatches().Count(ctx, owner)
		require.NoError(t, err)
		assert.Equal(t, int64(2), n, "the rows at and above the cutoff are a live cursor's resume tail")
	})

	// The floor the retention window CANNOT see, and the reason the sweep joins
	// user_state at all.
	//
	// state_payload is rewritten only by Manager.maybeCompact's 60s tick --
	// Manager.Stop does no final pass, and maybeCompact returns without
	// advancing on a CompactBatch error -- so a hub restart, an idle eviction,
	// or one transient write error within 60s of a user's last commit leaves
	// compaction_watermark below max_hlc with that tail living ONLY in this
	// table. Sweeping on the retention window alone deletes it once the account
	// goes dormant past the TTL, and the next Bootstrap (state_payload + every
	// batch above compaction_watermark) silently replays a short tail: the
	// user's last edits are gone for good, with no error raised anywhere.
	t.Run("retention sweep keeps batches the state payload has not absorbed", func(t *testing.T) {
		st := s.NewStore(t)
		owner := userid.MustNew(SeedUser(t, st, "uob-sweep-unabsorbed").ID)

		insertBatch(t, st, owner, "absorbed", 100)
		insertBatch(t, st, owner, "unabsorbed", 150)
		// Compaction reached 120 and then the hub went down. The row at 150 is
		// far below the retention cutoff but is the only copy of those ops.
		absorbedThrough(t, st, owner, 120)

		deleted, err := st.Cleanup().DeleteUserOpBatchesBeforePhysical(ctx, 200)
		require.NoError(t, err)
		assert.Equal(t, int64(1), deleted, "only the absorbed row is eligible")

		rows, err := st.UserOpBatches().ListAfter(ctx, store.ListUserOpBatchesAfterParams{
			UserID:            owner,
			AfterPhysicalMs:   120,
			AfterLogical:      0,
			AfterOriginClient: "",
			Limit:             store.CRDTBatchPageLimit,
		})
		require.NoError(t, err)
		require.Len(t, rows, 1, "the tail Bootstrap replays must still be there")
		assert.Equal(t, "unabsorbed", rows[0].BatchID)
	})

	// The LIMIT itself. Every other case here seeds a handful of rows, so the
	// sweep's `LIMIT 1000` never binds -- and that batching is the ONE place the
	// three dialects genuinely diverge in FORM: sqlite deletes by `rowid IN
	// (SELECT ... LIMIT 1000)`, postgres by the four-column primary key tuple
	// (deliberately not ctid, which YugabyteDB rejects outright), and mysql by a
	// bare `DELETE ... LIMIT 1000` over a correlated subquery. A form that
	// silently stopped honouring the LIMIT would delete the whole eligible set in
	// one statement -- a single unbounded transaction over an arbitrarily large
	// table on the shared cleanup schedule -- and one that applied it to the
	// wrong relation could delete far fewer rows per pass than the caller's drain
	// loop expects, or none at all.
	//
	// The LIMIT is a hard literal in the SQL (DeleteUserOpBatchesBeforePhysical
	// binds only the cutoff), so it cannot be shrunk for the test without
	// changing the production signature. Seed past it instead: 1500 rows, all
	// eligible.
	t.Run("retention sweep honours its LIMIT and drains across passes", func(t *testing.T) {
		st := s.NewStore(t)
		owner := userid.MustNew(SeedUser(t, st, "uob-sweep-limit").ID)

		const (
			seeded    = 1500
			sweepStep = 1000 // the LIMIT literal in every dialect's sweep
		)
		// One transaction for the whole seed: 1500 individually committed inserts
		// dominate this suite's runtime on the real SQL backends for no benefit.
		require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
			for i := range seeded {
				// Distinct physical_ms keeps every row a distinct primary key
				// (user_id, physical_ms, logical, origin_client) and every batch_id
				// distinct for the dedup index.
				insertBatch(t, tx, owner, fmt.Sprintf("bulk-%04d", i), int64(i+1))
			}
			return nil
		}))
		// Absorbed well past the seed, so the retention cutoff is the only floor
		// and all 1500 rows are eligible.
		absorbedThrough(t, st, owner, 10_000)

		countRows := func() int64 {
			n, err := st.UserOpBatches().Count(ctx, owner)
			require.NoError(t, err)
			return n
		}
		require.Equal(t, int64(seeded), countRows())

		// First pass: exactly the LIMIT, not the whole eligible set.
		deleted, err := st.Cleanup().DeleteUserOpBatchesBeforePhysical(ctx, seeded+1)
		require.NoError(t, err)
		require.Equal(t, int64(sweepStep), deleted,
			"one pass must delete exactly LIMIT rows, never the whole eligible set")
		require.Equal(t, int64(seeded-sweepStep), countRows())

		// The caller's contract: drain until a pass deletes nothing. Bounded so a
		// sweep that stopped making progress fails instead of hanging.
		var passes int
		for {
			passes++
			require.Less(t, passes, 10, "the drain loop must terminate; the sweep stopped making progress")
			n, derr := st.Cleanup().DeleteUserOpBatchesBeforePhysical(ctx, seeded+1)
			require.NoError(t, derr)
			if n == 0 {
				break
			}
		}
		assert.Zero(t, countRows(), "the drain must empty every eligible row")
	})

	// An owner who has never compacted has no user_state row at all, so EVERY
	// batch they hold is unabsorbed. The sweep must skip them rather than read a
	// missing watermark as zero -- which under a plain join default would delete
	// nothing, but under a COALESCE(...,0) would delete their entire journal.
	t.Run("retention sweep skips owners that have never compacted", func(t *testing.T) {
		st := s.NewStore(t)
		owner := userid.MustNew(SeedUser(t, st, "uob-sweep-nostate").ID)
		insertBatch(t, st, owner, "only", 100)

		deleted, err := st.Cleanup().DeleteUserOpBatchesBeforePhysical(ctx, 200)
		require.NoError(t, err)
		assert.Zero(t, deleted, "no state_payload means nothing has been absorbed yet")

		n, err := st.UserOpBatches().Count(ctx, owner)
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)
	})

	// Regression: a fresh user has no rows, no compaction watermark, so
	// the CRDT manager's bootstrap path calls ListAfter with a zero
	// HLC and the full CRDTBatchPageLimit. Earlier the SQLite query
	// mixed positional `?` with sqlc.arg() and sqlc emitted numbered
	// placeholders that left LIMIT unbound; bind_parameter_count came
	// back as 6 but only 5 args were passed, so the driver returned
	// "missing argument with index 6" and the workspace failed to
	// load. The case must run against every backend so a future
	// generator change in any of them surfaces immediately. UserID is
	// required -- a blank id short-circuits before SQL and would hide
	// the bind bug this case exists to catch.
	t.Run("list after zero watermark on empty journal", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "uob-empty-journal")

		rows, err := st.UserOpBatches().ListAfter(ctx, store.ListUserOpBatchesAfterParams{
			UserID:            userid.MustNew(user.ID),
			AfterPhysicalMs:   0,
			AfterLogical:      0,
			AfterOriginClient: "",
			Limit:             store.CRDTBatchPageLimit,
		})
		require.NoError(t, err)
		assert.Empty(t, rows)
	})

	// Every ownership predicate here is a `WHERE user_id = ?` bind, and a zero
	// userid.UserID unwraps to "" -- which does NOT fail to match, it matches
	// every blank-owner row. Two independent things stop that, and this case
	// pins both because they fail in different directions:
	//
	//   - No blank-owner row can exist. user_id REFERENCES users(id) on all four
	//     of these tables, and CreateUserParams.Validate now refuses the blank
	//     users.id that was their only possible parent.
	//   - The gates refuse the zero bind before the query, so a blank caller
	//     reads nothing even where a matching row could exist. That still
	//     matters: the closure above covers this API, not the database, which
	//     accepts "" as a TEXT key through raw SQL.
	//
	// The zero UserID is what keeps the second reachable through the typed
	// params: Go cannot forbid userid.UserID{}, so the type moves the refusal to
	// the bind rather than removing the need for one -- which is exactly why
	// userid.OwnerFilter still exists after the retype.
	t.Run("a blank owner is unrepresentable and a blank caller reads nothing", func(t *testing.T) {
		st := s.NewStore(t)
		realUser := SeedUser(t, st, "uob-real-owner")

		// The seam, closed at its source.
		require.ErrorIs(t, st.Users().Create(ctx, store.CreateUserParams{
			ID: "", Username: "uob-blank-id-user",
			PasswordHash: "h", DisplayName: "Blank", PasswordSet: true,
		}), store.ErrInvalidArgument,
			"a blank users.id is the parent key every blank-owner CRDT row needs")

		// And therefore closed for the child rows: the FK has nothing to point at.
		now := time.Now().UTC()
		require.Error(t, st.UserOpBatches().Insert(ctx, store.InsertUserOpBatchParams{
			UserID: userid.UserID{}, PhysicalMs: 1, Logical: 0, LastLogical: 0,
			OriginClient: "c", PrincipalID: "", BatchID: "blank-batch",
			BodyHash: []byte("h"), BatchPayload: []byte("p"), TransitionsPayload: []byte("t"), OpCount: 1, Epoch: 1,
		}), "a blank-owner op batch has no parent user row to reference")
		require.Error(t, st.UserState().Upsert(ctx, store.UpsertUserStateParams{
			UserID: userid.UserID{}, StatePayload: []byte("state"), CurrentEpoch: 1,
			EpochStartedAt: now, UpdatedAt: now,
		}), "a blank-owner state row has no parent user row to reference")

		// Every read below binds a zero owner against rows owned by someone REAL.
		// That is what keeps them non-vacuous now that no blank-owner row can
		// exist: a gate that stopped binding user_id would hand the blank caller
		// these rows, and each assertion would fail.
		require.NoError(t, st.UserOpBatches().Insert(ctx, store.InsertUserOpBatchParams{
			UserID: userid.MustNew(realUser.ID), PhysicalMs: 1, Logical: 0, LastLogical: 0,
			OriginClient: "c", PrincipalID: realUser.ID, BatchID: "real-batch",
			BodyHash: []byte("h"), BatchPayload: []byte("real-body"), TransitionsPayload: []byte("t"), OpCount: 1, Epoch: 1,
		}))
		require.NoError(t, st.UserState().Upsert(ctx, store.UpsertUserStateParams{
			UserID: userid.MustNew(realUser.ID), StatePayload: []byte("real-state"), CurrentEpoch: 1,
			EpochStartedAt: now, UpdatedAt: now,
		}))
		require.NoError(t, st.UserRecentBatchIDs().Insert(ctx, store.InsertUserRecentBatchIDParams{
			UserID: userid.MustNew(realUser.ID), BatchID: "real-recent", BodyHash: []byte("h"),
			PrincipalID: realUser.ID, CanonicalPhysicalMs: 1, CanonicalLogical: 0,
			CanonicalClient: "c", OpCount: 1, Epoch: 1, ExpiresAt: now.Add(time.Hour),
		}))
		require.NoError(t, st.LifecycleOutbox().Insert(ctx, store.InsertLifecycleOutboxParams{
			UserID: userid.MustNew(realUser.ID), OpType: "create", Payload: []byte("payload"),
		}))

		rows, err := st.UserOpBatches().ListAfter(ctx, store.ListUserOpBatchesAfterParams{
			UserID: userid.UserID{}, AfterPhysicalMs: 0, AfterLogical: 0, AfterOriginClient: "",
			Limit: store.CRDTBatchPageLimit,
		})
		require.NoError(t, err)
		assert.Empty(t, rows, "a blank user_id must not list another owner's op batches")

		n, err := st.UserOpBatches().Count(ctx, userid.UserID{})
		require.NoError(t, err)
		assert.Zero(t, n, "a blank user_id must not count another owner's op batches")

		_, err = st.UserState().Get(ctx, userid.UserID{})
		RequireNotFound(t, err)

		_, err = st.UserRecentBatchIDs().Get(ctx, userid.UserID{}, "real-recent")
		RequireNotFound(t, err)

		pending, err := st.LifecycleOutbox().ListPending(ctx, store.ListPendingLifecycleOutboxParams{
			UserID: userid.UserID{}, Limit: 100,
		})
		require.NoError(t, err)
		assert.Empty(t, pending, "a blank user_id must not list another owner's outbox rows")

		// Control: the gate still WORKS for a real owner.
		rows, err = st.UserOpBatches().ListAfter(ctx, store.ListUserOpBatchesAfterParams{
			UserID: userid.MustNew(realUser.ID), AfterPhysicalMs: 0, AfterLogical: 0, AfterOriginClient: "",
			Limit: store.CRDTBatchPageLimit,
		})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, "real-batch", rows[0].BatchID)
		// BOTH payloads, with DISTINCT values, and both directions asserted.
		// One-sided coverage cannot tell a swap from a correct read: the
		// postgres and mysql INSERTs gained `transitions_payload` in the MIDDLE
		// of their placeholder lists, so a positional slip there commits cleanly
		// and only surfaces as ErrResumeCorrupt on every resume for every user
		// on that backend -- with the sweep, boundary and LIMIT cases all still
		// green, because none of them reads a payload back.
		assert.Equal(t, []byte("real-body"), rows[0].BatchPayload,
			"batch_payload must round-trip through Insert → ListAfter across every backend")
		assert.Equal(t, []byte("t"), rows[0].TransitionsPayload,
			"transitions_payload must round-trip through Insert → ListAfter across every backend")
		assert.Equal(t, int64(1), rows[0].OpCount,
			"op_count is the completeness witness the resume scan gates on; a dialect that drops it bricks boot")

		state, err := st.UserState().Get(ctx, userid.MustNew(realUser.ID))
		require.NoError(t, err)
		assert.Equal(t, []byte("real-state"), state.StatePayload)
	})
}

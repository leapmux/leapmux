package storetest

import (
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
			BodyHash: []byte("h"), BatchPayload: []byte("p"), OpCount: 1, Epoch: 1,
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
			BodyHash: []byte("h"), BatchPayload: []byte("p"), OpCount: 1, Epoch: 1,
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

		state, err := st.UserState().Get(ctx, userid.MustNew(realUser.ID))
		require.NoError(t, err)
		assert.Equal(t, []byte("real-state"), state.StatePayload)
	})
}

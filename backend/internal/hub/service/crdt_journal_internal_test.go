package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// tabIndexKeys is a shape conversion from CRDT tab keys (untyped `UserID
// string`) to store keys (userid.UserID). It deliberately does NOT filter blank
// owners: a blank one mints to the ZERO UserID and travels on, because the
// refusal belongs to the store, which applies store.FilterTabIndexKeys at every
// site that binds an owner column so a future non-CRDT caller of
// BulkDeleteOwned inherits it. These cases pin the conversion (every key
// survives, fields map across) and, by asserting a blank owner passes THROUGH
// as the zero id, pin that this adapter is not silently re-acquiring a
// responsibility the store now owns -- see store.TestFilterTabIndexKeys and the
// storetest blank-owner case for the refusal itself.
func TestTabIndexKeys(t *testing.T) {
	t.Parallel()

	uid := userid.MustNew("u-real")

	t.Run("maps every field", func(t *testing.T) {
		got := tabIndexKeys([]crdt.TabKey{{UserID: uid.String(), TabID: "t1"}})
		if assert.Len(t, got, 1) {
			assert.Equal(t, uid.String(), got[0].UserID.String())
			assert.Equal(t, "t1", got[0].TabID)
		}
	})

	t.Run("passes a blank owner through to the store as the zero id", func(t *testing.T) {
		got := tabIndexKeys([]crdt.TabKey{
			{UserID: "", TabID: "blank"},
			{UserID: uid.String(), TabID: "real"},
		})
		require.Len(t, got, 2, "the adapter converts; the store refuses")
		assert.True(t, got[0].UserID.IsZero(), "a blank crdt owner mints to the zero UserID")
		assert.Equal(t, uid.String(), got[1].UserID.String())
		// ...and the store's guard is what drops it, keeping the neighbour.
		bound, dropped := store.FilterTabIndexKeys(got)
		assert.Equal(t, 1, dropped, "the store reports the drop rather than swallowing it")
		require.Len(t, bound, 1)
		assert.Equal(t, "real", bound[0].TabID())
	})

	t.Run("empty input", func(t *testing.T) {
		assert.Empty(t, tabIndexKeys(nil))
		assert.Empty(t, tabIndexKeys([]crdt.TabKey{}))
	})
}

// txTabIndexWriter supplies the owner column on the UPSERT paths from the
// COMMITTING tenant. There is no longer a competing source to prefer it over:
// crdt.TabIndexRow carries no owner at all (see
// crdt.TestTabIndexRowCarriesNoOwner), so a stale or foreign owner riding in on
// the diff is unspellable rather than merely ignored. This pins the other half
// of that shape -- every non-owner column still comes straight off the row.
func TestTxTabIndexWriterSuppliesTheCommittingTenant(t *testing.T) {
	t.Parallel()

	owner := userid.MustNew("u-committing")
	w := txTabIndexWriter{tx: nil, owner: owner}

	got := w.tabParams([]crdt.TabIndexRow{
		{WorkspaceID: "ws1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "t1", WorkerID: "wk1", TileID: "tile1", Position: "a0"},
		{WorkspaceID: "ws2", TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabID: "t2", WorkerID: "wk2", TileID: "tile2", Position: "a1"},
	})

	require.Len(t, got, 2)
	for i, p := range got {
		assert.Equal(t, owner.String(), p.UserID.String(), "row %d must be keyed by the committing tenant", i)
	}
	// Every other column still comes from the row.
	assert.Equal(t, store.UpsertOwnedTabParams{
		UserID: owner, WorkspaceID: "ws1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabID: "t1", WorkerID: "wk1", TileID: "tile1", Position: "a0",
	}, got[0])
	assert.Equal(t, store.UpsertOwnedTabParams{
		UserID: owner, WorkspaceID: "ws2", TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL,
		TabID: "t2", WorkerID: "wk2", TileID: "tile2", Position: "a1",
	}, got[1])

	assert.Empty(t, w.tabParams(nil))
}

// Every journal method mints the crdt side's `userID string` at the store
// boundary, and a blank one must fail closed rather than write a row whose
// owner no delete path could bind. Nothing produces a blank tenant today
// (crdt.Registry.Get refuses one), which is why the refusal is an ERROR: it
// reports a broken upstream invariant instead of silently doing nothing.
//
// The nil store is load-bearing: each method must refuse BEFORE touching it, so
// a method that lost its guard panics here instead of passing.
func TestCRDTJournalRefusesABlankTenant(t *testing.T) {
	t.Parallel()

	j := &crdtJournal{store: nil}
	ctx := context.Background()

	t.Run("LoadState", func(t *testing.T) {
		_, _, err := j.LoadState(ctx, "")
		assert.ErrorIs(t, err, errBlankTenant)
	})

	t.Run("AdvanceEpoch", func(t *testing.T) {
		assert.ErrorIs(t, j.AdvanceEpoch(ctx, "", 2, time.Now()), errBlankTenant)
	})

	t.Run("CommitBatch", func(t *testing.T) {
		// One tenant field, one mint: crdt.CommitBatch states the committing
		// user once, and every row the transaction writes (journal, dedup,
		// index views) takes its owner from it.
		assert.ErrorIs(t, j.CommitBatch(ctx, crdt.CommitBatch{
			UserID: "",
			Dedup:  crdt.DedupEntry{BatchID: "b1"},
		}), errBlankTenant)
	})

	t.Run("LookupRecentBatchID", func(t *testing.T) {
		// This one must refuse rather than fall through to the store: the
		// store's own blank-owner refusal is ErrNotFound, which this method
		// would translate to crdt.ErrNotFound -- indistinguishable from a
		// legitimate dedup miss, so a broken invariant would silently disable
		// retry idempotence instead of surfacing.
		_, err := j.LookupRecentBatchID(ctx, "", "batch-1")
		assert.ErrorIs(t, err, errBlankTenant)
	})

	t.Run("CompactBatch", func(t *testing.T) {
		assert.ErrorIs(t, j.CompactBatch(ctx, crdt.CompactBatch{
			State: &leapmuxv1.UserCrdtState{},
		}), errBlankTenant)
	})
}

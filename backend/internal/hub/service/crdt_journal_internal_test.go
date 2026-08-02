package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
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

// TestWrapCorruptRow_SentinelsViaErrorsIs pins the load-bearing contract
// that a per-row decode failure wraps as crdt.ErrResumeCorrupt and resolves
// via errors.Is — SubscribeWithACL branches on errors.Is(tailErr,
// crdt.ErrResumeCorrupt) to route a corrupt row to FALLBACK instead of failing
// the connection. The `%w` verb in wrapCorruptRow is what makes that
// resolve; this test fails immediately if someone changes it to `%v` or `%s`
// (which would make errors.Is return false and reconnect-loop the client on
// the bad row forever). The manager-level counterpart is
// TestSubscribeWithACL_CorruptRowFallsBackWithoutLosingOps, which injects the
// BARE sentinel; this one guards the WRAP the production path actually
// produces.
func TestWrapCorruptRow_SentinelsViaErrorsIs(t *testing.T) {
	t.Parallel()
	recoverable := wrapCorruptRow(true, "unmarshal transitions_payload", "batch-7", assert.AnError)
	assert.ErrorIs(t, recoverable, crdt.ErrResumeCorrupt,
		"a wrapped decode failure must resolve to crdt.ErrResumeCorrupt via errors.Is (the %%w verb is load-bearing)")

	// The two verdicts must be distinguishable, in BOTH directions. A single
	// sentinel for both made a fatal boot failure satisfy the predicate whose
	// documented meaning is "re-enter the full-snapshot FALLBACK path".
	fatal := wrapCorruptRow(false, "unmarshal user_op_batch", "batch-7", assert.AnError)
	assert.ErrorIs(t, fatal, crdt.ErrBootJournalCorrupt)
	assert.NotErrorIs(t, fatal, crdt.ErrResumeCorrupt,
		"a fatal boot failure must not be mistakable for a recoverable resume one")
	assert.NotErrorIs(t, recoverable, crdt.ErrBootJournalCorrupt)
}

// seedResumeJournal opens an in-memory store and writes `n` user_op_batches
// rows for one user, each holding a single op. Returns the journal, the owner,
// and the batch ids in commit order. `mangle` is applied to each row's params
// before insert so a test can corrupt a specific payload.
func seedResumeJournal(t *testing.T, n int, mangle func(i int, p *store.InsertUserOpBatchParams)) (*crdtJournal, userid.UserID) {
	t.Helper()
	st, err := sqlite.OpenTestable(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	// user_op_batches carries an FK to users, so the tenant must exist.
	owner := userid.MustNew(storetest.SeedUser(t, st, "resume-owner").ID)
	ctx := context.Background()
	for i := range n {
		batch := &leapmuxv1.OpBatch{
			BatchId: fmt.Sprintf("b%d", i),
			Ops: []*leapmuxv1.CrdtOp{{
				OpId: fmt.Sprintf("op%d", i),
				CanonicalHlc: &leapmuxv1.HLC{
					Physical: int64(1000 + i),
					Logical:  0,
					ClientId: "c1",
				},
			}},
		}
		payload, merr := proto.Marshal(batch)
		require.NoError(t, merr)
		transitions, merr := proto.Marshal(&leapmuxv1.BatchTransitions{})
		require.NoError(t, merr)
		p := store.InsertUserOpBatchParams{
			UserID:             owner,
			PhysicalMs:         int64(1000 + i),
			Logical:            0,
			LastLogical:        0,
			OriginClient:       "c1",
			PrincipalID:        "alice",
			BatchID:            batch.GetBatchId(),
			BodyHash:           []byte{byte(i)},
			BatchPayload:       payload,
			TransitionsPayload: transitions,
			OpCount:            1,
			Epoch:              1,
		}
		if mangle != nil {
			mangle(i, &p)
		}
		require.NoError(t, st.UserOpBatches().Insert(ctx, p))
	}
	return &crdtJournal{store: st}, owner
}

// nodeOpBatch builds a one-op batch whose target RESOLVES to a real entity, so
// a well-formed transitions_payload must carry an entry for it. (The
// seedResumeJournal default writes body-less ops, whose OpTarget is
// EntityKindUnknown and which therefore need no entry.)
func nodeOpBatch(t *testing.T, batchID, nodeID string) []byte {
	t.Helper()
	payload, err := proto.Marshal(&leapmuxv1.OpBatch{
		BatchId: batchID,
		Ops: []*leapmuxv1.CrdtOp{{
			OpId:         "op-" + nodeID,
			CanonicalHlc: &leapmuxv1.HLC{Physical: 1000, Logical: 0, ClientId: "c1"},
			Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
				NodeId: nodeID,
				Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: "p"},
			}},
		}},
	})
	require.NoError(t, err)
	return payload
}

// TestListBatchesAfter_IncompleteTransitionsIsCorrupt pins the completeness
// check on transitions_payload.
//
// The unmarshal guard alone cannot see this failure: proto3 repeated fields are
// length-delimited, so a payload truncated at an entry boundary -- or truncated
// all the way to zero bytes -- decodes WITHOUT error into a short entry list.
// Every op whose entry was lost then resolves to the zero transition
// {Pre:"", Post:""}, IsAllowed("") is false on both sides, filterVisibleOps
// drops it, and the batch ships as nothing but a BatchEnd that still advances
// the client's resume cursor past ops it never received. On the wire that is
// identical to the batch never having existed, and no later resume re-requests
// it. Comparing the entries against the batch's own ops is the only thing that
// tells those two apart.
func TestListBatchesAfter_IncompleteTransitionsIsCorrupt(t *testing.T) {
	t.Parallel()
	// Decodes cleanly, but carries no entry for n1.
	empty, err := proto.Marshal(&leapmuxv1.BatchTransitions{})
	require.NoError(t, err)

	j, owner := seedResumeJournal(t, 1, func(_ int, p *store.InsertUserOpBatchParams) {
		p.BatchPayload = nodeOpBatch(t, "b0", "n1")
		p.TransitionsPayload = empty
	})

	out, corrupt, err := j.scan(context.Background(), journalScan{owner: owner, mode: scanResume})
	assert.ErrorIs(t, err, crdt.ErrResumeCorrupt,
		"a transitions_payload that decodes but does not cover the batch's ops must route to FALLBACK")
	assert.Empty(t, out, "the scan must stop AT the bad row, not ship a prefix that omits it")
	require.Len(t, corrupt, 1)
	assert.Equal(t, "transitions_payload", corrupt[0].Field)
	assert.Equal(t, "b0", corrupt[0].BatchID)
}

// TestListBatchesAfter_TruncatedBatchPayloadIsCorrupt pins the batch_payload
// half of the same completeness gate.
//
// `ops` is a repeated field, so a batch_payload truncated at an element boundary
// -- including to zero bytes -- unmarshals WITHOUT error into a SHORT OpBatch.
// MissingTransitionOp cannot catch it (it tests ops ⊆ transitions, which passes
// vacuously on a short op list), so the short batch would ship its survivors and
// the BatchEnd would still advance the client past the whole row -- which
// ListAfter's strictly-greater cursor then guarantees is never re-sent. op_count
// is the independent witness: written from len(ops) at commit, DB-constrained
// > 0, and never touched by a payload truncation.
func TestListBatchesAfter_TruncatedBatchPayloadIsCorrupt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		payload func(t *testing.T) []byte
	}{
		{"decodes to zero ops", func(t *testing.T) []byte {
			// Well-formed and non-empty (batch_payload is NOT NULL), but its
			// whole repeated `ops` field is gone -- what truncating at the first
			// element boundary leaves behind.
			p, err := proto.Marshal(&leapmuxv1.OpBatch{BatchId: "b0"})
			require.NoError(t, err)
			return p
		}},
		{"truncated mid-list", func(t *testing.T) []byte {
			full := nodeOpBatch(t, "b0", "n1")
			return full[:len(full)/2]
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			j, owner := seedResumeJournal(t, 1, func(_ int, p *store.InsertUserOpBatchParams) {
				p.BatchPayload = tc.payload(t)
				// op_count records what was actually committed.
				p.OpCount = 1
			})

			out, corrupt, err := j.scan(context.Background(), journalScan{owner: owner, mode: scanResume})
			assert.ErrorIs(t, err, crdt.ErrResumeCorrupt,
				"a batch_payload carrying fewer ops than op_count must route to FALLBACK")
			assert.Empty(t, out, "the scan must stop AT the bad row")
			require.Len(t, corrupt, 1)
			assert.Equal(t, "batch_payload", corrupt[0].Field)
		})
	}
}

// The boot path has no snapshot to fall back to, so the same short row must be
// FATAL there rather than merely recoverable -- otherwise Bootstrap rebuilds a
// diverged state that the next maybeCompact persists as authoritative.
func TestListBatchesAfter_TruncatedBatchPayloadIsFatalAtBoot(t *testing.T) {
	t.Parallel()
	zeroOps, err := proto.Marshal(&leapmuxv1.OpBatch{BatchId: "b0"})
	require.NoError(t, err)
	j, owner := seedResumeJournal(t, 1, func(_ int, p *store.InsertUserOpBatchParams) {
		p.BatchPayload = zeroOps
		p.OpCount = 1
	})

	out, corrupt, err := j.scan(context.Background(), journalScan{owner: owner, mode: scanBoot})
	require.Error(t, err)
	// The BOOT sentinel: this scan is scanBoot, and the name of the
	// error is what tells a caller which of the two verdicts applies.
	assert.ErrorIs(t, err, crdt.ErrBootJournalCorrupt)
	assert.NotErrorIs(t, err, crdt.ErrResumeCorrupt)
	assert.Nil(t, out)
	assert.Nil(t, corrupt)
}

// TestListBatchesAfter_CompleteTransitionsAreNotCorrupt is the negative control
// for the check above: the completeness rule must not turn ordinary rows into
// spurious FALLBACKs. A row whose entries cover its ops reads clean.
func TestListBatchesAfter_CompleteTransitionsAreNotCorrupt(t *testing.T) {
	t.Parallel()
	covered, err := proto.Marshal(&leapmuxv1.BatchTransitions{
		Entries: []*leapmuxv1.BatchTransition{{
			Identity:      &leapmuxv1.BatchTransition_NodeId{NodeId: "n1"},
			PreWorkspace:  "w1",
			PostWorkspace: "w1",
		}},
	})
	require.NoError(t, err)

	j, owner := seedResumeJournal(t, 1, func(_ int, p *store.InsertUserOpBatchParams) {
		p.BatchPayload = nodeOpBatch(t, "b0", "n1")
		p.TransitionsPayload = covered
	})

	out, corrupt, err := j.scan(context.Background(), journalScan{owner: owner, mode: scanResume})
	require.NoError(t, err)
	assert.Empty(t, corrupt)
	require.Len(t, out, 1)
	assert.Equal(t, "b0", out[0].Batch.GetBatchId())
}

// TestListBatchesAfter_BootPathSkipsTransitionCompleteness pins that the check
// is scoped to the resume path. Bootstrap replays ops to rebuild state and asks
// for no transitions at all (scanBoot), so it must not be made
// to fail on a row the resume path would refuse -- there is no snapshot for it
// to fall back to.
func TestListBatchesAfter_BootPathSkipsTransitionCompleteness(t *testing.T) {
	t.Parallel()
	empty, err := proto.Marshal(&leapmuxv1.BatchTransitions{})
	require.NoError(t, err)

	j, owner := seedResumeJournal(t, 1, func(_ int, p *store.InsertUserOpBatchParams) {
		p.BatchPayload = nodeOpBatch(t, "b0", "n1")
		p.TransitionsPayload = empty
	})

	out, corrupt, err := j.scan(context.Background(), journalScan{owner: owner, mode: scanBoot})
	require.NoError(t, err, "boot decodes no transitions, so their completeness is not its concern")
	assert.Empty(t, corrupt)
	require.Len(t, out, 1)
}

// TestListBatchesAfter_ByteBudgetFallsBack pins the maxBytes ceiling on the
// REAL journal. It had no coverage at all: the manager always passes the
// package constant MaxResumeDeltaBytes (4 MiB), and the in-memory fake used to
// ignore the parameter outright, so every byte-budget FALLBACK was structurally
// unreachable from any test. The check must also run BEFORE the row is
// appended, so an over-budget row never rides along in the returned prefix.
func TestListBatchesAfter_ByteBudgetFallsBack(t *testing.T) {
	t.Parallel()
	j, owner := seedResumeJournal(t, 3, nil)

	// Budget of 1 byte: the very first row exceeds it.
	out, corrupt, err := j.scan(context.Background(), journalScan{owner: owner, maxBytes: 1, mode: scanResume})
	assert.ErrorIs(t, err, crdt.ErrDeltaTooLarge, "an over-budget tail must report ErrDeltaTooLarge")
	assert.Empty(t, out, "no row may be returned once the byte budget is blown")
	assert.Empty(t, corrupt)

	// A generous budget returns everything, proving the ceiling is what stopped
	// the scan above rather than an unrelated failure.
	out, _, err = j.scan(context.Background(), journalScan{owner: owner, maxBytes: 1 << 20, mode: scanResume})
	require.NoError(t, err)
	assert.Len(t, out, 3)
}

// TestListBatchesAfter_StopsOnCorruptPayload pins stop-on-corrupt on the REAL
// journal for BOTH payload columns, and pins that the recoverable flag selects
// the sentinel (resume) vs a hard error (boot).
func TestListBatchesAfter_StopsOnCorruptPayload(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		field  string
		mangle func(i int, p *store.InsertUserOpBatchParams)
	}{
		{
			name:  "batch_payload",
			field: "batch_payload",
			mangle: func(i int, p *store.InsertUserOpBatchParams) {
				if i == 1 {
					p.BatchPayload = []byte{0xFF, 0xFE, 0xFD}
				}
			},
		},
		{
			name:  "transitions_payload",
			field: "transitions_payload",
			mangle: func(i int, p *store.InsertUserOpBatchParams) {
				if i == 1 {
					p.TransitionsPayload = []byte{0xFF, 0xFE, 0xFD}
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			j, owner := seedResumeJournal(t, 3, tc.mangle)
			ctx := context.Background()

			// Resume path: recoverable -> ErrResumeCorrupt so the caller FALLBACKs.
			out, corrupt, err := j.scan(ctx, journalScan{owner: owner, mode: scanResume})
			assert.ErrorIs(t, err, crdt.ErrResumeCorrupt,
				"a corrupt %s must report ErrResumeCorrupt so buildResumeDelta falls back", tc.field)
			require.Len(t, corrupt, 1, "the offending row must be surfaced for logging")
			assert.Equal(t, "b1", corrupt[0].BatchID)
			assert.Equal(t, tc.field, corrupt[0].Field)
			// The scan must STOP: b2 (which would have advanced the client's
			// max_hlc past the hole) must not be in the prefix.
			assert.Len(t, out, 1, "the scan must stop at the corrupt row, not continue past it")

			// Boot path. Before the enum this read `{owner, decodeTransitions:
			// true}` -- the off-diagonal state, relying on `recoverable`'s zero
			// value to mean boot while asking for resume's transition decoding.
			// Naming the scan makes the two fields diverge, correctly:
			// scanBoot never reads transitions_payload, so only a corrupt
			// batch_payload can fail it.
			_, _, bootErr := j.scan(ctx, journalScan{owner: owner, mode: scanBoot})
			if tc.field == "transitions_payload" {
				assert.NoError(t, bootErr,
					"boot does not decode transitions_payload, so corruption there cannot fail it")
				return
			}
			require.Error(t, bootErr)
			{
				// The sentinel IS the verdict, so a fatal boot error must NOT
				// carry the recoverable one. It used to: this assertion read
				// `ErrorIs(bootErr, crdt.ErrResumeCorrupt)` and passed, pinning a
				// label whose documented meaning is "degrade to a snapshot" onto
				// the one path that has no snapshot to degrade to. Nothing
				// branches on it above registry.Get today; the first handler that
				// does would serve a snapshot built from a state blob missing
				// every op after the bad row and call the connect a success.
				assert.ErrorIs(t, bootErr, crdt.ErrBootJournalCorrupt,
					"a fatal boot failure carries the boot sentinel")
				assert.NotErrorIs(t, bootErr, crdt.ErrResumeCorrupt,
					"and must NOT carry the recoverable one, which means 'fall back to a snapshot'")
			}
		})
	}
}

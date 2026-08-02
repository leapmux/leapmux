package crdt_test

import (
	"context"
	"errors"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"google.golang.org/protobuf/proto"
)

// fakeJournal is an in-memory crdt.Journal used by the manager
// integration tests. It mirrors the production crdtJournal shape but
// keeps every row in memory, no transactions; the ATOMIC contract is
// trivially satisfied because the fake serializes through its own
// mutex.
type fakeJournal struct {
	mu       sync.Mutex
	state    *leapmuxv1.UserCrdtState
	stateRaw bool // true once Upsert has happened (compaction)

	// batches and transitions are parallel slices: one entry per committed
	// batch, paired the way the production journal's user_op_batches row +
	// transitions_payload column are. Append-only under f.mu.
	batches     []*leapmuxv1.OpBatch
	transitions []*leapmuxv1.BatchTransitions
	dedup       map[string]crdt.RecentBatchRecord // batch_id → row
	indexRows   map[string]crdt.TabIndexRow       // owned_tabs[tab_id] → row
	rendered    map[string]crdt.TabIndexRow       // rendered_tabs[tab_id] → row

	commitErr error // injectable CommitBatch failure
	lookupErr error // injectable LookupRecentBatchID failure
	listErr   error // injectable ListBatchesAfter failure (resume tail read)
	// corruptTransitions holds batch ids whose transitions_payload should be
	// reported as corrupt. The scan STOPS at that row and returns
	// ErrResumeCorrupt alongside the CorruptRow -- the batch's ops are NOT
	// shipped -- mirroring production, where continuing past the row would ship
	// a delta missing the batch while later frames still advanced the client's
	// watermark past it.
	corruptTransitions map[string]bool
	// listHold, when non-nil, blocks ListBatchesAfter until the channel is
	// closed before returning. Used by concurrency tests to hold a resume's
	// tail scan mid-flight and assert other manager operations are not stalled
	// on subscribeExpandMu / m.projection during the scan.
	listHold    chan struct{}
	listReached chan struct{} // closed when ListBatchesAfter enters the hold
}

func newFakeJournal() *fakeJournal {
	return &fakeJournal{
		dedup:     map[string]crdt.RecentBatchRecord{},
		indexRows: map[string]crdt.TabIndexRow{},
		rendered:  map[string]crdt.TabIndexRow{},
	}
}

func (f *fakeJournal) LoadState(_ context.Context, _ string) (*leapmuxv1.UserCrdtState, []*leapmuxv1.OpBatch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var state *leapmuxv1.UserCrdtState
	if f.state != nil {
		state = crdt.CloneState(f.state)
	}
	tail := append([]*leapmuxv1.OpBatch(nil), f.batches...)
	return state, tail, nil
}

// ListBatchesAfter mirrors crdtJournal.ListBatchesAfter against the in-memory
// batch slice: it returns batches (paired with their persisted transitions)
// whose FIRST op's canonical HLC is strictly after `cursor` (the same tuple
// ordering the SQL cursor uses), honoring the maxOps AND maxBytes budgets, the
// ErrDeltaTooLarge sentinel, and the stop-on-corrupt ErrResumeCorrupt contract,
// so resume tests exercise the same decision surface as production.
//
// maxBytes is measured as proto.Size(batch) + proto.Size(transitions), the
// fake's stand-in for len(batch_payload) + len(transitions_payload). Ignoring
// it (as this fake used to) made every byte-budget FALLBACK structurally
// unreachable from any manager-level test.
func (f *fakeJournal) ListBatchesAfter(_ context.Context, _ string, cursor, until *leapmuxv1.HLC, maxOps, maxBytes int) ([]crdt.ResumeBatch, []crdt.CorruptRow, error) {
	// Block BEFORE taking f.mu so a held scan does not self-deadlock against
	// the test's other fake-journal calls (the test closes listHold to release).
	// Signal entry so a test can deterministically wait for the scan to start.
	if f.listHold != nil {
		if f.listReached != nil {
			close(f.listReached)
		}
		<-f.listHold
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		// Injection hook for the recoverable sentinels (ErrDeltaTooLarge,
		// ErrResumeCorrupt) and for hard errors.
		return nil, nil, f.listErr
	}
	out := []crdt.ResumeBatch{}
	var corrupt []crdt.CorruptRow
	ops := 0
	bytes := 0
	for i, b := range f.batches {
		if len(b.GetOps()) == 0 {
			// A zero-op batch never matches (no HLC to order by) and would
			// panic the index below; the prod journal iterates GetOps() so it
			// is safe, and the manager rejects zero-op batches before they
			// reach the journal, but a test seeding batches directly must not
			// crash here.
			continue
		}
		first := b.GetOps()[0].GetCanonicalHlc()
		if crdt.HLCCmp(first, cursor) <= 0 {
			continue
		}
		if until != nil && crdt.HLCCmp(first, until) > 0 {
			return out, corrupt, nil
		}
		if maxOps > 0 && ops+len(b.GetOps()) > maxOps {
			return out, corrupt, crdt.ErrDeltaTooLarge
		}
		var trans *leapmuxv1.BatchTransitions
		if i < len(f.transitions) {
			trans = f.transitions[i]
		}
		// Byte budget, checked BEFORE appending (as production does) so an
		// over-budget row is never included in the returned prefix.
		rowBytes := proto.Size(b) + proto.Size(trans)
		if maxBytes > 0 && bytes+rowBytes > maxBytes {
			return out, corrupt, crdt.ErrDeltaTooLarge
		}
		// STOP-ON-CORRUPT: a seeded corrupt-transitions batch aborts the scan
		// with ErrResumeCorrupt so the manager FALLBACKs, mirroring production.
		// Substituting empty transitions and continuing would silently drop the
		// batch's ops while later frames advanced the client past it.
		if f.corruptTransitions[b.GetBatchId()] {
			corrupt = append(corrupt, crdt.CorruptRow{
				BatchID: b.GetBatchId(),
				Field:   "transitions_payload",
				Cause:   errors.Join(crdt.ErrResumeCorrupt, errors.New("fake: seeded corrupt transitions")),
			})
			return out, corrupt, crdt.ErrResumeCorrupt
		}
		ops += len(b.GetOps())
		bytes += rowBytes
		out = append(out, crdt.ResumeBatch{Batch: b, Transitions: trans})
	}
	return out, corrupt, nil
}

// seedState installs a compacted state payload, as if a previous compaction had
// written it. LoadState ignores the userID it is handed (the fake holds exactly
// one tenant's rows), which is what lets a test hand a manager a payload naming
// somebody else.
func (f *fakeJournal) seedState(state *leapmuxv1.UserCrdtState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = state
	f.stateRaw = true
}

func (f *fakeJournal) CommitBatch(_ context.Context, c crdt.CommitBatch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.commitErr != nil {
		return f.commitErr
	}
	f.batches = append(f.batches, c.Batch)
	f.transitions = append(f.transitions, c.Transitions)
	// The dedup TABLE row is the commit envelope's context plus the batch's own
	// fields; crdt.CommitBatch states the former once, so reassemble it here
	// the way the real journal adapter does when it writes the row.
	f.dedup[c.Dedup.BatchID] = crdt.RecentBatchRecord{
		UserID:            c.UserID,
		BatchID:           c.Dedup.BatchID,
		BodyHash:          c.Dedup.BodyHash,
		PrincipalID:       c.PrincipalID,
		CanonicalFirstHLC: c.Dedup.CanonicalFirstHLC,
		OpCount:           c.Dedup.OpCount,
		Epoch:             c.Epoch,
		ExpiresAt:         c.Dedup.ExpiresAt,
	}
	for _, row := range c.IndexDiff.OwnedUpserts {
		f.indexRows[row.TabID] = row
	}
	for _, key := range c.IndexDiff.OwnedDeletes {
		delete(f.indexRows, key.TabID)
	}
	for _, row := range c.IndexDiff.RenderedUpserts {
		f.rendered[row.TabID] = row
	}
	for _, key := range c.IndexDiff.RenderedDeletes {
		delete(f.rendered, key.TabID)
	}
	return nil
}

func (f *fakeJournal) LookupRecentBatchID(_ context.Context, _, batchID string) (*crdt.RecentBatchRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lookupErr != nil {
		return nil, f.lookupErr
	}
	row, ok := f.dedup[batchID]
	if !ok {
		return nil, crdt.ErrNotFound
	}
	clone := row
	return &clone, nil
}

func (f *fakeJournal) AdvanceEpoch(_ context.Context, _ string, epoch int64, startedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state != nil {
		f.state.CurrentEpoch = epoch
	}
	_ = startedAt
	return nil
}

func (f *fakeJournal) CompactBatch(_ context.Context, c crdt.CompactBatch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = crdt.CloneState(c.State)
	f.stateRaw = true
	if c.DropThrough == nil {
		return nil
	}
	kept := f.batches[:0]
	keptTransitions := f.transitions[:0]
	for i, batch := range f.batches {
		// Drop the batch if its last canonical HLC is ≤ watermark. Guard
		// zero-op batches the way ListBatchesAfter does: a test seeding
		// batches directly must not crash here, and such a batch has no HLC
		// to order by (so it can never be strictly > the watermark anyway).
		ops := batch.GetOps()
		if len(ops) == 0 {
			continue
		}
		last := ops[len(ops)-1].GetCanonicalHlc()
		if crdt.HLCCmp(last, c.DropThrough) > 0 {
			kept = append(kept, batch)
			// Keep the parallel slices in lockstep by index: append the paired
			// transition when present, or a nil placeholder when the batch was
			// seeded without one, so post-compaction kept[k] always pairs with
			// keptTransitions[k] (ListBatchesAfter already tolerates a nil
			// Transitions via its own bounds guard).
			var trans *leapmuxv1.BatchTransitions
			if i < len(f.transitions) {
				trans = f.transitions[i]
			}
			keptTransitions = append(keptTransitions, trans)
		}
	}
	f.batches = kept
	f.transitions = keptTransitions
	return nil
}

func (f *fakeJournal) CleanupExpiredRecentBatchIDs(_ context.Context, before time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	deleted := int64(0)
	for id, row := range f.dedup {
		if row.ExpiresAt.Before(before) {
			delete(f.dedup, id)
			deleted++
		}
	}
	return deleted, nil
}

func (f *fakeJournal) putDedup(row crdt.RecentBatchRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dedup[row.BatchID] = row
}

// snapshotIndex returns the current owned/rendered rows under lock.
func (f *fakeJournal) snapshotIndex() (owned, rendered map[string]crdt.TabIndexRow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	owned = make(map[string]crdt.TabIndexRow, len(f.indexRows))
	rendered = make(map[string]crdt.TabIndexRow, len(f.rendered))
	for k, v := range f.indexRows {
		owned[k] = v
	}
	for k, v := range f.rendered {
		rendered[k] = v
	}
	return
}

// dedupCount returns the number of rows currently in the dedup table.
func (f *fakeJournal) dedupCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.dedup)
}

// batchCount returns the number of journaled batches.
func (f *fakeJournal) batchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.batches)
}

// errCommitFailed is the canonical commit-failure used by tests that
// inject a journal error.
var errCommitFailed = errors.New("test: commit failed")

// errLookupFailed is the canonical dedup-lookup failure used by tests that
// inject a transient LookupRecentBatchID error.
var errLookupFailed = errors.New("test: dedup lookup failed")

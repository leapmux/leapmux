package crdt_test

import (
	"context"
	"errors"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// fakeJournal is an in-memory crdt.Journal used by the manager
// integration tests. It mirrors the production crdtJournal shape but
// keeps every row in memory, no transactions; the ATOMIC contract is
// trivially satisfied because the fake serializes through its own
// mutex.
type fakeJournal struct {
	mu       sync.Mutex
	state    *leapmuxv1.OrgCrdtState
	stateRaw bool // true once Upsert has happened (compaction)

	batches   []*leapmuxv1.OpBatch
	dedup     map[string]crdt.RecentBatchRecord // batch_id → row
	indexRows map[string]crdt.TabIndexRow       // owned_tabs[tab_id] → row
	rendered  map[string]crdt.TabIndexRow       // rendered_tabs[tab_id] → row

	commitErr error // injectable failure
}

func newFakeJournal() *fakeJournal {
	return &fakeJournal{
		dedup:     map[string]crdt.RecentBatchRecord{},
		indexRows: map[string]crdt.TabIndexRow{},
		rendered:  map[string]crdt.TabIndexRow{},
	}
}

func (f *fakeJournal) LoadState(_ context.Context, _ string) (*leapmuxv1.OrgCrdtState, []*leapmuxv1.OpBatch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var state *leapmuxv1.OrgCrdtState
	if f.state != nil {
		state = crdt.CloneState(f.state)
	}
	tail := append([]*leapmuxv1.OpBatch(nil), f.batches...)
	return state, tail, nil
}

func (f *fakeJournal) CommitBatch(_ context.Context, c crdt.CommitBatch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.commitErr != nil {
		return f.commitErr
	}
	f.batches = append(f.batches, c.Batch)
	f.dedup[c.DedupRow.BatchID] = c.DedupRow
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
	for _, batch := range f.batches {
		// Drop the batch if its last canonical HLC is ≤ watermark.
		ops := batch.GetOps()
		last := ops[len(ops)-1].GetCanonicalHlc()
		if crdt.HLCCmp(last, c.DropThrough) > 0 {
			kept = append(kept, batch)
		}
	}
	f.batches = kept
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

// fakeOutbox is a minimal LifecycleOutboxReader that never returns
// pending rows — adequate for the tests that don't exercise the
// outbox-driven lifecycle path.
type fakeOutbox struct{}

func (fakeOutbox) ListPendingLifecycleOutbox(_ context.Context, _ string) ([]crdt.LifecycleOutboxRow, error) {
	return nil, nil
}
func (fakeOutbox) MarkLifecycleOutboxConsumed(_ context.Context, _ int64, _ time.Time) error {
	return nil
}

// errCommitFailed is the canonical commit-failure used by tests that
// inject a journal error.
var errCommitFailed = errors.New("test: commit failed")

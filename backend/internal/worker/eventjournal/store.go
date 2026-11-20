package eventjournal

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"math"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

const terminalTabType = int64(leapmuxv1.TabType_TAB_TYPE_TERMINAL)

type retention struct {
	age                          time.Duration
	perTab, entries, bytes, tabs int64
}

func defaultRetention() retention {
	return retention{age: 7 * 24 * time.Hour, perTab: 4096, entries: 131072, bytes: 32 << 20, tabs: 4096}
}

type store struct {
	database  *sql.DB
	retention retention
}

type observation struct {
	tabID    string
	observed int64
	fact     []byte
	gap      leapmuxv1.SemanticWatchGapReason
}

func (s store) initialize(ctx context.Context, workerID string, owner userid.UserID, now time.Time) (row db.SemanticEventJournal, err error) {
	ownerID, ok := userid.OwnerFilter(owner)
	if !ok || workerID == "" {
		return row, errors.New("the event journal requires a Worker and its owner")
	}
	err = s.write(ctx, func(q *db.Queries) error {
		var err error
		row, err = q.GetSemanticEventJournal(ctx)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if row.WorkerID != workerID || row.OwnerUserID != ownerID {
			if err := q.DeleteSemanticEventTails(ctx); err != nil {
				return err
			}
			if err := q.DeleteSemanticEventJournal(ctx); err != nil {
				return err
			}
			key := make([]byte, 32)
			if _, err := rand.Read(key); err != nil {
				return err
			}
			if err := q.CreateSemanticEventJournal(ctx, db.CreateSemanticEventJournalParams{
				WorkerID: workerID, OwnerUserID: ownerID, JournalID: id.Generate(), CursorKey: key,
			}); err != nil {
				return err
			}
			row, err = q.GetSemanticEventJournal(ctx)
			return err
		}
		if err := s.pruneExpired(ctx, q, now); err != nil {
			return err
		}
		if row.Dirty != 0 {
			tails, err := q.ListSemanticEventTails(ctx)
			if err != nil {
				return err
			}
			for _, tail := range tails {
				_, err := s.append(ctx, q, observation{tabID: tail.TabID, observed: now.UnixMilli(),
					gap: leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_UNCLEAN_STOP})
				if err != nil {
					return err
				}
			}
		}
		return q.SetSemanticEventJournalDirty(ctx, 1)
	})
	return row, err
}

func (s store) write(ctx context.Context, apply func(*db.Queries) error) error {
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := apply(db.New(tx)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s store) ensureTail(ctx context.Context, q *db.Queries, tabID string, observed int64) (db.SemanticEventTail, error) {
	key := db.GetSemanticEventTailParams{TabType: terminalTabType, TabID: tabID}
	tail, err := q.GetSemanticEventTail(ctx, key)
	if !errors.Is(err, sql.ErrNoRows) {
		return tail, err
	}
	count, err := q.CountSemanticEventTails(ctx)
	if err != nil {
		return tail, err
	}
	if count >= s.retention.tabs {
		victim, err := q.GetOldestSemanticEventTailExcept(ctx, db.GetOldestSemanticEventTailExceptParams(key))
		if err != nil {
			return tail, err
		}
		if _, err := s.pruneThrough(ctx, q, victim.TabID, victim.HighSequence); err != nil {
			return tail, err
		}
		if err := q.DeleteSemanticEventTail(ctx, db.DeleteSemanticEventTailParams{TabType: terminalTabType, TabID: victim.TabID}); err != nil {
			return tail, err
		}
	}
	if err := q.InitSemanticEventTail(ctx, db.InitSemanticEventTailParams{
		TabType: terminalTabType, TabID: tabID, TabStreamID: id.Generate(), LastObservedUnixMillis: observed,
	}); err != nil {
		return tail, err
	}
	return q.GetSemanticEventTail(ctx, key)
}

func (s store) append(ctx context.Context, q *db.Queries, item observation) (int64, error) {
	if item.gap != 0 {
		item.fact = []byte{}
	}
	tail, err := s.ensureTail(ctx, q, item.tabID, item.observed)
	if err != nil {
		return 0, err
	}
	if tail.HighSequence == math.MaxInt64 {
		return 0, errors.New("the tab's event sequence is exhausted")
	}
	sequence, err := q.AdvanceSemanticEventTail(ctx, db.AdvanceSemanticEventTailParams{
		TabType: terminalTabType, TabID: item.tabID, ObservedUnixMillis: item.observed,
	})
	if err != nil {
		return 0, err
	}
	if err := q.InsertSemanticEventEntry(ctx, db.InsertSemanticEventEntryParams{
		TabType: terminalTabType, TabID: item.tabID, Sequence: sequence, ObservedUnixMillis: item.observed,
		Fact: item.fact, GapReason: sql.NullInt64{Valid: item.gap != 0, Int64: int64(item.gap)},
	}); err != nil {
		return 0, err
	}
	usage, err := q.ChangeSemanticEventJournalUsage(ctx, db.ChangeSemanticEventJournalUsageParams{EntryDelta: 1, ByteDelta: int64(len(item.fact))})
	if err != nil {
		return 0, err
	}
	if cutoff := sequence - s.retention.perTab; cutoff > tail.FloorSequence {
		usage, err = s.pruneThrough(ctx, q, item.tabID, cutoff)
		if err != nil {
			return 0, err
		}
	}
	for usage.EntryCount > s.retention.entries || usage.PayloadBytes > s.retention.bytes {
		oldest, err := q.GetOldestSemanticEventEntry(ctx)
		if err != nil {
			return 0, err
		}
		usage, err = s.pruneThrough(ctx, q, oldest.TabID, oldest.Sequence)
		if err != nil {
			return 0, err
		}
	}
	return sequence, nil
}

func (s store) pruneThrough(ctx context.Context, q *db.Queries, tabID string, sequence int64) (db.SemanticEventJournal, error) {
	usage, err := q.ReadSemanticEventUsageThrough(ctx, db.ReadSemanticEventUsageThroughParams{
		TabType: terminalTabType, TabID: tabID, ThroughSequence: sequence,
	})
	if err != nil {
		return db.SemanticEventJournal{}, err
	}
	if err := q.DeleteSemanticEventsThrough(ctx, db.DeleteSemanticEventsThroughParams{
		TabType: terminalTabType, TabID: tabID, ThroughSequence: sequence,
	}); err != nil {
		return db.SemanticEventJournal{}, err
	}
	if err := q.AdvanceSemanticEventFloor(ctx, db.AdvanceSemanticEventFloorParams{
		TabType: terminalTabType, TabID: tabID, FloorSequence: sequence,
	}); err != nil {
		return db.SemanticEventJournal{}, err
	}
	return q.ChangeSemanticEventJournalUsage(ctx, db.ChangeSemanticEventJournalUsageParams{
		EntryDelta: -usage.Entries, ByteDelta: -usage.Bytes,
	})
}

func (s store) pruneExpired(ctx context.Context, q *db.Queries, now time.Time) error {
	cutoffs, err := q.GetExpiredSemanticEventCutoffs(ctx, now.Add(-s.retention.age).UnixMilli())
	if err != nil {
		return err
	}
	for _, cutoff := range cutoffs {
		if _, err := s.pruneThrough(ctx, q, cutoff.TabID, cutoff.Cutoff); err != nil {
			return err
		}
	}
	return nil
}

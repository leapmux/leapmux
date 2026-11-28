package eventjournal

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"math"
	"slices"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

const globalEvictionBatchSize = 64

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

type journalOp interface {
	journalOp()
}

type appendOp struct {
	tab      Tab
	observed int64
	fact     []byte
	gap      leapmuxv1.SemanticWatchGapReason
}

func (appendOp) journalOp() {}

type sealOp struct {
	tabs     []Tab
	observed int64
	gap      leapmuxv1.SemanticWatchGapReason
}

func (sealOp) journalOp() {}

type batchUsage struct {
	entries      int64
	bytes        int64
	affectedTabs []Tab
}

func (u *batchUsage) noteTab(tab Tab) {
	if u == nil {
		return
	}
	if !slices.Contains(u.affectedTabs, tab) {
		u.affectedTabs = append(u.affectedTabs, tab)
	}
}

func (u *batchUsage) flush(ctx context.Context, q *db.Queries) (db.SemanticEventJournal, error) {
	row, err := q.ChangeSemanticEventJournalUsage(ctx, db.ChangeSemanticEventJournalUsageParams{
		EntryDelta: u.entries, ByteDelta: u.bytes,
	})
	if err == nil {
		u.entries, u.bytes = 0, 0
	}
	return row, err
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
		var usage batchUsage
		if err := s.pruneExpired(ctx, q, now, &usage); err != nil {
			return err
		}
		if row.Dirty != 0 {
			// The set of lost publications can include tabs whose tails were
			// never created or are no longer retained.
			for _, tabType := range supportedTabTypes {
				if _, err := advanceFeedPosition(ctx, q, tabType); err != nil {
					return err
				}
			}
		}
		tails, err := q.ListUnsealedSemanticEventTails(ctx, row.Dirty)
		if err != nil {
			return err
		}
		observed := now.UnixMilli()
		gap := leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_UNCLEAN_STOP
		for _, tail := range tails {
			if tail.ResourceClosed {
				if err := s.sealTail(ctx, q, tail, observed, gap, &usage); err != nil {
					return err
				}
			} else if row.Dirty != 0 {
				item := appendOp{tab: Tab{Type: tail.TabType, ID: tail.TabID}, observed: observed, gap: gap}
				if _, err := s.appendToTail(ctx, q, tail, item, &usage); err != nil {
					return err
				}
			}
		}
		if err := s.flushAndEnforceRetention(ctx, q, &usage); err != nil {
			return err
		}
		return q.SetSemanticEventJournalDirty(ctx, 1)
	})
	return row, err
}

func (s store) read(ctx context.Context, apply func(*db.Queries) error) error {
	tx, err := s.database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	return apply(db.New(tx))
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

func (s store) ensureTail(ctx context.Context, q *db.Queries, tab Tab, observed int64, verifiedLive bool, usage *batchUsage) (db.SemanticEventTail, error) {
	key := db.GetSemanticEventTailParams{TabType: tab.Type, TabID: tab.ID}
	tail, err := q.GetSemanticEventTail(ctx, key)
	if !errors.Is(err, sql.ErrNoRows) {
		return tail, err
	}
	if !verifiedLive {
		live, err := tab.live(ctx, q)
		if err != nil {
			return tail, err
		}
		if !live {
			return tail, ErrAdmission
		}
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
		victimTab := Tab{Type: victim.TabType, ID: victim.TabID}
		if usage != nil && (usage.entries != 0 || usage.bytes != 0) {
			if _, err := usage.flush(ctx, q); err != nil {
				return tail, err
			}
		}
		if _, err := s.pruneThrough(ctx, q, victimTab, victim.HighSequence); err != nil {
			return tail, err
		}
		if err := q.DeleteSemanticEventTail(ctx, db.DeleteSemanticEventTailParams{TabType: victim.TabType, TabID: victim.TabID}); err != nil {
			return tail, err
		}
		usage.noteTab(victimTab)
	}
	return q.InitSemanticEventTail(ctx, db.InitSemanticEventTailParams{
		TabType: tab.Type, TabID: tab.ID, TabStreamID: id.Generate(), LastObservedUnixMillis: observed,
	})
}

func (s store) append(ctx context.Context, q *db.Queries, item appendOp, usage *batchUsage) (int64, error) {
	tail, err := s.ensureTail(ctx, q, item.tab, item.observed, false, usage)
	if errors.Is(err, ErrAdmission) {
		return 0, recordUnverifiableClosedFact(ctx, q, item.tab)
	}
	if err != nil {
		return 0, err
	}
	return s.appendToTail(ctx, q, tail, item, usage)
}

func (s store) appendToTail(ctx context.Context, q *db.Queries, tail db.SemanticEventTail, item appendOp, usage *batchUsage) (int64, error) {
	if item.gap != 0 {
		item.fact = []byte{}
	}
	if tail.FinalSequence.Valid {
		return tail.FinalSequence.Int64, nil
	}
	if tail.HighSequence == math.MaxInt64 {
		return 0, errors.New("the tab's event sequence is exhausted")
	}
	sequence, err := q.AdvanceSemanticEventTail(ctx, db.AdvanceSemanticEventTailParams{
		TabType: item.tab.Type, TabID: item.tab.ID, ObservedUnixMillis: item.observed,
	})
	if err != nil {
		return 0, err
	}
	feedSequence, err := advanceFeedPosition(ctx, q, item.tab.Type)
	if err != nil {
		return 0, err
	}
	if err := q.InsertSemanticEventEntry(ctx, db.InsertSemanticEventEntryParams{
		TabType: item.tab.Type, TabID: item.tab.ID, Sequence: sequence,
		FeedSequence: feedSequence, ObservedUnixMillis: item.observed,
		Fact:      item.fact,
		GapReason: workerdb.OptionalStorageEnum(item.gap),
	}); err != nil {
		return 0, err
	}
	usage.entries++
	usage.bytes += int64(len(item.fact))
	usage.noteTab(item.tab)
	if cutoff := sequence - s.retention.perTab; cutoff > tail.FloorSequence {
		if _, err := usage.flush(ctx, q); err != nil {
			return 0, err
		}
		if _, err := s.pruneThrough(ctx, q, item.tab, cutoff); err != nil {
			return 0, err
		}
	}
	return sequence, nil
}

func (s store) flushAndEnforceRetention(ctx context.Context, q *db.Queries, usage *batchUsage) error {
	if usage.entries == 0 && usage.bytes == 0 {
		return nil
	}
	current, err := usage.flush(ctx, q)
	if err != nil {
		return err
	}
	for current.EntryCount > s.retention.entries || current.PayloadBytes > s.retention.bytes {
		oldest, err := q.ListOldestSemanticEventEntries(ctx, globalEvictionBatchSize)
		if err != nil {
			return err
		}
		if len(oldest) == 0 {
			break
		}
		var droppedEntries, droppedBytes int64
		cutoffs := make(map[Tab]int64, len(oldest))
		var tabs []Tab
		for _, row := range oldest {
			tab := Tab{Type: row.TabType, ID: row.TabID}
			if _, seen := cutoffs[tab]; !seen {
				tabs = append(tabs, tab)
			}
			if row.Sequence > cutoffs[tab] {
				cutoffs[tab] = row.Sequence
			}
			droppedEntries++
			droppedBytes += row.PayloadBytes
			if current.EntryCount-droppedEntries <= s.retention.entries && current.PayloadBytes-droppedBytes <= s.retention.bytes {
				break
			}
		}
		for _, tab := range tabs {
			current, err = s.pruneThrough(ctx, q, tab, cutoffs[tab])
			if err != nil {
				return err
			}
			usage.noteTab(tab)
		}
	}
	return nil
}

func advanceFeedPosition(ctx context.Context, q *db.Queries, tabType leapmuxv1.TabType) (int64, error) {
	sequence, err := q.AdvanceSemanticEventFeed(ctx, tabType)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errors.New("the Worker's event sequence is exhausted")
	}
	return sequence, err
}

// An eligible resource can close before the writer checks whether it has a
// tail. Reserve a missing feed position when the fact's admission is unknown.
// An already closed tab cannot start an exact history and evict a live tail.
func recordUnverifiableClosedFact(ctx context.Context, q *db.Queries, tab Tab) error {
	closed, err := tab.closed(ctx, q)
	if err != nil || !closed {
		return err
	}
	_, err = advanceFeedPosition(ctx, q, tab.Type)
	return err
}

func (s store) seal(ctx context.Context, q *db.Queries, tab Tab, observed int64, gap leapmuxv1.SemanticWatchGapReason, usage *batchUsage) error {
	key := db.GetSemanticEventTailParams{TabType: tab.Type, TabID: tab.ID}
	tail, err := q.GetSemanticEventTail(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		if gap != 0 {
			return recordUnverifiableClosedFact(ctx, q, tab)
		}
		return nil
	}
	if err != nil {
		return err
	}
	return s.sealTail(ctx, q, tail, observed, gap, usage)
}

func (s store) sealTail(ctx context.Context, q *db.Queries, tail db.SemanticEventTail, observed int64, gap leapmuxv1.SemanticWatchGapReason, usage *batchUsage) error {
	if !tail.ResourceClosed || tail.FinalSequence.Valid {
		return nil
	}
	tab := Tab{Type: tail.TabType, ID: tail.TabID}
	if gap != 0 {
		if _, err := s.appendToTail(ctx, q, tail, appendOp{tab: tab, observed: observed, gap: gap}, usage); err != nil {
			return err
		}
	}
	usage.noteTab(tab)
	return q.SealSemanticEventTail(ctx, db.SealSemanticEventTailParams{TabType: tail.TabType, TabID: tail.TabID})
}

func (s store) pruneThrough(ctx context.Context, q *db.Queries, tab Tab, sequence int64) (db.SemanticEventJournal, error) {
	usage, err := q.ReadSemanticEventUsageThrough(ctx, db.ReadSemanticEventUsageThroughParams{
		TabType: tab.Type, TabID: tab.ID, ThroughSequence: sequence,
	})
	if err != nil {
		return db.SemanticEventJournal{}, err
	}
	if err := q.DeleteSemanticEventsThrough(ctx, db.DeleteSemanticEventsThroughParams{
		TabType: tab.Type, TabID: tab.ID, ThroughSequence: sequence,
	}); err != nil {
		return db.SemanticEventJournal{}, err
	}
	if err := q.AdvanceSemanticEventFloor(ctx, db.AdvanceSemanticEventFloorParams{
		TabType: tab.Type, TabID: tab.ID, FloorSequence: sequence,
	}); err != nil {
		return db.SemanticEventJournal{}, err
	}
	return q.ChangeSemanticEventJournalUsage(ctx, db.ChangeSemanticEventJournalUsageParams{
		EntryDelta: -usage.Entries, ByteDelta: -usage.Bytes,
	})
}

func (s store) pruneExpired(ctx context.Context, q *db.Queries, now time.Time, usage *batchUsage) error {
	expiredAt := now.Add(-s.retention.age).UnixMilli()
	hasExpired, err := q.HasExpiredSemanticEventEntries(ctx, expiredAt)
	if err != nil || !hasExpired {
		return err
	}
	if usage != nil && (usage.entries != 0 || usage.bytes != 0) {
		if _, err := usage.flush(ctx, q); err != nil {
			return err
		}
	}
	cutoffs, err := q.GetExpiredSemanticEventCutoffs(ctx, expiredAt)
	if err != nil {
		return err
	}
	for _, cutoff := range cutoffs {
		tab := Tab{Type: cutoff.TabType, ID: cutoff.TabID}
		if _, err := s.pruneThrough(ctx, q, tab, cutoff.Cutoff); err != nil {
			return err
		}
		usage.noteTab(tab)
	}
	return nil
}

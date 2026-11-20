package eventjournal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

const readBatchSize = 32

// Reader follows a single admitted tab generation. It has no event queue.
type Reader struct {
	journal  *Journal
	tab      Tab
	streamID string
}

// Open validates a cursor before exposing positions. Creating a fresh cursor
// also gives a previously quiet tab an identity that survives a restart.
func (j *Journal) Open(ctx context.Context, tab Tab, cursor []byte) (reader *Reader, ready *leapmuxv1.SemanticWatchReady, err error) {
	if !tab.Valid() {
		return nil, nil, ErrCursor
	}
	if err := j.barrier(ctx); err != nil {
		return nil, nil, err
	}
	err = j.store.write(ctx, func(q *db.Queries) error {
		now := j.clock.Now()
		var tail db.SemanticEventTail
		var after int64
		var err error
		if len(cursor) == 0 {
			live, liveErr := tab.live(ctx, q)
			if liveErr != nil {
				return liveErr
			}
			if !live {
				return ErrAdmission
			}
			tail, err = j.store.ensureTail(ctx, q, tab, now.UnixMilli())
			after = tail.HighSequence
		} else {
			tail, err = q.GetSemanticEventTail(ctx, db.GetSemanticEventTailParams{TabType: int64(tab.Type), TabID: tab.ID})
			if errors.Is(err, sql.ErrNoRows) {
				return ErrCursor
			}
			if err == nil {
				after, err = j.identity.position(tab, tail.TabStreamID, cursor)
				if after > tail.HighSequence {
					return ErrCursor
				}
			}
		}
		if err != nil {
			return err
		}
		if err := j.store.pruneExpired(ctx, q, now); err != nil {
			return err
		}
		reader = &Reader{journal: j, tab: tab, streamID: tail.TabStreamID}
		ready = &leapmuxv1.SemanticWatchReady{
			Tab:      &leapmuxv1.TabIdent{TabType: tab.Type, TabId: tab.ID},
			WorkerId: j.identity.workerID, JournalId: j.identity.journalID, TabStreamId: tail.TabStreamID,
			AfterSequence: after, HighWaterSequence: tail.HighSequence, Cursor: reader.cursor(after),
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.signalLocked()
	if err := j.checkAvailableLocked(); err != nil {
		return nil, nil, err
	}
	return reader, ready, nil
}

func (r *Reader) cursor(sequence int64) []byte {
	return r.journal.identity.sign(r.tab, r.streamID, sequence)
}

// Read returns a consecutive page after the supplied position. The wake
// channel is captured before the query, so waiting after an empty page cannot
// miss a commit made during the query or before the wait starts.
func (r *Reader) Read(ctx context.Context, after int64) ([]*leapmuxv1.WatchSemanticEventsResponse, <-chan struct{}, error) {
	j := r.journal
	j.mu.Lock()
	changed, available := j.changed, j.checkAvailableLocked()
	j.mu.Unlock()
	if available != nil {
		return nil, nil, available
	}
	tx, err := j.store.database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)
	tail, err := q.GetSemanticEventTail(ctx, db.GetSemanticEventTailParams{TabType: int64(r.tab.Type), TabID: r.tab.ID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrCursor
	}
	if err != nil {
		return nil, nil, err
	}
	if tail.TabStreamID != r.streamID || after < 0 || after > tail.HighSequence {
		return nil, nil, ErrCursor
	}
	if after < tail.FloorSequence {
		gap := r.gap(leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_RETENTION, after, tail.FloorSequence)
		return []*leapmuxv1.WatchSemanticEventsResponse{gap}, changed, nil
	}
	if after == tail.HighSequence {
		if tail.FinalSequence.Valid {
			retired := &leapmuxv1.SemanticWatchRetired{FinalSequence: tail.FinalSequence.Int64}
			return []*leapmuxv1.WatchSemanticEventsResponse{
				{Event: &leapmuxv1.WatchSemanticEventsResponse_Retired{Retired: retired}},
			}, changed, nil
		}
		return nil, changed, nil
	}
	expired, err := q.GetExpiredSemanticEventCutoff(ctx, db.GetExpiredSemanticEventCutoffParams{
		TabType: int64(r.tab.Type), TabID: r.tab.ID, ExpiredAt: j.clock.Now().Add(-j.store.retention.age).UnixMilli(),
	})
	if err != nil {
		return nil, nil, err
	}
	if after < expired {
		// End the read transaction before acquiring the write lock. Persist
		// the cutoff so a later wall clock adjustment cannot restore it.
		if err := tx.Rollback(); err != nil {
			return nil, nil, err
		}
		floor, err := r.persistExpiredCutoff(ctx, expired)
		if err != nil {
			return nil, nil, err
		}
		gap := r.gap(leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_RETENTION, after, floor)
		return []*leapmuxv1.WatchSemanticEventsResponse{gap}, changed, nil
	}
	rows, err := q.ListSemanticEventEntriesAfter(ctx, db.ListSemanticEventEntriesAfterParams{
		TabType: int64(r.tab.Type), TabID: r.tab.ID, AfterSequence: after, PageSize: readBatchSize,
	})
	if err != nil {
		return nil, nil, err
	}
	if len(rows) == 0 {
		return nil, nil, fmt.Errorf("%w: missing committed events", ErrUnavailable)
	}
	frames := make([]*leapmuxv1.WatchSemanticEventsResponse, 0, len(rows))
	for _, row := range rows {
		if row.Sequence != after+1 {
			return nil, nil, fmt.Errorf("%w: discontinuous event sequence", ErrUnavailable)
		}
		if row.GapReason.Valid {
			frames = append(frames, r.gap(leapmuxv1.SemanticWatchGapReason(row.GapReason.Int64), after, row.Sequence))
		} else {
			var fact leapmuxv1.SemanticTabFact
			if err := proto.Unmarshal(row.Fact, &fact); err != nil || fact.GetEvent() == nil {
				return nil, nil, fmt.Errorf("%w: invalid stored event", ErrUnavailable)
			}
			frames = append(frames, &leapmuxv1.WatchSemanticEventsResponse{Event: &leapmuxv1.WatchSemanticEventsResponse_Record{
				Record: &leapmuxv1.SemanticWatchRecord{Sequence: row.Sequence, ObservedUnixMillis: row.ObservedUnixMillis, Fact: &fact, Cursor: r.cursor(row.Sequence)},
			}})
		}
		after = row.Sequence
	}
	return frames, changed, nil
}

func (r *Reader) persistExpiredCutoff(ctx context.Context, expired int64) (int64, error) {
	j := r.journal
	var floor int64
	err := j.store.write(ctx, func(q *db.Queries) error {
		tail, err := q.GetSemanticEventTail(ctx, db.GetSemanticEventTailParams{TabType: int64(r.tab.Type), TabID: r.tab.ID})
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCursor
		}
		if err != nil {
			return err
		}
		if tail.TabStreamID != r.streamID {
			return ErrCursor
		}
		floor = max(tail.FloorSequence, expired)
		if floor == tail.FloorSequence {
			return nil
		}
		_, err = j.store.pruneThrough(ctx, q, r.tab, floor)
		return err
	})
	return floor, err
}

func (r *Reader) gap(reason leapmuxv1.SemanticWatchGapReason, after, through int64) *leapmuxv1.WatchSemanticEventsResponse {
	return &leapmuxv1.WatchSemanticEventsResponse{Event: &leapmuxv1.WatchSemanticEventsResponse_Gap{
		Gap: &leapmuxv1.SemanticWatchGap{Reason: reason, AfterSequence: after, ResumeAfterSequence: through, Cursor: r.cursor(through)},
	}}
}

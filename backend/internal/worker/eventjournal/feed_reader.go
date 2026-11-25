package eventjournal

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/semanticevents"
	"github.com/leapmux/leapmux/internal/util/seqcursor"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// FeedReader follows all retained tabs of one kind on the Worker.
type FeedReader struct {
	journal *Journal
	tabType leapmuxv1.TabType
}

// OpenFeed admits a Worker feed at its current tail or at a signed position.
func (j *Journal) OpenFeed(ctx context.Context, tabType leapmuxv1.TabType, cursor []byte) (*FeedReader, *leapmuxv1.SemanticWorkerWatchReady, error) {
	if !SupportsTabType(tabType) {
		return nil, nil, ErrFeedCursor
	}
	if err := j.barrier(ctx); err != nil {
		return nil, nil, err
	}
	var (
		after, high int64
		expired     bool
		usage       batchUsage
	)
	now := j.clock.Now()
	expiredAt := now.Add(-j.store.retention.age).UnixMilli()
	err := j.store.read(ctx, func(q *db.Queries) error {
		var err error
		high, err = q.GetSemanticEventFeedPosition(ctx, tabType)
		if err != nil {
			return err
		}
		after = high
		if len(cursor) != 0 {
			after, err = j.identity.feedPosition(tabType, cursor)
			if err != nil || after > high {
				return ErrFeedCursor
			}
		}
		expired, err = q.HasExpiredSemanticEventEntries(ctx, expiredAt)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	if expired {
		err = j.store.write(ctx, func(q *db.Queries) error {
			var err error
			high, err = q.GetSemanticEventFeedPosition(ctx, tabType)
			if err != nil {
				return err
			}
			after = high
			if len(cursor) != 0 {
				after, err = j.identity.feedPosition(tabType, cursor)
				if err != nil || after > high {
					return ErrFeedCursor
				}
			}
			return j.store.pruneExpired(ctx, q, j.clock.Now(), &usage)
		})
		if err != nil {
			return nil, nil, err
		}
	}
	if err := j.finishOpen(usage.affectedTabs); err != nil {
		return nil, nil, err
	}
	reader := &FeedReader{journal: j, tabType: tabType}
	return reader, &leapmuxv1.SemanticWorkerWatchReady{
		TabType: tabType, WorkerId: j.identity.workerID, JournalId: j.identity.journalID,
		AfterSequence: after, HighWaterSequence: high, Cursor: reader.cursor(after),
	}, nil
}

func (r *FeedReader) cursor(sequence int64) []byte {
	return r.journal.identity.signFeedPosition(r.tabType, sequence)
}

// Read returns retained records and missing intervals from one committed view.
// It captures the wake channel before the query.
func (r *FeedReader) Read(ctx context.Context, after int64) ([]*leapmuxv1.WatchWorkerSemanticEventsResponse, <-chan struct{}, error) {
	j := r.journal
	changed, available := j.watchFeed(r.tabType)
	if available != nil {
		return nil, nil, available
	}
	for {
		now := j.clock.Now()
		rows, high, expired, err := r.snapshot(ctx, after, now)
		if err != nil {
			return nil, nil, err
		}
		if !expired {
			frames, err := r.frames(rows, after, high)
			return frames, changed, err
		}
		var usage batchUsage
		err = j.store.write(ctx, func(q *db.Queries) error {
			return j.store.pruneExpired(ctx, q, now, &usage)
		})
		if err != nil {
			return nil, nil, err
		}
	}
}

func (r *FeedReader) snapshot(ctx context.Context, after int64, now time.Time) ([]db.SemanticEventEntry, int64, bool, error) {
	j := r.journal
	var (
		rows    []db.SemanticEventEntry
		high    int64
		expired bool
	)
	err := j.store.read(ctx, func(q *db.Queries) error {
		var err error
		high, err = q.GetSemanticEventFeedPosition(ctx, r.tabType)
		if err != nil {
			return err
		}
		if after < 0 || after > high {
			return ErrFeedCursor
		}
		if after == high {
			return nil
		}
		expired, err = q.HasExpiredSemanticEventEntries(ctx, now.Add(-j.store.retention.age).UnixMilli())
		if expired || err != nil {
			return err
		}
		rows, err = q.ListSemanticEventFeedEntriesAfter(ctx, db.ListSemanticEventFeedEntriesAfterParams{
			TabType: r.tabType, AfterSequence: after, PageSize: readBatchSize,
		})
		return err
	})
	return rows, high, expired, err
}

func (r *FeedReader) frames(rows []db.SemanticEventEntry, after, high int64) ([]*leapmuxv1.WatchWorkerSemanticEventsResponse, error) {
	var frames []*leapmuxv1.WatchWorkerSemanticEventsResponse
	signer := r.journal.identity.feedSigner(r.tabType)
	missing := leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_HISTORY_UNAVAILABLE
	for _, row := range rows {
		sequence := row.FeedSequence
		if sequence <= after || sequence > high {
			return nil, fmt.Errorf("%w: invalid Worker event position", ErrUnavailable)
		}
		if sequence-1 > after {
			frames = append(frames, feedGapFrame(signer, missing, after, sequence-1))
			after = sequence - 1
		}
		if row.GapReason != nil {
			frames = append(frames, feedGapFrame(signer, *row.GapReason, after, sequence))
		} else {
			var fact leapmuxv1.SemanticTabFact
			if err := proto.Unmarshal(row.Fact, &fact); err != nil || semanticevents.FactTabType(&fact) != r.tabType {
				return nil, fmt.Errorf("%w: invalid stored Worker event", ErrUnavailable)
			}
			frames = append(frames, &leapmuxv1.WatchWorkerSemanticEventsResponse{Event: &leapmuxv1.WatchWorkerSemanticEventsResponse_Record{
				Record: &leapmuxv1.SemanticWorkerWatchRecord{
					Sequence: sequence, ObservedUnixMillis: row.ObservedUnixMillis,
					Tab: &leapmuxv1.TabIdent{TabType: r.tabType, TabId: row.TabID}, Fact: &fact, Cursor: signer.Sign(sequence),
				},
			}})
		}
		after = sequence
	}
	if len(rows) < readBatchSize && after < high {
		frames = append(frames, feedGapFrame(signer, missing, after, high))
	}
	return frames, nil
}

func feedGapFrame(signer seqcursor.Signer, reason leapmuxv1.SemanticWatchGapReason, after, through int64) *leapmuxv1.WatchWorkerSemanticEventsResponse {
	return &leapmuxv1.WatchWorkerSemanticEventsResponse{Event: &leapmuxv1.WatchWorkerSemanticEventsResponse_Gap{
		Gap: &leapmuxv1.SemanticWatchGap{Reason: reason, AfterSequence: after, ResumeAfterSequence: through, Cursor: signer.Sign(through)},
	}}
}

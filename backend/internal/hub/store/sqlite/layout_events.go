package sqlite

import (
	"context"
	"math"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type layoutEventStore struct {
	conn *sqliteConn
}

var _ store.LayoutEventStore = (*layoutEventStore)(nil)

func (s *sqliteStore) LayoutEvents() store.LayoutEventStore {
	return &layoutEventStore{conn: s.conn}
}

func (s *layoutEventStore) EnsureHead(ctx context.Context, owner userid.UserID, journalID string, key []byte) error {
	userID, ok := userid.OwnerFilter(owner)
	if !ok {
		return store.ErrNotFound
	}
	return s.conn.queryError(s.conn.q.EnsureLayoutEventHead(ctx, gendb.EnsureLayoutEventHeadParams{
		UserID: userID, JournalID: journalID, CursorKey: key,
	}))
}

func (s *layoutEventStore) ReservePositions(ctx context.Context, owner userid.UserID, count int64) (store.LayoutEventHead, error) {
	userID, ok := userid.OwnerFilter(owner)
	if !ok {
		return store.LayoutEventHead{}, store.ErrNotFound
	}
	if count < 1 {
		return store.LayoutEventHead{}, store.ErrLayoutEventSequenceExhausted
	}
	rows, err := s.conn.q.ReserveLayoutEventPositions(ctx, gendb.ReserveLayoutEventPositionsParams{
		UserID: userID, EventCount: count, SequenceLimit: math.MaxInt64 - count,
	})
	if err != nil {
		return store.LayoutEventHead{}, s.conn.queryError(err)
	}
	if rows != 1 {
		return store.LayoutEventHead{}, store.ErrLayoutEventSequenceExhausted
	}
	head, err := s.conn.q.ReadLayoutEventHeadForWrite(ctx, userID)
	if err != nil {
		return store.LayoutEventHead{}, s.conn.queryError(err)
	}
	return store.LayoutEventHead{
		JournalID: head.JournalID, CursorKey: head.CursorKey,
		HighSequence: head.HighSequence, FloorSequence: head.FloorSequence,
	}, nil
}

func (s *layoutEventStore) Insert(ctx context.Context, owner userid.UserID, event store.LayoutEventRow) error {
	userID, ok := userid.OwnerFilter(owner)
	if !ok {
		return store.ErrNotFound
	}
	return s.conn.queryError(s.conn.q.InsertLayoutEvent(ctx, gendb.InsertLayoutEventParams{
		UserID: userID, Sequence: event.Sequence, ObservedUnixMillis: event.ObservedUnixMillis, Fact: event.Fact,
	}))
}

func (s *layoutEventStore) PruneThrough(ctx context.Context, owner userid.UserID, sequence int64) error {
	userID, ok := userid.OwnerFilter(owner)
	if !ok {
		return store.ErrNotFound
	}
	if err := s.conn.q.AdvanceLayoutEventFloor(ctx, gendb.AdvanceLayoutEventFloorParams{
		UserID: userID, FloorSequence: sequence,
	}); err != nil {
		return s.conn.queryError(err)
	}
	return s.conn.queryError(s.conn.q.DeleteLayoutEventsThrough(ctx, gendb.DeleteLayoutEventsThroughParams{
		UserID: userID, ThroughSequence: sequence,
	}))
}

func (s *layoutEventStore) ReadPage(ctx context.Context, owner userid.UserID, workspaceID string, after int64, limit int32) (store.LayoutEventPage, error) {
	userID, ok := userid.OwnerFilter(owner)
	if !ok {
		return store.LayoutEventPage{}, store.ErrNotFound
	}
	rows, err := s.conn.q.ReadLayoutEventPage(ctx, gendb.ReadLayoutEventPageParams{
		UserID: userID, WorkspaceID: workspaceID, AfterSequence: after, PageLimit: int64(limit),
	})
	if err != nil {
		return store.LayoutEventPage{}, s.conn.queryError(err)
	}
	if len(rows) == 0 {
		return store.LayoutEventPage{}, store.ErrNotFound
	}
	first := rows[0]
	page := store.LayoutEventPage{Head: store.LayoutEventHead{
		JournalID: first.JournalID, CursorKey: first.CursorKey,
		HighSequence: first.HighSequence, FloorSequence: first.FloorSequence,
	}, WorkspaceLive: first.WorkspaceLive, OwnerLifecyclePending: first.OwnerLifecyclePending}
	if first.FinalSequence.Valid {
		page.WorkspaceFinalSequence = &first.FinalSequence.Int64
	}
	for _, row := range rows {
		if row.Sequence.Valid {
			page.Rows = append(page.Rows, store.LayoutEventRow{
				Sequence: row.Sequence.Int64, ObservedUnixMillis: row.ObservedUnixMillis.Int64, Fact: row.Fact,
			})
		}
	}
	return page, nil
}

func (s *layoutEventStore) ClaimLifecycle(ctx context.Context, owner userid.UserID, outboxID int64) (bool, error) {
	userID, ok := userid.OwnerFilter(owner)
	if !ok {
		return false, store.ErrNotFound
	}
	rows, err := s.conn.q.ClaimLayoutLifecycle(ctx, gendb.ClaimLayoutLifecycleParams{UserID: userID, OutboxID: outboxID})
	return rows == 1, s.conn.queryError(err)
}

func (s *layoutEventStore) MarkWorkspaceClosed(ctx context.Context, owner userid.UserID, workspaceID string, sequence int64) error {
	userID, ok := userid.OwnerFilter(owner)
	if !ok {
		return store.ErrNotFound
	}
	err := s.conn.q.MarkLayoutWorkspaceClosed(ctx, gendb.MarkLayoutWorkspaceClosedParams{
		UserID: userID, WorkspaceID: workspaceID, FinalSequence: sequence,
	})
	return s.conn.queryError(err)
}

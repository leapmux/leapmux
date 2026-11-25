package postgres

import (
	"context"
	"errors"
	"math"

	"github.com/jackc/pgx/v5"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

const bulkLayoutEventChunkRows = 4096

type layoutEventStore struct {
	conn *pgConn
}

var _ store.LayoutEventStore = (*layoutEventStore)(nil)

func (s *pgStore) LayoutEvents() store.LayoutEventStore {
	return &layoutEventStore{conn: s.conn}
}

func (s *layoutEventStore) EnsureHead(ctx context.Context, owner userid.UserID, journalID string, key []byte) error {
	userID, ok := userid.OwnerFilter(owner)
	if !ok {
		return store.ErrNotFound
	}
	return ensureLayoutHeadInConn(ctx, s.conn, userID, journalID, key)
}

func ensureLayoutHeadInConn(ctx context.Context, conn *pgConn, userID, journalID string, key []byte) error {
	sealedKey, err := conn.ks.SealBytes(key, sqlutil.LayoutEventHeadCursorKeyAAD(userID))
	if err != nil {
		return err
	}
	return mapErr(conn.q.EnsureLayoutEventHead(ctx, gendb.EnsureLayoutEventHeadParams{
		UserID: userID, JournalID: journalID, CursorKey: sealedKey,
	}))
}

func (s *layoutEventStore) Append(ctx context.Context, params store.AppendLayoutEventsParams) (store.LayoutEventHead, error) {
	userID, ok := userid.OwnerFilter(params.Owner)
	if !ok {
		return store.LayoutEventHead{}, store.ErrNotFound
	}
	if len(params.Facts) == 0 {
		return store.LayoutEventHead{}, store.ErrInvalidArgument
	}
	count := int64(len(params.Facts))
	var head store.LayoutEventHead
	err := s.conn.withTransaction(ctx, func(tx *pgConn) error {
		reserve := func() (gendb.ReserveLayoutEventPositionsRow, error) {
			return tx.q.ReserveLayoutEventPositions(ctx, gendb.ReserveLayoutEventPositionsParams{
				UserID: userID, EventCount: count, SequenceLimit: math.MaxInt64 - count,
			})
		}
		row, err := reserve()
		if errors.Is(err, pgx.ErrNoRows) {
			exists, existsErr := tx.q.LayoutEventHeadExists(ctx, userID)
			if existsErr != nil {
				return mapErr(existsErr)
			}
			if !exists {
				if params.NewHead == nil {
					return store.ErrNotFound
				}
				journalID, key, headErr := params.NewHead()
				if headErr != nil {
					return headErr
				}
				if headErr := ensureLayoutHeadInConn(ctx, tx, userID, journalID, key); headErr != nil {
					return headErr
				}
				row, err = reserve()
				if errors.Is(err, pgx.ErrNoRows) {
					return store.ErrLayoutEventSequenceExhausted
				}
			} else {
				return store.ErrLayoutEventSequenceExhausted
			}
		}
		if err != nil {
			return mapErr(err)
		}
		after := row.HighSequence - count
		if err := sqlutil.ChunkRange(len(params.Facts), bulkLayoutEventChunkRows, func(start, end int) error {
			chunk := params.Facts[start:end]
			sequences := make([]int64, len(chunk))
			observed := make([]int64, len(chunk))
			facts := make([][]byte, len(chunk))
			for i, fact := range chunk {
				seq := after + int64(start+i) + 1
				sealed, sealErr := tx.ks.SealBytes(fact, sqlutil.LayoutEventEntryFactAAD(userID, seq))
				if sealErr != nil {
					return sealErr
				}
				sequences[i] = seq
				observed[i] = params.ObservedUnixMillis
				facts[i] = sealed.Bytes()
			}
			return mapErr(tx.q.BulkInsertLayoutEvents(ctx, gendb.BulkInsertLayoutEventsParams{
				UserID:             userID,
				Sequences:          sequences,
				ObservedUnixMillis: observed,
				Facts:              facts,
			}))
		}); err != nil {
			return err
		}
		if params.ClosedWorkspaceID != "" {
			if err := tx.q.MarkLayoutWorkspaceClosed(ctx, gendb.MarkLayoutWorkspaceClosedParams{
				UserID: userID, WorkspaceID: params.ClosedWorkspaceID, FinalSequence: row.HighSequence,
			}); err != nil {
				return mapErr(err)
			}
		}
		floor := row.FloorSequence
		if params.RetainCount > 0 {
			if through := row.HighSequence - params.RetainCount; through > floor {
				if err := pruneLayoutEventsInTx(ctx, tx, params.Owner, through); err != nil {
					return err
				}
				floor = through
			}
		}
		head = store.LayoutEventHead{HighSequence: row.HighSequence, FloorSequence: floor}
		return nil
	})
	return head, err
}

func (s *layoutEventStore) PruneThrough(ctx context.Context, owner userid.UserID, sequence int64) error {
	if owner.IsZero() {
		return store.ErrNotFound
	}
	return s.conn.withTransaction(ctx, func(tx *pgConn) error {
		return pruneLayoutEventsInTx(ctx, tx, owner, sequence)
	})
}

func pruneLayoutEventsInTx(ctx context.Context, tx *pgConn, owner userid.UserID, sequence int64) error {
	userID, ok := userid.OwnerFilter(owner)
	if !ok {
		return store.ErrNotFound
	}
	if err := tx.q.AdvanceLayoutEventFloor(ctx, gendb.AdvanceLayoutEventFloorParams{
		UserID: userID, FloorSequence: sequence,
	}); err != nil {
		return mapErr(err)
	}
	return mapErr(tx.q.DeleteLayoutEventsThrough(ctx, gendb.DeleteLayoutEventsThroughParams{
		UserID: userID, ThroughSequence: sequence,
	}))
}

func (s *layoutEventStore) ReadHead(ctx context.Context, owner userid.UserID, workspaceID string) (store.LayoutEventHeadState, error) {
	return readLayoutEventHeadInConn(ctx, s.conn, owner, workspaceID)
}

func readLayoutEventHeadInConn(ctx context.Context, conn *pgConn, owner userid.UserID, workspaceID string) (store.LayoutEventHeadState, error) {
	userID, ok := userid.OwnerFilter(owner)
	if !ok {
		return store.LayoutEventHeadState{}, store.ErrNotFound
	}
	headRow, err := conn.q.ReadLayoutEventPageHead(ctx, gendb.ReadLayoutEventPageHeadParams{
		UserID: userID, WorkspaceID: workspaceID,
	})
	if err != nil {
		return store.LayoutEventHeadState{}, mapErr(err)
	}
	cursorKey, err := conn.ks.OpenBytes(headRow.CursorKey, sqlutil.LayoutEventHeadCursorKeyAAD(userID))
	if err != nil {
		return store.LayoutEventHeadState{}, err
	}
	state := store.LayoutEventHeadState{
		Head: store.LayoutEventHead{
			JournalID:     headRow.JournalID,
			CursorKey:     cursorKey,
			HighSequence:  headRow.HighSequence,
			FloorSequence: headRow.FloorSequence,
		},
		WorkspaceLive:         headRow.WorkspaceLive,
		OwnerLifecyclePending: headRow.OwnerLifecyclePending,
	}
	if headRow.FinalSequence.Valid {
		final := headRow.FinalSequence.Int64
		state.WorkspaceFinalSequence = &final
	}
	return state, nil
}

func (s *layoutEventStore) ReadPage(ctx context.Context, owner userid.UserID, workspaceID string, after int64, limit int32) (store.LayoutEventPage, error) {
	userID, ok := userid.OwnerFilter(owner)
	if !ok {
		return store.LayoutEventPage{}, store.ErrNotFound
	}
	if limit <= 0 {
		return store.LayoutEventPage{}, store.ErrInvalidArgument
	}
	var page store.LayoutEventPage
	err := s.conn.withTransaction(ctx, func(tx *pgConn) error {
		state, err := readLayoutEventHeadInConn(ctx, tx, owner, workspaceID)
		if err != nil {
			return err
		}
		page = store.LayoutEventPage{LayoutEventHeadState: state}
		maxSeq := state.Head.HighSequence
		if state.WorkspaceFinalSequence != nil {
			maxSeq = min(maxSeq, *state.WorkspaceFinalSequence)
		}
		if after < state.Head.FloorSequence || after >= maxSeq {
			return nil
		}
		entries, err := tx.q.ListLayoutEventEntries(ctx, gendb.ListLayoutEventEntriesParams{
			UserID:        userID,
			AfterSequence: after,
			MaxSequence:   maxSeq,
			PageLimit:     limit,
		})
		if err != nil {
			return mapErr(err)
		}
		page.Rows = make([]store.LayoutEventRow, 0, len(entries))
		for _, entry := range entries {
			fact, err := tx.ks.OpenBytes(entry.Fact, sqlutil.LayoutEventEntryFactAAD(userID, entry.Sequence))
			if err != nil {
				return err
			}
			page.Rows = append(page.Rows, store.LayoutEventRow{
				Sequence: entry.Sequence, ObservedUnixMillis: entry.ObservedUnixMillis, Fact: fact,
			})
		}
		return nil
	})
	return page, err
}

func (s *layoutEventStore) ClaimLifecycle(ctx context.Context, owner userid.UserID, outboxID int64) (bool, error) {
	userID, ok := userid.OwnerFilter(owner)
	if !ok {
		return false, store.ErrNotFound
	}
	rows, err := s.conn.q.ClaimLayoutLifecycle(ctx, gendb.ClaimLayoutLifecycleParams{UserID: userID, OutboxID: outboxID})
	return rows == 1, mapErr(err)
}

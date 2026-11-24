package store

import (
	"context"
	"errors"

	"github.com/leapmux/leapmux/internal/util/userid"
)

var ErrLayoutEventSequenceExhausted = errors.New("the layout event sequence is exhausted")

// LayoutEventHead identifies the account's history and its retained range.
type LayoutEventHead struct {
	JournalID     string
	CursorKey     []byte
	HighSequence  int64
	FloorSequence int64
}

type LayoutEventRow struct {
	Sequence           int64
	ObservedUnixMillis int64
	Fact               []byte
}

type LayoutEventPage struct {
	Head                   LayoutEventHead
	Rows                   []LayoutEventRow
	WorkspaceLive          bool
	OwnerLifecyclePending  bool
	WorkspaceFinalSequence *int64
}

// LayoutEventStore stores the facts derived from committed workspace and
// tab changes. Committers reserve, insert and prune in their transaction.
type LayoutEventStore interface {
	EnsureHead(ctx context.Context, owner userid.UserID, journalID string, key []byte) error
	ReservePositions(ctx context.Context, owner userid.UserID, count int64) (LayoutEventHead, error)
	Insert(ctx context.Context, owner userid.UserID, event LayoutEventRow) error
	PruneThrough(ctx context.Context, owner userid.UserID, sequence int64) error
	// ClaimLifecycle records publication of an outbox operation in the
	// transaction that appends its fact.
	ClaimLifecycle(ctx context.Context, owner userid.UserID, id int64) (bool, error)
	MarkWorkspaceClosed(ctx context.Context, owner userid.UserID, workspaceID string, sequence int64) error
	// ReadPage reads the head and entries from one snapshot.
	ReadPage(ctx context.Context, owner userid.UserID, workspaceID string, after int64, limit int32) (LayoutEventPage, error)
}

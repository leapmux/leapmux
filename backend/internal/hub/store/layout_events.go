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

type LayoutEventHeadState struct {
	Head                   LayoutEventHead
	WorkspaceLive          bool
	OwnerLifecyclePending  bool
	WorkspaceFinalSequence *int64
}

type LayoutEventPage struct {
	LayoutEventHeadState
	Rows []LayoutEventRow
}

// AppendLayoutEventsParams describes one atomic append to an owner's layout
// event log.
type AppendLayoutEventsParams struct {
	Owner              userid.UserID
	ObservedUnixMillis int64
	Facts              [][]byte
	// ClosedWorkspaceID, when non-empty, marks that workspace's final sequence
	// at the new high sequence in the same transaction.
	ClosedWorkspaceID string
	// RetainCount, when positive, prunes entries older than HighSequence - RetainCount
	// in the same transaction.
	RetainCount int64
	// NewHead supplies the journal ID and 32-byte cursor key when the account has
	// no head row yet. Called only on the cold first-append path so hot appends
	// consume no entropy. When nil and no head exists, Append returns ErrNotFound.
	NewHead func() (journalID string, cursorKey []byte, err error)
}

// LayoutEventStore stores the facts derived from committed workspace and
// tab changes.
type LayoutEventStore interface {
	EnsureHead(ctx context.Context, owner userid.UserID, journalID string, key []byte) error
	// Append reserves positions, inserts all facts, optionally closes a workspace
	// scope, and optionally prunes retained history in one transaction.
	Append(ctx context.Context, params AppendLayoutEventsParams) (LayoutEventHead, error)
	PruneThrough(ctx context.Context, owner userid.UserID, sequence int64) error
	// ClaimLifecycle records publication of an outbox operation in the
	// transaction that appends its fact.
	ClaimLifecycle(ctx context.Context, owner userid.UserID, id int64) (bool, error)
	// ReadHead reads the head and scope metadata without loading event rows.
	ReadHead(ctx context.Context, owner userid.UserID, workspaceID string) (LayoutEventHeadState, error)
	// ReadPage reads the head and entries from one snapshot.
	ReadPage(ctx context.Context, owner userid.UserID, workspaceID string, after int64, limit int32) (LayoutEventPage, error)
}

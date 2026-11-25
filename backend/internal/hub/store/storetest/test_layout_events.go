package storetest

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testLayoutEvents(t *testing.T) {
	st := s.NewStore(t)
	owner := userid.MustNew(SeedUser(t, st, "layout-fact-owner").ID)
	other := userid.MustNew(SeedUser(t, st, "layout-fact-other").ID)
	workspace := SeedWorkspace(t, st, owner.String(), "Layout replay")
	ctx := t.Context()

	// Appending before a head exists without NewHead must report ErrNotFound.
	_, err := st.LayoutEvents().Append(ctx, store.AppendLayoutEventsParams{
		Owner:              owner,
		ObservedUnixMillis: 100,
		Facts:              [][]byte{[]byte("missing-head")},
	})
	assert.ErrorIs(t, err, store.ErrNotFound)

	// Appending with empty facts must report ErrInvalidArgument.
	_, err = st.LayoutEvents().Append(ctx, store.AppendLayoutEventsParams{
		Owner:              owner,
		ObservedUnixMillis: 100,
	})
	assert.ErrorIs(t, err, store.ErrInvalidArgument)

	// ReadHead before a head exists reports ErrNotFound.
	_, err = st.LayoutEvents().ReadHead(ctx, owner, "")
	assert.ErrorIs(t, err, store.ErrNotFound)

	// Zero UserID fails closed with ErrNotFound across Append, ReadHead, and PruneThrough.
	var zeroOwner userid.UserID
	_, err = st.LayoutEvents().Append(ctx, store.AppendLayoutEventsParams{
		Owner: zeroOwner, ObservedUnixMillis: 100, Facts: [][]byte{[]byte("zero-owner")},
	})
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.LayoutEvents().ReadHead(ctx, zeroOwner, "")
	assert.ErrorIs(t, err, store.ErrNotFound)
	assert.ErrorIs(t, st.LayoutEvents().PruneThrough(ctx, zeroOwner, 1), store.ErrNotFound)

	// Failing NewHead propagates its error and commits neither a head nor rows.
	_, err = st.LayoutEvents().Append(ctx, store.AppendLayoutEventsParams{
		Owner:              owner,
		ObservedUnixMillis: 100,
		Facts:              [][]byte{[]byte("failed-head")},
		NewHead:            func() (string, []byte, error) { return "", nil, assert.AnError },
	})
	assert.ErrorIs(t, err, assert.AnError)
	_, err = st.LayoutEvents().ReadHead(ctx, owner, "")
	assert.ErrorIs(t, err, store.ErrNotFound)

	key := bytes.Repeat([]byte{42}, 32)
	require.NoError(t, st.LayoutEvents().EnsureHead(ctx, owner, "layout-journal", key))

	// Invalid non-positive page limits must fail with ErrInvalidArgument rather
	// than masquerading as a missing head (ErrNotFound).
	_, err = st.LayoutEvents().ReadPage(ctx, owner, "", 0, 0)
	assert.ErrorIs(t, err, store.ErrInvalidArgument)
	_, err = st.LayoutEvents().ReadPage(ctx, owner, "", 0, -5)
	assert.ErrorIs(t, err, store.ErrInvalidArgument)

	headState, err := st.LayoutEvents().ReadHead(ctx, owner, workspace)
	require.NoError(t, err)
	assert.Equal(t, "layout-journal", headState.Head.JournalID)
	assert.Equal(t, key, headState.Head.CursorKey)
	assert.True(t, headState.WorkspaceLive)
	assert.False(t, headState.OwnerLifecyclePending)
	assert.Nil(t, headState.WorkspaceFinalSequence)

	empty, err := st.LayoutEvents().ReadPage(ctx, owner, "", 0, 10)
	require.NoError(t, err)
	assert.Empty(t, empty.Rows)
	assert.Equal(t, "layout-journal", empty.Head.JournalID)
	assert.Equal(t, key, empty.Head.CursorKey)
	assert.False(t, empty.OwnerLifecyclePending)

	write := func(label string, outcome error) error {
		return st.RunInTransaction(ctx, func(tx store.Store) error {
			if _, err := tx.LayoutEvents().Append(ctx, store.AppendLayoutEventsParams{
				Owner:              owner,
				ObservedUnixMillis: 100,
				Facts:              [][]byte{[]byte(label)},
			}); err != nil {
				return err
			}
			return outcome
		})
	}
	require.ErrorIs(t, write("rolled back", assert.AnError), assert.AnError)
	const writers = 6
	results := make(chan error, writers)
	var group sync.WaitGroup
	for i := range writers {
		label := fmt.Sprintf("committed %d\x00\xff", i)
		group.Go(func() { results <- write(label, nil) })
	}
	group.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	page, err := st.LayoutEvents().ReadPage(ctx, owner, "", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Rows, writers)
	assert.Equal(t, int64(writers), page.Head.HighSequence)
	for i, row := range page.Rows {
		assert.Equal(t, int64(i+1), row.Sequence)
		assert.True(t, bytes.HasSuffix(row.Fact, []byte{0, 255}))
	}

	// Append with ClosedWorkspaceID and RetainCount closes the workspace scope
	// and prunes retained history atomically in one transaction.
	require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
		if _, err := tx.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{ID: workspace, OwnerUserID: owner}); err != nil {
			return err
		}
		head, err := tx.LayoutEvents().Append(ctx, store.AppendLayoutEventsParams{
			Owner:              owner,
			ObservedUnixMillis: 200,
			Facts:              [][]byte{[]byte("batch-1"), []byte("batch-2"), []byte("batch-3")},
			ClosedWorkspaceID:  workspace,
			RetainCount:        5,
		})
		if err != nil {
			return err
		}
		assert.Equal(t, int64(writers+3), head.HighSequence)
		assert.Equal(t, int64(writers+3-5), head.FloorSequence)
		return nil
	}))
	finalSeq := int64(writers + 3)
	floorSeq := finalSeq - 5

	gapPage, err := st.LayoutEvents().ReadPage(ctx, owner, "", 0, 1)
	require.NoError(t, err)
	assert.Empty(t, gapPage.Rows, "reads behind the floor skip loading pruned entries")
	assert.Equal(t, floorSeq, gapPage.Head.FloorSequence)
	assert.Equal(t, finalSeq, gapPage.Head.HighSequence)

	page, err = st.LayoutEvents().ReadPage(ctx, owner, "", floorSeq, 10)
	require.NoError(t, err)
	require.Len(t, page.Rows, 5)
	assert.Equal(t, floorSeq+1, page.Rows[0].Sequence)
	assert.Equal(t, []byte("batch-1"), page.Rows[2].Fact)
	assert.Equal(t, []byte("batch-3"), page.Rows[4].Fact)

	closed, err := st.LayoutEvents().ReadHead(ctx, owner, workspace)
	require.NoError(t, err)
	assert.False(t, closed.WorkspaceLive)
	require.NotNil(t, closed.WorkspaceFinalSequence)
	assert.Equal(t, finalSeq, *closed.WorkspaceFinalSequence)

	// Append with NewHead initializes a missing head on the first append and does
	// not call NewHead on subsequent appends.
	coldOwner := userid.MustNew(SeedUser(t, st, "layout-fact-cold").ID)
	headCalls := 0
	newHead := func() (string, []byte, error) {
		headCalls++
		return "cold-journal", key, nil
	}
	_, err = st.LayoutEvents().Append(ctx, store.AppendLayoutEventsParams{
		Owner:              coldOwner,
		ObservedUnixMillis: 300,
		Facts:              [][]byte{[]byte("first-cold")},
		NewHead:            newHead,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, headCalls)
	_, err = st.LayoutEvents().Append(ctx, store.AppendLayoutEventsParams{
		Owner:              coldOwner,
		ObservedUnixMillis: 301,
		Facts:              [][]byte{[]byte("second-warm")},
		NewHead:            newHead,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, headCalls, "warm appends must not call NewHead")

	// ClaimLifecycle and OwnerLifecyclePending across all dialects.
	require.NoError(t, st.LifecycleOutbox().Insert(ctx, store.InsertLifecycleOutboxParams{
		UserID:  owner,
		OpType:  leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_RENAME,
		Payload: []byte(`{"workspace_id":"w","title":"Renamed"}`),
	}))
	pending, err := st.LifecycleOutbox().ListPending(ctx, store.ListPendingLifecycleOutboxParams{UserID: owner, Limit: 10})
	require.NoError(t, err)
	require.Len(t, pending, 1)

	pendingPage, err := st.LayoutEvents().ReadPage(ctx, owner, "", finalSeq, 10)
	require.NoError(t, err)
	assert.True(t, pendingPage.OwnerLifecyclePending)

	// Wrong owner cannot claim the outbox row.
	claimed, err := st.LayoutEvents().ClaimLifecycle(ctx, other, pending[0].ID)
	require.NoError(t, err)
	assert.False(t, claimed)

	// First claim by the owner succeeds; second claim is idempotently false.
	claimed, err = st.LayoutEvents().ClaimLifecycle(ctx, owner, pending[0].ID)
	require.NoError(t, err)
	assert.True(t, claimed)
	claimed, err = st.LayoutEvents().ClaimLifecycle(ctx, owner, pending[0].ID)
	require.NoError(t, err)
	assert.False(t, claimed)

	require.NoError(t, st.LifecycleOutbox().MarkConsumed(ctx, store.MarkLifecycleOutboxConsumedParams{
		ID:         pending[0].ID,
		ConsumedAt: time.Now().UTC(),
	}))
	drainedPage, err := st.LayoutEvents().ReadPage(ctx, owner, "", finalSeq, 10)
	require.NoError(t, err)
	assert.False(t, drainedPage.OwnerLifecyclePending)
}

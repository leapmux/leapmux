package storetest

import (
	"bytes"
	"fmt"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testLayoutEvents(t *testing.T) {
	st := s.NewStore(t)
	owner := userid.MustNew(SeedUser(t, st, "layout-fact-owner").ID)
	workspace := SeedWorkspace(t, st, owner.String(), "Layout replay")
	ctx := t.Context()
	key := bytes.Repeat([]byte{42}, 32)
	require.NoError(t, st.LayoutEvents().EnsureHead(ctx, owner, "layout-journal", key))
	empty, err := st.LayoutEvents().ReadPage(ctx, owner, "", 0, 10)
	require.NoError(t, err)
	assert.Empty(t, empty.Rows)
	assert.Equal(t, "layout-journal", empty.Head.JournalID)
	assert.Equal(t, key, empty.Head.CursorKey)
	live, err := st.LayoutEvents().ReadPage(ctx, owner, workspace, 0, 10)
	require.NoError(t, err)
	assert.True(t, live.WorkspaceLive)
	assert.Nil(t, live.WorkspaceFinalSequence)

	write := func(label string, outcome error) error {
		return st.RunInTransaction(ctx, func(tx store.Store) error {
			log := tx.LayoutEvents()
			head, err := log.ReservePositions(ctx, owner, 1)
			if err != nil {
				return err
			}
			if err := log.Insert(ctx, owner, store.LayoutEventRow{
				Sequence: head.HighSequence, ObservedUnixMillis: 100, Fact: []byte(label),
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
	require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
		if _, err := tx.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{ID: workspace, OwnerUserID: owner}); err != nil {
			return err
		}
		if err := tx.LayoutEvents().MarkWorkspaceClosed(ctx, owner, workspace, 3); err != nil {
			return err
		}
		return tx.LayoutEvents().PruneThrough(ctx, owner, 4)
	}))
	page, err = st.LayoutEvents().ReadPage(ctx, owner, "", 0, 1)
	require.NoError(t, err)
	require.Len(t, page.Rows, 1)
	assert.Equal(t, int64(4), page.Head.FloorSequence)
	assert.Equal(t, int64(5), page.Rows[0].Sequence)
	assert.Equal(t, int64(writers), page.Head.HighSequence)
	closed, err := st.LayoutEvents().ReadPage(ctx, owner, workspace, 0, 10)
	require.NoError(t, err)
	assert.False(t, closed.WorkspaceLive)
	assert.Empty(t, closed.Rows, "later account records lie outside this scope")
	require.NotNil(t, closed.WorkspaceFinalSequence)
	assert.Equal(t, int64(3), *closed.WorkspaceFinalSequence)
}

package todoevents

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareSnapshotNormalizesAndCopies(t *testing.T) {
	t.Parallel()
	items := []Item{{Content: "0"}, {Content: "Active", Status: StatusInProgress}, {Content: "Done", Status: StatusCompleted}, {Content: "Deleted", Status: StatusDeleted}, {Content: "Blocked", Status: StatusBlocked}}
	prepared, err := PrepareSnapshot(items)
	require.NoError(t, err)
	items[0].Content = "changed"
	assert.Equal(t, Item{Content: "0", Status: StatusPending}, prepared[0])
	assert.Equal(t, StatusUnspecified, items[0].Status, "preparation must not mutate the caller's status")
	assert.Equal(t, []Item{{Content: "0", Status: StatusPending}, {Content: "Active", Status: StatusInProgress}, {Content: "Done", Status: StatusCompleted}, {Content: "Deleted", Status: StatusDeleted}, {Content: "Blocked", Status: StatusBlocked}}, prepared)
}

func TestPrepareSnapshotValidatesBeforeCapping(t *testing.T) {
	t.Parallel()
	for _, count := range []int{0, MaxTodos, MaxTodos + 1, 10000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			items := make([]Item, count)
			for i := range items {
				items[i] = Item{Content: fmt.Sprint(i), Status: StatusPending}
			}
			prepared, err := PrepareSnapshot(items)
			require.NoError(t, err)
			assert.Len(t, prepared, min(count, MaxTodos))
			if count != 0 {
				items[count-1].Status = 999
				prepared, err = PrepareSnapshot(items)
				require.Error(t, err)
				assert.Nil(t, prepared)
			}
		})
	}
}

package service

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryUpsertRequiresANonemptyRowKey(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"", "plain-key"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			_, sink, _, listRows := setupBgTaskTestWithService(t)
			err := sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: key, Kind: bgtask.KindShell, Status: bgtask.StatusRunning, Title: "Actual task"})
			if key == "" {
				assert.ErrorContains(t, err, "no row key")
				assert.Empty(t, listRows())
				return
			}
			require.NoError(t, err)
			rows := listRows()
			require.Len(t, rows, 1)
			assert.Equal(t, key, rows[0].RowKey)
			assert.Equal(t, "Actual task", rows[0].Title)
			assert.Equal(t, bgtask.StatusRunning, bgtask.Status(rows[0].Status))
		})
	}
}

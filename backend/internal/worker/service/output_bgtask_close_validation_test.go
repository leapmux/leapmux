package service

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func TestBackgroundTaskCloseRequiresAFinishedStatus(t *testing.T) {
	t.Parallel()
	for _, status := range []bgtask.Status{bgtask.StatusUnspecified, bgtask.StatusPending, bgtask.StatusRunning, bgtask.StatusPaused, bgtask.Status(-1), bgtask.Status(1 << 20)} {
		t.Run(fmt.Sprintf("status=%d", status), func(t *testing.T) {
			t.Parallel()
			rootID := fmt.Sprintf("close-validation-%d", status)
			svc, services := setupRootSink(t, rootID)
			require.NoError(t, services.UpsertBackgroundTask(bgtask.Upsert{RowKey: "task", Kind: bgtask.KindShell, Title: "Build", Status: bgtask.StatusRunning}))
			require.Error(t, services.CloseBackgroundTask("task", status))
			_, actual, exists, err := services.LookupBackgroundTask("task")
			require.NoError(t, err)
			require.True(t, exists)
			assert.Equal(t, bgtask.StatusRunning, actual)
			cache := svc.Output.bgTaskCache(rootID)
			cache.Mu.Lock()
			rows := append([]bgtask.Item(nil), cache.Rows...)
			cache.Mu.Unlock()
			require.Len(t, rows, 1)
			assert.Zero(t, rows[0].EndedAt)
		})
	}
}

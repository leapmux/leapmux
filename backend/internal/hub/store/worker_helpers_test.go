package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAssembleWorkerInventorySnapshot(t *testing.T) {
	t.Parallel()

	type row struct {
		revision int64
		workerID string
	}
	extract := func(r row) (int64, string) {
		return r.revision, r.workerID
	}

	t.Run("empty rows returns zero snapshot", func(t *testing.T) {
		got := AssembleWorkerInventorySnapshot([]row(nil), extract)
		assert.Zero(t, got.Revision)
		assert.Nil(t, got.WorkerIDs)
	})

	t.Run("single anchor row with empty worker ID preserves revision and nil slice", func(t *testing.T) {
		got := AssembleWorkerInventorySnapshot([]row{{revision: 7, workerID: ""}}, extract)
		assert.Equal(t, int64(7), got.Revision)
		assert.Nil(t, got.WorkerIDs)
	})

	t.Run("multiple unsorted rows sorts non-empty worker IDs", func(t *testing.T) {
		got := AssembleWorkerInventorySnapshot([]row{
			{revision: 12, workerID: "w-c"},
			{revision: 12, workerID: "w-a"},
			{revision: 12, workerID: ""},
			{revision: 12, workerID: "w-b"},
		}, extract)
		assert.Equal(t, WorkerInventorySnapshot{
			Revision:  12,
			WorkerIDs: []string{"w-a", "w-b", "w-c"},
		}, got)
	})

	t.Run("leading empty worker ID still collects later non-empty rows", func(t *testing.T) {
		got := AssembleWorkerInventorySnapshot([]row{
			{revision: 4, workerID: ""},
			{revision: 4, workerID: "w-2"},
			{revision: 4, workerID: "w-1"},
		}, extract)
		assert.Equal(t, WorkerInventorySnapshot{
			Revision:  4,
			WorkerIDs: []string{"w-1", "w-2"},
		}, got)
	})
}

package service

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRegistryWorkingSetPreservesCountsAcrossChangesAndSnapshots(t *testing.T) {
	t.Parallel()
	var set registryWorkingSet[string]
	set.include("activity", false)
	assert.True(t, set.hasWorking())
	assert.Zero(t, set.count())
	set.include("task", true)
	set.include("task", true)
	assert.Equal(t, int32(1), set.count(), "repeated admission is idempotent")
	set.include("task", false)
	assert.Zero(t, set.count())
	set.include("activity", true)
	snapshot := set.clone()
	set.rename("activity", "renamed")
	assert.False(t, set.has("activity"))
	assert.True(t, set.has("renamed"))
	assert.Equal(t, int32(1), set.count())
	set.remove("renamed")
	set.remove("absent")
	assert.Zero(t, set.count())
	assert.True(t, snapshot.has("activity"), "later changes do not mutate transaction snapshots")
	assert.Equal(t, int32(1), snapshot.count())
	set = snapshot.clone()
	set.remove("task")
	set.remove("activity")
	assert.False(t, set.hasWorking())
	assert.Zero(t, set.count())
	assert.True(t, snapshot.hasWorking())
}

var registryWorkingBenchmarkCount int32

func BenchmarkRegistryWorkingSetCount(b *testing.B) {
	for _, size := range []int{64, 4096} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			var set registryWorkingSet[int]
			for index := range size {
				set.include(index, index%2 == 0)
			}
			b.Run("scan", func(b *testing.B) {
				for b.Loop() {
					var count int32
					for _, counted := range set.entries {
						if counted {
							count++
						}
					}
					registryWorkingBenchmarkCount = count
				}
			})
			b.Run("indexed", func(b *testing.B) {
				for b.Loop() {
					registryWorkingBenchmarkCount = set.count()
				}
			})
		})
	}
}

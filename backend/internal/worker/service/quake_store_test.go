package service

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQuakeTerminalStore_LifecycleAndConcurrency(t *testing.T) {
	t.Parallel()

	t.Run("lifecycle and defensive cloning", func(t *testing.T) {
		t.Parallel()
		store := NewQuakeTerminalStore()

		t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
		t2 := t1.Add(time.Second)

		registered, created := store.RegisterOrAdopt(QuakeTerminal{
			ID:         "quake-1",
			WorkingDir: "/repo/a",
			Shell:      "/bin/sh",
			Title:      "Initial",
			Cols:       120,
			Rows:       24,
			CreatedAt:  t1,
		})
		require.True(t, created)
		assert.Equal(t, "quake-1", registered.ID)
		assert.True(t, store.Has("quake-1"))

		// Mutating returned copies must not alter store state.
		registered.Title = "Caller Mutated"
		got, ok := store.Get("quake-1")
		require.True(t, ok)
		assert.Equal(t, "Initial", got.Title)

		got.Title = "Mutated Again"
		byDir, ok := store.GetByWorkingDir("/repo/a")
		require.True(t, ok)
		assert.Equal(t, "Initial", byDir.Title)

		// Adopting an existing directory returns the winner's clone.
		adopted, created := store.RegisterOrAdopt(QuakeTerminal{
			ID:         "quake-loser",
			WorkingDir: "/repo/a",
			Title:      "Loser",
		})
		require.False(t, created)
		assert.Equal(t, "quake-1", adopted.ID)
		assert.False(t, store.Has("quake-loser"))

		// UpdateTitle and SetStartupError mutate stored fields; unknown IDs return false.
		assert.True(t, store.UpdateTitle("quake-1", "Renamed"))
		assert.False(t, store.UpdateTitle("missing", "Ignored"))
		assert.True(t, store.SetStartupError("quake-1", "spawn failed"))
		assert.False(t, store.SetStartupError("missing", "ignored"))

		got, ok = store.Get("quake-1")
		require.True(t, ok)
		assert.Equal(t, "Renamed", got.Title)
		assert.Equal(t, "spawn failed", got.StartupError)

		// Second directory registers independently and ListOpen preserves creation order.
		_, created = store.RegisterOrAdopt(QuakeTerminal{
			ID:         "quake-2",
			WorkingDir: "/repo/b",
			Title:      "Second",
			CreatedAt:  t2,
		})
		require.True(t, created)

		open := store.ListOpen()
		require.Len(t, open, 2)
		assert.Equal(t, []string{"quake-1", "quake-2"}, []string{open[0].ID, open[1].ID})

		// ListByWorkingDirs deduplicates requested directories and skips misses.
		byDirs := store.ListByWorkingDirs([]string{"/repo/b", "/repo/missing", "/repo/b", "/repo/a"})
		require.Len(t, byDirs, 2)
		assert.Equal(t, []string{"quake-2", "quake-1"}, []string{byDirs[0].ID, byDirs[1].ID})
		assert.Nil(t, store.ListByWorkingDirs(nil))

		// Delete removes both ID and directory indexes and allows a fresh registration.
		store.Delete("quake-1")
		store.Delete("quake-1") // idempotent
		assert.False(t, store.Has("quake-1"))
		_, ok = store.Get("quake-1")
		assert.False(t, ok)
		_, ok = store.GetByWorkingDir("/repo/a")
		assert.False(t, ok)

		replacement, created := store.RegisterOrAdopt(QuakeTerminal{
			ID:         "quake-3",
			WorkingDir: "/repo/a",
			Title:      "Replacement",
		})
		require.True(t, created)
		assert.Equal(t, "quake-3", replacement.ID)
	})

	t.Run("concurrent RegisterOrAdopt elects exactly one winner", func(t *testing.T) {
		t.Parallel()
		store := NewQuakeTerminalStore()
		const workers = 16
		type outcome struct {
			rec     QuakeTerminal
			created bool
		}
		results := make([]outcome, workers)

		var start, done sync.WaitGroup
		start.Add(1)
		for i := range workers {
			done.Add(1)
			go func(idx int) {
				defer done.Done()
				start.Wait()
				rec, created := store.RegisterOrAdopt(QuakeTerminal{
					ID:         fmt.Sprintf("quake-%d", idx),
					WorkingDir: "/repo/shared",
					Title:      fmt.Sprintf("Title %d", idx),
				})
				// Mutate local copy immediately to catch aliasing under -race.
				rec.Title = "mutated-by-caller"
				results[idx] = outcome{rec: rec, created: created}
			}(i)
		}
		start.Done()
		done.Wait()

		winners := 0
		var winnerID string
		for _, res := range results {
			if res.created {
				winners++
				winnerID = res.rec.ID
			}
		}
		require.Equal(t, 1, winners, "exactly one goroutine must win RegisterOrAdopt")
		require.NotEmpty(t, winnerID)
		for _, res := range results {
			assert.Equal(t, winnerID, res.rec.ID, "every loser must adopt the winner's record")
		}
		stored, ok := store.GetByWorkingDir("/repo/shared")
		require.True(t, ok)
		assert.Equal(t, winnerID, stored.ID)
		assert.NotEqual(t, "mutated-by-caller", stored.Title)
		assert.Len(t, store.ListOpen(), 1)
	})
}

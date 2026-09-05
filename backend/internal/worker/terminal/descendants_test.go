package terminal

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tree builds a snapshot from a pid -> ppid map, so each case below states the
// shape it means and nothing else.
func tree(pairs ...[2]int32) []procSnapshot {
	out := make([]procSnapshot, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, procSnapshot{pid: p[0], ppid: p[1]})
	}
	return out
}

func TestDescendantPIDs_ReachesEveryDepth(t *testing.T) {
	t.Parallel()

	// shell 100 -> make 200 -> cc 300 -> cc1 400. The whole point of the walk:
	// a depth-1 answer would miss the compiler the user actually cares about.
	snap := tree([2]int32{100, 1}, [2]int32{200, 100}, [2]int32{300, 200}, [2]int32{400, 300})

	got, total := descendantPIDs(snap, 100, 0)

	assert.Equal(t, []int32{200, 300, 400}, got)
	assert.Equal(t, 3, total)
}

func TestDescendantPIDs_ExcludesTheShellItself(t *testing.T) {
	t.Parallel()

	snap := tree([2]int32{100, 1}, [2]int32{200, 100})

	got, total := descendantPIDs(snap, 100, 0)

	assert.NotContains(t, got, int32(100),
		"the login shell is the Worker's own; listing it would warn about the tab's own existence")
	assert.Equal(t, []int32{200}, got)
	assert.Equal(t, 1, total)
}

func TestDescendantPIDs_IdleShellReportsNothing(t *testing.T) {
	t.Parallel()

	// A shell sitting at its prompt, plus an unrelated process tree.
	snap := tree([2]int32{100, 1}, [2]int32{900, 1}, [2]int32{901, 900})

	got, total := descendantPIDs(snap, 100, 0)

	assert.Empty(t, got)
	assert.Zero(t, total)
}

func TestDescendantPIDs_BreadthFirstSoDirectChildrenSurviveTheCap(t *testing.T) {
	t.Parallel()

	// Two direct children, each with a deep chain below it. Breadth-first is what
	// makes the cap useful: `npm` and `make` are the names a user recognises, so
	// they must outrank the leaf compiler invocations under them.
	snap := tree(
		[2]int32{100, 1},
		[2]int32{200, 100}, [2]int32{201, 200}, [2]int32{202, 201},
		[2]int32{300, 100}, [2]int32{301, 300}, [2]int32{302, 301},
	)

	got, total := descendantPIDs(snap, 100, 2)

	assert.Equal(t, []int32{200, 300}, got, "both DIRECT children, not one child and its grandchild")
	assert.Equal(t, 6, total, "the total counts everything found, including what the cap dropped")
}

func TestDescendantPIDs_CapZeroKeepsEverything(t *testing.T) {
	t.Parallel()

	snap := tree([2]int32{100, 1}, [2]int32{200, 100}, [2]int32{201, 100}, [2]int32{202, 100})

	got, total := descendantPIDs(snap, 100, 0)

	assert.Len(t, got, 3)
	assert.Equal(t, 3, total)
}

func TestDescendantPIDs_CycleTerminates(t *testing.T) {
	t.Parallel()

	// The parent index is not an atomic snapshot of the machine, so a stale ppid
	// pointing at a recycled pid can close a loop. Without the visited set this
	// walk never returns, and the Worker hangs inside a close.
	snap := tree([2]int32{100, 1}, [2]int32{200, 100}, [2]int32{300, 200}, [2]int32{200, 300})

	got, total := descendantPIDs(snap, 100, 0)

	assert.ElementsMatch(t, []int32{200, 300}, got)
	assert.Equal(t, 2, total)
}

func TestDescendantPIDs_SelfParentTerminates(t *testing.T) {
	t.Parallel()

	snap := tree([2]int32{100, 1}, [2]int32{200, 200}, [2]int32{201, 100})

	got, total := descendantPIDs(snap, 100, 0)

	assert.Equal(t, []int32{201}, got, "a self-parented process is nobody's descendant")
	assert.Equal(t, 1, total)
}

func TestDescendantPIDs_RootAbsentFromTheTable(t *testing.T) {
	t.Parallel()

	// The shell exited and was reaped between the snapshot and the walk.
	snap := tree([2]int32{900, 1})

	got, total := descendantPIDs(snap, 100, 0)

	require.Empty(t, got)
	assert.Zero(t, total)
}

func TestDescendantPIDs_EmptyTable(t *testing.T) {
	t.Parallel()

	got, total := descendantPIDs(nil, 100, 0)

	assert.Empty(t, got)
	assert.Zero(t, total)
}

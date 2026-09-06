package terminal

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tree builds the walk's parent index from a list of pid -> ppid pairs, so each
// case below states the shape it means and nothing else. It goes through the
// same indexByParent the real scan uses, so a case cannot pin an index shape the
// production path never builds.
func tree(pairs ...[2]int32) map[int32][]int32 {
	out := make([]procSnapshot, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, procSnapshot{pid: p[0], ppid: p[1]})
	}
	return indexByParent(out)
}

// reportAll is the predicate for a case about the SHAPE of the walk. The
// process-group filter has its own cases at the end of this file.
func reportAll(int32) bool { return true }

// reportExcept builds a predicate that hides the given pids, standing in for
// the shell's own process group.
func reportExcept(hidden ...int32) func(int32) bool {
	set := make(map[int32]struct{}, len(hidden))
	for _, pid := range hidden {
		set[pid] = struct{}{}
	}
	return func(pid int32) bool {
		_, isShellOwn := set[pid]
		return !isShellOwn
	}
}

func TestDescendantPIDs_ReachesEveryDepth(t *testing.T) {
	t.Parallel()

	// shell 100 -> make 200 -> cc 300 -> cc1 400. The whole point of the walk:
	// a depth-1 answer would miss the compiler the user actually cares about.
	byParent := tree([2]int32{100, 1}, [2]int32{200, 100}, [2]int32{300, 200}, [2]int32{400, 300})

	got, total := descendantPIDs(byParent, 100, 0, reportAll)

	assert.Equal(t, []int32{200, 300, 400}, got)
	assert.Equal(t, 3, total)
}

func TestDescendantPIDs_ExcludesTheShellItself(t *testing.T) {
	t.Parallel()

	byParent := tree([2]int32{100, 1}, [2]int32{200, 100})

	got, total := descendantPIDs(byParent, 100, 0, reportAll)

	assert.NotContains(t, got, int32(100),
		"the login shell is the Worker's own; listing it would warn about the tab's own existence")
	assert.Equal(t, []int32{200}, got)
	assert.Equal(t, 1, total)
}

func TestDescendantPIDs_IdleShellReportsNothing(t *testing.T) {
	t.Parallel()

	// A shell sitting at its prompt, plus an unrelated process tree.
	byParent := tree([2]int32{100, 1}, [2]int32{900, 1}, [2]int32{901, 900})

	got, total := descendantPIDs(byParent, 100, 0, reportAll)

	assert.Empty(t, got)
	assert.Zero(t, total)
}

func TestDescendantPIDs_BreadthFirstSoDirectChildrenSurviveTheCap(t *testing.T) {
	t.Parallel()

	// Two direct children, each with a deep chain below it. Breadth-first is what
	// makes the cap useful: `npm` and `make` are the names a user recognises, so
	// they must outrank the leaf compiler invocations under them.
	byParent := tree(
		[2]int32{100, 1},
		[2]int32{200, 100}, [2]int32{201, 200}, [2]int32{202, 201},
		[2]int32{300, 100}, [2]int32{301, 300}, [2]int32{302, 301},
	)

	got, total := descendantPIDs(byParent, 100, 2, reportAll)

	assert.Equal(t, []int32{200, 300}, got, "both DIRECT children, not one child and its grandchild")
	assert.Equal(t, 6, total, "the total counts everything found, including what the cap dropped")
}

func TestDescendantPIDs_CapZeroKeepsEverything(t *testing.T) {
	t.Parallel()

	byParent := tree([2]int32{100, 1}, [2]int32{200, 100}, [2]int32{201, 100}, [2]int32{202, 100})

	got, total := descendantPIDs(byParent, 100, 0, reportAll)

	assert.Len(t, got, 3)
	assert.Equal(t, 3, total)
}

func TestDescendantPIDs_CycleTerminates(t *testing.T) {
	t.Parallel()

	// The parent index is not an atomic snapshot of the machine, so a stale ppid
	// pointing at a recycled pid can close a loop. Without the visited set this
	// walk never returns, and the Worker hangs inside a close.
	byParent := tree([2]int32{100, 1}, [2]int32{200, 100}, [2]int32{300, 200}, [2]int32{200, 300})

	got, total := descendantPIDs(byParent, 100, 0, reportAll)

	assert.ElementsMatch(t, []int32{200, 300}, got)
	assert.Equal(t, 2, total)
}

func TestDescendantPIDs_SelfParentTerminates(t *testing.T) {
	t.Parallel()

	byParent := tree([2]int32{100, 1}, [2]int32{200, 200}, [2]int32{201, 100})

	got, total := descendantPIDs(byParent, 100, 0, reportAll)

	assert.Equal(t, []int32{201}, got, "a self-parented process is nobody's descendant")
	assert.Equal(t, 1, total)
}

func TestDescendantPIDs_RootAbsentFromTheTable(t *testing.T) {
	t.Parallel()

	// The shell exited and was reaped between the snapshot and the walk.
	byParent := tree([2]int32{900, 1})

	got, total := descendantPIDs(byParent, 100, 0, reportAll)

	require.Empty(t, got)
	assert.Zero(t, total)
}

func TestDescendantPIDs_EmptyTable(t *testing.T) {
	t.Parallel()

	got, total := descendantPIDs(nil, 100, 0, reportAll)

	assert.Empty(t, got)
	assert.Zero(t, total)
}

// --- The shell's own work is not the user's --------------------------------
//
// A shell puts every job it runs in a process group of its own and keeps its
// own bookkeeping in the group it already has, so a descendant in the shell's
// group IS the shell. `mise activate zsh` installs a precmd hook that forks
// before EVERY prompt; without this filter the close guard warned about an idle
// terminal.

func TestDescendantPIDs_HidesTheShellsOwnGroup(t *testing.T) {
	t.Parallel()

	// 200 is the prompt hook the shell forked; 300 is the user's `npm run dev`.
	byParent := tree([2]int32{100, 1}, [2]int32{200, 100}, [2]int32{300, 100})

	got, total := descendantPIDs(byParent, 100, 0, reportExcept(200))

	assert.Equal(t, []int32{300}, got, "only the job the user started")
	assert.Equal(t, 1, total, "the hidden process is not counted either, or the dialog says \"and 1 more\"")
}

func TestDescendantPIDs_AllShellOwnReportsIdle(t *testing.T) {
	t.Parallel()

	// The whole reason the filter exists: a terminal at its prompt, whose only
	// descendant is the shell's own hook, must close with no prompt at all.
	byParent := tree([2]int32{100, 1}, [2]int32{200, 100})

	got, total := descendantPIDs(byParent, 100, 0, reportExcept(200))

	assert.Empty(t, got)
	assert.Zero(t, total)
}

func TestDescendantPIDs_WalksThroughAHiddenProcessToTheWorkBelowIt(t *testing.T) {
	t.Parallel()

	// The filter hides what it REPORTS, never what it walks. A prompt hook that
	// backgrounds something leaves that child in a group of its own, and losing
	// it would hide exactly the work this guard exists to name.
	byParent := tree([2]int32{100, 1}, [2]int32{200, 100}, [2]int32{300, 200})

	got, total := descendantPIDs(byParent, 100, 0, reportExcept(200))

	assert.Equal(t, []int32{300}, got)
	assert.Equal(t, 1, total)
}

func TestDescendantPIDs_HiddenProcessesDoNotSpendTheCap(t *testing.T) {
	t.Parallel()

	// The cap limits what the dialog RENDERS. A hidden process that consumed a
	// slot would push a real one out of a list the user reads to decide.
	byParent := tree(
		[2]int32{100, 1},
		[2]int32{200, 100}, [2]int32{201, 100},
		[2]int32{300, 100}, [2]int32{301, 100},
	)

	got, total := descendantPIDs(byParent, 100, 2, reportExcept(200, 201))

	assert.Equal(t, []int32{300, 301}, got)
	assert.Equal(t, 2, total)
}

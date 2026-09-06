package terminal

import (
	"bytes"
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startShellPastInit spawns a real PTY and waits until the shell echoes a
// marker, which is the only reliable proof it is past its init scripts and
// accepting input. Every case below sends a command, so it needs a shell that
// will run one.
//
// It is no longer what keeps the walk quiet: a profile script's own children
// share the shell's process group, and reportsAsWork excludes that group.
func startShellPastInit(t *testing.T, m *Manager, id string) {
	t.Helper()
	require.NoError(t, m.StartTerminal(context.Background(), Options{
		ID:         id,
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
		Cols:       200,
		Rows:       24,
	}, func([]byte, int64, []Signal) {}, nil))
	testutil.RegisterTerminalCleanup(t, m, id)

	const marker = "descendants_ready_marker"
	require.NoError(t, m.SendInput(id, []byte("echo "+marker+testutil.TestShellEnter())))
	testutil.RequireEventually(t, func() bool {
		screen, _, _ := m.ScreenSnapshotSince(id, 0)
		return bytes.Contains(screen, []byte(marker))
	}, "expected the shell to echo the marker")
}

// namesOf normalizes each reported name so one assertion reads the same
// everywhere: Windows reports the snapshot's image name (PING.EXE) and macOS
// reports argv[0] for a long name, which carries a path (/bin/sh).
func namesOf(procs []ProcessInfo) []string {
	out := make([]string, 0, len(procs))
	for _, p := range procs {
		name := strings.ToLower(filepath.Base(p.Name))
		out = append(out, strings.TrimSuffix(name, ".exe"))
	}
	return out
}

// oneTerminal asks about a single terminal and flattens the batch answer, so
// each test below reads as "what is this tab running". An omitted terminal --
// idle, unknown, or exited -- answers empty, which is what every caller of the
// batch treats those three as.
func oneTerminal(t *testing.T, m *Manager, id string) ([]ProcessInfo, int, error) {
	t.Helper()
	out, err := m.DescendantProcesses(context.Background(), []string{id})
	if err != nil || len(out) == 0 {
		return nil, 0, err
	}
	require.Len(t, out, 1)
	require.Equal(t, id, out[0].TerminalID)
	return out[0].Processes, out[0].Total, nil
}

func TestDescendantProcesses_FindsABackgroundedChild(t *testing.T) {
	m := NewManager()
	const id = "desc-child"
	startShellPastInit(t, m, id)

	line, want := testutil.TestSleepCommand()
	require.NoError(t, m.SendInput(id, []byte(line+testutil.TestShellEnter())))

	var got []ProcessInfo
	testutil.AssertEventually(t, func() bool {
		procs, _, err := oneTerminal(t, m, id)
		if err != nil {
			return false
		}
		got = procs
		return len(procs) > 0
	}, "expected the backgrounded child to appear beneath the shell")

	assert.Contains(t, namesOf(got), want)
	for _, p := range got {
		assert.NotZero(t, p.PID, "every reported process carries a pid, even when the name is unreadable")
	}
}

func TestDescendantProcesses_ReachesAGrandchild(t *testing.T) {
	m := NewManager()
	const id = "desc-grandchild"
	startShellPastInit(t, m, id)

	// The `make` -> `cc` shape. A depth-1 answer would name the wrapper and miss
	// the process actually doing the work, which is the one the user loses.
	line, grandchild := testutil.TestNestedSleepCommand()
	require.NoError(t, m.SendInput(id, []byte(line+testutil.TestShellEnter())))

	var got []string
	var total int
	testutil.AssertEventually(t, func() bool {
		procs, n, err := oneTerminal(t, m, id)
		if err != nil {
			return false
		}
		got, total = namesOf(procs), n
		return contains(got, grandchild)
	}, "expected the grandchild beneath the intermediate shell, got %v", &got)

	// Finding the DEEPEST process is the whole assertion: it sits at depth 2, so
	// a walk that stopped at the shell's own children could not have produced it.
	// The intermediate shell's name is deliberately not asserted -- the OS
	// decides it, and on macOS /bin/sh reports "bash".
	assert.Contains(t, got, grandchild)
	assert.GreaterOrEqual(t, total, 2, "the wrapper and the process it spawned")
}

func TestDescendantProcesses_IdleShellReportsNothingAndNeverItself(t *testing.T) {
	m := NewManager()
	const id = "desc-idle"
	startShellPastInit(t, m, id)

	procs, total, err := oneTerminal(t, m, id)

	require.NoError(t, err)
	assert.Empty(t, procs, "an idle shell is running nothing worth warning about")
	assert.Zero(t, total)

	// The shell is the Worker's own process; warning about it would mean warning
	// about the tab's own existence, and every terminal close would prompt.
	//
	// The assertion holds for a real interactive shell too, which is the point of
	// the process-group filter: `mise`, starship and powerlevel10k all fork
	// before a prompt, and every one of those forks stays in the shell's own
	// group.
	shellPID := terminalOf(t, m, id).ShellPID()
	for _, p := range procs {
		assert.NotEqual(t, int32(shellPID), p.PID)
	}
}

func TestDescendantProcesses_ExitedShellReportsNothing(t *testing.T) {
	m := NewManager()
	const id = "desc-exited"
	startShellPastInit(t, m, id)

	m.StopTerminal(id)
	m.WaitForExit(id)

	procs, total, err := oneTerminal(t, m, id)

	// The pid is reused once the OS reaps it, so a walk from a dead shell can
	// enumerate a stranger's children under this tab's name.
	require.NoError(t, err, "a closed terminal is not an error, it is an empty answer")
	assert.Empty(t, procs)
	assert.Zero(t, total)
}

func TestDescendantProcesses_UnknownTerminalIsEmptyNotAnError(t *testing.T) {
	t.Parallel()

	m := NewManager()

	procs, total, err := oneTerminal(t, m, "no-such-terminal")

	// Routine, not exceptional: the DB row exists for the whole async-startup
	// window before the PTY does, and again after a worker restart. A close
	// dialog must not fail because the PTY is 200ms from existing.
	require.NoError(t, err)
	assert.Empty(t, procs)
	assert.Zero(t, total)
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func terminalOf(t *testing.T, m *Manager, id string) *Terminal {
	t.Helper()
	m.mu.RLock()
	defer m.mu.RUnlock()
	term, ok := m.terminals[id]
	require.True(t, ok, "terminal %s should be installed", id)
	return term
}

// TestProcessGroupOf_ShellAndItsJobsAreInDifferentGroups pins the OS fact the
// whole filter rests on: a shell with job control puts each job it runs in a
// process group of its own and keeps its own work in the group it already has.
//
// The negative half -- a descendant IN the shell's group -- has no portable PTY
// form. Producing one needs shell code that forks outside job control (a zsh
// `precmd` hook, a bash `PROMPT_COMMAND`), and the two spell it differently
// while /bin/sh is bash on macOS and dash on Linux, where neither exists.
// descendants_test.go covers that half against the traversal directly.
func TestProcessGroupOf_ShellAndItsJobsAreInDifferentGroups(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no process groups; processGroupOf declines and every descendant is reported")
	}
	m := NewManager()
	const id = "desc-groups"
	startShellPastInit(t, m, id)

	shellPID := terminalOf(t, m, id).ShellPID()
	shellGroup, res := processGroupOf(shellPID)
	require.Equal(t, processGroupFound, res,
		"the shell's own group must be readable, or the filter degrades to reporting everything")

	line, _ := testutil.TestSleepCommand()
	require.NoError(t, m.SendInput(id, []byte(line+testutil.TestShellEnter())))

	var got []ProcessInfo
	testutil.AssertEventually(t, func() bool {
		procs, _, err := oneTerminal(t, m, id)
		if err != nil {
			return false
		}
		got = procs
		return len(procs) > 0
	}, "expected the backgrounded child to appear beneath the shell")

	for _, p := range got {
		group, res := processGroupOf(int(p.PID))
		require.Equal(t, processGroupFound, res)
		assert.NotEqual(t, shellGroup, group,
			"a job the user started gets its own group; sharing the shell's would hide it from the guard")
	}
}

// TestDescendantProcesses_BatchAnswersEveryTerminalFromOneScan pins the batch
// contract the close guard depends on. A tile close asks about every terminal
// it holds at once, and the answer must name the RIGHT tab: a busy one comes
// back, an idle one and an unknown one are omitted, and the entries keep the
// order asked so a refusal reads in the order the close would have run.
func TestDescendantProcesses_BatchAnswersEveryTerminalFromOneScan(t *testing.T) {
	m := NewManager()
	const busyID, idleID = "batch-busy", "batch-idle"
	startShellPastInit(t, m, busyID)
	startShellPastInit(t, m, idleID)

	line, want := testutil.TestSleepCommand()
	require.NoError(t, m.SendInput(busyID, []byte(line+testutil.TestShellEnter())))

	var out []TerminalProcesses
	testutil.AssertEventually(t, func() bool {
		got, err := m.DescendantProcesses(context.Background(), []string{"no-such-terminal", idleID, busyID})
		if err != nil {
			return false
		}
		out = got
		return len(got) > 0
	}, "expected the busy terminal to report its child")

	ids := make([]string, 0, len(out))
	byID := map[string][]ProcessInfo{}
	for _, entry := range out {
		ids = append(ids, entry.TerminalID)
		byID[entry.TerminalID] = entry.Processes
	}
	// The answer must name the tab each process belongs to: reporting one tab's
	// work under another's is what would make the close guard warn about the
	// wrong terminal.
	assert.Contains(t, ids, busyID)
	assert.Contains(t, namesOf(byID[busyID]), want)
	assert.NotContains(t, ids, "no-such-terminal", "an id the manager does not hold is omitted")
	// idleID is deliberately not asserted absent. Its shell has just run the
	// readiness echo, and that job can still be finishing -- a real descendant
	// with its own process group. TestDescendantProcesses_IdleShellReportsNothing
	// AndNeverItself covers the idle case without racing a command.
}

func TestDescendantProcesses_RepeatedIDsAreWalkedOnce(t *testing.T) {
	m := NewManager()
	const id = "batch-repeat"
	startShellPastInit(t, m, id)

	line, _ := testutil.TestSleepCommand()
	require.NoError(t, m.SendInput(id, []byte(line+testutil.TestShellEnter())))

	var out []TerminalProcesses
	testutil.AssertEventually(t, func() bool {
		got, err := m.DescendantProcesses(context.Background(), []string{id, id, id, id})
		if err != nil {
			return false
		}
		out = got
		return len(got) > 0
	}, "expected the terminal to report its child")

	// The answer for one terminal is the same however many times the request
	// names it, and each repeat costs a full walk of the process table. The
	// reply is keyed by terminal id, so a duplicate could only produce a
	// duplicate row for the close dialog to render twice.
	require.Len(t, out, 1, "a repeated id is walked once and reported once")
	assert.Equal(t, id, out[0].TerminalID)
}

func TestDescendantProcesses_StopsOnACancelledContext(t *testing.T) {
	m := NewManager()
	const id = "batch-cancelled"
	startShellPastInit(t, m, id)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// The walk itself takes no context, so the caller's deadline reaches it only
	// through the per-target check. Without that, the scan's own timeout bounds
	// the table read alone and a wide request keeps walking long after the
	// answer can still be delivered.
	_, err := m.DescendantProcesses(ctx, []string{id})

	require.ErrorIs(t, err, context.Canceled)
}

func TestDescendantProcesses_EmptyRequestAsksNothing(t *testing.T) {
	t.Parallel()

	m := NewManager()

	out, err := m.DescendantProcesses(context.Background(), nil)

	// A tile of file tabs holds no terminal, and scanning the machine's process
	// table to answer a question nobody asked is pure cost.
	require.NoError(t, err)
	assert.Empty(t, out)
}

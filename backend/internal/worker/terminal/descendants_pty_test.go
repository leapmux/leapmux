package terminal

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startShellPastInit spawns a real PTY and waits until the shell has echoed a
// marker, which is the only reliable proof it is past its init scripts. A walk
// run before that can catch a profile script's own children and read as a busy
// terminal on every platform at random.
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

func TestDescendantProcesses_FindsABackgroundedChild(t *testing.T) {
	m := NewManager()
	const id = "desc-child"
	startShellPastInit(t, m, id)

	line, want := testutil.TestSleepCommand()
	require.NoError(t, m.SendInput(id, []byte(line+testutil.TestShellEnter())))

	var got []ProcessInfo
	testutil.AssertEventually(t, func() bool {
		procs, _, err := m.DescendantProcesses(context.Background(), id)
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
		procs, n, err := m.DescendantProcesses(context.Background(), id)
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

	procs, total, err := m.DescendantProcesses(context.Background(), id)

	require.NoError(t, err)
	assert.Empty(t, procs, "an idle shell is running nothing worth warning about")
	assert.Zero(t, total)

	// The shell is the Worker's own process; warning about it would mean warning
	// about the tab's own existence, and every terminal close would prompt.
	//
	// Safe to assert emptiness only because testutil.TestShell() is /bin/sh. A
	// user's real interactive shell is not: starship and powerlevel10k fork a
	// process per prompt, so the same assertion against zsh would flake.
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

	procs, total, err := m.DescendantProcesses(context.Background(), id)

	// The pid is reused once the OS reaps it, so a walk from a dead shell can
	// enumerate a stranger's children under this tab's name.
	require.NoError(t, err, "a closed terminal is not an error, it is an empty answer")
	assert.Empty(t, procs)
	assert.Zero(t, total)
}

func TestDescendantProcesses_UnknownTerminalIsEmptyNotAnError(t *testing.T) {
	t.Parallel()

	m := NewManager()

	procs, total, err := m.DescendantProcesses(context.Background(), "no-such-terminal")

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

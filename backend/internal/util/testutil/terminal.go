package testutil

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TerminalFreezer is the subset of terminal.Manager methods needed to stop a
// test terminal's PTY and read back where its cumulative byte counter landed.
type TerminalFreezer interface {
	StopTerminal(id string)
	WaitForExit(id string)
	WaitForReadDrained(id string) bool
	ScreenSnapshotSince(id string, after int64) ([]byte, int64, bool)
}

// FreezeTerminalOutput ends the terminal's PTY, waits for the shell to be
// reaped and its reader to drain, and returns the cumulative screen offset that
// results. After it returns, nothing but the caller can advance that counter,
// which is what lets a test assert an EXACT offset.
//
// Every step is load-bearing. A test terminal runs a real interactive login
// shell, which writes its own prompt (2 bytes for dash's "$ ", more for bash or
// cmd.exe) at a moment no test controls, and every one of those bytes advances
// the same counter -- a test that assumed a quiet PTY was really asserting the
// prompt had not arrived yet, true on an idle box and routinely false in
// parallel CI. WaitForExit specifically fences the exit notice: the service's
// exit handler appends it through AppendOutput, on the exit goroutine, which is
// NOT what the read-drain signal tracks.
//
// The manager keeps the entry after exit, so live-terminal code paths still
// take their live branch; only the writer nobody can schedule is gone.
func FreezeTerminalOutput(t *testing.T, mgr TerminalFreezer, id string) int64 {
	t.Helper()
	mgr.StopTerminal(id)
	mgr.WaitForExit(id)
	require.True(t, mgr.WaitForReadDrained(id), "terminal %s is not in the manager", id)
	_, offset, _ := mgr.ScreenSnapshotSince(id, 0)
	return offset
}

// TerminalCleaner is the subset of terminal.Manager methods needed to
// tear down a test terminal at the end of a subtest. Defined here so
// testutil stays decoupled from the terminal package.
type TerminalCleaner interface {
	StopTerminal(id string)
	WaitForExit(id string)
	RemoveTerminal(id string)
}

// RegisterTerminalCleanup arranges for the given terminal to be stopped
// and removed at the end of the test, so the manager's in-memory entry
// (and the spawned PTY process) does not leak between subtests. The
// WaitForExit call blocks until the manager's exit-handler goroutine
// has finished, which subsumes any test-local "exit handler fired"
// channel a wrapped ExitHandler might close.
//
// Register this AFTER any t.TempDir used as the shell's WorkingDir so
// LIFO cleanup kills the process before RemoveAll. On Windows a live
// shell keeps its CWD open, and TempDir cleanup then fails with
// "The process cannot access the file because it is being used by
// another process."
func RegisterTerminalCleanup(t *testing.T, mgr TerminalCleaner, id string) {
	t.Helper()
	t.Cleanup(func() {
		mgr.StopTerminal(id)
		mgr.WaitForExit(id)
		mgr.RemoveTerminal(id)
	})
}

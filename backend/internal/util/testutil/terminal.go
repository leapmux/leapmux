package testutil

import "testing"

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
func RegisterTerminalCleanup(t *testing.T, mgr TerminalCleaner, id string) {
	t.Helper()
	t.Cleanup(func() {
		mgr.StopTerminal(id)
		mgr.WaitForExit(id)
		mgr.RemoveTerminal(id)
	})
}

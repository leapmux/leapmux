//go:build !windows

package terminal

import (
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reportsAsWork decides what the close guard is allowed to warn about, so both
// of its answers matter: hiding the shell's own work, and refusing to hide
// anything it cannot classify.

func TestReportsAsWork_HidesTheShellsOwnGroup(t *testing.T) {
	t.Parallel()

	// This test process stands in for the shell. A process in its own group is
	// the shell's own work by definition, and reporting it would be the `mise`
	// precmd fork that made an idle terminal look busy.
	self := os.Getpid()
	require.NotPanics(t, func() { _, _ = syscall.Getpgid(self) })

	report := reportsAsWork(self, "/bin/zsh")

	assert.False(t, report(int32(self)), "a process in the shell's own group is the shell's own work")
}

func TestReportsAsWork_ReportsEverythingUnderAShellWithoutJobControl(t *testing.T) {
	t.Parallel()

	// PowerShell starts a native command through .NET's process API, which never
	// calls setpgid, so every command the user runs inherits pwsh's own group.
	// Comparing groups would hide all of it: the user runs `npm run dev`, clicks
	// the tab X, gets no dialog, and the build dies silently -- the one outcome
	// the guard exists to prevent. shells.go offers pwsh on Unix, so this is
	// reachable and not hypothetical.
	self := os.Getpid()
	report := reportsAsWork(self, "/usr/local/bin/pwsh")

	assert.True(t, report(int32(self)),
		"a shell without job control gives the group filter nothing to compare, so report everything")

	// The mirror, so the two halves cannot both drift: under a shell that DOES
	// implement job control the filter still hides the shell's own group.
	assert.False(t, reportsAsWork(self, "/bin/zsh")(int32(self)))
}

func TestReportsAsWork_ReportsEverythingWhenTheShellNameIsUnknown(t *testing.T) {
	t.Parallel()

	// A name the scan could not read cannot be vouched for either, and the guard
	// must not be silenced by a question it cannot answer.
	assert.True(t, reportsAsWork(os.Getpid(), "")(int32(os.Getpid())))
}

func TestReportsAsWork_ReportsEverythingWhenTheShellsGroupIsUnreadable(t *testing.T) {
	t.Parallel()

	// A shell whose group cannot be read gives nothing to compare against. The
	// guard exists to WARN, so it reports every descendant rather than none --
	// the same stance the Windows build takes, where there are no process
	// groups at all.
	report := reportsAsWork(-1, "/bin/zsh")

	assert.True(t, report(int32(os.Getpid())))
	assert.True(t, report(reapedPID(t)))
}

func TestReportsAsWork_HidesAProcessThatAlreadyExited(t *testing.T) {
	t.Parallel()

	// The walk judges pids from a snapshot taken earlier, so a compiler that
	// finished in between is simply absent by the time the filter asks. That is
	// an ANSWER -- "no such process" -- not a question the OS refused, and
	// reporting it names a dead pid in the close dialog and refuses a CLI close
	// over work that already stopped.
	//
	// getpgid documents exactly one error, ESRCH, so on Unix a failed read
	// always means gone. The refused case the filter still reports for is
	// Windows, which has no process group to return at all.
	report := reportsAsWork(os.Getpid(), "/bin/zsh")

	assert.False(t, report(reapedPID(t)))
}

// reapedPID returns the pid of a process that ran to completion and was reaped,
// so the OS holds no entry for it. That is the real race the filter faces: the
// scan listed the process, and it exited before the filter asked about it.
func reapedPID(t *testing.T) int32 {
	t.Helper()
	cmd := exec.Command("sh", "-c", "exit 0")
	require.NoError(t, cmd.Run())
	pid := cmd.Process.Pid
	_, err := syscall.Getpgid(pid)
	require.ErrorIs(t, err, syscall.ESRCH, "a reaped pid must be gone, or this case proves nothing")
	return int32(pid)
}

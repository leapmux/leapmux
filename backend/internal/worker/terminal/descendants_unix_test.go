//go:build !windows

package terminal

import (
	"os"
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

	report := reportsAsWork(self)

	assert.False(t, report(int32(self)), "a process in the shell's own group is the shell's own work")
}

func TestReportsAsWork_ReportsEverythingWhenTheShellsGroupIsUnreadable(t *testing.T) {
	t.Parallel()

	// A shell that exited between the scan and this read has no group to compare
	// against. The guard exists to WARN, so an unanswerable question must not
	// silence it -- the same stance the Windows build takes, where there are no
	// process groups at all.
	report := reportsAsWork(-1)

	assert.True(t, report(int32(os.Getpid())))
	assert.True(t, report(-1))
}

func TestReportsAsWork_ReportsAProcessWhoseOwnGroupIsUnreadable(t *testing.T) {
	t.Parallel()

	// The shell's group is known, but this descendant's is not: it exited
	// between the scan and the read. Report it rather than drop it.
	report := reportsAsWork(os.Getpid())

	assert.True(t, report(-1))
}

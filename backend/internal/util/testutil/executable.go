package testutil

import (
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// WriteExecutable writes a program for a test to run, with mode 0o755. Every
// test and test-support package writes such a file through this function, and
// TestEveryExecutableGoesThroughWriteExecutable fails any other write.
//
// A plain os.WriteFile can make the next start of the file fail with ETXTBSY
// (`text file busy`). A fork in another goroutine copies the descriptor that
// os.WriteFile holds open for writing, and the child keeps that copy until its
// own exec. Linux refuses to run a file that any process holds open for writing,
// so the start fails even after os.WriteFile closed its own descriptor. A rename
// does not help, because the copy still refers to the same file.
//
// The failure is easy to misread. A provider starts its program through a shell,
// so the error arrives as exit status 126 from a program that never ran. A
// reader that treats a failure as an empty list then reports no error at all.
//
// syscall.forkExec holds the write side of syscall.ForkLock across each fork.
// The read side therefore keeps every fork of this process out of the time that
// the descriptor is open, and no child can inherit it.
func WriteExecutable(t testing.TB, path, content string) {
	t.Helper()
	syscall.ForkLock.RLock()
	err := os.WriteFile(path, []byte(content), 0o755)
	syscall.ForkLock.RUnlock()
	require.NoError(t, err)
}

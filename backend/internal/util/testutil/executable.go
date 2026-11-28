package testutil

import (
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/execfile"
)

// WriteExecutable writes a program for a test to run, with mode 0o755. Every
// test and test-support package writes such a file through this function, and
// TestEveryExecutableGoesThroughWriteExecutable fails any other write.
//
// A plain os.WriteFile can make the next start of the file fail with ETXTBSY
// (`text file busy`), because a fork in another goroutine inherits the open
// descriptor; execfile.Overwrite states the mechanism and keeps every fork out.
//
// The failure is easy to misread. A provider starts its program through a shell,
// so the error arrives as exit status 126 from a program that never ran. A
// reader that treats a failure as an empty list then reports no error at all.
func WriteExecutable(t testing.TB, path, content string) {
	t.Helper()
	require.NoError(t, execfile.Overwrite(path, 0o755, func(w io.Writer) error {
		_, err := io.WriteString(w, content)
		return err
	}))
}

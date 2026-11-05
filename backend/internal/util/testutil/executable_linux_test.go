//go:build linux

package testutil

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Without the lock of WriteExecutable, about one start in a hundred fails with
// `text file busy` while the process forks, and these rounds caught every removal
// of the lock that was tried. With the lock in place the failure cannot occur.
//
// The test runs on Linux alone because it can fail nowhere else that the project
// tests: 7,200 starts on macOS without the lock produced no such failure.
func TestWriteExecutableRunsWhileTheProcessForks(t *testing.T) {
	t.Parallel()
	const (
		writers = 8
		forkers = 8
		rounds  = 40
	)
	stop := make(chan struct{})
	var forks sync.WaitGroup
	for range forkers {
		forks.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
					assert.NoError(t, exec.Command("/bin/sh", "-c", ":").Run())
				}
			}
		})
	}
	defer func() {
		close(stop)
		forks.Wait()
	}()

	root := t.TempDir()
	t.Run("writers", func(t *testing.T) {
		for writer := range writers {
			t.Run(strconv.Itoa(writer), func(t *testing.T) {
				t.Parallel()
				for round := range rounds {
					path := filepath.Join(root, fmt.Sprintf("program-%d-%d", writer, round))
					WriteExecutable(t, path, "#!/bin/sh\nexit 7\n")
					var exit *exec.ExitError
					require.ErrorAs(t, exec.Command(path).Run(), &exit, "the program must start")
					require.Equal(t, 7, exit.ExitCode())
				}
			})
		}
	})
}

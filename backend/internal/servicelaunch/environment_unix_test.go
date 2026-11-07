//go:build !windows

package servicelaunch

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestEnvironmentFileRefusesFIFOWithoutWriter(t *testing.T) {
	for _, throughLink := range []bool{false, true} {
		t.Run(fmt.Sprintf("symlink=%v", throughLink), func(t *testing.T) {
			t.Parallel()
			fifo := filepath.Join(t.TempDir(), "environment.fifo")
			require.NoError(t, unix.Mkfifo(fifo, 0o600))
			path := fifo
			if throughLink {
				path = filepath.Join(t.TempDir(), "environment.yaml")
				require.NoError(t, os.Symlink(fifo, path))
			}
			done := make(chan error, 1)
			go func() {
				_, err := (LaunchSpec{EnvironmentFile: &path}).LoadEnvironment()
				done <- err
			}()
			deadline := time.NewTimer(30 * time.Second)
			defer deadline.Stop()
			select {
			case err := <-done:
				require.ErrorContains(t, err, "regular file")
			case <-deadline.C:
				// A nonblocking read/write handle releases the old blocking open.
				// Keep it open until the reader checks the FIFO type and returns.
				fd, err := unix.Open(fifo, unix.O_RDWR|unix.O_NONBLOCK, 0)
				require.NoError(t, err)
				assert.ErrorContains(t, <-done, "regular file")
				require.NoError(t, unix.Close(fd))
				t.Fatal("environment loading blocked on a FIFO without a writer")
			}
		})
	}
}

func TestEnvironmentFileFollowsRegularSymlink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "environment.yaml")
	require.NoError(t, os.WriteFile(path, []byte("TOKEN: 'value'\n"), 0o644))
	link := filepath.Join(t.TempDir(), "linked.yaml")
	require.NoError(t, os.Symlink(path, link))
	values, err := (LaunchSpec{EnvironmentFile: &link}).LoadEnvironment()
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"TOKEN": "value"}, values)
}

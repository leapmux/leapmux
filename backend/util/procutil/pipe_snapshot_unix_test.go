//go:build darwin || linux

package procutil

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestPipeReadStateContinuesAfterSignalInterruption(t *testing.T) {
	t.Parallel()
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	const payload = "queued provider frame\n"
	_, err = writer.WriteString(payload)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	conn, err := reader.SyscallConn()
	require.NoError(t, err)

	// A concurrent fork can retain the closed-on-exec writer until exec.
	// Wait for hangup before taking the snapshots with injected interruptions.
	var events int16
	var pollErr error
	require.NoError(t, conn.Control(func(handle uintptr) {
		fds := []unix.PollFd{{Fd: int32(handle), Events: unix.POLLHUP}}
		_, pollErr = retryInterruptedPipeQuery(func() (int, error) {
			return unix.Poll(fds, int((30 * time.Second).Milliseconds()))
		})
		events = fds[0].Revents
	}))
	require.NoError(t, pollErr)
	require.NotZero(t, events&unix.POLLHUP, "the kernel must observe the last writer close")

	for _, interrupted := range []string{"count", "poll"} {
		t.Run(interrupted, func(t *testing.T) {
			ioctl, poll := unix.IoctlGetInt, unix.Poll
			sentSignal := false
			if interrupted == "count" {
				ioctl = func(fd int, request uint) (int, error) {
					if !sentSignal {
						sentSignal = true
						return 0, unix.EINTR
					}
					return unix.IoctlGetInt(fd, request)
				}
			} else {
				poll = func(fds []unix.PollFd, timeout int) (int, error) {
					if !sentSignal {
						sentSignal = true
						return 0, unix.EINTR
					}
					return unix.Poll(fds, timeout)
				}
			}
			var count int
			var atEOF bool
			var snapshotErr error
			require.NoError(t, conn.Control(func(handle uintptr) {
				count, atEOF, snapshotErr = inspectPipeReadState(int(handle), ioctl, poll)
			}))
			require.True(t, sentSignal)
			require.NoError(t, snapshotErr)
			assert.Equal(t, len(payload), count)
			assert.True(t, atEOF)
		})
	}
}

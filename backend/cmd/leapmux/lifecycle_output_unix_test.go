//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func fillConsolePipe(t *testing.T, _ *os.File, writer *os.File) {
	t.Helper()
	raw, err := writer.SyscallConn()
	require.NoError(t, err)
	var failure error
	written := 0
	require.NoError(t, raw.Control(func(fd uintptr) {
		if failure = unix.SetNonblock(int(fd), true); failure != nil {
			return
		}
		data := make([]byte, 4096)
		for written < 16<<20 {
			n, writeErr := unix.Write(int(fd), data)
			written += n
			if errors.Is(writeErr, unix.EINTR) {
				continue
			}
			if errors.Is(writeErr, unix.EAGAIN) || errors.Is(writeErr, unix.EWOULDBLOCK) {
				if len(data) == 1 {
					return
				}
				data = data[:len(data)/2]
				continue
			}
			if writeErr != nil {
				failure = writeErr
				return
			}
			if n == 0 {
				failure = fmt.Errorf("the console pipe accepted no bytes without reporting a failure")
				return
			}
		}
		failure = fmt.Errorf("the console pipe did not fill after 16 MiB")
	}))
	require.NoError(t, failure)
	require.Positive(t, written)
}

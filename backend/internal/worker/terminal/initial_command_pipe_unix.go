//go:build !windows

package terminal

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func makeInitialCommandPipe(path string) error {
	return unix.Mkfifo(path, 0o600)
}

// Opening a write-only FIFO without waiting succeeds only when the reader
// has opened it. Retain the descriptor so the reader waits for the release.
func openInitialCommandPipe(path string) (*os.File, error) {
	writer, err := os.OpenFile(path, os.O_WRONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENXIO) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	info, err := writer.Stat()
	if err == nil && info.Mode()&os.ModeNamedPipe != 0 {
		return writer, nil
	}
	_ = writer.Close()
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("%q is not a named pipe", path)
}

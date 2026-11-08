//go:build !windows

package servicelaunch

import (
	"os"

	"golang.org/x/sys/unix"
)

// Nonblocking open permits the caller to reject a FIFO on the opened handle.
// Checking the path before opening would allow a replacement to block the read.
func openEnvironmentFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

package terminal

import (
	"errors"
	"os"
)

var errInitialCommandPipeUnavailable = errors.New("the POSIX initial command pipe is unavailable")

func makeInitialCommandPipe(string) error { return errInitialCommandPipeUnavailable }

func openInitialCommandPipe(string) (*os.File, error) {
	return nil, errInitialCommandPipeUnavailable
}

func openInitialCommandMarker(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	return os.Open(path)
}

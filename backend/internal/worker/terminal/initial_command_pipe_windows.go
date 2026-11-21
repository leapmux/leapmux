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

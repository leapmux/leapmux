package main

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// Go preserves the native WriteFile errors for Windows pipes.
func isClosedConsolePipe(err error) bool {
	return errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, windows.ERROR_BROKEN_PIPE) ||
		errors.Is(err, windows.ERROR_NO_DATA) ||
		errors.Is(err, windows.ERROR_PIPE_NOT_CONNECTED)
}

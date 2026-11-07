//go:build !windows

package main

import (
	"errors"
	"syscall"
)

func isClosedConsolePipe(err error) bool {
	return errors.Is(err, syscall.EPIPE)
}

package main

import (
	"fmt"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/sys/windows"
)

func TestClosedConsolePipeWindowsErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		err    error
		closed bool
	}{
		{name: "nil"},
		{name: "portable broken pipe", err: syscall.EPIPE, closed: true},
		{name: "native broken pipe", err: windows.ERROR_BROKEN_PIPE, closed: true},
		{name: "native closing pipe", err: windows.ERROR_NO_DATA, closed: true},
		{name: "native disconnected pipe", err: windows.ERROR_PIPE_NOT_CONNECTED, closed: true},
		{name: "closed file", err: os.ErrClosed},
		{name: "invalid handle", err: windows.ERROR_INVALID_HANDLE},
		{name: "access denied", err: windows.ERROR_ACCESS_DENIED},
		{name: "busy pipe", err: windows.ERROR_PIPE_BUSY},
		{name: "invalid pipe", err: windows.ERROR_BAD_PIPE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.closed, isClosedConsolePipe(tc.err))
			if tc.err != nil {
				wrapped := fmt.Errorf("mirror console: %w", &os.PathError{Op: "write", Path: "console", Err: tc.err})
				assert.Equal(t, tc.closed, isClosedConsolePipe(wrapped))
			}
		})
	}
}

//go:build windows

package main

import (
	"errors"
	"net"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/sys/windows"
)

// Winsock reports fd exhaustion as WSAEMFILE (10024), never the POSIX EMFILE (24).
// A predicate written against the POSIX names compiles here and is permanently
// false, so the whole retry policy would ship inert on Windows and an exhaustion
// spike would delete the tunnel outright -- the exact regression the policy exists
// to prevent, on the one platform the constant list forgot.
func TestIsTemporaryAcceptError_Windows(t *testing.T) {
	wrap := func(err error) error {
		return &net.OpError{Op: "accept", Net: "tcp", Err: err}
	}

	for _, err := range []error{
		windows.WSAEMFILE,
		windows.WSAENOBUFS,
		windows.WSAECONNABORTED,
		windows.WSAEINTR,
	} {
		assert.True(t, isTemporaryAcceptError(wrap(err)),
			"%v is a transient Winsock accept failure and must be retried", err)
	}

	assert.False(t, isTemporaryAcceptError(wrap(syscall.EMFILE)),
		"the POSIX EMFILE is not what Winsock reports; matching it would prove nothing")

	for _, err := range []error{net.ErrClosed, errors.New("listener is broken")} {
		assert.False(t, isTemporaryAcceptError(err),
			"%v is not transient; the tunnel must fail rather than spin", err)
	}
}

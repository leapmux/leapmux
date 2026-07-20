//go:build unix

package main

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The retry policy must recognise fd exhaustion as it actually arrives from the
// listener: wrapped in a *net.OpError, not as a bare errno. errors.Is unwraps it,
// but only if the errno list is the platform's own -- which is why this predicate is
// build-tagged (see accept_error_windows.go, where Winsock's WSAEMFILE would make
// every POSIX name here permanently false).
func TestIsTemporaryAcceptError_Unix(t *testing.T) {
	wrap := func(err error) error {
		return &net.OpError{Op: "accept", Net: "tcp", Err: err}
	}

	// EMFILE/ENFILE are fd exhaustion; ENOBUFS/ENOMEM are the kernel failing to
	// allocate the accepted socket. Both clear on their own, and the Windows half
	// already classifies the latter (WSAENOBUFS) as transient.
	for _, err := range []error{syscall.EMFILE, syscall.ENFILE, syscall.ENOBUFS, syscall.ENOMEM} {
		assert.True(t, isTemporaryAcceptError(wrap(err)),
			"%v is transient resource exhaustion and must be retried", err)
		assert.True(t, isTemporaryAcceptError(err), "a bare %v must be recognised too", err)
	}

	for _, err := range []error{
		net.ErrClosed,
		errors.New("listener is broken"),
		syscall.EINVAL,
		fmt.Errorf("wrapped: %w", syscall.EBADF),
	} {
		assert.False(t, isTemporaryAcceptError(err),
			"%v is not transient; the tunnel must fail rather than spin", err)
	}
}

// transientAcceptErr returns the platform's canonical transient accept error
// (fd exhaustion) for tests that script-inject one. The POSIX spelling is
// EMFILE; the Windows half of this pair returns WSAEMFILE -- so a test that
// hard-codes syscall.EMFILE passes on Unix but is permanently non-transient
// on Windows, defeating the retry policy it is meant to exercise.
func transientAcceptErr() error { return syscall.EMFILE }

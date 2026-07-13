//go:build windows

package main

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isTemporaryAcceptError reports whether an Accept failure is transient -- the
// listener is still good and a later Accept can succeed.
//
// Winsock has its own errno space: a socket Accept surfaces WSAEMFILE (10024), never
// the POSIX syscall.EMFILE (24), so matching the POSIX names here would compile
// cleanly and be permanently false -- the whole retry policy silently inert on
// Windows, and an fd-exhaustion spike would delete the tunnel outright. See
// accept_error_unix.go for the POSIX half.
//
//   - WSAEMFILE: no more socket descriptors available (the fan-out case).
//   - WSAENOBUFS: no buffer space -- the same resource exhaustion, reported
//     differently depending on where Winsock runs out.
//   - WSAECONNABORTED / WSAEINTR: a peer that vanished between SYN and accept, and
//     an interrupted blocking call. Unlike the POSIX half (where the runtime poller
//     absorbs both before they reach us), these do reach callers on Windows.
func isTemporaryAcceptError(err error) bool {
	return errors.Is(err, windows.WSAEMFILE) ||
		errors.Is(err, windows.WSAENOBUFS) ||
		errors.Is(err, windows.WSAECONNABORTED) ||
		errors.Is(err, windows.WSAEINTR)
}

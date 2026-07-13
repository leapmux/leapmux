//go:build unix

package main

import (
	"errors"
	"syscall"
)

// isTemporaryAcceptError reports whether an Accept failure is transient -- the
// listener is still good and a later Accept can succeed.
//
// EMFILE/ENFILE are process/system fd exhaustion, and ENOBUFS/ENOMEM are kernel
// socket-buffer/memory exhaustion (accept(2) documents ENOBUFS/ENOMEM on Linux and
// ENOMEM on macOS: the new socket cannot be allocated, "often limited by the socket
// buffer limits"). All four are the same story -- a resource spike from a browser
// fanning out through SOCKS5, or a `git fetch` opening many port-forward conns,
// exactly the workloads the tunnel exists for -- and all four clear on their own
// within milliseconds. Treating any of them as fatal killed the tunnel permanently.
// ENOBUFS/ENOMEM are the POSIX spelling of what WSAENOBUFS already covers on the
// Windows half of this pair.
//
// ECONNABORTED and EINTR are deliberately absent: Go's runtime poller already
// retries the accept loop on both (internal/poll's accept never surfaces them), so
// listing them here would be dead code implying a guarantee this predicate does not
// provide. The Windows half of this pair carries a different list for the same
// reason -- see accept_error_windows.go.
func isTemporaryAcceptError(err error) bool {
	return errors.Is(err, syscall.EMFILE) ||
		errors.Is(err, syscall.ENFILE) ||
		errors.Is(err, syscall.ENOBUFS) ||
		errors.Is(err, syscall.ENOMEM)
}

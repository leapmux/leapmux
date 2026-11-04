package service

import (
	"errors"
	"fmt"

	"connectrpc.com/connect/v2"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// storeConnectError maps a store error onto the Connect surface: not found is
// the caller's selector, conflict is the caller's field, an invalid cursor or
// argument is the caller's input, and anything else is a hub fault. Every
// handler that answers a store error calls it, so one store failure reads as
// one code on every surface. op identifies the call that failed, and every
// answer's message carries it.
func storeConnectError(err error, op string) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return connect.Errorf(connect.CodeNotFound, "%s: not found", op)
	case errors.Is(err, store.ErrConflict):
		return connect.Errorf(connect.CodeAlreadyExists, "%s: conflict", op)
	case errors.Is(err, store.ErrInvalidCursor):
		// ErrInvalidCursor is its own sentinel and does NOT wrap
		// ErrInvalidArgument, so without this case it read as a hub fault.
		return invalidCursorError(err, op)
	case errors.Is(err, store.ErrInvalidArgument):
		return wrappedConnectError(connect.CodeInvalidArgument, fmt.Errorf("%s: %w", op, err))
	default:
		return wrappedConnectError(connect.CodeInternal, fmt.Errorf("%s: %w", op, err))
	}
}

// invalidCursorError is the one answer to a cursor the store cannot parse.
// The store wraps every parse failure in store.ErrInvalidCursor before any
// query runs. The fix is not obvious from the parse error alone, so the
// message states it.
func invalidCursorError(err error, op string) error {
	return wrappedConnectError(connect.CodeInvalidArgument, fmt.Errorf(
		"%s: %w (pass the cursor printed at the end of the previous page, or omit it to start from the first page)", op, err))
}

package service

import (
	"errors"

	"connectrpc.com/connect"

	"github.com/leapmux/leapmux/internal/util/userid"
)

// mintRowUserID converts a user id read from (or just written to) the store
// into the typed form the store params now require.
//
// A blank id here is corrupt data rather than a caller mistake -- users.id is
// a primary key -- so it surfaces as an internal fault. It deliberately does
// NOT panic: these are login / OAuth-link flows that already return errors, and
// userid.MustNew's contract is "the caller already knows this is non-empty",
// which holds for a literal but never for a column.
func mintRowUserID(rowID string) (userid.UserID, error) {
	uid, ok := userid.New(rowID)
	if !ok {
		return userid.UserID{}, connect.NewError(connect.CodeInternal, errors.New("user row has a blank id"))
	}
	return uid, nil
}

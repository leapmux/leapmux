package service

import (
	"context"
	"errors"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// The one selector rule, shared by the online admin RPCs and the offline
// `leapmux recover` verbs: a user is addressed by id OR username, never
// both and never neither.
//
// The rule is shared; the WORDING is not. The RPC surface gives proto
// fields to a machine caller and the CLI gives flags to a person, so
// Resolve returns these sentinels unformatted and each surface renders
// them for its own audience. Returning one surface's phrasing to the
// other was the alternative, and it puts `--id` in an RPC error.
var (
	// ErrNoUserSelector reports that neither id nor username was given.
	ErrNoUserSelector = errors.New("a user selector is required")
	// ErrAmbiguousUserSelector reports that both were given.
	ErrAmbiguousUserSelector = errors.New("id and username are mutually exclusive")
)

// ResolveUserSelector resolves an (id | username) selector to a live user
// row. A lookup failure is returned verbatim, so `errors.Is(err,
// store.ErrNotFound)` still classifies it at either surface.
func ResolveUserSelector(ctx context.Context, st store.Store, id, username string) (*store.User, error) {
	switch {
	case id == "" && username == "":
		return nil, ErrNoUserSelector
	case id != "" && username != "":
		return nil, ErrAmbiguousUserSelector
	case id != "":
		return st.Users().GetByID(ctx, id)
	default:
		return st.Users().GetByUsername(ctx, username)
	}
}

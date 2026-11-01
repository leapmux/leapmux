package service

import (
	"context"
	"errors"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// The one selector rule, shared by the online admin RPCs and the offline
// `leapmux recover` verbs: a user is addressed by a UserSelector, whose oneof
// holds an id OR a username. The oneof makes "both" unrepresentable, so the
// rule refuses only an absent selector and a selector that selects nothing.
//
// The rule is shared; the WORDING is not. The RPC surface gives proto
// fields to a machine caller and the CLI gives flags to a person, so the
// resolvers return these sentinels unformatted and each surface renders
// them for its own audience. Returning one surface's phrasing to the
// other was the alternative, and it puts `--id` in an RPC error.
var (
	// ErrNoUserSelector reports that the request carries no selector where it
	// must address one user.
	ErrNoUserSelector = errors.New("a user selector is required")
	// ErrEmptyUserSelector reports a selector that is present but selects no
	// case, or selects an empty id or username.
	ErrEmptyUserSelector = errors.New("the user selector selects no id or username")
)

// SelectUserByID returns a selector for the account with this id.
func SelectUserByID(id string) *leapmuxv1.UserSelector {
	return &leapmuxv1.UserSelector{By: &leapmuxv1.UserSelector_Id{Id: id}}
}

// SelectUserByUsername returns a selector for the account with this username.
func SelectUserByUsername(username string) *leapmuxv1.UserSelector {
	return &leapmuxv1.UserSelector{By: &leapmuxv1.UserSelector_Username{Username: username}}
}

// selectedUser returns the value a present selector selects, and whether that
// value is an id. It refuses a selector with no case selected and an empty
// value. It reads no store, so every refusal happens before a lookup.
func selectedUser(sel *leapmuxv1.UserSelector) (value string, byID bool, err error) {
	switch by := sel.GetBy().(type) {
	case *leapmuxv1.UserSelector_Id:
		value, byID = by.Id, true
	case *leapmuxv1.UserSelector_Username:
		value = by.Username
	}
	if value == "" {
		return "", false, ErrEmptyUserSelector
	}
	return value, byID, nil
}

// ResolveUserSelector resolves a selector that must address one user to a
// live user row. An absent selector is ErrNoUserSelector, and an empty one
// is ErrEmptyUserSelector; neither reaches the store. A lookup failure is
// returned verbatim, so `errors.Is(err, store.ErrNotFound)` still classifies
// it at either surface.
func ResolveUserSelector(ctx context.Context, st store.Store, sel *leapmuxv1.UserSelector) (*store.User, error) {
	if sel == nil {
		return nil, ErrNoUserSelector
	}
	value, byID, err := selectedUser(sel)
	if err != nil {
		return nil, err
	}
	if byID {
		return st.Users().GetByID(ctx, value)
	}
	return st.Users().GetByUsername(ctx, value)
}

// ResolveUserSelectorFilter resolves an optional listing filter to the id of
// the user it selects. An absent selector is no filter (nil, nil). A present
// selector that selects nothing is ErrEmptyUserSelector, before any lookup.
//
// The id path resolves through GetByIDIncludeDeleted: admin listings
// deliberately surface a soft-deleted owner's still-live rows for audit. A
// username resolves among live users only, because usernames are freed on
// soft-delete, so a name is not a stable handle for a deleted account. A
// lookup failure is returned verbatim.
func ResolveUserSelectorFilter(ctx context.Context, st store.Store, sel *leapmuxv1.UserSelector) (*string, error) {
	if sel == nil {
		return nil, nil
	}
	value, byID, err := selectedUser(sel)
	if err != nil {
		return nil, err
	}
	var user *store.User
	if byID {
		user, err = st.Users().GetByIDIncludeDeleted(ctx, value)
	} else {
		user, err = st.Users().GetByUsername(ctx, value)
	}
	if err != nil {
		return nil, err
	}
	return &user.ID, nil
}

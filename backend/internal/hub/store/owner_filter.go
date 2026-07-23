package store

import "github.com/leapmux/leapmux/internal/util/userid"

// OwnerFilter unwraps an owner id for binding into a `WHERE <owner_col> = ?`
// ownership predicate. ok is false for a zero id, and the caller MUST refuse
// rather than bind.
//
// This is the SQL-side half of userid.Matches. A zero id unwraps to "", and ""
// does not fail to match -- it matches every row whose owner column is blank,
// which SQLite permits (it accepts "" as a TEXT primary key, so a blank-id user
// and rows owned by it are representable). An ownership gate that binds a blank
// parameter therefore fails OPEN, which is the exact pairing userid.UserID
// exists to close. Every dialect routes its ownership predicates through this
// so the three cannot drift.
func OwnerFilter(u userid.UserID) (string, bool) {
	if u.IsZero() {
		return "", false
	}
	return u.String(), true
}

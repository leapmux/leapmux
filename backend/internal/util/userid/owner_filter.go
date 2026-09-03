package userid

// OwnerFilter unwraps an owner id for binding into a `WHERE <owner_col> = ?`
// ownership predicate. ok is false for a zero id, and the caller MUST refuse
// rather than bind.
//
// This is the SQL-side half of Matches. A zero id unwraps to "", and "" does
// not fail to match -- it matches every row whose owner column is blank, which
// SQLite permits (it accepts "" as a TEXT primary key, so a blank-id user and
// rows owned by it are representable). An ownership gate that binds a blank
// parameter therefore fails OPEN, which is the exact pairing UserID exists to
// close. Every hub dialect routes its ownership predicates through this so the
// three cannot drift -- single-row predicates call it directly, and the bulk tab
// deletes reach it through store.FilterTabIndexKeys.
//
// It lives HERE rather than in internal/hub/store because the worker process
// binds owner columns too (worker_tab_payloads is keyed by (user_id, tab_id)) and
// does not -- must not -- import the hub store. A guard the worker cannot call
// is a guard the repo-wide audit in internal/audit could only enforce over half
// the queries it scans; that is precisely the blind spot that let an owner-blind
// worker read ship. For an id that never passed through New -- a database column,
// a normalized string -- New IS this guard: mint, refuse a blank, bind String().
//
// The audit in internal/audit recognises a call to this as the shared owner
// guard, and additionally requires the caller to act on ok (see
// ownerFilterRefusalHonoured): a presence-only check reintroduces the fail-open.
func OwnerFilter(u UserID) (string, bool) {
	if u.IsZero() {
		return "", false
	}
	return u.String(), true
}

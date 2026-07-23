package audit

// ownerColumns are the columns that name a row's owner. A query filtering on
// one of these in its WHERE clause is an ownership predicate, and the Go method
// that runs it must refuse an unminted caller before binding.
//
// This slice is the single source of truth: the regex that recognises an
// ownership predicate is BUILT from it, so a column added here is covered
// everywhere at once. It used to be restated inside that regex, and the rule's
// one silent failure mode is a column the regex does not know -- the query is
// simply never classified as an ownership predicate, and nothing anywhere
// reports a gap.
var ownerColumns = []string{"user_id", "owner_user_id", "registered_by", "created_by"}

// unguardedOwnerFilterQueries are the queries whose WHERE clause names an owner
// column but whose adapter method deliberately does NOT route the bind through
// store.OwnerFilter, each with why.
//
// The rule this backs is the one that actually shipped a live fail-open: a zero
// userid.UserID unwraps to "", and "" does not fail to match a `WHERE user_id =
// ?` predicate -- it MATCHES every row whose owner column is blank, which all
// three dialects permit. ListAccessibleWorkspaces bound it raw and returned
// every blank-owner workspace in the org.
//
// It is derived from the SQL rather than from the Go on purpose. An INSERT has
// no WHERE, so writes are out of scope automatically and need no allowlist; the
// ~37 raw unwraps that remain are column VALUES, not predicates, and this rule
// never looks at them.
//
// The map below is EMPTY today, which is the outcome to preserve. Deriving the rule from
// the SQL made it precise enough that every ownership predicate in the repo is
// genuinely guarded: writes have no WHERE and never enter scope, `UPDATE ... SET
// user_id = ?` binds a value rather than a predicate, and LockUserAuthState
// filters `users.id` -- a primary key, not an owner reference. An entry here
// should therefore be rare and always carry a reason a reviewer can check.
var unguardedOwnerFilterQueries = map[string]string{}

package audit

// ownerColumns are the columns that name a row's owner. A query filtering on
// one of these in its WHERE clause is an ownership predicate, and the Go method
// that runs it must refuse an unminted caller before binding.
//
// This slice is the single source of truth for BOTH halves of the rule: the
// regex that recognises an ownership predicate in a sqlc query is built from
// it, and the function-level scan over runtime-composed SQL reads it directly
// (mentionsOwnerColumn). A column added here is therefore covered everywhere at
// once. It used to be restated inside that regex, and the rule's one silent
// failure mode is a column the scan does not know -- the query is simply never
// classified as an ownership predicate, and nothing anywhere reports a gap.
var ownerColumns = []string{"user_id", "owner_user_id", "registered_by", "created_by"}

// unguardedOwnerFilterQueries are the queries whose WHERE clause names an owner
// column but whose caller deliberately does NOT route the bind through
// userid.OwnerFilter, each with why.
//
// The rule this backs is the one that actually shipped a live fail-open: a zero
// userid.UserID unwraps to "", and "" does not fail to match a `WHERE user_id =
// ?` predicate -- it MATCHES every row whose owner column is blank, which all
// three dialects permit. ListAccessibleWorkspaces bound it raw and returned
// every blank-owner workspace for that user.
//
// It is derived from the SQL rather than from the Go on purpose. An INSERT has
// no WHERE, so writes are out of scope automatically and need no allowlist; the
// ~37 raw unwraps that remain are column VALUES, not predicates, and this rule
// never looks at them.
//
// Deriving the rule from the SQL made it precise enough that the HUB half needs
// no entries at all, and that is the outcome to preserve there: writes have no
// WHERE and never enter scope, `UPDATE ... SET user_id = ?` binds a value rather
// than a predicate, and LockUserAuthState filters `users.id` -- a primary key,
// not an owner reference. An entry here should be rare and always carry a reason
// a reviewer can check.
//
// The three entries below are all one argument, and it is a property of the
// SCHEMA rather than of any caller: worktree_tabs.user_id is deliberately the empty string for
// AGENT/TERMINAL links. Agent and terminal ids are globally unique, so those
// links need no owner to disambiguate them, and worker/service.worktreeTabUserID
// normalizes BOTH the write and every read to that same empty string. The blank owner these
// queries bind is therefore the value the row was written with, not an unminted
// caller -- refusing it would make every agent/terminal tab close a silent no-op
// and leak the worktree it should have released. FILE links, where the owner IS
// load-bearing (file tab ids are unique only within a user), carry a real owner
// through the same parameter, and checkOwnerScopedQueries independently requires
// each of these queries to NAME user_id so that half stays scoped.
var unguardedOwnerFilterQueries = map[string]string{
	"GetWorktreeForTab":         "worktree_tabs.user_id is '' by design for AGENT/TERMINAL links; see the block comment above",
	"RemoveWorktreeTab":         "worktree_tabs.user_id is '' by design for AGENT/TERMINAL links; see the block comment above",
	"DeleteWorktreeTabsByTabID": "worktree_tabs.user_id is '' by design for AGENT/TERMINAL links; see the block comment above",
}

// unscopedOwnerKeyedQueries are the queries that touch an owner-keyed table --
// one whose PRIMARY KEY is composite AND contains an owner column -- without
// naming that column in their WHERE clause, each with why.
//
// The rule this backs (checkOwnerScopedQueries) is the UNIVERSAL half of the
// store-bind rule, and it exists because the other half is a conditional: "IF a
// query binds an owner column, THEN its caller must refuse an unminted id". A
// query that simply omits the owner column is outside that domain, so it is
// never classified and nothing reports it -- green by construction. That is how
// a cross-tenant read of workspace_tab_owned, filtered on workspace_id alone
// while the row identity is (user_id, tab_id), shipped past both SQL-derived
// rules.
//
// An entry here is a claim that the query is owner-blind ON PURPOSE and that
// matching another user's row is the intended behaviour -- a whole-table sweep,
// a ref-count, a cascade. That is a much narrower claim than "this caller is
// trusted", which is why the composite-key narrowing matters: widening the
// population to every table with an owner FK measured at ~200 queries
// repo-wide, and an exemption table that size is a rubber stamp rather than a
// reviewed list.
var unscopedOwnerKeyedQueries = map[string]string{
	// ---- deliberately cross-owner sweeps ----
	//
	// Each of these exists to walk EVERY owner's rows; naming the owner column
	// would not narrow a mistake, it would break the feature.

	// Encryption-key rotation re-encrypts every stored OAuth token, and the
	// admin driving it is not the owner of any of them.
	"ListOAuthTokensByKeyVersion":  "key rotation re-encrypts every owner's tokens; scoping it to one owner would leave the rest at the old key version",
	"CountOAuthTokensByKeyVersion": "the progress counter for the rotation above; it must count what that query will rewrite",
	// Background refresh, driven by a timer rather than by a caller.
	"ListExpiringOAuthTokens": "the refresh sweep is server-initiated and has no caller identity; it must see every owner's expiring tokens or those users silently lose their provider session",
	// Provider teardown: deleting an oauth_providers row must take every
	// user's link and token with it, which is what the FK cascade would do
	// anyway.
	"DeleteOAuthTokensByProvider":    "deleting a provider must revoke every owner's tokens for it; the users(id) FK cascades the other axis identically",
	"DeleteOAuthUserLinksByProvider": "deleting a provider must drop every owner's link to it; see DeleteOAuthTokensByProvider",
	// Retention GC over the CRDT dedup window.
	"DeleteExpiredRecentBatchIDs": "a retention sweep over expired dedup entries; the cutoff is the predicate and every owner's expired rows must go",

	// ---- the non-owner half IS a key ----

	// oauth_user_links carries UNIQUE INDEX (provider_id, provider_subject),
	// so this lookup cannot match a second row -- and it is the query that
	// DISCOVERS the owner during a provider login, so it has none to bind.
	"GetOAuthUserLink": "idx_oauth_user_links_provider_subject makes (provider_id, provider_subject) unique, and this is the login lookup that resolves WHICH user the external subject maps to",

	// ---- section teardown ----
	//
	// section_id is a globally-unique id and SectionService verifies the
	// caller owns the section (requireOwnedSection) before either of these
	// runs. The sweep must then reach every item pointing at that section --
	// including a cross-owner row, which would otherwise survive its section
	// and dangle.
	"DeleteWorkspaceSectionItemsBySection": "the caller's ownership of section_id is verified by requireOwnedSection; the teardown must reach every item pointing at it",
	"HasWorkspaceSectionItemsBySection":    "the emptiness probe for the teardown above; it must see exactly what that delete will remove",

	// ---- worker: deliberately cross-owner ----

	// The reconciler and the backfill both walk the whole table on purpose;
	// the per-row owner is read back out and used to scope everything they do
	// with it (see OrphanReconciler.reconcileFileTabs' ownerScope guard).
	"ListAllWorkerFileTabs": "the orphan reconciler and the worktree-link backfill walk EVERY owner's rows by design, and re-scope per row from the user_id they read back",
	// worktree_tabs ref-counting is cross-owner BY DEFINITION: the count
	// exists to stop one user's close from removing a worktree another user
	// still has a file tab open in.
	"CountWorktreeTabs": "the worktree ref-count must see every owner's links, or one user's close removes a worktree another user is still working in",
	// The cascade half of the same ref-count.
	"DeleteWorktreeTabsByWorktreeID": "the worktree row is gone, so every owner's link to it must go with it",
}

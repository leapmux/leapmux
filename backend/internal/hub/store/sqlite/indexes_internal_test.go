package sqlite

import (
	"context"
	"database/sql"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWorkersIndexesServeCompositeKeyset pins the worker index set now that no
// column stores a status. Every keyset listing filters the live/deleted
// predicate it reads from deleted_at, so each rides a PARTIAL index on that
// predicate (pinned below and in TestPaginatedListingsRideTheirKeysetIndexes).
// The HardDeleteUsersBefore NOT EXISTS (workers.registered_by = users.id)
// point probe, and the registered_by foreign key, must see soft-deleted rows
// too, so they need a NON-partial registered_by index of their own. This is a
// plan-level concern: a regression leaves the result rows identical and would
// slip through every result-based storetest, so it needs a schema assertion.
func TestWorkersIndexesServeCompositeKeyset(t *testing.T) {
	_, db := newSessionTestStore(t)

	type idx struct{ name, sql string }
	rows, err := db.QueryContext(context.Background(),
		`SELECT name, sql FROM sqlite_master WHERE type='index' AND tbl_name='workers' AND name LIKE 'idx_workers_%'`)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rows.Close()) })

	got := map[string]string{}
	for rows.Next() {
		var ix idx
		require.NoError(t, rows.Scan(&ix.name, &ix.sql))
		got[ix.name] = ix.sql
	}
	require.NoError(t, rows.Err())

	probe, ok := got["idx_workers_registered_by"]
	require.True(t, ok, "the registered_by probe index is missing; got %v", got)
	assert.NotContains(t, strings.ToUpper(probe), "WHERE",
		"the registered_by index must be non-partial: the HardDeleteUsersBefore probe and the foreign key must see soft-deleted rows")
	for name, sqlText := range got {
		assert.NotContains(t, strings.ToLower(sqlText), "status",
			"%s indexes a status column; the status is derived and no column stores it", name)
	}
}

// indexSQL returns the CREATE INDEX statement for the named index, failing the
// test if it is absent. Pinning index presence + shape via sqlite_master catches
// regressions that result-level storetests cannot: a partial-vs-non-partial flip
// or a dropped `id DESC` tiebreaker leaves result rows identical while regressing
// every keyset page to a full scan plus sort.
func indexSQL(t *testing.T, db *sql.DB, table, indexName string) string {
	t.Helper()
	var sqlText string
	err := db.QueryRowContext(context.Background(),
		`SELECT sql FROM sqlite_master WHERE type='index' AND tbl_name=? AND name=?`, table, indexName,
	).Scan(&sqlText)
	require.NoError(t, err, "index %s on %s missing", indexName, table)
	return sqlText
}

// assertCompositeKeysetIndex pins that a keyset-serving index carries the exact
// column list the composite ORDER BY (col DESC, id DESC) needs -- so a leading-
// column flip (e.g. last_active_at -> created_at) regresses to a sort, not just a
// dropped trailing id -- plus the right partial-ness: wantPartial is the exact
// partial predicate (e.g. "WHERE deleted_at IS NULL") when the index's query
// carries the matching filter (so the planner can match the partial index), or
// empty when it does not (so a WHERE clause cannot make the index ineligible).
func assertCompositeKeysetIndex(t *testing.T, db *sql.DB, table, indexName, wantColumns, wantPartial string) {
	t.Helper()
	sqlText := strings.ToUpper(indexSQL(t, db, table, indexName))
	assert.Contains(t, sqlText, strings.ToUpper(wantColumns),
		"%s must carry the column list %s so the composite ORDER BY rides the index; a leading-column change silently regresses every page to a sort while result-level tests stay green", indexName, wantColumns)
	if wantPartial != "" {
		assert.Contains(t, sqlText, strings.ToUpper(wantPartial),
			"%s must be partial `%s`: its query carries the matching filter, and a non-partial match is impossible to guarantee once the planner considers OR-probes", indexName, wantPartial)
	} else {
		assert.NotContains(t, sqlText, "WHERE",
			"%s must be non-partial: its query carries no matching filter, and a WHERE clause would make it ineligible", indexName)
	}
}

// TestWorkersRegisteredByCreatedIndex pins the index behind the per-user live
// listings (ListWorkersByUserID and ListWorkersAdminLiveByUser: registered_by=?,
// deleted_at IS NULL). It must be partial on deleted_at (the queries filter it)
// and carry the trailing id DESC; without it every per-user page top-N sorts.
func TestWorkersRegisteredByCreatedIndex(t *testing.T) {
	_, db := newSessionTestStore(t)
	assertCompositeKeysetIndex(t, db, "workers", "idx_workers_registered_by_created", "registered_by, created_at DESC, id DESC", "WHERE deleted_at IS NULL")
}

// TestUsersKeysetIndexShape pins idx_users_created_at: ListAllUsers and
// SearchUsers filter `deleted_at IS NULL`, so the index must be partial on it
// (matching the query exactly) and carry the trailing id DESC.
func TestUsersKeysetIndexShape(t *testing.T) {
	_, db := newSessionTestStore(t)
	assertCompositeKeysetIndex(t, db, "users", "idx_users_created_at", "created_at DESC, id DESC", "WHERE deleted_at IS NULL")
}

// TestRegistrationKeysKeysetIndexShape pins idx_worker_registration_keys_created_at:
// ListRegistrationKeysAdmin filters `expires_at > now` (not deleted_at --
// worker_registration_keys has no deleted_at column), so the index must be
// non-partial and carry the trailing id DESC.
func TestRegistrationKeysKeysetIndexShape(t *testing.T) {
	_, db := newSessionTestStore(t)
	assertCompositeKeysetIndex(t, db, "worker_registration_keys", "idx_worker_registration_keys_created_at", "created_at DESC, id DESC", "")
}

// TestSessionsKeysetIndexShape pins idx_user_sessions_last_active:
// ListAllActiveSessions filters `expires_at > now` (user_sessions has no
// deleted_at column) and orders by (last_active_at DESC, id DESC), so the index
// must be non-partial and carry the trailing id DESC.
func TestSessionsKeysetIndexShape(t *testing.T) {
	_, db := newSessionTestStore(t)
	assertCompositeKeysetIndex(t, db, "user_sessions", "idx_user_sessions_last_active", "last_active_at DESC, id DESC", "")
}

// TestSessionsUserKeysetIndexShape pins idx_user_sessions_user_last_active: the
// per-user session listing ListUserSessionsByUserID (user_id =, expires_at
// residual, ORDER BY last_active_at DESC, id DESC) both seeks the leading
// user_id column and rides the trailing (last_active_at DESC, id DESC) order;
// the same index serves the plain user_id lookups (DeleteUserSessionsByUser,
// DeleteOtherUserSessions) via its prefix, so no separate user_id-only index
// exists. Non-partial: user_sessions has no deleted_at column.
func TestSessionsUserKeysetIndexShape(t *testing.T) {
	_, db := newSessionTestStore(t)
	assertCompositeKeysetIndex(t, db, "user_sessions", "idx_user_sessions_user_last_active", "user_id, last_active_at DESC, id DESC", "")
}

// TestWorkersDeletedKeysetIndexes pins the two indexes behind the deleted half
// of the admin worker listing (ListWorkersAdminDeleted and
// ListWorkersAdminDeletedByUser). Both queries filter `deleted_at IS NOT NULL`,
// so the indexes must be partial on exactly that predicate and carry the
// trailing id DESC.
func TestWorkersDeletedKeysetIndexes(t *testing.T) {
	_, db := newSessionTestStore(t)
	assertCompositeKeysetIndex(t, db, "workers", "idx_workers_deleted_created", "created_at DESC, id DESC", "WHERE deleted_at IS NOT NULL")
	assertCompositeKeysetIndex(t, db, "workers", "idx_workers_registered_by_deleted_created", "registered_by, created_at DESC, id DESC", "WHERE deleted_at IS NOT NULL")
}

// TestWorkersDeregisteringIndex pins the stale-deregistration sweep's index:
// partial on the sweep's own predicate, so it holds only the few workers in
// mid-deregistration.
func TestWorkersDeregisteringIndex(t *testing.T) {
	_, db := newSessionTestStore(t)
	assertCompositeKeysetIndex(t, db, "workers", "idx_workers_deregistering", "deregistering_at", "WHERE deregistering_at IS NOT NULL AND deleted_at IS NULL")
	details := explainPlan(t, db, loadQuerySQL(t, "workers.sql", "CompleteStaleDeregistrations"))
	assert.Condition(t, func() bool {
		for _, d := range details {
			if strings.Contains(d, "INDEX idx_workers_deregistering") {
				return true
			}
		}
		return false
	}, "the sweep must seek idx_workers_deregistering; plan: %v", details)
}

// TestWorkersCreatedAtIndex pins idx_workers_created_at: the all-user live
// admin listing (ListWorkersAdminLive) filters `deleted_at IS NULL`, so the
// index must be partial on it (matching the query exactly so the planner can
// use it) and carry the trailing id DESC.
func TestWorkersCreatedAtIndex(t *testing.T) {
	_, db := newSessionTestStore(t)
	assertCompositeKeysetIndex(t, db, "workers", "idx_workers_created_at", "created_at DESC, id DESC", "WHERE deleted_at IS NULL")
}

// TestAPITokensKeysetIndexShape pins idx_api_tokens_created_at: the admin
// ListAllAPITokens listing filters `revoked_at IS NULL`, so the index must be
// partial on it (matching the query exactly so the planner can use it) and
// carry the trailing id DESC.
func TestAPITokensKeysetIndexShape(t *testing.T) {
	_, db := newSessionTestStore(t)
	assertCompositeKeysetIndex(t, db, "api_tokens", "idx_api_tokens_created_at", "created_at DESC, id DESC", "WHERE revoked_at IS NULL")
}

// TestDelegationTokensKeysetIndexShape pins idx_delegation_tokens_created_at:
// the admin ListAllDelegationTokens listing filters `revoked_at IS NULL`, so
// the index must be partial on it and carry the trailing id DESC.
func TestDelegationTokensKeysetIndexShape(t *testing.T) {
	_, db := newSessionTestStore(t)
	assertCompositeKeysetIndex(t, db, "delegation_tokens", "idx_delegation_tokens_created_at", "created_at DESC, id DESC", "WHERE revoked_at IS NULL")
}

// TestAPITokensUserKeysetIndexShape pins idx_api_tokens_user_created: the
// admin ListAllAPITokensByUser listing (the --user-id path) seeks the leading
// user_id equality and rides (created_at DESC, id DESC); without this index
// the per-user page seeks idx_api_tokens_user and pays a TEMP B-TREE sort.
// Partial on revoked_at IS NULL, matching the query's live-token filter.
func TestAPITokensUserKeysetIndexShape(t *testing.T) {
	_, db := newSessionTestStore(t)
	assertCompositeKeysetIndex(t, db, "api_tokens", "idx_api_tokens_user_created", "user_id, created_at DESC, id DESC", "WHERE revoked_at IS NULL")
}

// TestDelegationTokensUserKeysetIndexShape is the delegation twin of
// TestAPITokensUserKeysetIndexShape.
func TestDelegationTokensUserKeysetIndexShape(t *testing.T) {
	_, db := newSessionTestStore(t)
	assertCompositeKeysetIndex(t, db, "delegation_tokens", "idx_delegation_tokens_user_created", "user_id, created_at DESC, id DESC", "WHERE revoked_at IS NULL")
}

// loadQuerySQL extracts one named query from a db/queries file and rewrites
// it to plain executable SQL: the block runs from the `-- name:` marker to the
// next marker (comment lines are stripped BEFORE cutting at the terminating
// semicolon, since comment prose may legally contain one), `sqlc.embed(x)`
// becomes `x.*`, and every sqlc.arg/sqlc.narg becomes a bare `?`. Reading the
// REAL query text keeps these plan pins incapable of drifting from the SQL
// sqlc compiles.
func loadQuerySQL(t *testing.T, file, name string) string {
	t.Helper()
	data, err := os.ReadFile("db/queries/" + file)
	require.NoError(t, err)
	src := string(data)
	loc := regexp.MustCompile(`-- name: ` + name + ` :\w+\n`).FindStringIndex(src)
	require.NotNil(t, loc, "query %s not found in %s", name, file)
	block := src[loc[1]:]
	if next := strings.Index(block, "\n-- name: "); next >= 0 {
		block = block[:next]
	}
	block = regexp.MustCompile(`(?m)^--[^\n]*\n?`).ReplaceAllString(block, "")
	sql, _, found := strings.Cut(block, ";")
	require.True(t, found, "query %s in %s has no terminating semicolon", name, file)
	sql = regexp.MustCompile(`sqlc\.embed\((\w+)\)`).ReplaceAllString(sql, "$1.*")
	sql = regexp.MustCompile(`sqlc\.n?arg\('?\w+'?\)`).ReplaceAllString(sql, "?")
	return sql
}

// TestPaginatedListingsRideTheirKeysetIndexes pins the query PLAN of every
// keyset-paginated listing: each must read its expected index (a SEARCH seek
// for the equality-prefixed queries, an ordered SCAN for the full listings)
// with NO `USE TEMP B-TREE FOR ORDER BY`. This closes the failure mode the
// DDL-shape pins above cannot see: a QUERY-side edit that leaves the index
// intact but makes it ineligible -- a re-added residual filter that breaks
// partial-index matching, a function wrapped around the ORDER BY column, a
// predicate rewrite the planner cannot push into the seek -- regresses every
// page to scan-plus-sort while result rows (and every result-level test) stay
// identical. The four IncludingRevoked forensics variants are deliberately
// absent: they have no matching partial index and may top-N sort by design
// (see their query comments).
func TestPaginatedListingsRideTheirKeysetIndexes(t *testing.T) {
	_, db := newSessionTestStore(t)

	cases := []struct{ file, query, index string }{
		{"users.sql", "ListAllUsers", "idx_users_created_at"},
		{"users.sql", "SearchUsers", "idx_users_created_at"},
		{"workers.sql", "ListWorkersByUserID", "idx_workers_registered_by_created"},
		{"workers.sql", "ListWorkersAdminLive", "idx_workers_created_at"},
		{"workers.sql", "ListWorkersAdminLiveByUser", "idx_workers_registered_by_created"},
		{"workers.sql", "ListWorkersAdminDeleted", "idx_workers_deleted_created"},
		{"workers.sql", "ListWorkersAdminDeletedByUser", "idx_workers_registered_by_deleted_created"},
		{"worker_registration_keys.sql", "ListRegistrationKeysAdmin", "idx_worker_registration_keys_created_at"},
		{"user_sessions.sql", "ListUserSessionsByUserID", "idx_user_sessions_user_last_active"},
		{"user_sessions.sql", "ListAllActiveSessions", "idx_user_sessions_last_active"},
		{"api_tokens.sql", "ListAllAPITokens", "idx_api_tokens_created_at"},
		{"api_tokens.sql", "ListAllAPITokensByUser", "idx_api_tokens_user_created"},
		{"delegation_tokens.sql", "ListAllDelegationTokens", "idx_delegation_tokens_created_at"},
		{"delegation_tokens.sql", "ListAllDelegationTokensByUser", "idx_delegation_tokens_user_created"},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			details := explainPlan(t, db, loadQuerySQL(t, tc.file, tc.query))
			// "USING INDEX" or "USING COVERING INDEX"; the LEFT JOIN users
			// branch contributes its own row, so match any row.
			assert.Condition(t, func() bool {
				for _, d := range details {
					if strings.Contains(d, "INDEX "+tc.index) {
						return true
					}
				}
				return false
			}, "%s must read %s; plan: %v", tc.query, tc.index, details)
			for _, d := range details {
				assert.NotContains(t, d, "USE TEMP B-TREE FOR ORDER BY",
					"%s must ride its index order, not sort every page; plan: %v", tc.query, details)
			}
		})
	}
}

// explainPlan returns the detail column of EXPLAIN QUERY PLAN for sqlText,
// binding NULL for every placeholder: SQLite chooses the plan at prepare
// time, so parameter values cannot change it.
func explainPlan(t *testing.T, db *sql.DB, sqlText string) []string {
	t.Helper()
	args := make([]any, strings.Count(sqlText, "?"))
	rows, err := db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+sqlText, args...)
	require.NoError(t, err)
	var details []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &notUsed, &detail))
		details = append(details, detail)
	}
	require.NoError(t, rows.Close())
	require.NoError(t, rows.Err())
	require.NotEmpty(t, details)
	return details
}

// TestRetentionAndBootstrapScansAreSargable pins the plans of the schema's
// remaining tuned index consumers (the keyset listings and revoked-token
// sweeps have their own pins above):
//
//   - GetFirstAdmin must read idx_users_is_admin. This pins the partial
//     predicate's SPELLING: SQLite's partial-index matcher is syntactic, so
//     rewriting the migration's `WHERE is_admin = 1` back to bare
//     `WHERE is_admin` silently orphans the index and this fails.
//   - ListExpiringOAuthTokens and DeleteExpiredDelegationTokensBefore must
//     seek their expires_at indexes with an upper limit -- the former
//     regressed to a full scan under its old datetime() wrap.
//   - The revocation-events compaction subquery must SEEK an index rather
//     than scan. Both sqlite_autoindex(seq) and idx_revocation_events_published
//     are eligible now that published_at compares raw (sargable); which wins
//     is a stats-dependent cost choice, so only the seek is pinned.
func TestRetentionAndBootstrapScansAreSargable(t *testing.T) {
	_, db := newSessionTestStore(t)

	admin := explainPlan(t, db, loadQuerySQL(t, "users.sql", "GetFirstAdmin"))
	assert.Contains(t, admin[0], "INDEX idx_users_is_admin",
		"GetFirstAdmin must ride the partial admin index; plan: %v", admin)

	oauth := explainPlan(t, db, loadQuerySQL(t, "oauth_tokens.sql", "ListExpiringOAuthTokens"))
	assert.Contains(t, oauth[0], "INDEX idx_oauth_tokens_expires_at", "plan: %v", oauth)
	assert.Contains(t, oauth[0], "expires_at<",
		"the expiring-token scan must carry its upper limit into the seek; plan: %v", oauth)

	expiry := explainPlan(t, db, loadQuerySQL(t, "delegation_tokens.sql", "DeleteExpiredDelegationTokensBefore"))
	assert.Contains(t, expiry[0], "INDEX idx_delegation_tokens_expires_at", "plan: %v", expiry)
	assert.Contains(t, expiry[0], "expires_at<", "plan: %v", expiry)

	compact := explainPlan(t, db, loadQuerySQL(t, "revocation_events.sql", "DeleteCompactablePublishedRevocationEvents"))
	var compactSeeks bool
	for _, d := range compact {
		require.NotContains(t, d, "SCAN ev",
			"the compaction subquery must not full-scan published events; plan: %v", compact)
		if strings.Contains(d, "SEARCH ev USING") {
			compactSeeks = true
		}
	}
	assert.True(t, compactSeeks, "the compaction subquery must seek an index; plan: %v", compact)

	// The revoked-session probe runs while the caller holds the user-auth row
	// lock, on a table that retains every sign-out, api-token revoke,
	// user_tokens fence and user_info event for the whole cleanup window. Its
	// partial index writes an entry only for the rarest of those kinds, so the
	// insert cost this was once left unindexed to avoid is close to zero.
	revoked := explainPlan(t, db, loadQuerySQL(t, "revocation_events.sql", "SessionRevokedEventExists"))
	var seeks bool
	for _, d := range revoked {
		assert.NotContains(t, d, "SCAN revocation_events",
			"the revoked-session probe must not scan the retention window; plan: %v", revoked)
		if strings.Contains(d, "INDEX idx_revocation_events_session_revoked") {
			seeks = true
		}
	}
	assert.True(t, seeks, "the revoked-session probe must seek its partial index; plan: %v", revoked)
}

// TestRevokedTokenSweepsAreSargable pins the query PLAN of the two hourly
// revoked-token retention sweeps: the raw-string `revoked_at < ?` compare
// (bound a canonical-layout SQLiteNullTime cutoff) must seek
// idx_*_revoked_at with an UPPER limit, touching only the cutoff-eligible
// rows. The prior `datetime(revoked_at) < datetime(?)` shape wrapped the
// indexed column in a function, so the planner emitted a lower-limit-only
// SEARCH (`revoked_at>?` -- the partial index's IS NOT NULL floor) and
// evaluated datetime() per revoked row on every sweep; the regression is
// plan-level only (deleted rows stay identical), so it needs an EXPLAIN
// assertion.
func TestRevokedTokenSweepsAreSargable(t *testing.T) {
	_, db := newSessionTestStore(t)
	for _, tc := range []struct{ table, index string }{
		{"api_tokens", "idx_api_tokens_revoked_at"},
		{"delegation_tokens", "idx_delegation_tokens_revoked_at"},
	} {
		rows, err := db.QueryContext(context.Background(),
			`EXPLAIN QUERY PLAN DELETE FROM `+tc.table+` WHERE revoked_at IS NOT NULL AND revoked_at < ?`,
			"2026-01-01T00:00:00.000Z")
		require.NoError(t, err)
		var details []string
		for rows.Next() {
			var id, parent, notUsed int
			var detail string
			require.NoError(t, rows.Scan(&id, &parent, &notUsed, &detail))
			details = append(details, detail)
		}
		require.NoError(t, rows.Close())
		require.NoError(t, rows.Err())
		require.Len(t, details, 1, "%s sweep plan: %v", tc.table, details)
		// "USING INDEX" or "USING COVERING INDEX", depending on planner choice.
		assert.Contains(t, details[0], "INDEX "+tc.index,
			"%s sweep must seek its partial revoked_at index", tc.table)
		assert.Contains(t, details[0], "revoked_at<",
			"%s sweep must carry the UPPER limit into the index seek (the datetime() wrap loses it and scans every revoked row)", tc.table)
	}
}

// TestWebAuthnSessionsUserKindIndexIsPartialUnique pins the index that makes
// two concurrent Begin* calls fail at insert instead of leaving two encrypted
// rows. persistSession is Delete then Create; without UNIQUE the race is
// invisible to every sequential storetest. The WHERE excludes signup (NULL
// user_id) so concurrent anonymous ceremonies can coexist.
func TestWebAuthnSessionsUserKindIndexIsPartialUnique(t *testing.T) {
	_, db := newSessionTestStore(t)
	sqlText := indexSQL(t, db, "webauthn_sessions", "idx_webauthn_sessions_user_kind")
	upper := strings.ToUpper(sqlText)
	assert.Contains(t, upper, "UNIQUE",
		"idx_webauthn_sessions_user_kind must be UNIQUE so two concurrent Begins cannot leave two rows of the same (user_id, kind)")
	assert.Contains(t, upper, "(USER_ID, KIND)")
	assert.Contains(t, upper, "WHERE USER_ID IS NOT NULL",
		"the unique index must exclude signup rows (NULL user_id); a non-partial UNIQUE still allows multiple NULLs on SQLite but hides the intent")
}

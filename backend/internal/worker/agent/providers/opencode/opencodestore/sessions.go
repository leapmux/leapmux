// Package opencodestore reads the session store that OpenCode, Kilo, ZCode and
// MiMo Code share.
//
// OpenCode keeps its sessions in one SQLite database, in a `session` table
// whose columns this package reads. Kilo is a fork of OpenCode, ZCode is a
// derivative of it, and MiMo Code is a fork of it, and all four ship the same
// table: `id`, `parent_id`, `directory`, `title`, `time_updated`,
// `time_archived`. Verified against all four live databases -- the columns are
// identical, and ZCode's own `task_type` discriminator agrees exactly with
// `parent_id IS NULL` (every `subagent_child` row has a parent), so the shared
// query needs no column that only one of them has.
//
// Hence ONE reader with four callers, each supplying its own database path
// from its own package. The alternative -- four near-identical queries -- would
// drift the moment one of them renames a column. A fork that marks some of its
// sessions in a table of its own states that table as an Exclusion, and the
// shared query stays the one query.
package opencodestore

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// openCodeFamilySessionsSelect selects the resumable top-level sessions of one
// working directory. The exclusions, when a caller states any, follow the WHERE
// clause and precede openCodeFamilySessionsOrder.
//
// `parent_id IS NULL` drops subagent sessions, which outnumber real ones by an
// order of magnitude in a live store (1524 to 138 in the ZCode database this
// was written against), so a picker without the filter shows almost nothing a
// user recognises. `time_archived IS NULL` drops what the user already put
// away. `directory` is matched exactly; see sessionstore.Query, which states
// why the comparison stays in SQL.
//
// NULLIF around each timestamp, not COALESCE alone: COALESCE skips a NULL and
// keeps a stored 0, and a 0 here means the same thing a NULL does -- no
// recorded time -- so without it a row that carries one would sort to the epoch
// instead of falling back to when it was created.
const openCodeFamilySessionsSelect = `
SELECT id,
       COALESCE(title, ''),
       COALESCE(NULLIF(time_updated, 0), NULLIF(time_created, 0), 0)
FROM session
WHERE directory = ?
  AND parent_id IS NULL
  AND time_archived IS NULL`

// openCodeFamilySessionsOrder orders and limits the selected sessions.
const openCodeFamilySessionsOrder = `
ORDER BY 3 DESC
LIMIT ?`

// ListSessions reads the sessions one OpenCode-schema database holds
// for the query's working directory.
func ListSessions(ctx context.Context, dbPath string, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return sessionstore.Query(ctx, dbPath, familySessionsSQL(nil), q, sessionstore.ScanEpochMillisSession)
}

// Exclusion drops every session whose id one column of one table lists. A fork
// states one for the sessions it marks in a table of its own, such as the
// sessions MiMo Code imported from another program.
//
// Table and Column are identifiers the caller owns. They reach the SQL text
// unquoted, because SQLite binds no identifier, so each must be a plain name;
// familySessionsSQL panics on anything else. No input can reach them.
type Exclusion struct {
	Table  string
	Column string
}

// sqlIdentifier matches the names an Exclusion may carry.
var sqlIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ListSessionsExcept reads the same sessions as ListSessions, minus every
// session that an exclusion lists.
//
// An exclusion whose table or column the store does not hold excludes nothing.
// A release that predates the table never wrote a row to it, so it has nothing
// to exclude, and a query that specified the absent table would fail the
// whole listing instead.
func ListSessionsExcept(ctx context.Context, dbPath string, q agent.StoredSessionQuery, exclusions ...Exclusion) ([]agent.StoredSession, error) {
	if strings.TrimSpace(q.WorkingDir) == "" {
		return nil, nil
	}
	present, err := presentExclusions(ctx, dbPath, exclusions)
	if err != nil {
		if errors.Is(err, sessionstore.ErrAbsent) {
			return nil, nil
		}
		return nil, err
	}
	return sessionstore.Query(ctx, dbPath, familySessionsSQL(present), q, sessionstore.ScanEpochMillisSession)
}

// familySessionsSQL builds the listing query with one predicate per exclusion.
//
// Each subquery drops a NULL id. `id NOT IN (...)` is NULL rather than true for
// EVERY row when the list holds one NULL, so a single import row that recorded
// no session would otherwise empty the whole listing.
func familySessionsSQL(exclusions []Exclusion) string {
	var query strings.Builder
	query.WriteString(openCodeFamilySessionsSelect)
	for _, exclusion := range exclusions {
		mustBeIdentifier(exclusion.Table)
		mustBeIdentifier(exclusion.Column)
		fmt.Fprintf(&query, "\n  AND id NOT IN (SELECT %[2]s FROM %[1]s WHERE %[2]s IS NOT NULL)", exclusion.Table, exclusion.Column)
	}
	query.WriteString(openCodeFamilySessionsOrder)
	return query.String()
}

func mustBeIdentifier(name string) {
	if !sqlIdentifier.MatchString(name) {
		panic(fmt.Sprintf("opencodestore: %q is not a plain SQL identifier", name))
	}
}

// presentExclusions returns the exclusions whose table and column the store
// holds, in the order the caller gave them.
func presentExclusions(ctx context.Context, dbPath string, exclusions []Exclusion) ([]Exclusion, error) {
	if len(exclusions) == 0 {
		return nil, nil
	}
	db, err := sessionstore.OpenDB(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	present := make([]Exclusion, 0, len(exclusions))
	for _, exclusion := range exclusions {
		mustBeIdentifier(exclusion.Table)
		mustBeIdentifier(exclusion.Column)
		// pragma_table_info is a table-valued function, so the table name binds as
		// a parameter. An absent table yields no row, not an error.
		var found int
		err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`,
			exclusion.Table, exclusion.Column).Scan(&found)
		if err != nil {
			return nil, fmt.Errorf("inspect session store table %s: %w", exclusion.Table, err)
		}
		if found > 0 {
			present = append(present, exclusion)
		}
	}
	return present, nil
}

package mysql

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store/storetest"
)

// TestEveryCreateTableDeclaresBinaryCollation pins the migration convention that
// every MySQL CREATE TABLE must carry COLLATE=utf8mb4_bin so id and FK columns
// collate byte-wise (case-sensitive), matching SQLite's BINARY default and
// keeping the composite cursor's `id < ?` tiebreak deterministic for mixed-case
// ids. The per-query BINARY casts were dropped from workspaces.sql and
// workspace_section_items.sql in the keyset-pagination rebuild, so the
// table-level collation is now the sole guarantee -- a table added without the
// suffix silently inherits the database default (typically case-insensitive) and
// the cursor id tiebreak nondeterministically re-returns or skips rows. Closes
// https://github.com/leapmux/leapmux/issues/300.
//
// This is a static check of the migration source, so it runs without Docker.
// Its live twin, TestMySQLBinaryCollationLive (mysql_test.go, -tags
// integration), asserts the same invariant against the actual migrated schema
// and is migration-count-agnostic.
func TestEveryCreateTableDeclaresBinaryCollation(t *testing.T) {
	sqlBytes, err := os.ReadFile("db/migrations/00001_initial.sql")
	require.NoError(t, err)
	// Strip line comments (-- to end of line) so a ';' inside a prose comment
	// (e.g. "...issued; force-expires...") doesn't split a CREATE TABLE body.
	sql := strings.ToUpper(storetest.StripSQLLineComments(string(sqlBytes)))

	const create = "CREATE TABLE"
	idx := 0
	count := 0
	for {
		i := strings.Index(sql[idx:], create)
		if i < 0 {
			break
		}
		i += idx
		semi := strings.Index(sql[i:], ";")
		require.GreaterOrEqual(t, semi, 0, "CREATE TABLE at offset %d missing terminating ;", i)
		stmt := sql[i : i+semi]
		assert.Contains(t, stmt, "COLLATE=UTF8MB4_BIN",
			"CREATE TABLE at offset %d must declare COLLATE=utf8mb4_bin so id/FK columns collate byte-wise; the dropped per-query BINARY casts rely on it", i)
		idx = i + semi
		count++
	}
	require.GreaterOrEqual(t, count, 1, "found no CREATE TABLE statements in the migration")
	// Sanity: the migration is non-trivial -- a healthy schema has many tables.
	assert.Greater(t, count, 20, "expected many CREATE TABLE statements; got %d (is the migration readable?)", count)
}

// TestEveryTimestampColumnDeclaresDatetime pins the decltype spelling the sqlc
// type-keyed override (sqlc.yaml `db_type: "datetime"`) matches: every `_at`
// column must be declared DATETIME(n) with n >= 3 so sqlc retypes its params
// and result fields to the ms-flooring sqltime.MySQLTime/MySQLNullTime valuers
// AND the column can actually hold the floored millisecond -- MySQL ROUNDS a
// fractional second that exceeds the column precision, so a DATETIME(0..2)
// column could store an instant AFTER the ms-floored bound, violating the
// "stored instant never postdates the bound" invariant while still matching
// the db_type override. sqlc silently ignores an override that matches
// nothing, so a column declared with another time type (e.g. TIMESTAMP) would
// fall through to a raw, unfloored time.Time bind with no red flag anywhere --
// this scan turns that drift into a static test failure at migration time.
// TIMESTAMP is also rejected outright for any column: besides evading the
// override, it carries session-timezone conversion semantics DATETIME
// deliberately avoids.
//
// This is a static check of the migration source, so it runs without Docker.
// Its live twin, TestMySQLDatetimeColumnsLive (mysql_test.go, -tags
// integration), asserts the same invariant against information_schema of the
// actual migrated database and is immune to the text parse's line-shape
// assumptions (see storetest.WalkCreateTableColumns).
func TestEveryTimestampColumnDeclaresDatetime(t *testing.T) {
	sqlBytes, err := os.ReadFile("db/migrations/00001_initial.sql")
	require.NoError(t, err)

	atColumns := 0
	storetest.WalkCreateTableColumns(string(sqlBytes), func(name, typeTok string) {
		if strings.HasSuffix(name, "_at") {
			atColumns++
			assert.Regexp(t, `^DATETIME\([3-6]\)$`, typeTok,
				"column %s is declared %s; `_at` columns must be DATETIME(3..6) so the sqlc db_type override retypes them to the flooring valuer and the stored precision can hold the floored millisecond (below 3, MySQL ROUNDS past the floor)", name, typeTok)
		} else {
			assert.False(t, strings.HasPrefix(typeTok, "DATETIME") || strings.HasPrefix(typeTok, "TIMESTAMP"),
				"column %s is declared %s; time columns must be named *_at (and TIMESTAMP is banned outright)", name, typeTok)
		}
	})
	// Sanity: the scan actually saw the schema's many timestamp columns.
	assert.Greater(t, atColumns, 20, "expected many _at columns; got %d (is the migration readable?)", atColumns)
}

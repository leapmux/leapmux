package mysql

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	sql := strings.ToUpper(stripLineComments(string(sqlBytes)))

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

// stripLineComments removes a SQL line comment (-- to end of line) from each
// line. The migration uses only line comments (no /* */ block comments, no
// string literals containing --), so this is a faithful comment strip for the
// static COLLATE scan.
func stripLineComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

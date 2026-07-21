package sqlite

import (
	"context"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTruncateTableOrderCoversEverySchemaTable pins
// sqlutil.SQLTruncateTableOrder against the live schema: every application
// table must appear in the list exactly once, or TestHelper.TruncateAll
// silently skips it and rows leak across suite subtests (device_authorizations
// was missing and leaked expired grants into unrelated sweep tests,
// intermittently, because the leak only bit once the leftover rows' short
// expiries lapsed). All dialects share one schema shape, so pinning against
// SQLite covers them all.
func TestTruncateTableOrderCoversEverySchemaTable(t *testing.T) {
	testable, err := OpenTestable(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testable.Close()) })
	db := testable.(*testableSQLiteStore).conn.shared.db

	rows, err := db.QueryContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name != 'goose_db_version' ORDER BY name`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var schemaTables []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		schemaTables = append(schemaTables, name)
	}
	require.NoError(t, rows.Err())
	require.NotEmpty(t, schemaTables)

	assert.ElementsMatch(t, schemaTables, sqlutil.SQLTruncateTableOrder,
		"SQLTruncateTableOrder must list every application table exactly once (FK-ordered, children before parents)")
}

// TestTruncateTableOrderRespectsForeignKeys pins the ORDER of
// sqlutil.SQLTruncateTableOrder, not just its membership: every FK child must
// precede its parent. The order is load-bearing for Postgres-family dialects,
// whose TruncateAll issues plain DELETEs in list order with FK constraints
// enforced (sqlite and mysql disable FK checks during truncation), so a bad
// reorder would otherwise surface only in the gated -tags integration suites.
// FK edges are discovered from the live schema (pragma_foreign_key_list), so
// a new FK can never be forgotten here.
func TestTruncateTableOrderRespectsForeignKeys(t *testing.T) {
	testable, err := OpenTestable(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testable.Close()) })
	db := testable.(*testableSQLiteStore).conn.shared.db

	pos := make(map[string]int, len(sqlutil.SQLTruncateTableOrder))
	for i, name := range sqlutil.SQLTruncateTableOrder {
		pos[name] = i
	}

	rows, err := db.QueryContext(context.Background(), `
		SELECT m.name, fk."table"
		FROM sqlite_master m, pragma_foreign_key_list(m.name) fk
		WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%' AND m.name != 'goose_db_version'`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	edges := 0
	for rows.Next() {
		var child, parent string
		require.NoError(t, rows.Scan(&child, &parent))
		if child == parent {
			// A self-reference imposes no cross-table delete ordering.
			continue
		}
		edges++
		childPos, ok := pos[child]
		require.True(t, ok, "FK child table %q missing from SQLTruncateTableOrder", child)
		parentPos, ok := pos[parent]
		require.True(t, ok, "FK parent table %q missing from SQLTruncateTableOrder", parent)
		assert.Less(t, childPos, parentPos,
			"SQLTruncateTableOrder must delete FK child %q before its parent %q -- Postgres TruncateAll deletes in list order with constraints enforced", child, parent)
	}
	require.NoError(t, rows.Err())
	require.NotZero(t, edges, "schema FK discovery returned no edges -- the pragma_foreign_key_list join is broken")
}

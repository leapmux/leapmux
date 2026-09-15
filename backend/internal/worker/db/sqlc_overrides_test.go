package db_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sqlcColumnOverride matches one `- column: "table.column"` entry of a sqlc
// configuration. sqlc validates only the SHAPE of that value.
var sqlcColumnOverride = regexp.MustCompile(`(?m)^\s*-\s*column:\s*"([^"]+)"`)

// TestEveryWorkerSqlcOverrideIdentifiesALiveColumn refuses a column-keyed sqlc
// override that resolves to nothing in the worker's schema.
//
// sqlc ignores such an override in SILENCE. It walks the columns that exist and
// asks each override whether it matches, so one that matches nothing retypes
// nothing and reports nothing -- the column keeps its raw integer type and every
// call site goes on hand-casting, which is what the project's "enum columns store
// proto enum ordinals" rule exists to remove. A dead entry in the HUB's config did
// exactly that to the two TabType columns.
//
// The worker resolves against the LIVE schema rather than the migration text,
// which its hub sibling (storetest.TestEverySqlcColumnOverrideIdentifiesALiveColumn)
// cannot: three of the overrides here identify a column of the VIEW tab_locations,
// and a CREATE TABLE scan finds no view at all. pragma_table_info answers for a view
// as readily as for a table, and it costs one in-memory migration the package
// already runs for its partial-index guard. Postgres and MySQL have no such option
// without Docker, which is why the hub half stays textual.
func TestEveryWorkerSqlcOverrideIdentifiesALiveColumn(t *testing.T) {
	t.Parallel()

	config, err := os.ReadFile(filepath.Join("..", "sqlc.yaml"))
	require.NoError(t, err)
	overrides := sqlcColumnOverride.FindAllStringSubmatch(string(config), -1)
	require.NotEmpty(t, overrides, "worker/sqlc.yaml carries column overrides; a scan that finds none is broken")

	connection, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, connection.Close()) })
	require.NoError(t, workerdb.Migrate(t.Context(), connection))

	for _, match := range overrides {
		reference := strings.ToLower(match[1])
		// `[catalog.][schema.]table.column`, so the COLUMN is the last part and
		// the relation is the one before it.
		dot := strings.LastIndexByte(reference, '.')
		require.Positivef(t, dot, "override %q must be table.column", reference)
		require.NotContainsf(t, reference, "*", "override %q uses a sqlc glob, which this scan cannot resolve", reference)
		require.NotContainsf(t, reference, "?", "override %q uses a sqlc glob, which this scan cannot resolve", reference)
		relation, column := reference[:dot], reference[dot+1:]
		if catalog := strings.LastIndexByte(relation, '.'); catalog >= 0 {
			relation = relation[catalog+1:]
		}

		var live int
		require.NoError(t, connection.QueryRowContext(t.Context(),
			`SELECT count(*) FROM pragma_table_info(?) WHERE lower(name) = ?`, relation, column).Scan(&live))
		assert.Equalf(t, 1, live,
			"sqlc override %q identifies no column the worker schema declares; "+
				"sqlc ignores it in silence, so the column keeps its raw integer type", reference)
	}
}

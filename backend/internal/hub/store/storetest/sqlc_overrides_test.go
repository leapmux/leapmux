package storetest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sqlcColumnOverride matches one `- column: "table.column"` entry of a sqlc
// configuration. sqlc validates only the SHAPE of that value, so BOTH halves are
// what this test checks against the schema.
var sqlcColumnOverride = regexp.MustCompile(`(?m)^\s*-\s*column:\s*"([^"]+)"`)

// TestEverySqlcColumnOverrideIdentifiesALiveColumn refuses a column-keyed sqlc
// override whose TABLE the migration does not declare.
//
// sqlc ignores such an override in SILENCE. It validates the format of the
// value and then walks the columns that exist, asking each override whether it
// matches; one that identifies no column matches nothing, and no diagnostic says so.
// The column it was meant to retype therefore keeps its raw integer type, and
// every call site goes on hand-casting in both directions -- which is exactly
// what the project's "enum columns store proto enum ordinals" rule exists to
// remove.
//
// BOTH halves, because sqlc is equally silent about either. The match runs over
// the columns that EXIST and asks each override whether it matches, so a live
// table with a renamed column misses in exactly the same way as a table that is
// gone -- and a check of the table half alone would pass the whole time.
//
// This is not hypothetical. An override specified `workspace_tabs.tab_type` after
// that table was removed, so the two columns that really hold a TabType stayed
// int64 and int32 while the entry looked like it covered them.
//
// The scan is deliberately textual, like its sibling
// TestEveryTimestampColumnDeclaresTimestamptz: it needs no database and it runs
// in the default suite, so a wrong override fails at migration time rather than
// after somebody notices a cast that will not go away.
func TestEverySqlcColumnOverrideIdentifiesALiveColumn(t *testing.T) {
	t.Parallel()

	for _, dialect := range hubDialects {
		t.Run(dialect, func(t *testing.T) {
			t.Parallel()

			config, err := os.ReadFile(filepath.Join("..", dialect, "sqlc.yaml"))
			require.NoError(t, err)
			schema, err := os.ReadFile(filepath.Join("..", dialect, "db", "migrations", "00001_initial.sql"))
			require.NoError(t, err)

			columns := map[string]struct{}{}
			WalkCreateTableColumns(string(schema), func(table, column, _ string) {
				columns[table+"."+column] = struct{}{}
			})
			require.NotEmpty(t, columns, "the migration declares columns; a scan that finds none is broken")

			overrides := sqlcColumnOverride.FindAllStringSubmatch(string(config), -1)
			require.NotEmpty(t, overrides, "sqlc.yaml carries column overrides; a scan that finds none is broken")
			for _, match := range overrides {
				reference := strings.ToLower(match[1])
				// `[catalog.][schema.]table.column`, so the COLUMN is the last part
				// and the table is the one before it. Cutting at the FIRST dot took
				// the catalog for the table on a three- or four-part value.
				dot := strings.LastIndexByte(reference, '.')
				require.Positivef(t, dot, "override %q must be table.column", reference)
				require.NotContainsf(t, reference, "*", "override %q uses a sqlc glob, which this scan cannot resolve", reference)
				require.NotContainsf(t, reference, "?", "override %q uses a sqlc glob, which this scan cannot resolve", reference)
				table := reference[:dot]
				if catalog := strings.LastIndexByte(table, '.'); catalog >= 0 {
					table = table[catalog+1:]
				}
				_, live := columns[table+"."+reference[dot+1:]]
				assert.Truef(t, live,
					"sqlc override %q identifies no column that %s/db/migrations/00001_initial.sql declares; "+
						"sqlc ignores it in silence, so the column keeps its raw integer type",
					reference, dialect)
			}
		})
	}
}

// hubDialects are the three sqlc configurations that describe the same schema.
var hubDialects = []string{"sqlite", "postgres", "mysql"}

// TestEveryDialectOverridesTheSameEnumColumns refuses an enum override added to one
// dialect and forgotten in the others.
//
// sqlc has no include or inherit mechanism and YAML anchors do not cross files, so
// each dialect's overrides are hand-copied. The guard above catches an override that
// resolves to nothing; nothing catches one that is simply MISSING, and a missing one
// is silent in the same way -- the column keeps its raw integer type, on that dialect
// alone, and the hand-casting the "enum columns store proto enum ordinals" rule exists
// to remove comes back for whoever runs Postgres.
//
// MySQL carries extra overrides of its own (hash and blob column types its driver
// needs), so this compares the enum entries the three share rather than the whole set:
// an override is shared when at least one OTHER dialect declares it too.
func TestEveryDialectOverridesTheSameEnumColumns(t *testing.T) {
	t.Parallel()

	perDialect := map[string]map[string]struct{}{}
	for _, dialect := range hubDialects {
		config, err := os.ReadFile(filepath.Join("..", dialect, "sqlc.yaml"))
		require.NoError(t, err)
		refs := map[string]struct{}{}
		for _, match := range sqlcColumnOverride.FindAllStringSubmatch(string(config), -1) {
			refs[strings.ToLower(match[1])] = struct{}{}
		}
		require.NotEmptyf(t, refs, "%s/sqlc.yaml carries column overrides; a scan that finds none is broken", dialect)
		perDialect[dialect] = refs
	}

	declaredBy := map[string][]string{}
	for _, dialect := range hubDialects {
		for reference := range perDialect[dialect] {
			declaredBy[reference] = append(declaredBy[reference], dialect)
		}
	}
	for reference, dialects := range declaredBy {
		if len(dialects) < 2 {
			// One dialect alone: a driver-specific override, not a shared enum.
			continue
		}
		for _, dialect := range hubDialects {
			_, has := perDialect[dialect][reference]
			assert.Truef(t, has,
				"%d of the %d hub dialects override %q and %s does not; sqlc ignores the omission "+
					"in silence, so that column keeps its raw integer type on %s alone",
				len(dialects), len(hubDialects), reference, dialect, dialect)
		}
	}
}

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
// configuration. sqlc validates only the SHAPE of that value, so the table half
// is what this test checks against the schema.
var sqlcColumnOverride = regexp.MustCompile(`(?m)^\s*-\s*column:\s*"([^"]+)"`)

// sqlcCreateTable matches the table name of a CREATE TABLE statement, in the
// two spellings the migrations use.
var sqlcCreateTable = regexp.MustCompile(`(?mi)^\s*CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?` + "`?" + `"?([A-Za-z_][A-Za-z0-9_]*)`)

// TestEverySqlcColumnOverrideNamesALiveTable refuses a column-keyed sqlc
// override whose TABLE the migration does not declare.
//
// sqlc ignores such an override in SILENCE. It validates the format of the
// value and then walks the columns that exist, asking each override whether it
// matches; one that names no table matches nothing, and no diagnostic says so.
// The column it was meant to retype therefore keeps its raw integer type, and
// every call site goes on hand-casting in both directions -- which is exactly
// what the project's "enum columns store proto enum ordinals" rule exists to
// remove.
//
// This is not hypothetical. An override named `workspace_tabs.tab_type` after
// that table was removed, so the two columns that really hold a TabType stayed
// int64 and int32 while the entry looked like it covered them.
//
// The scan is deliberately textual, like its sibling
// TestEveryTimestampColumnDeclaresTimestamptz: it needs no database and it runs
// in the default suite, so a wrong override fails at migration time rather than
// after somebody notices a cast that will not go away.
func TestEverySqlcColumnOverrideNamesALiveTable(t *testing.T) {
	t.Parallel()

	for _, dialect := range []string{"sqlite", "postgres", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			t.Parallel()

			config, err := os.ReadFile(filepath.Join("..", dialect, "sqlc.yaml"))
			require.NoError(t, err)
			schema, err := os.ReadFile(filepath.Join("..", dialect, "db", "migrations", "00001_initial.sql"))
			require.NoError(t, err)

			tables := map[string]struct{}{}
			for _, match := range sqlcCreateTable.FindAllStringSubmatch(string(schema), -1) {
				tables[strings.ToLower(match[1])] = struct{}{}
			}
			require.NotEmpty(t, tables, "the migration declares tables; a scan that finds none is broken")

			overrides := sqlcColumnOverride.FindAllStringSubmatch(string(config), -1)
			require.NotEmpty(t, overrides, "sqlc.yaml carries column overrides; a scan that finds none is broken")
			for _, match := range overrides {
				reference := match[1]
				table, _, found := strings.Cut(reference, ".")
				require.Truef(t, found, "override %q must be table.column", reference)
				_, live := tables[strings.ToLower(table)]
				assert.Truef(t, live,
					"sqlc override %q names table %q, which %s/db/migrations/00001_initial.sql does not declare; "+
						"sqlc ignores it in silence, so the column keeps its raw integer type",
					reference, table, dialect)
			}
		})
	}
}

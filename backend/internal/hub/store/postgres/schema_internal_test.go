package postgres

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store/storetest"
)

// TestEveryTimestampColumnDeclaresTimestamptz pins the decltype spelling the
// sqlc type-keyed override (sqlc.yaml `db_type: "timestamptz"`) matches: every
// `_at` column must be declared exactly TIMESTAMPTZ so sqlc retypes its params
// and result fields to the microsecond-flooring pgtime.Time/pgtime.NullTime
// valuers. The exact-equality assertion also rejects a precision-qualified
// TIMESTAMPTZ(p) spelling: below the default p=6 Postgres ROUNDS a fractional
// second that exceeds the column precision, so a lower-precision column could
// store an instant AFTER the microsecond-floored bound. sqlc silently ignores
// an override that matches nothing, so a column declared with another time
// type (e.g. bare TIMESTAMP, which is WITHOUT TIME ZONE) would fall through to
// a raw, unfloored pgtype.Timestamptz/time.Time with no red flag anywhere --
// this scan turns that drift into a static test failure at migration time.
// Bare TIMESTAMP is also rejected outright for any column: besides evading the
// override, a timezone-naive column silently reinterprets instants across
// session timezones.
//
// This is a static check of every embedded migration file (migrate.go's
// //go:embed db/migrations/*.sql), so it runs without Docker: a future
// migration with a wrong decltype fails the default suite as long as its column
// is declared in the conventional one-per-line form the text scan recognizes.
// Its live twin, TestPostgresTimestamptzColumnsLive (postgres_test.go, -tags
// integration), asserts the same invariant against information_schema of the
// actual migrated database and is the authoritative guarantee for the
// line-shape parse blind spots the source scan cannot see (see
// storetest.WalkCreateTableColumns).
func TestEveryTimestampColumnDeclaresTimestamptz(t *testing.T) {
	files, err := storetest.MigrationFiles(migrations)
	require.NoError(t, err)

	atColumns := 0
	for _, f := range files {
		storetest.WalkCreateTableColumns(f.SQL, func(name, typeTok string) {
			if strings.HasSuffix(name, "_at") {
				atColumns++
				assert.Equal(t, "TIMESTAMPTZ", typeTok,
					"%s: column %s is declared %s; `_at` columns must be exactly TIMESTAMPTZ (default microsecond precision) so the sqlc db_type override retypes them to the flooring valuer and no lower precision can round past the floor", f.Name, name, typeTok)
			} else {
				assert.False(t, strings.HasPrefix(typeTok, "TIMESTAMP"),
					"%s: column %s is declared %s; time columns must be named *_at (and timezone-naive TIMESTAMP is banned outright)", f.Name, name, typeTok)
			}
		})
	}
	// Sanity: the scan actually saw the schema's many timestamp columns.
	assert.Greater(t, atColumns, 20, "expected many _at columns; got %d (are the migrations readable?)", atColumns)
}

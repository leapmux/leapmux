package sqlitedb

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The higher-level canonical-storage tests (hub sqlite store, worker service)
// only ever assert the EMPTY offender case, which would pass vacuously if the
// probe stopped matching anything. The tests here pin the detection itself:
// a known-bad value must be reported, with the right count and sample.

const canonicalValue = "2025-01-02T12:00:00.000Z"

// driverLayoutValue is what a raw time.Time bind stores through modernc with
// _time_format=sqlite: space separator, zone offset suffix -- the exact
// corruption shape FindNonCanonicalDatetimes exists to catch.
const driverLayoutValue = "2025-01-02 21:00:00.000+09:00"

// secondPrecisionValue is canonical-shaped but truncated to whole seconds
// (20 chars), as datetime('now') would store: same instant grid drift, caught
// by the length probe rather than the separator probe.
const secondPrecisionValue = "2025-01-02T12:00:00Z"

func openCanonicalTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(":memory:", Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	// The lowercase `datetime` decltype on bravo.updated_at pins that
	// discovery is decltype-case-insensitive; junk in the TEXT and TIMESTAMP
	// columns pins that only DATETIME columns are value-walked. The
	// non-DATETIME columns deliberately avoid the `_at` suffix so they stay
	// clear of the timestamp-named decltype guard (pinned separately below).
	_, err = db.Exec(`
		CREATE TABLE alpha (
			id INTEGER PRIMARY KEY,
			created_at DATETIME,
			note TEXT
		);
		CREATE TABLE bravo (
			id INTEGER PRIMARY KEY,
			expires_at DATETIME,
			updated_at datetime,
			recorded TIMESTAMP
		);`)
	require.NoError(t, err)
	return db
}

func TestFindNonCanonicalDatetimes_CleanSchema(t *testing.T) {
	db := openCanonicalTestDB(t)

	_, err := db.Exec(`INSERT INTO alpha (created_at, note) VALUES (?, ?), (NULL, ?)`,
		canonicalValue, "not a timestamp at all", "also not one")
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO bravo (expires_at, updated_at, recorded) VALUES (?, ?, ?)`,
		canonicalValue, canonicalValue, driverLayoutValue)
	require.NoError(t, err)

	offenders, walked, err := FindNonCanonicalDatetimes(context.Background(), db)
	require.NoError(t, err)
	assert.Empty(t, offenders, "canonical values, NULLs, and non-DATETIME columns must not be reported")
	assert.Equal(t, 3, walked,
		"must walk exactly the DATETIME columns (alpha.created_at, bravo.expires_at, bravo.updated_at), regardless of decltype case")
}

func TestFindNonCanonicalDatetimes_ReportsEveryOffendingColumn(t *testing.T) {
	db := openCanonicalTestDB(t)

	// alpha.created_at: two distinct corruption shapes plus one clean row --
	// the count must include only the bad rows, the sample is the MIN value.
	_, err := db.Exec(`INSERT INTO alpha (created_at) VALUES (?), (?), (?)`,
		driverLayoutValue, secondPrecisionValue, canonicalValue)
	require.NoError(t, err)
	// bravo: one bad row in the lowercase-decltype column, clean elsewhere.
	_, err = db.Exec(`INSERT INTO bravo (expires_at, updated_at) VALUES (?, ?)`,
		canonicalValue, driverLayoutValue)
	require.NoError(t, err)

	offenders, walked, err := FindNonCanonicalDatetimes(context.Background(), db)
	require.NoError(t, err)
	assert.Equal(t, 3, walked)
	assert.Equal(t, []string{
		`alpha.created_at: 2 value(s), e.g. "` + driverLayoutValue + `"`,
		`bravo.updated_at: 1 value(s), e.g. "` + driverLayoutValue + `"`,
	}, offenders)
}

func TestFindNonCanonicalDatetimes_ReportsTimestampNamedColumnWithWrongDecltype(t *testing.T) {
	db := openCanonicalTestDB(t)

	// The guard fires on the declaration alone -- no rows needed. A column
	// named like a timestamp but not declared DATETIME sits outside the value
	// walk, which is exactly the drift the guard exists to make loud.
	_, err := db.Exec(`CREATE TABLE charlie (id INTEGER PRIMARY KEY, seen_at TEXT, logged_at TIMESTAMP)`)
	require.NoError(t, err)

	offenders, walked, err := FindNonCanonicalDatetimes(context.Background(), db)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"charlie.seen_at: declared TEXT, want DATETIME (timestamp-named column outside the canonical walk)",
		"charlie.logged_at: declared TIMESTAMP, want DATETIME (timestamp-named column outside the canonical walk)",
	}, offenders)
	assert.Equal(t, 3, walked, "the guard must not add non-DATETIME columns to the value walk")

	// Excluding the table silences the decltype guard along with the walk.
	offenders, _, err = FindNonCanonicalDatetimes(context.Background(), db, "charlie")
	require.NoError(t, err)
	assert.Empty(t, offenders)
}

func TestFindNonCanonicalDatetimes_ExcludeSkipsTable(t *testing.T) {
	db := openCanonicalTestDB(t)

	_, err := db.Exec(`INSERT INTO bravo (expires_at) VALUES (?)`, driverLayoutValue)
	require.NoError(t, err)

	offenders, walked, err := FindNonCanonicalDatetimes(context.Background(), db, "bravo")
	require.NoError(t, err)
	assert.Empty(t, offenders, "an excluded table's values must not be probed")
	assert.Equal(t, 1, walked, "excluded tables must not count toward the walked total")
}

func TestFindNonCanonicalDatetimes_EmptySchemaWalksNothing(t *testing.T) {
	db, err := Open(":memory:", Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	offenders, walked, err := FindNonCanonicalDatetimes(context.Background(), db)
	require.NoError(t, err)
	assert.Empty(t, offenders)
	assert.Zero(t, walked, "an empty schema must report walked == 0 so callers can judge vacuity")
}

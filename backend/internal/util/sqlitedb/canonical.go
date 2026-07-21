package sqlitedb

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// ColumnCoverage describes one DATETIME column discovered by
// FindNonCanonicalDatetimes: its (table, column) identity and how many
// non-null rows the value probe inspected. NonNullRows == 0 means the column's
// canonical-layout check passed vacuously -- there was no stored value to
// contradict it -- so a test that wants a real guarantee for every column must
// also assert coverage (see UncoveredColumns).
type ColumnCoverage struct {
	Table       string
	Column      string
	NonNullRows int64
}

// UncoveredColumns returns the "table.column" names of every discovered
// DATETIME column whose probe inspected zero non-null rows. The dedicated
// canonical-storage tests (hub store, worker service) assert this list is
// empty so no column's canonical-layout contract ships exercised only
// vacuously; the per-subtest CheckCanonicalTimestamps walk deliberately does
// NOT (no single subtest writes every table).
func UncoveredColumns(columns []ColumnCoverage) []string {
	var uncovered []string
	for _, c := range columns {
		if c.NonNullRows == 0 {
			uncovered = append(uncovered, c.Table+"."+c.Column)
		}
	}
	return uncovered
}

// FindNonCanonicalDatetimes walks every DATETIME column of every table in the
// connected SQLite database (excluding sqlite_* internals and the tables named
// in exclude, typically goose's version table) and reports stored values that
// deviate from the canonical 24-char strftime('%Y-%m-%dT%H:%M:%fZ') layout
// (timefmt.ISO8601). Columns are discovered mechanically from the live schema
// (sqlite_master x pragma_table_info), so a new table or column can never be
// forgotten the way a hand-curated allowlist could. The probe runs SQL-side
// (length/substr over the stored TEXT) because modernc reformats DATETIME
// values on scan, which would hide a non-canonical layout -- a single raw
// time.Time bind stores the driver's own layout (space at byte 11, offset
// suffix) and silently corrupts raw-string compares (keyset ORDER BY columns,
// liveness filters, cleanup cutoffs).
//
// It returns one human-readable line per offending column plus one
// ColumnCoverage per discovered (table, column) pair; len(columns) == 0 means
// the schema holds no DATETIME columns (e.g. a migrator test rolled it back),
// which callers should judge for themselves. Each ColumnCoverage carries the
// count of non-null rows the value probe inspected, so callers can also detect
// a column that passed only vacuously (see UncoveredColumns).
//
// The walk's coverage is keyed on the DATETIME decltype, so a timestamp column
// declared with any other type would silently escape it. To make that drift
// loud instead of silent, the walk also reports every timestamp-named column
// (name ending in `_at`) whose decltype is not DATETIME as an offender: a
// future migration must either declare the column DATETIME (bringing it under
// the walk) or exclude its table deliberately.
func FindNonCanonicalDatetimes(ctx context.Context, db *sql.DB, exclude ...string) (offenders []string, columns []ColumnCoverage, err error) {
	columns, err = discoverDatetimeColumns(ctx, db, exclude)
	if err != nil {
		return nil, nil, err
	}
	mistyped, err := findMistypedTimestampColumns(ctx, db, exclude)
	if err != nil {
		return nil, nil, err
	}
	offenders = append(offenders, mistyped...)
	if len(columns) > 0 {
		valueOffenders, err := probeColumnValues(ctx, db, columns)
		if err != nil {
			return nil, nil, err
		}
		offenders = append(offenders, valueOffenders...)
	}
	return offenders, columns, nil
}

// discoverDatetimeColumns lists every DATETIME-decltyped (table, column) pair
// in the live schema, minus sqlite_* internals and the excluded tables.
// UPPER: SQLite decltypes are case-insensitive, so a migration that declares
// a column as lowercase `datetime` must still be walked.
func discoverDatetimeColumns(ctx context.Context, db *sql.DB, exclude []string) ([]ColumnCoverage, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT m.name, ti.name
		FROM sqlite_master m, pragma_table_info(m.name) ti
		WHERE m.type = 'table'
		  AND m.name NOT LIKE 'sqlite_%'
		  AND UPPER(ti.type) = 'DATETIME'
		ORDER BY m.name, ti.cid`)
	if err != nil {
		return nil, fmt.Errorf("canonical-timestamp column discovery: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var columns []ColumnCoverage
	for rows.Next() {
		var tc ColumnCoverage
		if err := rows.Scan(&tc.Table, &tc.Column); err != nil {
			return nil, fmt.Errorf("canonical-timestamp column discovery: %w", err)
		}
		if slices.Contains(exclude, tc.Table) {
			continue
		}
		columns = append(columns, tc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("canonical-timestamp column discovery: %w", err)
	}
	return columns, nil
}

// findMistypedTimestampColumns is the schema-shape guard: a timestamp-named
// column that is not DATETIME sits outside the value walk, so it reports the
// declaration itself as an offender.
func findMistypedTimestampColumns(ctx context.Context, db *sql.DB, exclude []string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT m.name, ti.name, ti.type
		FROM sqlite_master m, pragma_table_info(m.name) ti
		WHERE m.type = 'table'
		  AND m.name NOT LIKE 'sqlite_%'
		  AND ti.name LIKE '%\_at' ESCAPE '\'
		  AND UPPER(ti.type) != 'DATETIME'
		ORDER BY m.name, ti.cid`)
	if err != nil {
		return nil, fmt.Errorf("canonical-timestamp decltype guard: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var offenders []string
	for rows.Next() {
		var table, col, decltype string
		if err := rows.Scan(&table, &col, &decltype); err != nil {
			return nil, fmt.Errorf("canonical-timestamp decltype guard: %w", err)
		}
		if slices.Contains(exclude, table) {
			continue
		}
		offenders = append(offenders,
			fmt.Sprintf("%s.%s: declared %s, want DATETIME (timestamp-named column outside the canonical walk)", table, col, decltype))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("canonical-timestamp decltype guard: %w", err)
	}
	return offenders, nil
}

// probeColumnValues runs the compound value probe over the discovered columns,
// fills each ColumnCoverage's NonNullRows in place (columns is a slice, so the
// caller's elements are updated), and returns one offender line per column
// holding non-canonical values.
//
// One compound probe instead of a round trip per column: the walk runs after
// every storetest subtest, so ~58 sequential QueryRow calls per invocation add
// up across a suite. Table and column names come from the live schema, never
// caller input. SQLite caps compound SELECTs at 500 arms by default; a schema
// anywhere near that many DATETIME columns fails this query loudly, not
// silently.
func probeColumnValues(ctx context.Context, db *sql.DB, columns []ColumnCoverage) ([]string, error) {
	var probe strings.Builder
	for i, tc := range columns {
		if i > 0 {
			probe.WriteString(" UNION ALL ")
		}
		// The offending-value condition moves into FILTER clauses so the
		// same single-pass aggregate can also report COUNT(col) -- the
		// total non-null rows inspected -- which feeds ColumnCoverage.
		// A NULL value never matches the filter (length(NULL) is NULL),
		// so the IS NOT NULL guard is kept only for clarity.
		text := fmt.Sprintf("CAST(%s AS TEXT)", tc.Column)
		bad := fmt.Sprintf("%s IS NOT NULL AND (length(%s) != 24 OR substr(%s, 11, 1) != 'T')",
			tc.Column, text, text)
		fmt.Fprintf(&probe,
			"SELECT %d, COUNT(*) FILTER (WHERE %s), MIN(%s) FILTER (WHERE %s), COUNT(%s) FROM %s",
			i, bad, text, bad, tc.Column, tc.Table)
	}
	rows, err := db.QueryContext(ctx, probe.String())
	if err != nil {
		return nil, fmt.Errorf("canonical-timestamp walk: %w", err)
	}
	defer func() { _ = rows.Close() }()
	counts := make([]int, len(columns))
	samples := make([]sql.NullString, len(columns))
	for rows.Next() {
		var idx, count int
		var sample sql.NullString
		var nonNull int64
		if err := rows.Scan(&idx, &count, &sample, &nonNull); err != nil {
			return nil, fmt.Errorf("canonical-timestamp walk: %w", err)
		}
		if idx < 0 || idx >= len(columns) {
			return nil, fmt.Errorf("canonical-timestamp walk: probe returned out-of-range column index %d", idx)
		}
		counts[idx], samples[idx] = count, sample
		columns[idx].NonNullRows = nonNull
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("canonical-timestamp walk: %w", err)
	}
	var offenders []string
	for i, tc := range columns {
		if counts[i] > 0 {
			offenders = append(offenders,
				fmt.Sprintf("%s.%s: %d value(s), e.g. %q", tc.Table, tc.Column, counts[i], samples[i].String))
		}
	}
	return offenders, nil
}

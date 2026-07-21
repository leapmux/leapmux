package sqlitedb

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

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
// It returns one human-readable line per offending column and the number of
// (table, column) pairs walked; walked == 0 means the schema holds no DATETIME
// columns (e.g. a migrator test rolled it back), which callers should judge
// for themselves.
//
// The walk's coverage is keyed on the DATETIME decltype, so a timestamp column
// declared with any other type would silently escape it. To make that drift
// loud instead of silent, the walk also reports every timestamp-named column
// (name ending in `_at`) whose decltype is not DATETIME as an offender: a
// future migration must either declare the column DATETIME (bringing it under
// the walk) or exclude its table deliberately.
func FindNonCanonicalDatetimes(ctx context.Context, db *sql.DB, exclude ...string) (offenders []string, walked int, err error) {
	// UPPER: SQLite decltypes are case-insensitive, so a migration that
	// declares a column as lowercase `datetime` must still be walked.
	rows, err := db.QueryContext(ctx, `
		SELECT m.name, ti.name
		FROM sqlite_master m, pragma_table_info(m.name) ti
		WHERE m.type = 'table'
		  AND m.name NOT LIKE 'sqlite_%'
		  AND UPPER(ti.type) = 'DATETIME'
		ORDER BY m.name, ti.cid`)
	if err != nil {
		return nil, 0, fmt.Errorf("canonical-timestamp column discovery: %w", err)
	}
	defer func() { _ = rows.Close() }()
	type tableColumn struct{ table, col string }
	var columns []tableColumn
	for rows.Next() {
		var tc tableColumn
		if err := rows.Scan(&tc.table, &tc.col); err != nil {
			return nil, 0, fmt.Errorf("canonical-timestamp column discovery: %w", err)
		}
		if slices.Contains(exclude, tc.table) {
			continue
		}
		columns = append(columns, tc)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("canonical-timestamp column discovery: %w", err)
	}
	// Schema-shape guard: a timestamp-named column that is not DATETIME sits
	// outside the value walk below, so report the declaration itself.
	declRows, err := db.QueryContext(ctx, `
		SELECT m.name, ti.name, ti.type
		FROM sqlite_master m, pragma_table_info(m.name) ti
		WHERE m.type = 'table'
		  AND m.name NOT LIKE 'sqlite_%'
		  AND ti.name LIKE '%\_at' ESCAPE '\'
		  AND UPPER(ti.type) != 'DATETIME'
		ORDER BY m.name, ti.cid`)
	if err != nil {
		return nil, 0, fmt.Errorf("canonical-timestamp decltype guard: %w", err)
	}
	defer func() { _ = declRows.Close() }()
	for declRows.Next() {
		var table, col, decltype string
		if err := declRows.Scan(&table, &col, &decltype); err != nil {
			return nil, 0, fmt.Errorf("canonical-timestamp decltype guard: %w", err)
		}
		if slices.Contains(exclude, table) {
			continue
		}
		offenders = append(offenders,
			fmt.Sprintf("%s.%s: declared %s, want DATETIME (timestamp-named column outside the canonical walk)", table, col, decltype))
	}
	if err := declRows.Err(); err != nil {
		return nil, 0, fmt.Errorf("canonical-timestamp decltype guard: %w", err)
	}
	if len(columns) > 0 {
		// One compound probe instead of a round trip per column: the walk runs
		// after every storetest subtest, so ~58 sequential QueryRow calls per
		// invocation add up across a suite. Table and column names come from
		// the live schema, never caller input. SQLite caps compound SELECTs at
		// 500 arms by default; a schema anywhere near that many DATETIME
		// columns fails this query loudly, not silently.
		var probe strings.Builder
		for i, tc := range columns {
			if i > 0 {
				probe.WriteString(" UNION ALL ")
			}
			fmt.Fprintf(&probe,
				"SELECT %d, COUNT(*), MIN(CAST(%s AS TEXT)) FROM %s WHERE %s IS NOT NULL AND (length(CAST(%s AS TEXT)) != 24 OR substr(CAST(%s AS TEXT), 11, 1) != 'T')",
				i, tc.col, tc.table, tc.col, tc.col, tc.col)
		}
		probeRows, err := db.QueryContext(ctx, probe.String())
		if err != nil {
			return nil, 0, fmt.Errorf("canonical-timestamp walk: %w", err)
		}
		defer func() { _ = probeRows.Close() }()
		counts := make([]int, len(columns))
		samples := make([]sql.NullString, len(columns))
		for probeRows.Next() {
			var idx, count int
			var sample sql.NullString
			if err := probeRows.Scan(&idx, &count, &sample); err != nil {
				return nil, 0, fmt.Errorf("canonical-timestamp walk: %w", err)
			}
			if idx < 0 || idx >= len(columns) {
				return nil, 0, fmt.Errorf("canonical-timestamp walk: probe returned out-of-range column index %d", idx)
			}
			counts[idx], samples[idx] = count, sample
		}
		if err := probeRows.Err(); err != nil {
			return nil, 0, fmt.Errorf("canonical-timestamp walk: %w", err)
		}
		for i, tc := range columns {
			if counts[i] > 0 {
				offenders = append(offenders,
					fmt.Sprintf("%s.%s: %d value(s), e.g. %q", tc.table, tc.col, counts[i], samples[i].String))
			}
		}
	}
	return offenders, len(columns), nil
}

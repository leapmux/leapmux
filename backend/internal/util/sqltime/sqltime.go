// Package sqltime provides driver.Valuer/sql.Scanner time types that floor
// every Go instant to the millisecond grid (UTC) as it crosses into a database
// driver, and -- for SQLite -- serialize it in the canonical 24-char ISO 8601
// layout SQLite stores via strftime('%Y-%m-%dT%H:%M:%fZ').
//
// Flooring rationale: the dialects that store millisecond columns ROUND
// sub-millisecond fractions instead of truncating them -- SQLite's
// strftime('%f') and MySQL/TiDB's DATETIME(3) both round half up -- so an
// un-floored bind could store an instant up to half a millisecond LATER than
// the one the caller supplied. That is enough to overclaim a credential
// deadline by a whole second once a ceil-to-seconds lifetime report is derived
// from the roundtripped value (pinned per dialect by the storetest time_floor
// group). Flooring in Value() keeps every stored instant on the millisecond
// grid, so stored <= bound always holds. Postgres-family dialects floor to
// microseconds instead (see the pgtime subpackage) because pgx already floors
// nanoseconds to timestamptz microseconds and ms would discard precision
// Postgres legitimately stores.
//
// Canonical-layout rationale (SQLite only): strftime('%Y-%m-%dT%H:%M:%fZ')
// writes fixed 3-digit fractional seconds. Every SQL-side write path (column
// DEFAULTs, strftime('now') soft-deletes) stores canonical 24-char values ON
// DISK for EVERY DATETIME column, enforced mechanically by the canonical walk
// in sqlitedb.FindNonCanonicalDatetimes. SQLiteTime.Value() emits byte-exact
// the same layout, so the raw-string keyset predicates and cleanup cutoff
// compares -- which run SQL-side against the stored bytes -- stay byte-exact,
// including at trailing-zero milliseconds. A raw time.Time bind would store the
// driver's own layout (space at byte 11, offset suffix) and silently corrupt
// those compares.
//
// CAUTION for tests and tooling: modernc TRIMS trailing fractional zeros when a
// DATETIME column is scanned into a Go string (a stored ".130Z" arrives as
// ".13Z") -- a driver presentation artifact only. Expression columns that lose
// their DATETIME decltype also arrive as raw TEXT rather than time.Time. Both
// are why SQLiteTime.Scan accepts string/[]byte via RFC3339Nano (which tolerates
// the trailing-zero trim); production DATETIME reads scan into time.Time.
//
// Misuse is fenced mechanically where Go allows: every wrapper carries a
// noCompare marker, so == and map-key use (which would compare time.Time's
// wall/monotonic/location fields, not the instant) are compile errors --
// compare via the promoted/field Equal. Each wrapper also overrides String()
// to print its stored representation, so fmt/slog output matches the DB bytes
// instead of time.Time's default layout.
//
// CAUTION for new queries: sqlc types a result column from its inferred
// decltype and nullability. A SELECT expression that keeps the DATETIME
// decltype but can evaluate to NULL (e.g. CASE ... ELSE NULL END) would be
// typed as the non-null SQLiteTime and hard-error in Scan (loud, not silent);
// give such an expression an explicit nullable override or CAST so it
// resolves to SQLiteNullTime. Aggregates and decltype-losing expressions
// instead surface as interface{} fields in the generated code, which the
// generated-params allowlist walk (hub store's
// TestGeneratedInterfaceParamsAreAllowlisted) flags for a conscious typing
// decision.
//
// Why a Valuer type instead of per-call-site convention: passing a raw
// time.Time to a param typed as one of these becomes a compile error, so the
// floor/canonical layout cannot be forgotten on a new write path. Value() is
// the one place every value crosses into a driver -- it guards struct literals,
// DB-roundtripped rebinds, and cursor parses alike. Applied via sqlc db_type
// overrides; see https://github.com/leapmux/leapmux/issues/303.
//
// Zero value note: a zero time.Time through a non-null type stores
// "0001-01-01T00:00:00.000Z" (SQLite) or the ms-floored zero instant (MySQL) --
// use the Null variant for a column that should hold NULL.
package sqltime

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"time"

	"github.com/leapmux/leapmux/internal/util/timefmt"
)

// noCompare, embedded as a blank zero-size field, makes the wrapper structs
// non-comparable: == on time.Time-carrying values compares the wall/monotonic/
// location fields rather than the instant, so wrapper == wrapper (or map-key
// use) is a latent bug -- the marker turns it into a compile error. Compare
// via the promoted/field Equal instead.
type noCompare [0]func()

// SQLiteTime is a required (NOT NULL) SQLite DATETIME column: it floors to the
// millisecond grid and serializes in the canonical strftime layout on Value(),
// and normalizes to UTC on Scan. The embedded time.Time promotes Equal/UTC/
// Before/... to the wrapper.
type SQLiteTime struct {
	_ noCompare
	time.Time
}

// SQLiteNullTime is a nullable SQLite DATETIME column. The exported Time/Valid
// fields mirror sql.NullTime so existing RequireTime(row.X.Time, row.X.Valid,
// ...) call sites compile unchanged.
type SQLiteNullTime struct {
	_     noCompare
	Time  time.Time
	Valid bool
}

// MySQLTime is a required (NOT NULL) MySQL DATETIME(3)/DATETIME(6) column. Its
// Value() returns an ms-floored UTC time.Time (go-sql-driver serializes it);
// this preserves the prior BindTime behavior for both the DATETIME(3) columns
// and the nine DATETIME(6) columns. Escape hatch: a Go bind that must STORE
// microsecond precision needs a distinct type -- ms is the floor on the bind
// path here by deliberate design.
//
// Value/Scan asymmetry: the ms floor is a BIND-path property only. Scan does
// NOT re-floor -- it normalizes to UTC and keeps whatever precision the
// column stored. A DATETIME(6) column stamped server-side (DEFAULT
// CURRENT_TIMESTAMP(6), e.g. revocation_events.created_at, whose microsecond
// precision is retained deliberately for ordering resolution) therefore
// delivers sub-millisecond residue into the scanned .Time that domain code
// reads directly. Do NOT assume a scanned MySQLTime sits on the millisecond
// grid: compare such a value via Equal against another scanned value, never
// against a freshly ms-floored instant.
type MySQLTime struct {
	_ noCompare
	time.Time
}

// MySQLNullTime is a nullable MySQL DATETIME column; see MySQLTime for the floor
// rationale.
type MySQLNullTime struct {
	_     noCompare
	Time  time.Time
	Valid bool
}

var (
	_ driver.Valuer = SQLiteTime{}
	_ sql.Scanner   = (*SQLiteTime)(nil)
	_ driver.Valuer = SQLiteNullTime{}
	_ sql.Scanner   = (*SQLiteNullTime)(nil)
	_ driver.Valuer = MySQLTime{}
	_ sql.Scanner   = (*MySQLTime)(nil)
	_ driver.Valuer = MySQLNullTime{}
	_ sql.Scanner   = (*MySQLNullTime)(nil)
)

// FloorMillis floors t to the millisecond grid in UTC -- the exact floor every
// SQLite/MySQL valuer in this package applies in Value(). Exported so callers
// that must mint an in-memory instant equal to its eventual DB roundtrip (e.g.
// the worker's message stamps) derive it from the storage floor instead of
// re-rolling UTC().Truncate by hand.
func FloorMillis(t time.Time) time.Time { return t.UTC().Truncate(time.Millisecond) }

// canonicalSQLite floors to the millisecond grid and formats the canonical
// 24-char layout. Go's Format truncates (never rounds) fractional seconds to the
// layout's precision, matching the explicit Truncate.
func canonicalSQLite(t time.Time) string { return FloorMillis(t).Format(timefmt.ISO8601) }

// scanTimeUTC normalizes the non-text driver shapes a DATETIME read can
// produce: a time.Time (UTC-normalized), NULL (an error -- use the Null
// variant), or any other type (an error naming the driver's actual type).
// SQLite's TEXT expression-column path is parsed in SQLiteTime.Scan before it
// delegates here; MySQL (parseTime=true) only ever hands back time.Time, so it
// delegates directly.
func scanTimeUTC(src any, into *time.Time, typeName string) error {
	switch v := src.(type) {
	case time.Time:
		*into = v.UTC()
		return nil
	case nil:
		return fmt.Errorf("cannot scan NULL into %s (use the Null variant)", typeName)
	default:
		return fmt.Errorf("cannot scan %T into %s", src, typeName)
	}
}

// NewSQLiteTime floors t and wraps it. The in-memory copy equals the DB
// roundtrip, so a value held after binding (e.g. an armed due_at) matches what a
// later read scans back.
func NewSQLiteTime(t time.Time) SQLiteTime { return SQLiteTime{Time: FloorMillis(t)} }

// NewSQLiteNullTime mirrors the former BindNullTime: nil yields an invalid
// (NULL) value, non-nil is floored and marked valid.
func NewSQLiteNullTime(t *time.Time) SQLiteNullTime {
	if t == nil {
		return SQLiteNullTime{}
	}
	return SQLiteNullTime{Time: FloorMillis(*t), Valid: true}
}

// SQLiteNullTimeOf mirrors the former BindTimeValid: a required instant bound
// through a nullable column type, floored and always valid.
func SQLiteNullTimeOf(t time.Time) SQLiteNullTime {
	return SQLiteNullTime{Time: FloorMillis(t), Valid: true}
}

// Value floors to the millisecond grid and serializes the canonical 24-char
// strftime layout.
func (t SQLiteTime) Value() (driver.Value, error) { return canonicalSQLite(t.Time), nil }

// String shadows the promoted time.Time.String so fmt/slog print the exact
// canonical bytes Value() stores, keeping log lines byte-comparable with the
// DB rows they describe.
func (t SQLiteTime) String() string { return canonicalSQLite(t.Time) }

// Scan accepts a time.Time (modernc parses a DATETIME decltype into one),
// normalized to UTC, or a string/[]byte parsed via RFC3339Nano (expression
// columns lose their decltype and arrive as raw TEXT; RFC3339Nano tolerates
// modernc's trailing-zero trim). NULL is an error -- use SQLiteNullTime.
func (t *SQLiteTime) Scan(src any) error {
	var text string
	switch v := src.(type) {
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		return scanTimeUTC(src, &t.Time, "SQLiteTime")
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return fmt.Errorf("scan SQLiteTime: parse %q: %w", text, err)
	}
	t.Time = parsed.UTC()
	return nil
}

// Value returns nil when invalid, otherwise the canonical 24-char layout.
func (t SQLiteNullTime) Value() (driver.Value, error) {
	if !t.Valid {
		return nil, nil
	}
	return canonicalSQLite(t.Time), nil
}

// Scan maps NULL to an invalid value; otherwise it delegates to SQLiteTime.Scan.
func (t *SQLiteNullTime) Scan(src any) error {
	if src == nil {
		t.Time, t.Valid = time.Time{}, false
		return nil
	}
	var inner SQLiteTime
	if err := inner.Scan(src); err != nil {
		return err
	}
	t.Time, t.Valid = inner.Time, true
	return nil
}

// Ptr returns a copy of the instant, or nil when invalid. It replaces the former
// sqlutil.NullTimePtr.
func (t SQLiteNullTime) Ptr() *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

// String prints NULL when invalid, otherwise the canonical bytes Value()
// stores; see SQLiteTime.String.
func (t SQLiteNullTime) String() string {
	if !t.Valid {
		return "NULL"
	}
	return canonicalSQLite(t.Time)
}

// NewMySQLTime floors t and wraps it; see NewSQLiteTime for the copy-equals-
// roundtrip rationale.
func NewMySQLTime(t time.Time) MySQLTime { return MySQLTime{Time: FloorMillis(t)} }

// NewMySQLNullTime mirrors the former BindNullTime for MySQL columns.
func NewMySQLNullTime(t *time.Time) MySQLNullTime {
	if t == nil {
		return MySQLNullTime{}
	}
	return MySQLNullTime{Time: FloorMillis(*t), Valid: true}
}

// MySQLNullTimeOf mirrors the former BindTimeValid for MySQL columns.
func MySQLNullTimeOf(t time.Time) MySQLNullTime {
	return MySQLNullTime{Time: FloorMillis(t), Valid: true}
}

// Value returns the ms-floored UTC time.Time; go-sql-driver serializes it.
func (t MySQLTime) Value() (driver.Value, error) { return FloorMillis(t.Time), nil }

// String shadows the promoted time.Time.String so fmt/slog print the stored
// instant -- ms-floored UTC in the ISO 8601 ms layout -- rather than
// time.Time's default layout with sub-ms residue the storage floors away.
func (t MySQLTime) String() string { return FloorMillis(t.Time).Format(timefmt.ISO8601) }

// Scan accepts a time.Time only (the parseTime=true DSN hands DATETIME columns
// back as time.Time), normalized to UTC. NULL is an error -- use MySQLNullTime.
func (t *MySQLTime) Scan(src any) error {
	return scanTimeUTC(src, &t.Time, "MySQLTime")
}

// Value returns nil when invalid, otherwise the ms-floored UTC time.Time.
func (t MySQLNullTime) Value() (driver.Value, error) {
	if !t.Valid {
		return nil, nil
	}
	return FloorMillis(t.Time), nil
}

// Scan maps NULL to an invalid value; otherwise it delegates to MySQLTime.Scan.
func (t *MySQLNullTime) Scan(src any) error {
	if src == nil {
		t.Time, t.Valid = time.Time{}, false
		return nil
	}
	var inner MySQLTime
	if err := inner.Scan(src); err != nil {
		return err
	}
	t.Time, t.Valid = inner.Time, true
	return nil
}

// Ptr returns a copy of the instant, or nil when invalid.
func (t MySQLNullTime) Ptr() *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

// String prints NULL when invalid, otherwise the stored instant; see
// MySQLTime.String.
func (t MySQLNullTime) String() string {
	if !t.Valid {
		return "NULL"
	}
	return FloorMillis(t.Time).Format(timefmt.ISO8601)
}

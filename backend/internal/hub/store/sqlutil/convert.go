package sqlutil

import (
	"database/sql"
	"fmt"
	"time"
)

// RowsAffected extracts the number of affected rows from a sql.Result,
// mapping the error through the provided mapErrFn first.
func RowsAffected(result sql.Result, err error, mapErrFn func(error) error) (int64, error) {
	if err != nil {
		return 0, mapErrFn(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, mapErrFn(err)
	}
	return n, nil
}

// NullTimePtr converts a sql.NullTime to *time.Time, returning nil for
// invalid (NULL) values.
func NullTimePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

// BindTime floors a Go instant to the millisecond grid (UTC) before it is
// handed to a database driver. The dialects that store millisecond columns
// ROUND sub-millisecond fractions instead of truncating them -- SQLite's
// strftime('%f') and MySQL/TiDB's DATETIME(3) both round half up -- so an
// un-floored bind could store an instant up to half a millisecond LATER than
// the one the caller supplied. That is enough to overclaim a credential
// deadline by a whole second once a ceil-to-seconds lifetime report is
// derived from the roundtripped value (pinned by
// TestAPIAuth_Refresh_CASRecoveryReportsWinnerRemainingLifetime and, per
// dialect, by the storetest time_floor group). Flooring Go-side keeps every
// stored instant on the millisecond grid, so stored <= bound always holds.
// Postgres-family dialects don't need it (pgx floors nanoseconds to
// timestamptz microseconds natively) but tolerate it.
//
// Enforcement is per call site by convention; replacing that with a
// mechanical choke point (a driver.Valuer time type applied via sqlc db_type
// overrides) is tracked in https://github.com/leapmux/leapmux/issues/303.
func BindTime(t time.Time) time.Time { return t.UTC().Truncate(time.Millisecond) }

// BindNullTime is BindTime for optional instants: nil yields an invalid
// NullTime (NULL on the wire), non-nil is floored to the millisecond grid.
func BindNullTime(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: BindTime(*t), Valid: true}
}

// BindTimeValid is BindTime for required instants bound through a nullable
// column type: the value is floored to the millisecond grid and always
// marked valid. Callers with an optional *time.Time use BindNullTime
// instead; hand-assembling sql.NullTime around BindTime at call sites is
// exactly the pattern this helper exists to remove.
func BindTimeValid(t time.Time) sql.NullTime {
	return sql.NullTime{Time: BindTime(t), Valid: true}
}

// RequireInt64 unwraps a nullable database integer that the schema requires.
func RequireInt64(value int64, valid bool, column string) (int64, error) {
	if !valid {
		return 0, fmt.Errorf("database row returned NULL %s", column)
	}
	return value, nil
}

// RequireTime unwraps a nullable database timestamp that the schema requires
// and normalizes it to UTC.
func RequireTime(value time.Time, valid bool, column string) (time.Time, error) {
	if !valid {
		return time.Time{}, fmt.Errorf("database row returned NULL %s", column)
	}
	return value.UTC(), nil
}

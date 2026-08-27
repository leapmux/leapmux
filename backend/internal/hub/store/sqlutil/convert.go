package sqlutil

import (
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"
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

// RequireInt64 unwraps a nullable database integer that the schema requires.
func RequireInt64(value int64, valid bool, column string) (int64, error) {
	if !valid {
		return 0, fmt.Errorf("database row returned NULL %s", column)
	}
	return value, nil
}

// CoerceInt64 reads an integer that sqlc typed as a bare interface{} -- an
// expression such as COALESCE(LENGTH(col), 0), whose type the generator cannot
// infer per dialect. The drivers answer int64, int32, float64 or []byte
// depending on the engine, so every numeric kind is accepted; anything else is
// zero, which reads as "absent" for the boolean-sized uses this serves.
func CoerceInt64(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int32:
		return int64(v)
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case []byte:
		n, _ := strconv.ParseInt(string(v), 10, 64)
		return n
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	default:
		return 0
	}
}

// RequireTime unwraps a nullable database timestamp that the schema requires
// and normalizes it to UTC.
func RequireTime(value time.Time, valid bool, column string) (time.Time, error) {
	if !valid {
		return time.Time{}, fmt.Errorf("database row returned NULL %s", column)
	}
	return value.UTC(), nil
}

// NullUserID maps a user id to its nullable-TEXT representation: a zero
// (never-minted) id becomes SQL NULL, and only a minted one becomes a value.
//
// It exists so one place asks the "is this id set?" question, through
// userid.UserID's own IsZero, instead of each call site re-deriving it as
// `u.String() != ""` -- the raw emptiness comparison the type was introduced to
// remove, and the one that would silently stop meaning "was this ever minted"
// if UserID's internal representation ever changed.
func NullUserID(u userid.UserID) sql.NullString {
	if u.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: u.String(), Valid: true}
}

// NullNonEmpty maps an OPTIONAL string column: empty means "not set", which
// is SQL NULL rather than the empty string. A column that distinguishes the
// two would need a different helper; none here does.
func NullNonEmpty(v string) sql.NullString {
	if v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}

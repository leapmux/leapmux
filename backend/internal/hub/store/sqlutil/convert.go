package sqlutil

import (
	"database/sql"
	"fmt"
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
// It exists so the "is this id set?" question is asked ONCE, through
// userid.UserID's own IsZero, instead of being re-derived per call site as
// `u.String() != ""` -- the raw emptiness comparison the type was introduced to
// remove, and the one that would silently stop meaning "was this ever minted"
// if UserID's internal representation ever changed.
func NullUserID(u userid.UserID) sql.NullString {
	if u.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: u.String(), Valid: true}
}

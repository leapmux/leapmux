package sqlutil

import (
	"database/sql"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// RowsAffected extracts the number of affected rows from a sql.Result,
// mapping the error through the provided mapErrFn first.
func RowsAffected(result sql.Result, err error, mapErrFn func(error) error) (int64, error) {
	if err != nil {
		return 0, mapErrFn(err)
	}
	n, _ := result.RowsAffected()
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

// ToNullTime converts a *time.Time to sql.NullTime, normalizing the
// instant to UTC for stable storage. nil yields an invalid NullTime
// (NULL on the wire).
func ToNullTime(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t.UTC(), Valid: true}
}

// MapRevocations projects sqlc rows from a `revoked_at IS NOT NULL`
// query into the backend-neutral TokenRevocationRecord shape, keeping
// the per-row WHAT-comment "revoked_at is non-null per the WHERE
// clause; the generated row type still wraps it in sql.NullTime" in
// one place. Rows whose `revoked_at` somehow scans as invalid (eg a
// concurrent unrevoke between query and read) are skipped.
func MapRevocations[R any](
	rows []R,
	getID func(R) string,
	getUserID func(R) string,
	getRevokedAt func(R) sql.NullTime,
) []store.TokenRevocationRecord {
	out := make([]store.TokenRevocationRecord, 0, len(rows))
	for _, r := range rows {
		revoked := getRevokedAt(r)
		if !revoked.Valid {
			continue
		}
		out = append(out, store.TokenRevocationRecord{
			ID:        getID(r),
			UserID:    getUserID(r),
			RevokedAt: revoked.Time,
		})
	}
	return out
}

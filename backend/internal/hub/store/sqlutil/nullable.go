package sqlutil

import (
	"database/sql"
	"time"
)

// NullTimeToPtr converts a sql.NullTime to a *time.Time.
func NullTimeToPtr(nt sql.NullTime) *time.Time {
	if nt.Valid {
		return &nt.Time
	}
	return nil
}

// PtrToNullTime converts a *time.Time to a sql.NullTime.
func PtrToNullTime(t *time.Time) sql.NullTime {
	if t != nil {
		return sql.NullTime{Time: *t, Valid: true}
	}
	return sql.NullTime{}
}

// NullStringToPtr converts a sql.NullString to a *string.
func NullStringToPtr(ns sql.NullString) *string {
	if ns.Valid {
		return &ns.String
	}
	return nil
}

// PtrToNullString converts a *string to a sql.NullString.
func PtrToNullString(s *string) sql.NullString {
	if s != nil {
		return sql.NullString{String: *s, Valid: true}
	}
	return sql.NullString{}
}

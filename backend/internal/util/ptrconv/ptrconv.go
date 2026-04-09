package ptrconv

import (
	"database/sql"
	"time"
)

// Convert converts a pointer of one integer type to another.
// Returns nil if the input is nil.
func Convert[From, To ~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64](v *From) *To {
	if v == nil {
		return nil
	}
	t := To(*v)
	return &t
}

// Ptr returns a pointer to the given value.
func Ptr[T any](v T) *T { return &v }

// BoolToInt64 converts a bool to an int64 (1 for true, 0 for false),
// matching the convention used by SQLite INTEGER columns for boolean values.
func BoolToInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// Int64ToBool converts an int64 to a bool (non-zero is true),
// matching the convention used by SQLite INTEGER columns for boolean values.
func Int64ToBool(i int64) bool { return i != 0 }

// TimeToPtr returns a pointer to t, or nil if t is the zero value.
func TimeToPtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// DerefTime returns the time value pointed to by t, or zero time if nil.
func DerefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// StringToPtr returns a pointer to s, or nil if s is empty.
func StringToPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// DerefString returns the string pointed to by s, or empty string if nil.
func DerefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// NonNil ensures a nil slice becomes an empty slice, so JSON
// serialization produces [] rather than null.
func NonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// OrEmpty returns b if non-nil, otherwise an empty byte slice.
func OrEmpty(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

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

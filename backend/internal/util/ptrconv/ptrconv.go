package ptrconv

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

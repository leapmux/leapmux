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

package timefmt

import "time"

// ISO8601 is the ISO-8601 format used for timestamp serialization.
const ISO8601 = "2006-01-02T15:04:05.000Z"

// Format formats a time.Time to the standard string representation.
func Format(t time.Time) string {
	return t.UTC().Format(ISO8601)
}

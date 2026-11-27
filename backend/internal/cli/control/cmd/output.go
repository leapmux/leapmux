package cmd

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/leapmux/leapmux/internal/util/timefmt"
)

// The CLI envelope's timestamp helpers. They live here, outside the admin
// verb tree they were born in, because every listing verb reads them: the
// auth verbs print the credential file's own deadlines and the admin verbs
// print the hub's -- one spelling of "a timestamp in a JSON envelope", so
// every row a script reads parses by one rule.

// formatTime renders an optional timestamp via timefmt.Format, or "" when the
// hub sent none, so `omitempty` drops it from the result.
func formatTime(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return ""
	}
	return timefmt.Format(ts.AsTime())
}

// formatWallTime is formatTime for a wall-clock time.Time.
func formatWallTime(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return timefmt.Format(ts)
}

// pluralize formats a count with its noun. Mirrors the frontend helper of the
// same name so the two surfaces word the same fact identically.
func pluralize(count int, singular string, plural ...string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	word := singular + "s"
	if len(plural) > 0 {
		word = plural[0]
	}
	return fmt.Sprintf("%d %s", count, word)
}

package cmd

import (
	"encoding/json"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// The CLI envelope's timestamp helpers. They live here, outside the admin
// verb tree they were born in, because every listing verb reads them: the
// auth verbs print the credential file's own deadlines and the admin verbs
// print the hub's -- one spelling of "a timestamp in a JSON envelope", so
// every row a script reads parses by one rule.

// timeFormat is the envelope's one timestamp layout: UTC, millisecond
// precision, always ending in Z. A timestamp that carries a writer's offset
// sorts wrongly beside the hub's own Z-ending ones, and a script reading
// both cannot compare them without parsing zones first.
const timeFormat = "2006-01-02T15:04:05.000Z07:00"

// formatTime renders an optional timestamp in timeFormat, or "" when the hub
// sent none, so `omitempty` drops it from the result.
func formatTime(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return ""
	}
	return ts.AsTime().UTC().Format(timeFormat)
}

// formatWallTime is formatTime for a wall-clock time.Time.
func formatWallTime(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(timeFormat)
}

// putTime writes one optional timestamp into an output row, omitting the
// field when the hub sent none.
func putTime(row map[string]any, key string, ts *timestamppb.Timestamp) {
	if ts == nil {
		return
	}
	row[key] = ts.AsTime().UTC().Format(timeFormat)
}

// jsonObject encodes {key: value}.
func jsonObject(key string, value any) ([]byte, error) {
	return json.Marshal(map[string]any{key: value})
}

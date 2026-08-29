package cmd

import (
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

// putTime writes one optional timestamp into an output row, omitting the
// field when the hub sent none.
func putTime(row map[string]any, key string, ts *timestamppb.Timestamp) {
	if ts == nil {
		return
	}
	row[key] = ts.AsTime().UTC().Format(timeFormat)
}

// putWallTime is putTime for a wall-clock time.Time -- the credential
// file's own fields, saved in whatever zone wrote them (SaveCredentials
// normalizes new writes to UTC; this keeps older files honest too).
func putWallTime(row map[string]any, key string, ts time.Time) {
	if ts.IsZero() {
		return
	}
	row[key] = ts.UTC().Format(timeFormat)
}

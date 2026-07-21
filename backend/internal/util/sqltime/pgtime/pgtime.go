// Package pgtime provides pgtype.TimestamptzValuer/TimestamptzScanner time types
// for Postgres-family dialects (Postgres, CockroachDB, YugabyteDB). Unlike the
// SQLite/MySQL types in the parent sqltime package, these floor to MICROSECONDS,
// not milliseconds: timestamptz stores microsecond precision, so a ms floor
// would discard precision Postgres legitimately keeps. pgx already floors
// nanoseconds to microseconds when it encodes the binary protocol; the explicit
// Truncate makes the floor mechanical regardless of transport and survives a
// future text-protocol bind where the server cast would round half-to-even.
//
// pgx dispatches on TimestamptzValuer/TimestamptzScanner without codec
// registration, so wiring these types via sqlc db_type overrides is enough.
// See https://github.com/leapmux/leapmux/issues/303.
package pgtime

import (
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// noCompare, embedded as a blank zero-size field, makes the wrapper structs
// non-comparable: == on time.Time-carrying values compares the wall/monotonic/
// location fields rather than the instant, so wrapper == wrapper (or map-key
// use) is a latent bug -- the marker turns it into a compile error. Compare
// via the promoted/field Equal instead. Mirrors the marker in the parent
// sqltime package.
type noCompare [0]func()

// iso8601Micros is the microsecond-precision ISO 8601 layout String() prints:
// timestamptz stores microseconds, so the printed instant carries the same
// precision the column does (the parent sqltime package prints milliseconds
// for the same reason).
const iso8601Micros = "2006-01-02T15:04:05.000000Z"

// Time is a required (NOT NULL) timestamptz column. The embedded time.Time
// promotes Equal/UTC/Before/... to the wrapper.
type Time struct {
	_ noCompare
	time.Time
}

// NullTime is a nullable timestamptz column; the exported Time/Valid fields
// mirror the shape used across the store layer.
type NullTime struct {
	_     noCompare
	Time  time.Time
	Valid bool
}

var (
	_ pgtype.TimestamptzValuer  = Time{}
	_ pgtype.TimestamptzScanner = (*Time)(nil)
	_ pgtype.TimestamptzValuer  = NullTime{}
	_ pgtype.TimestamptzScanner = (*NullTime)(nil)
)

func floorMicros(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

// New floors t to microseconds and wraps it.
func New(t time.Time) Time { return Time{Time: floorMicros(t)} }

// NewNull mirrors an optional instant: nil yields an invalid (NULL) value,
// non-nil is floored to microseconds and marked valid.
func NewNull(t *time.Time) NullTime {
	if t == nil {
		return NullTime{}
	}
	return NullTime{Time: floorMicros(*t), Valid: true}
}

// NullOf is a required instant bound through a nullable column type: floored and
// always valid.
func NullOf(t time.Time) NullTime {
	return NullTime{Time: floorMicros(t), Valid: true}
}

// TimestamptzValue floors to microseconds (UTC) and marks the value finite.
func (t Time) TimestamptzValue() (pgtype.Timestamptz, error) {
	return pgtype.Timestamptz{Time: floorMicros(t.Time), Valid: true}, nil
}

// ScanTimestamptz rejects NULL and non-finite (infinity) timestamps and
// normalizes the instant to UTC.
func (t *Time) ScanTimestamptz(v pgtype.Timestamptz) error {
	if !v.Valid {
		return fmt.Errorf("cannot scan NULL into pgtime.Time (use pgtime.NullTime)")
	}
	if v.InfinityModifier != pgtype.Finite {
		return fmt.Errorf("cannot scan non-finite timestamp (%v) into pgtime.Time", v.InfinityModifier)
	}
	t.Time = v.Time.UTC()
	return nil
}

// TimestamptzValue returns an invalid (NULL) value when !Valid, otherwise the
// microsecond-floored UTC instant.
func (t NullTime) TimestamptzValue() (pgtype.Timestamptz, error) {
	if !t.Valid {
		return pgtype.Timestamptz{}, nil
	}
	return pgtype.Timestamptz{Time: floorMicros(t.Time), Valid: true}, nil
}

// ScanTimestamptz maps NULL to an invalid value, rejects non-finite timestamps,
// and normalizes to UTC otherwise.
func (t *NullTime) ScanTimestamptz(v pgtype.Timestamptz) error {
	if !v.Valid {
		t.Time, t.Valid = time.Time{}, false
		return nil
	}
	if v.InfinityModifier != pgtype.Finite {
		return fmt.Errorf("cannot scan non-finite timestamp (%v) into pgtime.NullTime", v.InfinityModifier)
	}
	t.Time, t.Valid = v.Time.UTC(), true
	return nil
}

// Ptr returns a copy of the instant, or nil when invalid.
func (t NullTime) Ptr() *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

// String shadows the promoted time.Time.String so fmt/slog print the stored
// instant -- microsecond-floored UTC -- rather than time.Time's default layout
// with sub-microsecond residue the storage floors away.
func (t Time) String() string { return floorMicros(t.Time).Format(iso8601Micros) }

// String prints NULL when invalid, otherwise the stored instant; see
// Time.String.
func (t NullTime) String() string {
	if !t.Valid {
		return "NULL"
	}
	return floorMicros(t.Time).Format(iso8601Micros)
}

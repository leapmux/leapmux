package pgtime

import (
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTimeValueFloorsToMicroseconds(t *testing.T) {
	// 999ns tail below the microsecond grid must be dropped, never rounded up.
	probe := time.Date(2025, 1, 2, 12, 0, 0, 123_456_999, time.FixedZone("UTC+9", 9*60*60))
	ts, err := New(probe).TimestamptzValue()
	require.NoError(t, err)
	require.True(t, ts.Valid)
	assert.Equal(t, time.UTC, ts.Time.Location())
	// 12:00 UTC+9 == 03:00 UTC; 123_456_999ns floors to 123_456_000ns.
	assert.Equal(t, time.Date(2025, 1, 2, 3, 0, 0, 123_456_000, time.UTC), ts.Time)
}

func TestTimeValueMicrosecondGridPassesThrough(t *testing.T) {
	exact := time.Date(2025, 1, 2, 3, 0, 0, 123_456_000, time.UTC)
	ts, err := New(exact).TimestamptzValue()
	require.NoError(t, err)
	assert.True(t, ts.Time.Equal(exact))
}

func TestTimestamptzValueFloorsStructLiteral(t *testing.T) {
	// TimestamptzValue is the choke point: it must floor even when the flooring
	// constructor is bypassed via struct-literal construction.
	probe := time.Date(2025, 1, 2, 12, 0, 0, 123_456_999, time.FixedZone("UTC+9", 9*60*60))
	ts, err := Time{Time: probe}.TimestamptzValue()
	require.NoError(t, err)
	assert.Equal(t, time.Date(2025, 1, 2, 3, 0, 0, 123_456_000, time.UTC), ts.Time)

	ts, err = NullTime{Time: probe, Valid: true}.TimestamptzValue()
	require.NoError(t, err)
	require.True(t, ts.Valid)
	assert.Equal(t, time.Date(2025, 1, 2, 3, 0, 0, 123_456_000, time.UTC), ts.Time)
}

func TestTimeScanNormalizesUTC(t *testing.T) {
	var pt Time
	instant := time.Date(2025, 1, 2, 12, 0, 0, 0, time.FixedZone("UTC+9", 9*60*60))
	require.NoError(t, pt.ScanTimestamptz(pgtype.Timestamptz{Time: instant, Valid: true}))
	assert.Equal(t, time.UTC, pt.Location())
	assert.True(t, pt.Equal(instant))
}

func TestTimeScanRejectsNull(t *testing.T) {
	var pt Time
	require.Error(t, pt.ScanTimestamptz(pgtype.Timestamptz{Valid: false}))
}

func TestTimeScanRejectsInfinity(t *testing.T) {
	var pt Time
	require.Error(t, pt.ScanTimestamptz(pgtype.Timestamptz{Valid: true, InfinityModifier: pgtype.Infinity}))
	require.Error(t, pt.ScanTimestamptz(pgtype.Timestamptz{Valid: true, InfinityModifier: pgtype.NegativeInfinity}))
}

func TestNullTimeValue(t *testing.T) {
	ts, err := NullTime{}.TimestamptzValue()
	require.NoError(t, err)
	assert.False(t, ts.Valid, "invalid must bind NULL")

	local := time.Date(2025, 1, 2, 12, 0, 0, 123_456_999, time.FixedZone("UTC+9", 9*60*60))
	ts, err = NullOf(local).TimestamptzValue()
	require.NoError(t, err)
	require.True(t, ts.Valid)
	assert.Equal(t, time.Date(2025, 1, 2, 3, 0, 0, 123_456_000, time.UTC), ts.Time)
}

func TestNewNull(t *testing.T) {
	assert.False(t, NewNull(nil).Valid)
	local := time.Date(2025, 1, 2, 12, 0, 0, 0, time.UTC)
	assert.True(t, NewNull(&local).Valid)
}

func TestNullTimeScanNull(t *testing.T) {
	pt := NullTime{Time: time.Now(), Valid: true}
	require.NoError(t, pt.ScanTimestamptz(pgtype.Timestamptz{Valid: false}))
	assert.False(t, pt.Valid)
}

func TestNullTimeScanRejectsInfinity(t *testing.T) {
	var pt NullTime
	require.Error(t, pt.ScanTimestamptz(pgtype.Timestamptz{Valid: true, InfinityModifier: pgtype.Infinity}))
}

func TestNullTimePtr(t *testing.T) {
	assert.Nil(t, NullTime{}.Ptr())
	instant := time.Date(2025, 1, 2, 12, 0, 0, 0, time.UTC)
	nt := NullOf(instant)
	ptr := nt.Ptr()
	require.NotNil(t, ptr)
	assert.True(t, ptr.Equal(instant))
	assert.NotSame(t, &nt.Time, ptr)
}

// TestCodecRoundTrip drives the value through the pgtype.TimestamptzCodec encode
// and decode plans, the same path pgx uses on the wire, to confirm the Valuer/
// Scanner integrate without codec registration.
func TestCodecRoundTrip(t *testing.T) {
	m := pgtype.NewMap()
	codec := pgtype.TimestamptzCodec{}
	oid := uint32(pgtype.TimestamptzOID)

	src := New(time.Date(2025, 1, 2, 3, 0, 0, 123_456_000, time.UTC))
	plan := codec.PlanEncode(m, oid, pgtype.BinaryFormatCode, src)
	require.NotNil(t, plan, "encode plan must exist for pgtime.Time")
	encoded, err := plan.Encode(src, nil)
	require.NoError(t, err)

	var dst Time
	scanPlan := codec.PlanScan(m, oid, pgtype.BinaryFormatCode, &dst)
	require.NotNil(t, scanPlan, "scan plan must exist for *pgtime.Time")
	require.NoError(t, scanPlan.Scan(encoded, &dst))
	assert.True(t, dst.Equal(src.Time))
}

func TestStringPrintsStoredRepresentation(t *testing.T) {
	// String() must shadow the promoted time.Time.String so fmt output carries
	// the stored microsecond precision in UTC. Struct literals (not the
	// flooring constructors) so the assertions pin String's OWN normalization:
	// the probe's UTC+9 zone would leak into the output if String printed the
	// raw embedded time.
	probe := time.Date(2025, 1, 2, 12, 0, 0, 123_456_999, time.FixedZone("UTC+9", 9*60*60))
	assert.Equal(t, "2025-01-02T03:00:00.123456Z", Time{Time: probe}.String())
	assert.Equal(t, "NULL", NullTime{}.String())
	assert.Equal(t, "2025-01-02T03:00:00.123456Z", NullTime{Time: probe, Valid: true}.String())
}

func TestWrappersAreNotComparable(t *testing.T) {
	// The noCompare marker's whole contract: wrapper == wrapper (and map-key
	// use) must be a compile error, because time.Time field equality is not
	// instant equality. reflect pins the marker so removing the field fails
	// here instead of silently re-legalizing ==.
	for _, typ := range []reflect.Type{reflect.TypeOf(Time{}), reflect.TypeOf(NullTime{})} {
		assert.False(t, typ.Comparable(), "%s must stay non-comparable (noCompare marker)", typ)
	}
}

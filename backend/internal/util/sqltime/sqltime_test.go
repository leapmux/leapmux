package sqltime

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// subMsProbe carries 750us + 999ns of sub-millisecond residue: a round-half-up
// conversion would land on the NEXT millisecond; the floor must land on the
// current one. It is minted in UTC+9 so tests also pin UTC normalization.
func subMsProbe() time.Time {
	return time.Date(2025, 1, 2, 12, 0, 0, 123_750_999, time.FixedZone("UTC+9", 9*60*60))
}

func TestSQLiteTimeValueFloorsAndCanonicalizes(t *testing.T) {
	v, err := NewSQLiteTime(subMsProbe()).Value()
	require.NoError(t, err)
	// 12:00 UTC+9 == 03:00 UTC; 123_750_999ns floors to .123.
	assert.Equal(t, "2025-01-02T03:00:00.123Z", v)
}

func TestSQLiteTimeValueWholeMillisecondIdentity(t *testing.T) {
	exact := time.Date(2025, 1, 2, 3, 0, 0, 123_000_000, time.UTC)
	v, err := NewSQLiteTime(exact).Value()
	require.NoError(t, err)
	assert.Equal(t, "2025-01-02T03:00:00.123Z", v)
}

func TestSQLiteTimeValueTrailingZeroMillisecond(t *testing.T) {
	// A whole-decisecond instant must still serialize fixed 3-digit fractional
	// seconds (".130Z", not ".13Z") so raw-string keyset compares stay byte-exact.
	v, err := NewSQLiteTime(time.Date(2025, 1, 2, 3, 0, 0, 130_000_000, time.UTC)).Value()
	require.NoError(t, err)
	assert.Equal(t, "2025-01-02T03:00:00.130Z", v)
	assert.Len(t, v, 24)
}

func TestValueFloorsStructLiteral(t *testing.T) {
	// Value() is the choke point: it must floor and canonicalize even when the
	// constructor (which also floors) is bypassed via struct-literal
	// construction -- e.g. a DB-roundtripped rebind or a hand-built params
	// struct in a test.
	v, err := SQLiteTime{Time: subMsProbe()}.Value()
	require.NoError(t, err)
	assert.Equal(t, "2025-01-02T03:00:00.123Z", v)

	v, err = SQLiteNullTime{Time: subMsProbe(), Valid: true}.Value()
	require.NoError(t, err)
	assert.Equal(t, "2025-01-02T03:00:00.123Z", v)

	v, err = MySQLTime{Time: subMsProbe()}.Value()
	require.NoError(t, err)
	assert.Equal(t, subMsProbe().UTC().Truncate(time.Millisecond), v)

	v, err = MySQLNullTime{Time: subMsProbe(), Valid: true}.Value()
	require.NoError(t, err)
	assert.Equal(t, subMsProbe().UTC().Truncate(time.Millisecond), v)
}

func TestSQLiteTimeValueNeverRoundsUp(t *testing.T) {
	v, err := NewSQLiteTime(time.Date(2025, 1, 2, 3, 0, 0, 999_999_999, time.UTC)).Value()
	require.NoError(t, err)
	assert.Equal(t, "2025-01-02T03:00:00.999Z", v)
}

func TestSQLiteTimeScanTimeTime(t *testing.T) {
	var st SQLiteTime
	local := time.Date(2025, 1, 2, 12, 0, 0, 0, time.FixedZone("UTC+9", 9*60*60))
	require.NoError(t, st.Scan(local))
	assert.Equal(t, time.UTC, st.Location())
	assert.True(t, st.Equal(local))
}

func TestSQLiteTimeScanCanonicalString(t *testing.T) {
	var st SQLiteTime
	require.NoError(t, st.Scan("2025-01-02T03:00:00.130Z"))
	assert.True(t, st.Equal(time.Date(2025, 1, 2, 3, 0, 0, 130_000_000, time.UTC)))
}

func TestSQLiteTimeScanTrimmedString(t *testing.T) {
	// modernc trims trailing fractional zeros on a Go-string scan (".130Z" ->
	// ".13Z"); RFC3339Nano must still parse it to the same instant.
	var st SQLiteTime
	require.NoError(t, st.Scan("2025-01-02T03:00:00.13Z"))
	assert.True(t, st.Equal(time.Date(2025, 1, 2, 3, 0, 0, 130_000_000, time.UTC)))
}

func TestSQLiteTimeScanBytes(t *testing.T) {
	var st SQLiteTime
	require.NoError(t, st.Scan([]byte("2025-01-02T03:00:00.500Z")))
	assert.True(t, st.Equal(time.Date(2025, 1, 2, 3, 0, 0, 500_000_000, time.UTC)))
}

func TestSQLiteTimeScanNilErrors(t *testing.T) {
	var st SQLiteTime
	require.Error(t, st.Scan(nil))
}

func TestScanUnsupportedTypeErrors(t *testing.T) {
	// A DATETIME column can only arrive as time.Time or (for SQLite expression
	// columns) TEXT; anything else is a schema or driver regression that must
	// fail loudly, not zero out the field.
	var st SQLiteTime
	require.Error(t, st.Scan(int64(1736823600)))
	var snt SQLiteNullTime
	require.Error(t, snt.Scan(int64(1736823600)))
	var mt MySQLTime
	require.Error(t, mt.Scan(int64(1736823600)))
	var mnt MySQLNullTime
	require.Error(t, mnt.Scan(int64(1736823600)))
}

func TestSQLiteTimeScanInvalidStringErrors(t *testing.T) {
	var st SQLiteTime
	require.Error(t, st.Scan("not-a-timestamp"))
	// The driver's own space-separated layout must be rejected too: accepting
	// it would silently mask a write path that stored a non-canonical layout.
	require.Error(t, st.Scan("2025-01-02 03:00:00.13+00:00"))
}

func TestSQLiteTimeValueZero(t *testing.T) {
	// The documented zero-value contract: a zero time.Time still serializes the
	// canonical 24-char layout (year 1), never NULL or a short string.
	v, err := SQLiteTime{}.Value()
	require.NoError(t, err)
	assert.Equal(t, "0001-01-01T00:00:00.000Z", v)
}

func TestSQLiteNullTimeValue(t *testing.T) {
	v, err := SQLiteNullTime{}.Value()
	require.NoError(t, err)
	assert.Nil(t, v, "invalid must bind NULL")

	v, err = SQLiteNullTimeOf(subMsProbe()).Value()
	require.NoError(t, err)
	assert.Equal(t, "2025-01-02T03:00:00.123Z", v)
}

func TestNewSQLiteNullTime(t *testing.T) {
	assert.False(t, NewSQLiteNullTime(nil).Valid, "nil must stay NULL")

	local := subMsProbe()
	nt := NewSQLiteNullTime(&local)
	require.True(t, nt.Valid)
	assert.Equal(t, local.UTC().Truncate(time.Millisecond), nt.Time)
}

func TestSQLiteNullTimeScanNil(t *testing.T) {
	st := SQLiteNullTime{Time: time.Now(), Valid: true}
	require.NoError(t, st.Scan(nil))
	assert.False(t, st.Valid)
}

func TestSQLiteNullTimeScanValid(t *testing.T) {
	// The non-NULL delegation branch: both driver shapes must land Valid=true
	// with the instant normalized to UTC.
	var st SQLiteNullTime
	local := time.Date(2025, 1, 2, 12, 0, 0, 130_000_000, time.FixedZone("UTC+9", 9*60*60))
	require.NoError(t, st.Scan(local))
	require.True(t, st.Valid)
	assert.Equal(t, time.UTC, st.Time.Location())
	assert.True(t, st.Time.Equal(local))

	var fromText SQLiteNullTime
	require.NoError(t, fromText.Scan("2025-01-02T03:00:00.130Z"))
	require.True(t, fromText.Valid)
	assert.True(t, fromText.Time.Equal(time.Date(2025, 1, 2, 3, 0, 0, 130_000_000, time.UTC)))
}

func TestSQLiteNullTimePtr(t *testing.T) {
	assert.Nil(t, SQLiteNullTime{}.Ptr(), "invalid (NULL) must map to nil")

	instant := time.Date(2025, 1, 2, 12, 0, 0, 0, time.UTC)
	nt := SQLiteNullTimeOf(instant)
	ptr := nt.Ptr()
	require.NotNil(t, ptr)
	assert.True(t, ptr.Equal(instant))
	assert.NotSame(t, &nt.Time, ptr, "must return a copy, not an alias into the NullTime")
}

func TestMySQLTimeValueReturnsFlooredTimeNotString(t *testing.T) {
	v, err := NewMySQLTime(subMsProbe()).Value()
	require.NoError(t, err)
	got, ok := v.(time.Time)
	require.True(t, ok, "MySQL Value must be a time.Time, not a string")
	assert.Equal(t, time.UTC, got.Location())
	assert.Equal(t, subMsProbe().UTC().Truncate(time.Millisecond), got)
	assert.Equal(t, 123_000_000, got.Nanosecond())
}

func TestMySQLNullTimeValue(t *testing.T) {
	v, err := MySQLNullTime{}.Value()
	require.NoError(t, err)
	assert.Nil(t, v)

	v, err = MySQLNullTimeOf(subMsProbe()).Value()
	require.NoError(t, err)
	got, ok := v.(time.Time)
	require.True(t, ok)
	assert.Equal(t, subMsProbe().UTC().Truncate(time.Millisecond), got)
}

func TestMySQLTimeScanTimeTimeOnly(t *testing.T) {
	var mt MySQLTime
	local := time.Date(2025, 1, 2, 12, 0, 0, 0, time.FixedZone("UTC+9", 9*60*60))
	require.NoError(t, mt.Scan(local))
	assert.Equal(t, time.UTC, mt.Location())

	require.Error(t, mt.Scan("2025-01-02T03:00:00.130Z"), "MySQL must reject string scans")
	require.Error(t, mt.Scan(nil))
}

func TestNewMySQLNullTime(t *testing.T) {
	assert.False(t, NewMySQLNullTime(nil).Valid)
	local := subMsProbe()
	nt := NewMySQLNullTime(&local)
	require.True(t, nt.Valid)
	assert.Equal(t, local.UTC().Truncate(time.Millisecond), nt.Time)
}

func TestMySQLNullTimeScanValid(t *testing.T) {
	var nt MySQLNullTime
	local := time.Date(2025, 1, 2, 12, 0, 0, 0, time.FixedZone("UTC+9", 9*60*60))
	require.NoError(t, nt.Scan(local))
	require.True(t, nt.Valid)
	assert.Equal(t, time.UTC, nt.Time.Location())
	assert.True(t, nt.Time.Equal(local))

	require.NoError(t, nt.Scan(nil))
	assert.False(t, nt.Valid, "a NULL rescan must clear a previously valid value")
}

func TestMySQLNullTimePtr(t *testing.T) {
	assert.Nil(t, MySQLNullTime{}.Ptr())
	instant := time.Date(2025, 1, 2, 12, 0, 0, 0, time.UTC)
	nt := MySQLNullTimeOf(instant)
	ptr := nt.Ptr()
	require.NotNil(t, ptr)
	assert.True(t, ptr.Equal(instant))
	assert.NotSame(t, &nt.Time, ptr)
}

func TestFloorMillis(t *testing.T) {
	// Floors sub-ms residue (never rounds up) and normalizes to UTC.
	got := FloorMillis(subMsProbe())
	assert.Equal(t, time.Date(2025, 1, 2, 3, 0, 0, 123_000_000, time.UTC), got)
	assert.Equal(t, time.UTC, got.Location())
	// Idempotent on an already-floored instant.
	assert.Equal(t, got, FloorMillis(got))
}

func TestMySQLTimeScanRejectsBytesNamingOriginalType(t *testing.T) {
	// MySQL DATETIME reads arrive as time.Time under the enforced
	// parseTime=true DSN; []byte means the DSN contract broke. The error must
	// name the driver's actual type ([]uint8), not the string it was
	// normalized to, so the DSN misconfiguration is diagnosable.
	var mt MySQLTime
	err := mt.Scan([]byte("2025-01-02T03:00:00.130Z"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "[]uint8")
}

func TestStringPrintsStoredRepresentation(t *testing.T) {
	// String() must shadow the promoted time.Time.String so fmt output is
	// byte-comparable with what Value() stores (ms precision, UTC). Struct
	// literals (not the flooring constructors) so the assertions pin String's
	// OWN normalization: subMsProbe carries a UTC+9 zone and sub-ms residue
	// that a String delegating to the raw embedded time would leak.
	assert.Equal(t, "2025-01-02T03:00:00.123Z", SQLiteTime{Time: subMsProbe()}.String())
	assert.Equal(t, "2025-01-02T03:00:00.123Z", MySQLTime{Time: subMsProbe()}.String())

	assert.Equal(t, "NULL", SQLiteNullTime{}.String())
	assert.Equal(t, "NULL", MySQLNullTime{}.String())
	assert.Equal(t, "2025-01-02T03:00:00.123Z", SQLiteNullTime{Time: subMsProbe(), Valid: true}.String())
	assert.Equal(t, "2025-01-02T03:00:00.123Z", MySQLNullTime{Time: subMsProbe(), Valid: true}.String())
}

func TestMySQLTimeScanPreservesSubMillisecondPrecision(t *testing.T) {
	// The deliberate Value/Scan asymmetry: Value floors to ms on the bind
	// path, but Scan must preserve the precision the column stored. A
	// server-stamped DATETIME(6) column (revocation_events.created_at) hands
	// back microseconds the settled design retains for ordering; a Scan-side
	// floor would destroy it.
	micros := time.Date(2025, 1, 2, 3, 0, 0, 123_456_000, time.UTC)

	var mt MySQLTime
	require.NoError(t, mt.Scan(micros))
	assert.Equal(t, 123_456_000, mt.Nanosecond(), "Scan must NOT floor: microsecond residue must survive")
	assert.True(t, mt.Equal(micros))

	var mnt MySQLNullTime
	require.NoError(t, mnt.Scan(micros))
	require.True(t, mnt.Valid)
	assert.Equal(t, 123_456_000, mnt.Time.Nanosecond(), "NullTime Scan must preserve microsecond residue")

	// Pin the other half of the asymmetry so the two are locked as a pair.
	v, err := NewMySQLTime(micros).Value()
	require.NoError(t, err)
	assert.Equal(t, 123_000_000, v.(time.Time).Nanosecond(), "Value floors to ms on the bind path")
}

func TestWrappersAreNotComparable(t *testing.T) {
	// The noCompare marker's whole contract: wrapper == wrapper (and map-key
	// use) must be a compile error, because time.Time field equality is not
	// instant equality. reflect pins the marker so removing the field fails
	// here instead of silently re-legalizing ==.
	for _, typ := range []reflect.Type{
		reflect.TypeOf(SQLiteTime{}),
		reflect.TypeOf(SQLiteNullTime{}),
		reflect.TypeOf(MySQLTime{}),
		reflect.TypeOf(MySQLNullTime{}),
	} {
		assert.False(t, typ.Comparable(), "%s must stay non-comparable (noCompare marker)", typ)
	}
}

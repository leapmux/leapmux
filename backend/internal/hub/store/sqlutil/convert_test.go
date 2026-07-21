package sqlutil

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rowsAffectedResult struct {
	rowsAffected int64
	err          error
}

func (r rowsAffectedResult) LastInsertId() (int64, error) { return 0, errors.New("unused") }

func (r rowsAffectedResult) RowsAffected() (int64, error) {
	if r.err != nil {
		return 0, r.err
	}
	return r.rowsAffected, nil
}

func TestRowsAffectedMapsRowsAffectedError(t *testing.T) {
	driverErr := errors.New("driver rows affected failed")
	mappedErr := errors.New("mapped")

	n, err := RowsAffected(rowsAffectedResult{err: driverErr}, nil, func(error) error {
		return mappedErr
	})

	require.ErrorIs(t, err, mappedErr)
	assert.Equal(t, int64(0), n)
}

func TestRowsAffectedReturnsCount(t *testing.T) {
	n, err := RowsAffected(rowsAffectedResult{rowsAffected: 3}, nil, func(err error) error {
		return err
	})

	require.NoError(t, err)
	assert.Equal(t, int64(3), n)
}

func TestNullTimePtr(t *testing.T) {
	assert.Nil(t, NullTimePtr(sql.NullTime{}), "invalid (NULL) must map to nil")

	instant := time.Date(2025, 1, 2, 12, 0, 0, 0, time.UTC)
	nt := sql.NullTime{Time: instant, Valid: true}
	ptr := NullTimePtr(nt)
	require.NotNil(t, ptr)
	assert.True(t, ptr.Equal(instant))
	assert.NotSame(t, &nt.Time, ptr, "must return a copy, not an alias into the NullTime")
}

func TestBindTimeFloorsToMillisecondGridInUTC(t *testing.T) {
	// 750us + 999ns of sub-ms residue: a round-half-up conversion would land
	// on the NEXT millisecond; BindTime must floor to the current one.
	local := time.Date(2025, 1, 2, 12, 0, 0, 123_750_999, time.FixedZone("test", 9*60*60))

	bound := BindTime(local)

	assert.Equal(t, time.UTC, bound.Location())
	assert.Equal(t, local.UTC().Truncate(time.Millisecond), bound)
	assert.Equal(t, 123_000_000, bound.Nanosecond())
	assert.False(t, bound.After(local), "bound instant must never postdate its input")
}

func TestBindTimeWholeMillisecondIsIdentity(t *testing.T) {
	exact := time.Date(2025, 1, 2, 12, 0, 0, 123_000_000, time.UTC)
	assert.True(t, BindTime(exact).Equal(exact))
}

func TestBindNullTime(t *testing.T) {
	assert.False(t, BindNullTime(nil).Valid, "nil must stay NULL on the wire")

	local := time.Date(2025, 1, 2, 12, 0, 0, 999_999_999, time.FixedZone("test", 9*60*60))
	bound := BindNullTime(&local)
	require.True(t, bound.Valid)
	assert.Equal(t, BindTime(local), bound.Time)
}

func TestBindTimeValid(t *testing.T) {
	local := time.Date(2025, 1, 2, 12, 0, 0, 999_999_999, time.FixedZone("test", 9*60*60))

	bound := BindTimeValid(local)

	require.True(t, bound.Valid, "a required instant must never bind NULL")
	assert.Equal(t, BindTime(local), bound.Time)
}

func TestRequireInt64(t *testing.T) {
	value, err := RequireInt64(42, true, "seq")
	require.NoError(t, err)
	assert.Equal(t, int64(42), value)

	_, err = RequireInt64(0, false, "seq")
	require.EqualError(t, err, "database row returned NULL seq")
}

func TestRequireTime(t *testing.T) {
	local := time.Date(2025, 1, 2, 12, 0, 0, 0, time.FixedZone("test", 9*60*60))
	value, err := RequireTime(local, true, "published_at")
	require.NoError(t, err)
	assert.Equal(t, local.UTC(), value)

	_, err = RequireTime(time.Time{}, false, "published_at")
	require.EqualError(t, err, "database row returned NULL published_at")
}

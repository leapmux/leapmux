package sqlutil

import (
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

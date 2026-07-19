package mysql

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// TestParseCursorTruncatesToMillisecond pins the DATETIME(3) precision
// contract: the driver serializes a bound time.Time with full sub-millisecond
// digits, so a hand-crafted cursor carrying any would make the composite
// predicate's "col = ?" tiebreak branch unsatisfiable (the stored value is
// millisecond-quantized) while "col < ?" still matched the boundary row --
// re-returning the previous page's tail as duplicates. decodeCursorParams must
// therefore quantize the cursor timestamp to the column's precision.
func TestParseCursorTruncatesToMillisecond(t *testing.T) {
	sub := time.Date(2026, 7, 20, 12, 34, 56, 123_456_789, time.UTC)
	want := time.Date(2026, 7, 20, 12, 34, 56, 123_000_000, time.UTC)

	ct, cid, err := decodeCursorParams(store.EncodeCursor(sub, "abc"))
	require.NoError(t, err)
	assert.True(t, ct.Valid, "cursor_time must be Valid for a non-empty cursor")
	assert.True(t, ct.Time.Equal(want), "cursor_time = %v, want %v", ct.Time, want)
	assert.Equal(t, sql.NullString{String: "abc", Valid: true}, cid)
}

// TestParseCursorFirstPage pins the empty-cursor contract the queries'
// "(narg IS NULL OR ...)" first branch relies on: invalid (NULL) cursor fields
// select every row.
func TestParseCursorFirstPage(t *testing.T) {
	ct, cid, err := decodeCursorParams("")
	require.NoError(t, err)
	assert.False(t, ct.Valid)
	assert.False(t, cid.Valid)
}

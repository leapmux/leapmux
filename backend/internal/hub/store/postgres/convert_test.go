package postgres

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// TestParseCursorTruncatesToMicrosecond pins the timestamptz precision contract:
// pgx sends pgtype.Timestamptz over the binary protocol floored to microseconds,
// so a hand-crafted cursor carrying sub-microsecond digits would make the
// composite predicate's "col = $cursor" tiebreak branch unsatisfiable (the
// stored value is microsecond-quantized) while "col < $cursor" still matched the
// boundary row -- re-returning the previous page's tail as duplicates. decodeCursorParams
// must therefore quantize the cursor timestamp to the column's precision. Mirrors
// mysql/convert_test.go::TestParseCursorTruncatesToMillisecond.
func TestParseCursorTruncatesToMicrosecond(t *testing.T) {
	sub := time.Date(2026, 7, 20, 12, 34, 56, 123_456_789, time.UTC)  // 123.456_789 us
	want := time.Date(2026, 7, 20, 12, 34, 56, 123_456_000, time.UTC) // truncated to 123.456 us

	ct, cid, err := decodeCursorParams(store.EncodeCursor(sub, "abc"))
	require.NoError(t, err)
	assert.True(t, ct.Valid, "cursor_time must be Valid for a non-empty cursor")
	assert.True(t, ct.Time.Equal(want), "cursor_time = %v, want %v", ct.Time, want)
	assert.Equal(t, pgtype.Text{String: "abc", Valid: true}, cid)
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

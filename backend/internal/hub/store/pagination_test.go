package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeCursor_UsesUnderscoreDelimiter(t *testing.T) {
	got := EncodeCursor(time.Date(2026, 7, 20, 12, 34, 56, 789_000_000, time.UTC), "V1StGXR8")
	// The delimiter is an underscore and the timestamp half is RFC3339Nano;
	// splitting on the first underscore must recover the id verbatim.
	ts, id, ok := strings.Cut(got, "_")
	require.True(t, ok, "cursor missing underscore delimiter: %q", got)
	_, err := time.Parse(time.RFC3339Nano, ts)
	require.NoError(t, err, "timestamp half not RFC3339Nano")
	assert.Equal(t, "V1StGXR8", id)
}

func TestParseCursor_RoundTrip(t *testing.T) {
	want := time.Date(2026, 7, 20, 12, 34, 56, 789_000_000, time.UTC)
	const wantID = "abc123"
	c, err := ParseCursor(EncodeCursor(want, wantID))
	require.NoError(t, err)
	require.NotNil(t, c, "non-empty cursor must not decode as first page")
	assert.True(t, c.Time.Equal(want), "time = %v, want %v", c.Time, want)
	assert.Equal(t, wantID, c.ID)
}

func TestParseCursor_RoundTripWholeSecond(t *testing.T) {
	// A whole-second timestamp encodes without a fractional component (RFC3339Nano
	// drops it) and must still round-trip exactly.
	want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	c, err := ParseCursor(EncodeCursor(want, "id"))
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.True(t, c.Time.Equal(want), "time = %v, want %v", c.Time, want)
}

func TestParseCursor_EmptyIsFirstPage(t *testing.T) {
	c, err := ParseCursor("")
	require.NoError(t, err)
	assert.Nil(t, c, "empty cursor should decode as first page (nil cursor)")
}

func TestParseCursor_IDContainingUnderscore(t *testing.T) {
	// The id alphabet today is pure [A-Za-z0-9], but the parser splits on only
	// the FIRST underscore, so a future id format that embeds one survives in
	// the id half rather than being mistaken for the delimiter.
	const wantID = "a_b"
	c, err := ParseCursor(EncodeCursor(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), wantID))
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Equal(t, wantID, c.ID)
}

func TestParseCursor_Errors(t *testing.T) {
	cases := map[string]string{
		"missing delimiter": "2026-07-20T12:34:56.789Z",
		"empty id":          "2026-07-20T12:34:56.789Z_",
		"bad timestamp":     "not-a-time_abc",
		"bare value":        "not-a-time",
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			cur, err := ParseCursor(c)
			require.Error(t, err, "ParseCursor(%q)", c)
			// Every parse failure must wrap ErrInvalidCursor so API boundaries
			// can classify a malformed cursor as bad client input via errors.Is.
			assert.ErrorIs(t, err, ErrInvalidCursor)
			assert.Nil(t, cur)
		})
	}
}

func TestFetchLimit(t *testing.T) {
	cases := map[string]struct {
		limit int64
		want  int64
	}{
		"normal limit gains the probe row": {limit: 50, want: 51},
		"limit of one":                     {limit: 1, want: 2},
		"zero stays terminal":              {limit: 0, want: 0},
		"negative clamps to zero":          {limit: -5, want: 0},
		"max int32 caps probe-safe":        {limit: math.MaxInt32, want: math.MaxInt32},
		"beyond int32 clamps to max":       {limit: math.MaxInt32 + 10, want: math.MaxInt32},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, c.want, FetchLimit(c.limit))
		})
	}
}

func TestNewPage_TerminalWhenNoProbeRowCameBack(t *testing.T) {
	// Fewer rows than the limit: the probe row did not materialize, so the
	// page is terminal with every fetched row kept.
	rows := []User{{ID: "a"}, {ID: "b"}}
	page := NewPage(rows, 5)
	assert.Equal(t, rows, page.Rows)
	assert.False(t, page.HasMore())
	assert.Empty(t, page.NextCursor)
}

func TestNewPage_ExactlyLimitRowsIsTerminal(t *testing.T) {
	// The limit+1 fetch returned exactly limit rows: total is an exact
	// multiple of the page size, and unlike the old len(rows)==limit
	// heuristic the page must NOT claim a further page.
	page := NewPage([]User{{ID: "a"}, {ID: "b"}}, 2)
	assert.Len(t, page.Rows, 2)
	assert.False(t, page.HasMore())
	assert.Empty(t, page.NextCursor)
}

func TestNewPage_SlicesProbeRowAndEncodesLastKeptRow(t *testing.T) {
	newest := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	rows := []User{
		{ID: "a", CreatedAt: newest},
		{ID: "b", CreatedAt: newest.Add(-time.Second)},
		{ID: "c", CreatedAt: newest.Add(-2 * time.Second)}, // the probe row
	}
	page := NewPage(rows, 2)
	require.Len(t, page.Rows, 2)
	assert.True(t, page.HasMore())
	// The cursor continues after the last KEPT row ("b"), not the probe row.
	assert.Equal(t, EncodeCursor(rows[1].CreatedAt, "b"), page.NextCursor)
}

func TestNewPage_EncodesOrderByColumnViaPageCursor(t *testing.T) {
	// ActiveSession pages on last_active_at, not created_at; NewPage must
	// pick the column up from the row type's PageCursor so no caller can
	// mispair the timestamp column.
	created := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	active := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	rows := []ActiveSession{
		{ID: "s1", CreatedAt: created, LastActiveAt: active},
		{ID: "s2", CreatedAt: created, LastActiveAt: active.Add(-time.Second)},
		{ID: "s3", CreatedAt: created, LastActiveAt: active.Add(-2 * time.Second)},
	}
	page := NewPage(rows, 2)
	require.True(t, page.HasMore())
	assert.Equal(t, EncodeCursor(rows[1].LastActiveAt, "s2"), page.NextCursor)
}

func TestNewPage_EmptyRows(t *testing.T) {
	page := NewPage([]User{}, 5)
	assert.Empty(t, page.Rows)
	assert.False(t, page.HasMore())
	assert.Empty(t, page.NextCursor)
}

func TestNewPage_ZeroLimitIsTerminal(t *testing.T) {
	// Guard for the FetchLimit(0)=0 contract: even if a caller violates it
	// and hands rows in with a zero limit, the page must not panic slicing
	// or mint a cursor pointing nowhere.
	page := NewPage([]User{{ID: "a"}}, 0)
	assert.False(t, page.HasMore())
	assert.Empty(t, page.NextCursor)
}

// TestQueryPage pins the shared listing skeleton every dialect's queryPage
// forwarder routes through: a params-build error short-circuits UNWRAPPED
// (cursor errors must stay errors.Is-able as ErrInvalidCursor at API
// boundaries) without running the query, a query error comes back through the
// dialect's mapErr, and a success maps rows and applies the probe-row
// accounting via NewPage.
func TestQueryPage(t *testing.T) {
	ctx := context.Background()
	mapErr := func(err error) error { return fmt.Errorf("mapped: %w", err) }
	identity := func(u User) User { return u }

	t.Run("build error short-circuits unwrapped", func(t *testing.T) {
		buildErr := fmt.Errorf("%w: bad cursor", ErrInvalidCursor)
		queryRan := false
		page, err := QueryPage(ctx, 5,
			func() (struct{}, error) { return struct{}{}, buildErr },
			func(context.Context, struct{}) ([]User, error) { queryRan = true; return nil, nil },
			identity, mapErr)
		require.ErrorIs(t, err, ErrInvalidCursor,
			"a cursor decode error must surface errors.Is-able, not mapped")
		assert.NotContains(t, err.Error(), "mapped:", "build errors bypass mapErr")
		assert.False(t, queryRan, "the query must not run after a build error")
		assert.Empty(t, page.Rows)
	})

	t.Run("query error routes through mapErr", func(t *testing.T) {
		queryErr := errors.New("boom")
		_, err := QueryPage(ctx, 5,
			func() (struct{}, error) { return struct{}{}, nil },
			func(context.Context, struct{}) ([]User, error) { return nil, queryErr },
			identity, mapErr)
		require.ErrorIs(t, err, queryErr)
		assert.Contains(t, err.Error(), "mapped:", "query errors must pass through the dialect's mapErr")
	})

	t.Run("success maps rows and slices the probe row", func(t *testing.T) {
		newest := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
		rows := []User{
			{ID: "a", CreatedAt: newest},
			{ID: "b", CreatedAt: newest.Add(-time.Second)},
			{ID: "c", CreatedAt: newest.Add(-2 * time.Second)}, // the probe row
		}
		page, err := QueryPage(ctx, 2,
			func() (struct{}, error) { return struct{}{}, nil },
			func(context.Context, struct{}) ([]User, error) { return rows, nil },
			identity, mapErr)
		require.NoError(t, err)
		require.Len(t, page.Rows, 2)
		assert.True(t, page.HasMore())
		assert.Equal(t, EncodeCursor(rows[1].CreatedAt, "b"), page.NextCursor)
	})
}

func TestEncodeCursor_NormalizesTimezone(t *testing.T) {
	// A caller may hand EncodeCursor a non-UTC time; it must encode the UTC
	// instant so the cursor compares against the UTC-stored column. The same
	// wall-clock instant in two zones must produce the same cursor.
	utc := time.Date(2026, 7, 20, 12, 34, 56, 789_000_000, time.UTC)
	pst := time.Date(2026, 7, 20, 4, 34, 56, 789_000_000, time.FixedZone("PST", -8*3600))
	assert.Equal(t, EncodeCursor(utc, "id"), EncodeCursor(pst, "id"),
		"EncodeCursor must be timezone-normalized")
	// And the encoded timestamp half must carry a trailing Z (UTC), not an offset.
	ts, _, ok := strings.Cut(EncodeCursor(utc, "id"), "_")
	require.True(t, ok)
	assert.True(t, strings.HasSuffix(ts, "Z"), "EncodeCursor did not normalize to UTC (Z suffix): %q", ts)
}

// TestPageCursorReturnsListingOrderByColumn pins the contract that each row
// type's PageCursor returns the SAME column its listing's ORDER BY sorts on.
// A mismatch silently corrupts paging (EncodeCursor checklist item #4): the
// cursor encodes one column's value while the predicate + ORDER BY use another,
// so the next page skips or repeats rows. The zero distractor field is what
// makes the test bite -- if a PageCursor implementation flips to the distractor
// (e.g. User.PageCursor returning UpdatedAt instead of CreatedAt), the zero
// fails the assertion. Covers all seven PageCursorer implementations; the SQL
// ORDER BY side is pinned per-dialect by the keyset contract (see EncodeCursor's
// doc comment) and by the sqlite indexes_internal_test.go index-shape pins.
func TestPageCursorReturnsListingOrderByColumn(t *testing.T) {
	sentinel := time.Unix(1_750_000_000, 0).UTC()
	cases := []struct {
		name string
		row  PageCursorer
		want time.Time
	}{
		// users ListAll/Search order by (created_at DESC, id DESC).
		{"User_orders_by_created_at", User{CreatedAt: sentinel, UpdatedAt: time.Time{}}, sentinel},
		// workers ListByUserID/ListAdmin order by (created_at DESC, id DESC).
		{"Worker_orders_by_created_at", Worker{CreatedAt: sentinel}, sentinel},
		// registration keys ListAdmin orders by (created_at DESC, id DESC);
		// ExpiresAt is the distractor a flip might land on.
		{"WorkerRegistrationKey_orders_by_created_at", WorkerRegistrationKey{CreatedAt: sentinel, ExpiresAt: time.Time{}}, sentinel},
		// active sessions order by (last_active_at DESC, id DESC); CreatedAt is
		// the distractor.
		{"ActiveSession_orders_by_last_active_at", ActiveSession{CreatedAt: time.Time{}, LastActiveAt: sentinel}, sentinel},
		// per-user sessions (ListByUserID) order by (last_active_at DESC, id
		// DESC); CreatedAt and ExpiresAt are the distractors.
		{"UserSession_orders_by_last_active_at", UserSession{CreatedAt: time.Time{}, ExpiresAt: time.Time{}, LastActiveAt: sentinel}, sentinel},
		// admin api-token listing (ListAllAPITokens) orders by
		// (created_at DESC, id DESC).
		{"APITokenWithOwner_orders_by_created_at", APITokenWithOwner{APIToken: APIToken{CreatedAt: sentinel}}, sentinel},
		// admin delegation-token listing (ListAllDelegationTokens) orders by
		// (created_at DESC, id DESC); ExpiresAt is the distractor.
		{"DelegationTokenWithOwner_orders_by_created_at", DelegationTokenWithOwner{DelegationToken: DelegationToken{CreatedAt: sentinel, ExpiresAt: time.Time{}}}, sentinel},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := c.row.PageCursor()
			require.Equal(t, c.want, got, "PageCursor must return the column the listing ORDER BYs; a mismatch silently corrupts paging")
		})
	}
}

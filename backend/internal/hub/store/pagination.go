package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	// CleanupBatchLimit caps the number of IDs collected before deletion in
	// cleanup operations, preventing unbounded memory accumulation.
	CleanupBatchLimit = 1000

	// SearchPageSize is the page size used when scanning through bucket
	// tables for client-side search filtering.
	SearchPageSize = 500

	// SearchMaxExamine caps the total number of items examined during a
	// client-side prefix search, preventing unbounded GSI scans when
	// matching items are sparse.
	SearchMaxExamine = 10000
)

// cursorDelimiter separates the timestamp and id components of an opaque list
// cursor (see EncodeCursor / ParseCursor). The choice is constrained on three
// sides: it must never appear in a time.RFC3339Nano string (that layout is
// fixed to digits, '-', 'T', ':', '.', 'Z'); it must be shell-safe so an
// operator can paste a cursor into a --cursor flag without quoting; and it
// must be absent from the project's id alphabet (the nanoid generator in
// internal/util/id uses pure [A-Za-z0-9]) so the first occurrence is
// unambiguously the split point. Underscore satisfies all three.
const cursorDelimiter = "_"

// ErrInvalidCursor wraps every ParseCursor failure so an API boundary can
// classify a malformed opaque cursor as bad client input (errors.Is on the
// store call's error -> InvalidArgument) without re-parsing the cursor itself,
// while genuine store failures keep mapping to Internal.
var ErrInvalidCursor = errors.New("invalid cursor")

// Cursor is a decoded composite list cursor: the keyset position of the last
// row of the previous page -- the timestamp column the query orders by plus
// the unique id tiebreaker.
type Cursor struct {
	Time time.Time
	ID   string
}

// EncodeCursor builds the opaque cursor that continues a keyset page after the
// row identified by (t, id). The composite form is required because the listed
// tables order by a millisecond-precision timestamp DESC with a unique id
// tiebreaker; a timestamp-only cursor silently drops rows that share the last
// page's millisecond (https://github.com/leapmux/leapmux/issues/287).
//
// Adding a new keyset-paginated query? Match all four pieces or paging breaks:
//  1. WHERE (t < cursor_time OR (t = cursor_time AND id < cursor_id)).
//  2. ORDER BY the same columns: t DESC, id DESC.
//  3. A composite index on (t DESC, id DESC), scoped by any equality filter,
//     or the scan degrades to a full sort every page.
//  4. The column passed here as t MUST be the query's ORDER BY column
//     (created_at for most tables, last_active_at for sessions); a mismatch
//     silently corrupts paging.
func EncodeCursor(t time.Time, id string) string {
	return t.UTC().Format(time.RFC3339Nano) + cursorDelimiter + id
}

// ParseCursor decodes a cursor produced by EncodeCursor. An empty cursor
// indicates the first page and returns (nil, nil). A non-empty cursor must
// carry both a timestamp and an id joined by cursorDelimiter; anything else is
// an error wrapping ErrInvalidCursor rather than silently treated as "start
// from the beginning", so a stale or truncated cursor surfaces loudly.
func ParseCursor(cursor string) (*Cursor, error) {
	if cursor == "" {
		return nil, nil
	}
	timeStr, id, found := strings.Cut(cursor, cursorDelimiter)
	if !found {
		return nil, fmt.Errorf("%w: missing %q delimiter", ErrInvalidCursor, cursorDelimiter)
	}
	t, err := time.Parse(time.RFC3339Nano, timeStr)
	if err != nil {
		return nil, fmt.Errorf("%w: bad timestamp: %w", ErrInvalidCursor, err)
	}
	if id == "" {
		return nil, fmt.Errorf("%w: missing id", ErrInvalidCursor)
	}
	return &Cursor{Time: t, ID: id}, nil
}

// PageCursorer is implemented by every row type a keyset-paginated listing
// returns. PageCursor yields the row's keyset position -- the timestamp
// column the listing's ORDER BY sorts on plus the unique id tiebreaker --
// which NewPage encodes into the page's next cursor. The implementation MUST
// match the ORDER BY of every keyset query returning the type; a mismatch
// silently corrupts paging (checklist item 4 on EncodeCursor).
type PageCursorer interface {
	PageCursor() (time.Time, string)
}

// Page is one keyset page of a paginated listing. The store assembles it via
// NewPage so the next cursor is always encoded from the query's own ORDER BY
// column (callers cannot mispair the timestamp column) and the has-more signal
// comes from an extra-row probe rather than the len(rows)==limit heuristic,
// which false-positives when the total row count is an exact multiple of the
// page size and sends the client one guaranteed-empty extra request.
type Page[T PageCursorer] struct {
	Rows []T
	// NextCursor continues the listing after the last row of this page.
	// Empty on the final page.
	NextCursor string
}

// HasMore reports whether a further page exists: exactly when NewPage's
// extra-row probe produced a next cursor. Deriving it from NextCursor makes it
// mechanically impossible for the has-more flag and the cursor to contradict
// each other -- a page cannot claim more rows without saying where they start.
func (p Page[T]) HasMore() bool { return p.NextCursor != "" }

// FetchLimit returns the LIMIT a keyset list query should request for a
// caller-facing page limit: one row beyond the clamped limit, so NewPage can
// detect a further page without a second query. The result stays within
// [0, math.MaxInt32], keeping the dialects' int32 LIMIT casts safe -- the
// clamp ceiling is math.MaxInt32-1 (see ClampListLimit), so even the largest
// page's probe row fits and HasMore stays exact at every limit. A zero clamped
// limit stays zero (such a page is terminal; see NewPage).
func FetchLimit(limit int64) int64 {
	clamped := ClampListLimit(limit)
	if clamped == 0 {
		return clamped
	}
	return clamped + 1
}

// NewPage assembles a keyset page from rows fetched with FetchLimit(limit):
// an extra row beyond the clamped limit proves a further page exists; it is
// sliced off and the next cursor is encoded from the last returned row's
// PageCursor.
func NewPage[T PageCursorer](rows []T, limit int64) Page[T] {
	clamped := ClampListLimit(limit)
	if clamped == 0 || int64(len(rows)) <= clamped {
		return Page[T]{Rows: rows}
	}
	rows = rows[:clamped]
	t, id := rows[len(rows)-1].PageCursor()
	return Page[T]{Rows: rows, NextCursor: EncodeCursor(t, id)}
}

// QueryPage runs one keyset-paginated listing: build the dialect's sqlc
// params, execute the generated query, wrap any query error via the dialect's
// mapErr, and assemble the page. Every dialect's thin queryPage forwarder
// binds its package-level mapErr here, so the build -> query -> NewPage
// skeleton shared by all listings lives at this ONE site -- an edit to the
// error wrapping or the probe-row accounting cannot silently reach only some
// dialects or only some listings.
func QueryPage[P any, R any, I PageCursorer](
	ctx context.Context,
	limit int64,
	build func() (P, error),
	query func(context.Context, P) ([]R, error),
	mapRow func(R) I,
	mapErr func(error) error,
) (Page[I], error) {
	params, err := build()
	if err != nil {
		return Page[I]{}, err
	}
	rows, err := query(ctx, params)
	if err != nil {
		return Page[I]{}, mapErr(err)
	}
	return NewPage(MapSlice(rows, mapRow), limit), nil
}

// MapSlice converts a slice of type In to a slice of type Out using fn.
func MapSlice[In, Out any](in []In, fn func(In) Out) []Out {
	out := make([]Out, len(in))
	for i, v := range in {
		out[i] = fn(v)
	}
	return out
}

// maxListLimit is the clamp ceiling for caller-supplied page limits: one below
// math.MaxInt32 so FetchLimit's +1 probe row still fits the dialects' int32
// LIMIT casts. Capping at MaxInt32 itself would force a probe-less fetch at
// exactly that limit, silently degrading HasMore to false there -- lowering
// the ceiling by one dissolves that special case instead of documenting it.
const maxListLimit = math.MaxInt32 - 1

// ClampListLimit normalizes a caller-supplied page limit into the range every
// dialect can carry. The Postgres and MySQL LIMIT columns are int32, so an
// unclamped int64 (an admin passing --limit 3000000000 or 4294967297) would
// truncate on the int32 cast -- wrapping to a negative LIMIT (a DB error) or a
// tiny positive one (4294967297 -> 1 silent under-fetch) -- while SQLite's int64
// LIMIT would return a wildly different set for the same flag. Clamping to
// [0, maxListLimit] makes the int32 conversion always safe (probe row
// included; see maxListLimit) and the three dialects agree: a value past the
// ceiling caps there, a negative floors at 0. A limit of 0 is preserved (the
// paginated queries treat it as "no rows"); this only rewrites values a page
// limit could never legitimately hold.
func ClampListLimit(limit int64) int64 {
	if limit < 0 {
		return 0
	}
	if limit > maxListLimit {
		return maxListLimit
	}
	return limit
}

package service

import (
	"errors"
	"fmt"

	"connectrpc.com/connect/v2"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// The page-size limits every paginated RPC applies.
//
// Exported because the CLI reads them: it refuses an out-of-range --limit
// before the dial so the operator gets an answer that identifies the flag.
// The two sides therefore state ONE range. They act on it differently -- the
// hub caps an oversized limit and returns a page, the CLI refuses it -- and
// that is deliberate: an operator who asks for 900 rows is better served by
// the range than by 500 rows that look like the whole answer.
const (
	// DefaultPageLimit is what an omitted or non-positive limit
	// resolves to.
	DefaultPageLimit = 50
	// MaxPageLimit caps what one page may return, so a caller cannot
	// ask the hub for the whole table in one response.
	MaxPageLimit = 500
)

// NormalizePageParams normalizes one paginated request's cursor and limit.
//
// The normalization belongs HERE, not in a client. `store.ClampListLimit`
// preserves a limit of 0, and the paginated queries read 0 as "return no
// rows" -- so a caller that simply omits `limit` (the proto3 default) gets
// an empty page it cannot tell apart from an empty table. Every paginated
// handler builds its PageParams through this function, so the guarantee
// holds for the CLI, the frontend, and any script alike.
func NormalizePageParams(cursor string, limit int64) store.PageParams {
	switch {
	case limit <= 0:
		limit = DefaultPageLimit
	case limit > MaxPageLimit:
		limit = MaxPageLimit
	}
	return store.PageParams{Cursor: cursor, Limit: limit}
}

// pageParams converts a request's page into the store's form. An absent page
// reads the first page at the default limit.
func pageParams(page *leapmuxv1.PageRequest) store.PageParams {
	return NormalizePageParams(page.GetCursor(), int64(page.GetLimit()))
}

// pageResponse ends one page of a listing. An empty nextCursor tells the
// client that no more rows exist.
func pageResponse(nextCursor string) *leapmuxv1.PageResponse {
	return &leapmuxv1.PageResponse{NextCursor: nextCursor}
}

// pageListError maps an error from a paginated store listing onto the
// Connect surface: a malformed or stale cursor is the caller's input, and any
// other failure is a hub fault.
func pageListError(err error, op string) error {
	if errors.Is(err, store.ErrInvalidCursor) {
		return invalidCursorError(err, op)
	}
	return wrappedConnectError(connect.CodeInternal, fmt.Errorf("%s: %w", op, err))
}

// invalidCursorError is the one answer to a cursor the store cannot parse.
// The store wraps every parse failure in store.ErrInvalidCursor before any
// query runs. The fix is not obvious from the parse error alone, so the
// message states it.
func invalidCursorError(err error, op string) error {
	return wrappedConnectError(connect.CodeInvalidArgument, fmt.Errorf(
		"%s: %w (pass the cursor printed at the end of the previous page, or omit it to start from the first page)", op, err))
}

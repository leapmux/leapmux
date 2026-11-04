package service

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// normalizePageParams normalizes one paginated request's cursor and limit to
// the page limits of contracts/pagination.json: an omitted or non-positive
// limit reads contracts.DefaultPageLimit rows, and a larger one is capped at
// contracts.MaxPageLimit, so a caller cannot ask for the whole table in one
// response.
//
// The normalization belongs HERE, not in a client. `store.ClampListLimit`
// preserves a limit of 0, and the paginated queries read 0 as "return no
// rows" -- so a caller that simply omits `limit` (the proto3 default) gets
// an empty page it cannot tell apart from an empty table. Every paginated
// handler builds its PageParams through this function, so the guarantee
// holds for the CLI, the frontend, and any script alike.
//
// The CLI states the same range from the same contract, but it acts on it
// differently, on purpose: the hub caps an oversized limit and returns a page,
// and the CLI refuses it before the dial, because an operator who asks for
// 900 rows is better served by the range than by 500 rows that look like the
// whole answer.
func normalizePageParams(cursor string, limit int64) store.PageParams {
	switch {
	case limit <= 0:
		limit = contracts.DefaultPageLimit
	case limit > contracts.MaxPageLimit:
		limit = contracts.MaxPageLimit
	}
	return store.PageParams{Cursor: cursor, Limit: limit}
}

// pageParams converts a request's page into the store's form. An absent page
// reads the first page at the default limit.
func pageParams(page *leapmuxv1.PageRequest) store.PageParams {
	return normalizePageParams(page.GetCursor(), int64(page.GetLimit()))
}

// pageResponse ends one page of a listing. An empty nextCursor tells the
// client that no more rows exist.
func pageResponse(nextCursor string) *leapmuxv1.PageResponse {
	return &leapmuxv1.PageResponse{NextCursor: nextCursor}
}

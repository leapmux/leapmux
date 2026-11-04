package cmd

import (
	"errors"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

const (
	// listPageSize is the hub's maximum page, READ from the page-limit
	// contract (contracts/pagination.json) rather than restated. An omitted
	// limit resolves to contracts.DefaultPageLimit, so a loop that asked for
	// nothing took ten times the requests and covered a tenth of what
	// maxListPages claims.
	listPageSize = contracts.MaxPageLimit
	// maxListPages limits one listing loop. At listPageSize that covers a
	// quarter of a million rows, so it is a guard against a cursor that never
	// advances, not a limit anybody reaches.
	maxListPages = 500
)

// errListingIncomplete reports that a guard stopped a listing before the hub
// ended it. The caller gets no rows: printing the rows read so far would
// present part of the listing as all of it.
var errListingIncomplete = errors.New("the hub did not end the listing")

// readAllPages reads EVERY page of a keyset-paginated hub listing, not the
// first. fetch sends the page request it is given and returns the page's rows
// and the hub's page response; an empty next cursor ends the listing.
//
// readAllPages builds each page request itself: the cursor comes from the
// previous page, and the limit is listPageSize. A caller therefore cannot ask
// for a smaller page, or send a stale cursor.
//
// The loop does not trust the cursor alone to end it. A client loop whose
// only exit is a value the server chooses is a hang the server can cause, so
// it also stops when the cursor fails to advance and after maxListPages
// pages. Either stop returns errListingIncomplete and no rows, as a failed
// page read does: the hub ends every listing with an empty cursor, so a stop
// means a fault in the hub, and the rows read so far are not the listing.
//
// Rows are keyed by keyOf while assembling: a keyset boundary can repeat a row
// across two pages. A repeated key keeps the position of its first occurrence and the value of its
// last, so no row prints twice and the row printed is the latest one the hub
// sent. The browser's fetchAllPages (frontend/src/lib/fetchAllPages.ts) keeps
// the same rule.
func readAllPages[T any](fetch func(page *leapmuxv1.PageRequest) ([]T, *leapmuxv1.PageResponse, error), keyOf func(T) string) ([]T, error) {
	indexOf := make(map[string]int)
	out := make([]T, 0)
	cursor := ""
	for range maxListPages {
		rows, page, err := fetch(&leapmuxv1.PageRequest{Cursor: cursor, Limit: listPageSize})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			key := keyOf(row)
			if i, seen := indexOf[key]; seen {
				out[i] = row
				continue
			}
			indexOf[key] = len(out)
			out = append(out, row)
		}
		next := page.GetNextCursor()
		if next == "" {
			return out, nil
		}
		if next == cursor {
			return nil, fmt.Errorf("%w: the next-page cursor did not advance", errListingIncomplete)
		}
		cursor = next
	}
	return nil, fmt.Errorf("%w: it continued past %d pages", errListingIncomplete, maxListPages)
}

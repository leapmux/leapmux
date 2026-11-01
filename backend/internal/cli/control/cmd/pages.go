package cmd

import (
	"github.com/leapmux/leapmux/internal/hub/service"
)

const (
	// listPageSize is the hub's maximum page, READ from the hub's own
	// constant rather than restated. An omitted limit resolves to
	// service.DefaultPageLimit, so a loop that asked for nothing took ten
	// times the requests and covered a tenth of what maxListPages claims.
	listPageSize = service.MaxPageLimit
	// maxListPages limits one listing loop. At listPageSize that covers a
	// quarter of a million rows, so it is a guard against a cursor that never
	// advances, not a limit anybody reaches.
	maxListPages = 500
)

// readAllPages reads EVERY page of a keyset-paginated hub listing, not the
// first. fetch reads the page that starts at cursor and returns its rows and
// the next cursor; an empty next cursor ends the listing.
//
// The loop does not trust the cursor alone to end it. A client loop whose
// only exit is a value the server chooses is a hang the server can cause, so
// it also stops when the cursor fails to advance and after maxListPages
// pages.
//
// Rows are keyed by keyOf while assembling: a keyset boundary can repeat a row
// across two pages, and a stalled cursor makes the last page arrive twice. The
// first occurrence of a key wins, so no row prints twice.
func readAllPages[T any](fetch func(cursor string) ([]T, string, error), keyOf func(T) string) ([]T, error) {
	seen := make(map[string]bool)
	out := make([]T, 0)
	cursor := ""
	for range maxListPages {
		rows, next, err := fetch(cursor)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			key := keyOf(row)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, row)
		}
		if next == "" || next == cursor {
			break
		}
		cursor = next
	}
	return out, nil
}

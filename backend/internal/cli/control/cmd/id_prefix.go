package cmd

import "strings"

// lookupByIDPrefix finds the item whose id is handle, or else the one item
// whose id starts with handle. When no item or several items share the
// prefix, it reports false with every item whose id starts with handle, so
// the caller words its own not-found and ambiguous errors. An exact id wins
// over a prefix that it shares with another id.
func lookupByIDPrefix[T any](items []T, handle string, idOf func(T) string) (T, []T, bool) {
	var matches []T
	for _, item := range items {
		id := idOf(item)
		if id == handle {
			return item, nil, true
		}
		if strings.HasPrefix(id, handle) {
			matches = append(matches, item)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil, true
	}
	var none T
	return none, matches, false
}

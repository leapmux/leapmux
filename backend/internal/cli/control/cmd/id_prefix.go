package cmd

import "strings"

// minHandlePrefixLength is the shortest prefix allowed when matching short
// per-agent handles (control request claim tokens, queued input ids). Exact
// matches remain valid at any length.
const minHandlePrefixLength = 3

// lookupByIDPrefix finds the item whose id is handle, or else the one item
// whose id starts with handle (requiring at least minHandlePrefixLength
// characters for a prefix match). When no item or several items share the
// prefix, it reports false with every item whose id starts with handle, so
// the caller words its own not-found and ambiguous errors. An exact id wins
// over a prefix that it shares with another id.
func lookupByIDPrefix[T any](items []T, handle string, idOf func(T) string) (T, []T, bool) {
	var matches []T
	allowPrefix := len(handle) >= minHandlePrefixLength
	for _, item := range items {
		id := idOf(item)
		if id == handle {
			return item, nil, true
		}
		if allowPrefix && strings.HasPrefix(id, handle) {
			matches = append(matches, item)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil, true
	}
	var none T
	return none, matches, false
}

package cmd

import (
	"strings"

	"github.com/leapmux/leapmux/internal/cli"
)

// minHandlePrefixLength is the shortest prefix allowed when matching short
// per-agent handles (control request claim tokens, queued input ids). Exact
// matches remain valid at any length.
const minHandlePrefixLength = 3

// normalizeIDHandle trims whitespace and strips an optional leading "id:"
// qualifier from a per-agent handle (such as a control request claim token or
// a queued input id). Prefix matching remains enabled on the returned handle.
func normalizeIDHandle(raw, noun string) (string, error) {
	handle := strings.TrimSpace(raw)
	if rest, ok := strings.CutPrefix(handle, "id:"); ok {
		handle = strings.TrimSpace(rest)
		if handle == "" {
			return "", cli.Usagef(`missing %s after "id:"`, noun)
		}
		return handle, nil
	}
	if handle == "" {
		return "", cli.Usagef("the %s is empty", noun)
	}
	return handle, nil
}

// lookupByIDPrefix finds the item whose id is handle, or else the one item
// whose id starts with handle (requiring at least minHandlePrefixLength
// characters for a prefix match). When no item or several items share the
// prefix, it reports false with every item whose id starts with handle, so
// the caller words its own not-found and ambiguous errors. An exact id wins
// over a prefix that it shares with another id.
func lookupByIDPrefix[T any](items []T, handle string, idOf func(T) string) (T, []T, bool) {
	return lookupByIDPrefixMin(items, handle, minHandlePrefixLength, idOf)
}

// lookupByIDPrefixMin finds the item whose id equals handle, or else the one
// item whose id starts with handle when len(handle) >= minLen. An exact id
// wins over a prefix that it shares with another id. When no item or several
// items match, it reports ok=false; ambiguous holds the prefix matches when
// two or more items share the prefix, and nil otherwise.
func lookupByIDPrefixMin[T any](items []T, handle string, minLen int, idOf func(T) string) (match T, ambiguous []T, ok bool) {
	var matches []T
	allowPrefix := len(handle) >= minLen
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

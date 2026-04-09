package store

import (
	"cmp"
	"fmt"
	"slices"
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

// MapSlice converts a slice of type In to a slice of type Out using fn.
func MapSlice[In, Out any](in []In, fn func(In) Out) []Out {
	out := make([]Out, len(in))
	for i, v := range in {
		out[i] = fn(v)
	}
	return out
}

// PluckStrings extracts a string field from each item in a slice.
func PluckStrings[T any](items []T, fn func(T) string) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = fn(item)
	}
	return out
}

// UniqueStrings deduplicates ids (DynamoDB BatchGetItem silently drops duplicates).
func UniqueStrings(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// ParseCursorTime parses an RFC3339Nano cursor string into a time.Time.
// Returns zero time and false for an empty cursor (first page).
func ParseCursorTime(cursor string) (time.Time, bool, error) {
	if cursor == "" {
		return time.Time{}, false, nil
	}
	t, err := time.Parse(time.RFC3339Nano, cursor)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("invalid cursor: %w", err)
	}
	return t, true, nil
}

// SortFilterLimit sorts items by timeVal descending, applies cursor-based
// filtering, and limits results. Generic helper for paginated listings.
func SortFilterLimit[T any](items []T, timeVal func(T) time.Time, cursor string, limit int64) ([]T, error) {
	slices.SortFunc(items, func(a, b T) int {
		return cmp.Compare(timeVal(b).UnixNano(), timeVal(a).UnixNano())
	})
	if cursor != "" {
		cursorTime, ok, err := ParseCursorTime(cursor)
		if err != nil {
			return nil, err
		}
		if ok {
			var filtered []T
			for _, item := range items {
				if timeVal(item).Before(cursorTime) {
					filtered = append(filtered, item)
				}
			}
			items = filtered
		}
	}
	return ApplyOffsetLimit(items, 0, limit), nil
}

// SortAndPaginateWorkers sorts workers by CreatedAt descending, applies
// cursor-based filtering, and limits results.
func SortAndPaginateWorkers(workers []*Worker, cursor string, limit int64) ([]*Worker, error) {
	return SortFilterLimit(workers, func(w *Worker) time.Time { return w.CreatedAt }, cursor, limit)
}

// PrefixMatchUser returns true if the lowered query is a prefix of the
// user's username, display name, or email (case-insensitive).
// The caller must pass a pre-lowercased query.
func PrefixMatchUser(u User, loweredQuery string) bool {
	return strings.HasPrefix(strings.ToLower(u.Username), loweredQuery) ||
		strings.HasPrefix(strings.ToLower(u.DisplayName), loweredQuery) ||
		strings.HasPrefix(strings.ToLower(u.Email), loweredQuery)
}

// PrefixMatchOrg returns true if the lowered query is a prefix of the
// org's name (case-insensitive).
// The caller must pass a pre-lowercased query.
func PrefixMatchOrg(o Org, loweredQuery string) bool {
	return strings.HasPrefix(strings.ToLower(o.Name), loweredQuery)
}

// ApplyOffsetLimit returns the slice all[offset:offset+limit], clamped to bounds.
// It always returns a non-nil slice.
func ApplyOffsetLimit[T any](all []T, offset, limit int64) []T {
	if offset >= int64(len(all)) {
		return []T{}
	}
	end := offset + limit
	if end > int64(len(all)) {
		end = int64(len(all))
	}
	return all[offset:end]
}

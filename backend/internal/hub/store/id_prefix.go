package store

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/leapmux/leapmux/internal/util/id"
)

// IDPrefixFilter classifies a caller-supplied ID prefix for indexed lookup.
//
// LeapMux generates entity IDs with id.Generate(), which always produces
// id.GeneratedLength (48) ASCII alphanumeric characters. When a non-empty
// filter has at least id.GeneratedLength bytes, Exact is true so stores execute
// an exact primary-key equality query (`id = :id`). Shorter non-empty prefixes
// execute a byte-wise half-open range scan (`id >= :lo AND id < :hi`), which
// rides the primary-key B-tree in SQLite (BINARY), PostgreSQL (COLLATE "C"),
// and MySQL (utf8mb4_bin) without LIKE wildcard escaping or case folding.
type IDPrefixFilter struct {
	Raw      string
	Exact    bool
	Lower    string
	Upper    string
	HasUpper bool
}

// ParseIDPrefix normalizes raw into an IDPrefixFilter. An empty or
// whitespace-only input returns (zero, false, nil), meaning no ID filter is
// active. Invalid UTF-8 returns ErrInvalidArgument so PostgreSQL never sees a
// malformed UTF-8 bind parameter.
func ParseIDPrefix(raw string) (IDPrefixFilter, bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return IDPrefixFilter{}, false, nil
	}
	if !utf8.ValidString(trimmed) {
		return IDPrefixFilter{}, false, fmt.Errorf("%w: id_prefix is not valid UTF-8", ErrInvalidArgument)
	}
	if len(trimmed) >= id.GeneratedLength {
		return IDPrefixFilter{
			Raw:   trimmed,
			Exact: true,
			Lower: trimmed,
		}, true, nil
	}
	upper, hasUpper := nextUTF8Prefix(trimmed)
	return IDPrefixFilter{
		Raw:      trimmed,
		Exact:    false,
		Lower:    trimmed,
		Upper:    upper,
		HasUpper: hasUpper,
	}, true, nil
}

// CursorAfter returns the strict lower-bound ID (`id > gt`) for continuing an
// ID-ordered prefix listing after cursor, and reports empty=true when the
// cursor has already passed the entire matching range (so the caller can
// return an empty page with no database query).
func (f IDPrefixFilter) CursorAfter(cursor *Cursor) (gt string, empty bool) {
	if cursor == nil {
		return "", false
	}
	if f.Exact {
		if cursor.ID >= f.Lower {
			return "", true
		}
		return cursor.ID, false
	}
	if f.HasUpper && cursor.ID >= f.Upper {
		return "", true
	}
	return cursor.ID, false
}

// nextUTF8Prefix returns the smallest valid UTF-8 string strictly greater than
// every valid UTF-8 string that starts with prefix, in byte-wise order. Because
// UTF-8 preserves Unicode code-point order, incrementing the last rune below
// utf8.MaxRune (skipping the surrogate range U+D800..U+DFFF) produces an exact
// half-open upper bound that PostgreSQL, MySQL, and SQLite all accept as valid
// UTF-8 text. Returns ("", false) only when every rune in prefix is
// utf8.MaxRune (U+10FFFF).
func nextUTF8Prefix(prefix string) (string, bool) {
	for len(prefix) > 0 {
		r, size := utf8.DecodeLastRuneInString(prefix)
		if r < utf8.MaxRune {
			next := r + 1
			if next == 0xD800 {
				next = 0xE000
			}
			return prefix[:len(prefix)-size] + string(next), true
		}
		prefix = prefix[:len(prefix)-size]
	}
	return "", false
}

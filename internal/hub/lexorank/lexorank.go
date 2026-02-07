// Package lexorank provides lexicographic ranking for ordered items.
// Ranks are strings that sort lexicographically, allowing insertions
// between existing items without renumbering.
package lexorank

import "strings"

const (
	minChar = 'a'
	maxChar = 'z'
	midChar = 'n'
)

// First returns an initial rank suitable for the first item.
func First() string {
	return string(midChar)
}

// After returns a rank that sorts after s.
func After(s string) string {
	// Append midChar to get something after s.
	return s + string(midChar)
}

// Mid returns a rank between a and b. If a is empty, it returns a rank
// before b. If b is empty, it returns a rank after a. If both are empty,
// it returns First().
func Mid(a, b string) string {
	if a == "" && b == "" {
		return First()
	}
	if a == "" {
		return before(b)
	}
	if b == "" {
		return After(a)
	}
	return between(a, b)
}

// before returns a rank that sorts before s.
func before(s string) string {
	if len(s) == 0 {
		return First()
	}

	// Try to decrement the last character.
	last := s[len(s)-1]
	if last > minChar+1 {
		mid := (minChar + last) / 2
		return s[:len(s)-1] + string(mid)
	}

	// Last char is 'a' or 'b', insert a mid char before it.
	return s[:len(s)-1] + string(minChar) + string(midChar)
}

// between returns a rank between a and b where a < b.
func between(a, b string) string {
	// Pad a and b to the same length for comparison.
	maxLen := len(a)
	if len(b) > maxLen {
		maxLen = len(b)
	}

	pa := padRight(a, maxLen)
	pb := padRight(b, maxLen)

	// Find first position where they differ.
	for i := 0; i < maxLen; i++ {
		ca := pa[i]
		cb := pb[i]

		if ca == cb {
			continue
		}

		// Found difference. Try to find midpoint.
		if cb-ca > 1 {
			mid := (ca + cb) / 2
			return pa[:i] + string(mid)
		}

		// Adjacent characters - recurse into next position.
		// Keep ca and go deeper comparing rest of a against minChar.
		suffix := between(
			trimTrailing(pa[i+1:], minChar),
			strings.Repeat(string(maxChar), 1),
		)
		return pa[:i+1] + suffix
	}

	// Strings are equal (shouldn't happen) - append midChar.
	return a + string(midChar)
}

func padRight(s string, length int) string {
	for len(s) < length {
		s += string(minChar)
	}
	return s
}

func trimTrailing(s string, c byte) string {
	return strings.TrimRight(s, string(c))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

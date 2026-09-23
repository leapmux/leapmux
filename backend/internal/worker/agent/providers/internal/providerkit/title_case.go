package providerkit

// title_case.go holds the provider-neutral label helpers: capitalizing a word and
// title-casing an option id. Several providers build display labels with them.

import (
	"strings"
	"unicode"
)

// CapitalizeFirst returns s with its first rune upper-cased.
func CapitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	for _, r := range s {
		return string(unicode.ToUpper(r)) + s[len(string(r)):]
	}
	return s
}

// TitleCaseID returns name if it is a distinct display name (non-empty and
// different from id). Otherwise it title-cases the id by splitting on
// underscores or hyphens, capitalizing each word, and joining with spaces
// (e.g. "smart_approve" → "Smart Approve", "full-auto" → "Full Auto").
func TitleCaseID(id, name string) string {
	if name != "" && name != id {
		return name
	}
	if id == "" {
		return ""
	}
	// Determine separator: prefer underscore, fall back to hyphen.
	sep := "_"
	if !strings.Contains(id, "_") && strings.Contains(id, "-") {
		sep = "-"
	}
	parts := strings.Split(id, sep)
	for i, p := range parts {
		parts[i] = CapitalizeFirst(p)
	}
	return strings.Join(parts, " ")
}

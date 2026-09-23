package cursor

import (
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// model_names.go turns Cursor's bare model ids into display names.

// modelDisplayNameAcronyms upper-cases segments that are conventionally acronyms when
// humanizing a model id (e.g. "gpt-5.5" -> "GPT 5.5"). Extend as new vendors appear.
var modelDisplayNameAcronyms = map[string]string{
	"gpt": "GPT",
	"ai":  "AI",
	"llm": "LLM",
}

// stripModelIDBrackets removes a trailing "[...]" metadata suffix from a model id,
// e.g. "composer-2.5[fast=true]" -> "composer-2.5". Returns the id unchanged when it
// carries no bracket.
func stripModelIDBrackets(id string) string {
	if open := strings.IndexByte(id, '['); open >= 0 {
		return id[:open]
	}
	return id
}

// isNumericModelSegment reports whether a model-id segment is a bare version number
// (digits and dots), e.g. "4", "8", "2.5" -- but not "k2.5" or "codex".
func isNumericModelSegment(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}

// humanizeModelID turns a bare, hyphen-separated model id into a friendly display name:
//
//	composer-2.5      -> Composer 2.5
//	claude-opus-4-8   -> Claude Opus 4.8
//	gpt-5.3-codex     -> GPT 5.3 Codex
//	gemini-3.1-pro    -> Gemini 3.1 Pro
//	kimi-k2.5         -> Kimi K2.5
//
// Consecutive numeric segments are joined with "." (so a hyphen-separated minor version
// like "4-8" reads as "4.8"), word segments are capitalized, and known acronyms (GPT)
// are upper-cased. Any "[...]" metadata suffix is stripped first. This is the more
// capable sibling of providerkit.TitleCaseID: a model id needs the version-number coalescing that a
// plain option label (which providerkit.TitleCaseID handles) does not.
func humanizeModelID(id string) string {
	id = stripModelIDBrackets(id)
	if id == "" {
		return ""
	}
	var parts []string
	for _, seg := range strings.Split(id, "-") {
		if seg == "" {
			continue
		}
		// Coalesce a run of numeric segments ("4","8" -> "4.8") into the prior part.
		if isNumericModelSegment(seg) && len(parts) > 0 && isNumericModelSegment(parts[len(parts)-1]) {
			parts[len(parts)-1] += "." + seg
			continue
		}
		parts = append(parts, seg)
	}
	for i, p := range parts {
		if up, ok := modelDisplayNameAcronyms[strings.ToLower(p)]; ok {
			parts[i] = up
		} else {
			parts[i] = providerkit.CapitalizeFirst(p)
		}
	}
	return strings.Join(parts, " ")
}

package agent

// acp_text.go holds the pure string helpers for humanizing model ids and option labels. Split
// out of acp_common.go; these depend on nothing but the standard library.

import (
	"strings"
	"unicode"
)

// capitalizeFirst returns s with its first rune upper-cased.
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	for _, r := range s {
		return string(unicode.ToUpper(r)) + s[len(string(r)):]
	}
	return s
}

// normalizeOptionName trims an ACP option's server-reported display name and
// treats a blank or id-equal name as absent (""), so titleCaseID falls back to
// title-casing the id. Applied on both the handshake (buildPrimaryAgentOptions)
// and runtime (buildConfigOptionSelect) option-building paths so an option renders
// identically regardless of which path produced it -- OpenCode-family agents often
// report name == id or whitespace-only names.
func normalizeOptionName(name, id string) string {
	name = strings.TrimSpace(name)
	if name == id {
		return ""
	}
	return name
}

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
// capable sibling of titleCaseID: a model id needs the version-number coalescing that a
// plain option label (which titleCaseID handles) does not.
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
			parts[i] = capitalizeFirst(p)
		}
	}
	return strings.Join(parts, " ")
}

// titleCaseID returns name if it is a distinct display name (non-empty and
// different from id). Otherwise it title-cases the id by splitting on
// underscores or hyphens, capitalizing each word, and joining with spaces
// (e.g. "smart_approve" → "Smart Approve", "full-auto" → "Full Auto").
func titleCaseID(id, name string) string {
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
		parts[i] = capitalizeFirst(p)
	}
	return strings.Join(parts, " ")
}

// thoughtChunkSeparator returns the string (if any) that should be inserted
// between an existing buffer and the next chunk to keep paragraph structure
// intact at chunk seams.
//
// The wire format does not expose reasoning-part boundaries (the ACP
// `agent_thought_chunk` notification only carries `messageId`, never the
// underlying part ID). When the boundary lines up with a chunk seam — most
// commonly on session replay, where each complete reasoning part arrives as
// one notification — naive concatenation glues "previous sentence." onto
// "**Next title**" or "feedback." onto "The proposed". We detect that with
// two complementary heuristics:
//
//  1. The new chunk's first non-empty line is a markdown bold heading
//     (^\*\*[^*]+\*\*$) or ATX heading (^#{1,6} ).
//  2. The seam glues sentence-ending punctuation (.?!) directly onto a
//     capital letter or `**`, with no whitespace on either side.
//
// Live token-by-token deltas usually have whitespace on at least one side of
// the seam (or split mid-word), so they don't trigger either heuristic.
func thoughtChunkSeparator(buffer, chunk string) string {
	if buffer == "" || chunk == "" {
		return ""
	}
	// Already separated by paragraph break across the seam.
	trailingNL := countTrailing(buffer, '\n')
	leadingNL := countLeading(chunk, '\n')
	if trailingNL+leadingNL >= 2 {
		return ""
	}

	if looksLikeMarkdownHeading(firstNonEmptyLine(chunk)) {
		// Pad with whatever newlines the seam doesn't already provide.
		return strings.Repeat("\n", 2-(trailingNL+leadingNL))
	}

	// Sentence-end glued to capital letter or bold marker, with no
	// whitespace at the seam. e.g. "...feedback." + "The proposed..."
	if trailingNL == 0 && leadingNL == 0 {
		bufLast := lastRune(buffer)
		chunkFirst := firstRune(chunk)
		if isSentenceEnd(bufLast) && (unicode.IsUpper(chunkFirst) || strings.HasPrefix(chunk, "**")) &&
			!unicode.IsSpace(bufLast) && !unicode.IsSpace(chunkFirst) {
			return "\n\n"
		}
	}
	return ""
}

func countTrailing(s string, r byte) int {
	n := 0
	for i := len(s) - 1; i >= 0 && s[i] == r; i-- {
		n++
	}
	return n
}

func countLeading(s string, r byte) int {
	n := 0
	for i := 0; i < len(s) && s[i] == r; i++ {
		n++
	}
	return n
}

func firstNonEmptyLine(s string) string {
	for {
		nl := strings.IndexByte(s, '\n')
		var line string
		if nl < 0 {
			line = s
			s = ""
		} else {
			line = s[:nl]
			s = s[nl+1:]
		}
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
		if s == "" {
			return ""
		}
	}
}

// looksLikeMarkdownHeading recognises a line that is either a fully wrapped
// markdown bold heading (`**Title**`) or an ATX heading (`# Title`).
// Inline bold within prose (a delta like just `**` or `**word`) does not
// match because the line must both start AND end with `**`.
func looksLikeMarkdownHeading(line string) bool {
	if line == "" {
		return false
	}
	if strings.HasPrefix(line, "**") && strings.HasSuffix(line, "**") && len(line) >= 5 {
		inner := line[2 : len(line)-2]
		if strings.TrimSpace(inner) != "" && !strings.Contains(inner, "**") {
			return true
		}
	}
	if strings.HasPrefix(line, "#") {
		hashes := 0
		for hashes < len(line) && line[hashes] == '#' {
			hashes++
		}
		if hashes >= 1 && hashes <= 6 && hashes < len(line) && line[hashes] == ' ' {
			return true
		}
	}
	return false
}

func isSentenceEnd(r rune) bool {
	switch r {
	case '.', '!', '?':
		return true
	}
	return false
}

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

func lastRune(s string) rune {
	var last rune
	for _, r := range s {
		last = r
	}
	return last
}

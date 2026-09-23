package cursor

import (
	"math"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
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

func normalizeCursorModelID(model string) string {
	if model == cursorCLIModelAutoWire {
		return cursorCLIModelAuto
	}
	return model
}

// cursorModelBracketParams extracts the key=value metadata Cursor bakes into a model
// id's trailing brackets, e.g. "claude-fable-5[thinking=true,context=300k,effort=high]"
// -> {thinking:true, context:300k, effort:high}. Returns nil when there is no bracket
// or it is empty (e.g. "default[]"). The bracketed id IS the wire id Cursor expects, so
// callers parse it for display metadata without rewriting the id.
func cursorModelBracketParams(id string) map[string]string {
	open := strings.IndexByte(id, '[')
	if open < 0 || !strings.HasSuffix(id, "]") {
		return nil
	}
	inner := id[open+1 : len(id)-1]
	if inner == "" {
		return nil
	}
	params := make(map[string]string)
	for _, pair := range strings.Split(inner, ",") {
		if k, v, ok := strings.Cut(pair, "="); ok && strings.TrimSpace(k) != "" {
			params[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return params
}

// parseCursorContextWindow parses a Cursor context value like "300k"/"272k"/"200000"
// into a token count, or 0 when unparseable.
func parseCursorContextWindow(v string) int64 {
	if v == "" {
		return 0
	}
	mult := int64(1)
	switch v[len(v)-1] {
	case 'k', 'K':
		mult, v = 1000, v[:len(v)-1]
	case 'm', 'M':
		mult, v = 1_000_000, v[:len(v)-1]
	}
	n, err := strconv.ParseFloat(v, 64)
	// ParseFloat accepts "inf"/"nan"; int64(±Inf) and int64(NaN) are
	// implementation-defined, so reject non-finite values rather than surfacing a
	// garbage context window in the picker.
	if err != nil || n <= 0 || math.IsInf(n, 0) || math.IsNaN(n) {
		return 0
	}
	// A finite but out-of-int64-range value (an absurd server-reported context like
	// "99999999999999999999k") also converts to an implementation-defined garbage int64
	// (it saturates to MaxInt64 on arm64, wraps to MinInt64 on amd64), so reject it too.
	// float64(math.MaxInt64) rounds up to 2^63, so >= catches everything that overflows.
	scaled := n * float64(mult)
	if scaled >= float64(math.MaxInt64) {
		return 0
	}
	return int64(scaled)
}

// decorateCursorModel surfaces the metadata Cursor bakes into a model id (which the
// server reports only inside the opaque bracketed id, not in the model's name) as the
// ModelInfo's ContextWindow and a human-readable Description, so the picker shows the
// effort / reasoning / extended-thinking / context window each variant carries. It also
// replaces the server's bare-id model name with a friendly display name.
func decorateCursorModel(m *agent.ModelInfo) {
	// Cursor's server reports a model's name as the bare bracket-less id
	// ("composer-2.5", "claude-opus-4-8"); humanize it into a friendly display name when
	// the server didn't already supply a better one (it does only for "Auto"). Done
	// before the params early-return so bracket-less variants ("gemini-3.1-pro[]") are
	// humanized too.
	humanized := false
	if bare := stripModelIDBrackets(m.Id); m.DisplayName == "" || strings.EqualFold(m.DisplayName, bare) {
		m.DisplayName = humanizeModelID(m.Id)
		humanized = true
	}
	params := cursorModelBracketParams(m.Id)
	if len(params) == 0 {
		return
	}
	if cw := parseCursorContextWindow(params["context"]); cw > 0 {
		m.ContextWindow = cw
	}
	// Append the variant's distinguishing attribute to the display name so two variants of
	// the same base model don't collapse to identical picker labels: the reasoning-effort
	// level when present ("GPT 5.5" -> "GPT 5.5 Medium"), else the extended-thinking or fast
	// flag ("Composer 2.5" -> "Composer 2.5 Fast"). Only an AUTO-humanized name is suffixed
	// -- a real server-provided name is trusted to already disambiguate, so it is left as-is
	// (no "Composer 2.5 (Fast) Fast"). The fuller form stays in the tooltip Description below.
	if humanized {
		if suffix := cursorModelNameSuffix(params); suffix != "" {
			m.DisplayName += " " + suffix
		}
	}
	var parts []string
	if params["thinking"] == "true" {
		parts = append(parts, "Extended thinking")
	}
	// Cursor spells a model's reasoning-effort level three ways and means one thing by all
	// of them (cursorReasoningAttribute), mutually exclusive in practice. Show it ONCE, in
	// that function's order, so this tooltip cannot disagree with the name suffix, which
	// reads the same function. (A model reporting more than one -- which would contradict
	// the same-concept assumption -- then renders consistently in both places.)
	if level, noun := cursorReasoningAttribute(params); level != "" {
		parts = append(parts, providerkit.CapitalizeFirst(level)+" "+noun)
	}
	if params["fast"] == "true" {
		parts = append(parts, "Fast")
	}
	if len(parts) == 0 {
		return
	}
	suffix := strings.Join(parts, " · ")
	if m.Description != "" {
		m.Description += " · " + suffix
	} else {
		m.Description = suffix
	}
}

// cursorReasoningAttribute returns a model's reasoning-effort level and the noun that
// renders it. Cursor's catalogue spells the one concept three ways -- "effort" on Claude
// models, "reasoning" on GPT models, "reasoning_effort" on Grok -- so this is the single
// place that states their order of preference, and both the name suffix and the tooltip
// read it. Returns "" when the id carries none of the three.
//
// "reasoning_effort" is an effort level and says so in Cursor's own tooltip ("low
// effort"), so it takes the effort noun rather than a third wording.
func cursorReasoningAttribute(params map[string]string) (level string, noun string) {
	if level := params["effort"]; level != "" {
		return level, "effort"
	}
	if level := params["reasoning"]; level != "" {
		return level, "reasoning"
	}
	return params["reasoning_effort"], "effort"
}

// cursorReasoningLevel returns the level alone, for the callers that render it through
// the shared effort-label table rather than as a tooltip sentence.
func cursorReasoningLevel(params map[string]string) string {
	level, _ := cursorReasoningAttribute(params)
	return level
}

// cursorModelNameSuffix returns the short distinguishing suffix for a model variant's
// display name, preferring the reasoning-effort level (cased like cursorEffortLabel),
// then the extended-thinking flag, then the fast flag. Returns "" when the variant carries
// no distinguishing attribute. Keeps variants of the same base model from rendering as
// identical labels in the picker; the fuller attribute list lives in the Description.
func cursorModelNameSuffix(params map[string]string) string {
	if level := cursorReasoningLevel(params); level != "" {
		return cursorEffortLabel(level)
	}
	if params["thinking"] == "true" {
		return "Thinking"
	}
	if params["fast"] == "true" {
		return "Fast"
	}
	return ""
}

// cursorEffortLabel renders a Cursor reasoning-effort level for the model-name
// suffix. It reads the shared table, so an id Cursor shows in a model name and
// the same id in another provider's effort picker cannot be spelled differently
// -- "xhigh" used to render "XHigh" here and "Extra High" everywhere else.
func cursorEffortLabel(level string) string {
	return providerkit.EffortLabel(level)
}

func cursorModelIDForWire(model string) string {
	if model == cursorCLIModelAuto {
		return cursorCLIModelAutoWire
	}
	return model
}

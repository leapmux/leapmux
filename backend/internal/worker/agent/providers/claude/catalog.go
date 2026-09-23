package claude

import (
	"slices"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// claudeCodeModelInfo is one entry of the `models` array in the Claude Code
// initialize control response (SDK schema ModelInfoSchema). Only the fields
// LeapMux consumes are decoded; adaptive-thinking/fast-mode/auto-mode flags are
// derived separately (modelSupportsAdaptiveThinking) and omitted here.
type claudeCodeModelInfo struct {
	Value                 string   `json:"value"`                 // id passed to --model / set_model (e.g. "opus[1m]"); "default" is an alias sentinel
	DisplayName           string   `json:"displayName"`           // e.g. "Opus (1M context)"
	Description           string   `json:"description"`           // capability blurb shown on hover
	SupportsEffort        bool     `json:"supportsEffort"`        // false ⇒ no effort selector (e.g. Haiku)
	SupportedEffortLevels []string `json:"supportedEffortLevels"` // CLI levels, weakest→strongest: low|medium|high|xhigh|max
	Disabled              bool     `json:"disabled"`              // visible but not selectable; dropped during conversion
}

// ensureSettledModelListed adds the settled model to the dynamic picker catalog when
// the CLI's selectable list omits it but the static fallback fully describes it. The
// account-default sentinel can resolve to a concrete model the CLI does NOT surface
// as a separately selectable row -- an account whose default is Opus, which Claude
// Code exposes only behind "default", is the motivating case. After
// refreshSettingsFromAgent settles a.model onto that concrete id, the picker
// (effortCatalog renders a.availableModels verbatim) would show no matching row,
// leaving the model unnamed in the trigger, unselected in the RadioGroup, and without
// an effort menu. Inserting the static-catalog entry fixes all three from the backend
// alone -- the frontend lookups (modelDisplayName, the model RadioGroup's current
// value, effortItems) all key on a.model and now find it.
//
// We inject ONLY a model the static catalog lists, never a synthesized placeholder
// for a model in neither catalog. A model in neither catalog is already "unknown" to
// effortResolver.definedEfforts (which scans both catalogs), and the effort/ultracode
// trust path deliberately TRUSTS an unknown model's CLI report rather than clamping it
// (see effortFromApplied / updateFlagSettings). Injecting an effort-less placeholder
// would flip definedEfforts to "known with no efforts", silently downgrading a session
// the CLI is running at ultracode/xhigh -- the exact relabel those methods guard
// against. A static-catalog model is the safe set: it is ALREADY known via the
// fallback, so injecting the same entry changes no resolver verdict; it only makes the
// picker show what the resolver could already speak to. A genuinely new model (one
// that postdates the static catalog and the CLI hides behind "default") stays unlisted
// -- the lesser evil -- until it is added to the static catalog, the way Fable was.
//
// Runs only from runStartupHandshake, on the Start goroutine in the
// pre-registration window: a.availableModels is written only here during startup
// (convertClaudeModels, then this), and the lock-free readers either run on this same
// goroutine (refreshSettingsFromAgent) or only on a later user turn, which happens-
// after the agent is registered. So no a.Mu is needed and no concurrent reader exists.
//
// No-op when:
//   - the model is unresolved (empty, or still the literal sentinel because
//     get_settings degraded -- see refreshSettingsFromAgent);
//   - the dynamic list is empty (old CLI / parse failure): effortCatalog already
//     falls back to the static catalog, which lists every shipped model, and
//     appending one entry here would REPLACE that full fallback with a singleton;
//   - the settled model is already listed (the common case: the default resolved to
//     a listed model, or the user pinned one);
//   - the model is in neither catalog (see above -- left unlisted on purpose).
func (a *Agent) ensureSettledModelListed() {
	if agent.UsesAccountDefaultModel(a.model) {
		return
	}
	if len(a.availableModels) == 0 || agent.FindAvailableModel(a.availableModels, a.model) != nil {
		return
	}
	entry := agent.FindAvailableModel(claudeCodeAvailableModels, a.model)
	if entry == nil {
		return
	}
	// Place the resolved model at its CANONICAL slot -- the position it holds in the
	// static catalog's most->least-powerful ordering (sentinel, Fable, Opus, Sonnet,
	// Haiku) -- rather than right after the sentinel. The CLI's own selectable list
	// already follows that ordering, so inserting the resolved model before the first
	// listed model that outranks it drops it exactly where the static catalog puts it:
	// opus[1m] (which the CLI hides behind "default") lands AFTER Fable, not jammed
	// between the sentinel and Fable. A naive "right after the sentinel" insert put a
	// resolved Opus ahead of Fable, contradicting the picker's documented order. Models
	// the static catalog doesn't know (a future dynamic-only id) rank last, so the
	// resolved static model sorts ahead of them. The inserted pointer is the shared
	// static-catalog entry, read only exactly as effortCatalog hands out
	// claudeCodeAvailableModels directly -- agent.ModelOptionGroup projects it into fresh
	// protos and withModelGroupDefaultMarked clones before touching them.
	rank := canonicalModelRank(a.model)
	insertAt := len(a.availableModels)
	for i, m := range a.availableModels {
		if canonicalModelRank(m.GetId()) > rank {
			insertAt = i
			break
		}
	}
	a.availableModels = slices.Insert(a.availableModels, insertAt, entry)
}

// canonicalModelRank returns modelID's index in the static claudeCodeAvailableModels
// catalog, whose order IS the canonical most->least-powerful picker ordering (the
// account-default sentinel first, then Fable, Opus, Sonnet, Haiku). A model the static
// catalog does not list (a future dynamic-only id) ranks last so it sorts after every
// catalog-known model. ensureSettledModelListed uses it to drop a resolved-but-unlisted
// model into its canonical slot instead of right after the sentinel.
func canonicalModelRank(modelID string) int {
	if i := slices.IndexFunc(claudeCodeAvailableModels, func(m *agent.ModelInfo) bool {
		return m.GetId() == modelID
	}); i >= 0 {
		return i
	}
	return len(claudeCodeAvailableModels)
}

// availableModelCatalog returns the Claude Code model/effort catalog projected
// into OptionGroups: the per-agent list discovered from the initialize response
// when present, else the static claudeCodeAvailableModels fallback (see
// effortCatalog). Returns nil when a third-party LLM provider is detected (from
// settings at startup, or the shell wrapper's can_change_model_and_effort=false
// metadata), which omits the model/effort groups so the frontend hides them.
//
// The returned slice and every ModelInfo/EffortInfo it points at are shared,
// immutable catalog data: the same providerkit.EffortTier* pointers back multiple model
// slices (both the static catalog and the converted dynamic list), so a mutation
// through any returned entry would corrupt every model that shares it. Callers
// MUST treat the result as read-only; copy before mutating.
func (a *Agent) availableModelCatalog() []*agent.ModelInfo {
	if a.hidesModelEffortUI() {
		return nil
	}
	return a.effortCatalog()
}

// hidesModelEffortUI reports whether this session presents no model/effort UI: a
// third-party LLM provider detected from settings at startup, or one the shell
// wrapper flagged via can_change_model_and_effort=false. AvailableModels returns
// nil in that case (hiding the model/effort settings), and the startup effort
// reconcile is skipped, so a session whose user can neither see nor control effort
// is never pushed an effort/ultracode apply_flag_settings.
func (a *Agent) hidesModelEffortUI() bool {
	return a.thirdPartyFromSettings || a.PreambleMetaValue(claudeMetaCanChangeModelAndEffort) == "false"
}

// effortCatalog returns the dynamic-first model list backing AvailableModels: the
// per-agent list discovered from the initialize response when present, else the
// static claudeCodeAvailableModels fallback. AvailableModels layers the third-party
// gate on top; effortCatalog itself is ungated, so that gate lives in one place.
//
// This is the picker view -- a whole-list dynamic-or-static swap that shows only the
// models the CLI reported. Effort/ultracode/context-window resolution does NOT use it:
// those go through effortResolver, which carries the static catalog as a PER-ENTRY
// fallback so a model the live CLI dropped from its list still resolves its
// capabilities and window.
//
// availableModels is written only during the pre-registration startup handshake and
// never mutated afterward, so this read is safe without a.Mu.
func (a *Agent) effortCatalog() []*agent.ModelInfo {
	if len(a.availableModels) > 0 {
		return a.availableModels
	}
	return claudeCodeAvailableModels
}

// normalizeClaudeCodeModel collapses the fully-qualified model ID that
// Claude Code's get_settings "applied.model" field returns (e.g.
// "claude-opus-4-7", "claude-haiku-4-5-20251001", "claude-sonnet-4-6[1m]")
// back to the short alias leapmux uses (opus[1m], sonnet, sonnet[1m], haiku).
// Short aliases pass through unchanged.
//
// Rules:
//   - Strip an optional "claude-" prefix.
//   - Preserve a trailing "[...]" bracket suffix (the 1M-context marker).
//   - Keep only the leading alphabetic token (opus/sonnet/haiku), dropping
//     version numbers (e.g. "-4-7") and date suffixes (e.g. "-20251001").
//   - Fable and Opus ship only as 1M-context models, so every spelling of
//     either collapses to "fable[1m]" / "opus[1m]" regardless of suffix --
//     bare "opus" (the legacy standard-context alias the CLI no longer lists)
//     is canonicalized to "opus[1m]" like every other Opus spelling.
func normalizeClaudeCodeModel(model string) string {
	if model == "" {
		return ""
	}
	// Lowercase first so a mixed-case CLI value (e.g. "OPUS[1M]", "Claude-Sonnet")
	// collapses to the same canonical alias the static catalog and a.model use; the
	// catalog id space is all lowercase, so an uppercased value would otherwise
	// never match its own entry.
	core := strings.TrimPrefix(strings.ToLower(model), "claude-")
	var suffix string
	if i := strings.IndexByte(core, '['); i >= 0 {
		suffix = core[i:]
		core = core[:i]
	}
	// The family alias is the first run of [a-z], AFTER skipping any leading non-alpha
	// (digits/hyphens). Family-first ids ("opus-4-6") have the family first, but a
	// version-first id ("3-5-sonnet") leads with numeric version tokens; skipping them
	// finds the family in either layout, where a from-position-0 scan returned "" for
	// the version-first shape and leaked the raw id (so a running version-first model
	// never matched its own catalog entry). A purely-numeric core ("123") still yields
	// "" and falls through to the raw-display fallback below.
	start := 0
	for start < len(core) {
		if c := core[start]; c >= 'a' && c <= 'z' {
			break
		}
		start++
	}
	end := start
	for end < len(core) {
		if c := core[end]; c < 'a' || c > 'z' {
			break
		}
		end++
	}
	alias := core[start:end]
	if alias == "" {
		// Unrecognized shape — return the original input unchanged so the
		// caller can still display it.
		return model
	}
	// Fable and Opus ship only as 1M-context models, and their canonical ids
	// carry the "[1m]" marker -- matching what the live CLI reports
	// ("claude-fable-5[1m]", "claude-opus-4-8[1m]"). There is no standard-context
	// Fable, and the standard-context Opus is a legacy id the live CLI no longer
	// lists, so every spelling of either (bare "fable"/"opus" from an operator
	// override or an older CLI listing, a fully-qualified value, an already-"[1m]"
	// id, or a "[1m-beta]"-style decoration) collapses to "<family>[1m]" so a
	// running Fable/Opus always matches its own catalog entry instead of splitting
	// into "<family>" vs "<family>[1m]". If a standard-context Opus is ever
	// reintroduced, this collapse must be revisited.
	if alias == "fable" || alias == "opus" {
		return alias + "[1m]"
	}
	return alias + suffix
}

// modelSupportsAdaptiveThinking reports whether Claude Code emits
// thinking.type:"adaptive" for this model (Opus and Sonnet) versus
// thinking.type:"enabled" with a budget (Haiku). Unknown model strings
// default to true to match Claude Code's first-party fallback. This is
// used purely to pick the "Adaptive" vs "On" display label in
// AvailableOptionGroups — the wire payload (alwaysThinkingEnabled) is
// identical either way.
//
// Expects the short alias (e.g. "opus", "haiku"). `a.model` is kept
// normalized after every refreshSettingsFromAgent call, so internal
// callers can pass it directly without re-normalizing.
func modelSupportsAdaptiveThinking(model string) bool {
	return !strings.HasPrefix(model, "haiku")
}

// Claude Code effort levels are model-dependent. Keep each slice ordered
// strongest → weakest so the RadioGroup renders in the same order. Descriptions
// mirror Claude Code's own effort copy (binary MP5()/ultracode strings) so the
// LeapMux selector reads identically to the CLI's /effort menu.

// Claude context-window sizes. The CLI does not report a window, so we infer it
// from the model id (see claudeContextWindowForValue). Naming the two values
// single-sources the "[1m]-suffix ⇒ 1M, else 200K" rule shared by the static
// catalog entries, claudeContextWindowForValue, and the unresolved-sentinel
// fallback in extractAndBroadcastUsage.
const (
	claudeStandardContextWindow   = 200_000
	claudeOneMillionContextWindow = 1_000_000
)

// claudeCodeAvailableModels is the static model catalog. It is the source of
// truth for DefaultModel(provider) (registry defaultModels) and the fallback
// OptionGroups()'s model projection uses when the per-agent dynamic catalog is empty
// (old CLI, third-party provider, or parse failure). When the live CLI reports its
// own catalog, the dynamic list (convertClaudeModels) supersedes this.
//
// The leading DefaultModelSentinel entry is the IsDefault choice: a new tab (and
// any account, including non-Opus tiers) starts on it, and buildModelEffortArgs
// omits --model so the CLI resolves it to that account's concrete default --
// which get_settings then reports back, so the tab settles on the real model
// after startup. agent.AccountDefaultModelEntry states why it carries no efforts.
// The concrete models follow in
// most→least powerful order, matching Claude Code's own ordering ("Fable for the
// hardest problems, Opus for complex work, Sonnet for most tasks, Haiku for
// quick questions").
var claudeCodeAvailableModels = []*agent.ModelInfo{
	agent.AccountDefaultModelEntry("Use your account's default model"),
	// Fable 5 is 1M-context only; its canonical id carries the [1m] marker so it
	// matches the live CLI's "claude-fable-5[1m]" (normalizeClaudeCodeModel
	// collapses every Fable spelling, bare "fable" included, to "fable[1m]"). The
	// display name omits "(1M context)" -- there is no standard-context Fable to
	// distinguish it from.
	{Id: "fable[1m]", DisplayName: "Fable 5", Description: "Most powerful for the hardest problems", DefaultEffort: agent.EffortXHigh, SupportedEfforts: claudeEffortXHighMax, ContextWindow: claudeOneMillionContextWindow},
	// opus is the legacy standard-context alias. normalizeClaudeCodeModel now
	// collapses every Opus spelling (bare "opus" included) to "opus[1m]", so no
	// path resolves to this entry by id anymore -- it is retained purely as a
	// Hidden, exact-match-only safety net for any un-normalized legacy id that
	// might still reach a FindAvailableModel lookup. FindAvailableModel matches
	// raw ids, so this entry can never shadow the selectable opus[1m] below.
	// Hidden from the picker: the live CLI no longer lists the standard-context
	// Opus -- only opus[1m] -- so the static fallback must not resurrect it as a
	// selectable option.
	{Id: "opus", DisplayName: "Opus", Description: "Most capable for complex work", DefaultEffort: agent.EffortXHigh, SupportedEfforts: claudeEffortXHighMax, ContextWindow: claudeStandardContextWindow, Hidden: true},
	{Id: "opus[1m]", DisplayName: "Opus (1M context)", Description: "Most capable for complex work", DefaultEffort: agent.EffortXHigh, SupportedEfforts: claudeEffortXHighMax, ContextWindow: claudeOneMillionContextWindow},
	// Sonnet carries the xhigh tiers because the live CLI reports
	// "low,medium,high,xhigh,max" for it, and claudeDefaultEffort resolves that
	// level set to xhigh. Declaring max-only here made the fallback disagree with
	// the session: the effort menu opened without xhigh/ultracode and grew both
	// the moment any settings change replaced the fallback with the live catalog.
	{Id: "sonnet", DisplayName: "Sonnet", Description: "Best for everyday tasks", DefaultEffort: agent.EffortXHigh, SupportedEfforts: claudeEffortXHighMax, ContextWindow: claudeStandardContextWindow},
	{Id: "sonnet[1m]", DisplayName: "Sonnet (1M context)", Description: "Best for everyday tasks", DefaultEffort: agent.EffortXHigh, SupportedEfforts: claudeEffortXHighMax, ContextWindow: claudeOneMillionContextWindow},
	{Id: "haiku", DisplayName: "Haiku", Description: "Fastest for quick answers", ContextWindow: claudeStandardContextWindow},
}

// convertClaudeModels converts the model list from the Claude Code initialize
// response into LeapMux's AvailableModel catalog, mirroring Codex's
// queryAvailableModels: efforts are ordered strongest→weakest with an "auto"
// sentinel prepended, and the LeapMux-only "ultracode" tier is offered for any
// model whose CLI effort levels include xhigh (ultracode == xhigh + standing
// workflow orchestration, so xhigh support is the entitlement we key off).
//
// Entries the user can't actually select are dropped: disabled entries and
// anything reported in unavailable_models. The DefaultModelSentinel ("default")
// entry IS surfaced -- it is a real "let the CLI pick the account default"
// option (the model-side analogue of EffortAuto); buildModelEffortArgs omits
// --model for it and withModelGroupDefaultMarked gives its projected option the
// default badge. IsDefault is left unset here -- the manager applies it. Returns nil when
// models is empty so OptionGroups()'s model projection falls back to the static catalog.
//
// The returned entries reuse the shared providerkit.EffortTier* pointers, so the same
// read-only contract as claudeCodeAvailableModels applies (see OptionGroups).
func convertClaudeModels(models, unavailable []claudeCodeModelInfo) []*agent.ModelInfo {
	if len(models) == 0 {
		return nil
	}
	skip := buildModelSkipSet(unavailable)
	out := make([]*agent.ModelInfo, 0, len(models))
	seen := make(map[string]bool, len(models))
	sentinelSeen := false
	for _, m := range models {
		if m.Value == "" || m.Disabled {
			continue
		}
		// The account-default sentinel is identified by its RAW value ("default")
		// and OWNS the reserved DefaultModelSentinel id deterministically. Route it
		// before normalizing so its dedup can't race a concrete model that merely
		// normalizes to "default" (below); keep only the first occurrence. This also
		// precedes the unavailable_models (skip) filter -- the account default is
		// always selectable, so a "default" reported in unavailable_models is ignored
		// (a disabled:true sentinel IS dropped, via the m.Disabled guard above).
		if isDefaultSentinel(m.Value) {
			if !sentinelSeen {
				sentinelSeen = true
				out = append(out, convertClaudeModel(m, agent.DefaultModelSentinel))
			}
			continue
		}
		// Normalize the CLI value into the same alias space a.model lives in
		// (refreshSettingsFromAgent stores normalizeClaudeCodeModel(applied.model)).
		// The CLI may report a fully-qualified value (e.g. "claude-fable-5[1m]")
		// for the account-resolved model; storing it verbatim would mean a running
		// model never matches its own catalog entry, breaking effort/ultracode
		// lookups and the frontend's effort selector and display name.
		id := normalizeClaudeCodeModel(m.Value)
		// A concrete model can't claim the sentinel's reserved id: launch
		// (buildModelEffortArgs) and the badge logic (defaultModelIDForList) treat
		// id=="default" as the sentinel, so a concrete model whose value merely
		// normalizes to "default" (e.g. "default5") would be mishandled as the
		// sentinel. Drop it -- deterministically, regardless of CLI ordering --
		// rather than letting it masquerade.
		if id == agent.DefaultModelSentinel {
			continue
		}
		// Drop unavailable and duplicate entries, both keyed by normalized id: an
		// unavailable model reported under a different spelling than its models
		// entry is still filtered, and two entries that normalize to the same id
		// (or a malformed payload repeating a value) collapse to one rather than
		// rendering twice and making the catalog lookups (first match wins)
		// ambiguous.
		if skip[id] || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, convertClaudeModel(m, id))
	}
	return out
}

// buildModelSkipSet collects the normalized ids of unavailable_models so
// convertClaudeModels can filter them. Keying by normalized id, not raw value,
// matters because the models and unavailable_models arrays may spell the same
// model differently (one fully-qualified like "claude-fable-5[1m]", one aliased
// like "fable[1m]"); both collapse to the same id, so matching on the raw value
// could leak an unavailable model through.
func buildModelSkipSet(unavailable []claudeCodeModelInfo) map[string]bool {
	skip := make(map[string]bool, len(unavailable))
	for _, m := range unavailable {
		if m.Value == "" {
			continue
		}
		skip[normalizeClaudeCodeModel(m.Value)] = true
	}
	return skip
}

// convertClaudeModel converts one CLI model entry (already normalized to id) into
// an AvailableModel. IsDefault is left unset -- the manager applies it. The caller
// (convertClaudeModels) owns sentinel routing: it passes id == DefaultModelSentinel
// only for the genuine account-default entry (matched on the RAW value) and has
// already dropped any concrete model whose value merely normalizes to "default", so
// the id check below is exact -- no need to re-examine the raw value here.
func convertClaudeModel(m claudeCodeModelInfo, id string) *agent.ModelInfo {
	displayName := m.DisplayName
	if displayName == "" {
		displayName = claudeFallbackDisplayName(id)
	}
	am := &agent.ModelInfo{
		Id:          id,
		DisplayName: displayName,
		Description: m.Description,
	}
	// The account-default sentinel carries no efforts or context window: those
	// belong to the concrete model it resolves to, which isn't known until after
	// startup. This mirrors the static catalog's "default" entry and keeps a fresh
	// launch from forwarding an --effort/ultracode the resolved model may not
	// support (the CLI reports the sentinel WITH a full effort menu, which we
	// deliberately drop here).
	if id == agent.DefaultModelSentinel {
		return am
	}
	supported := normalizedEffortLevelSet(m)
	am.DefaultEffort = claudeDefaultEffort(supported)
	am.SupportedEfforts = claudeSupportedEfforts(supported)
	am.ContextWindow = claudeContextWindowForValue(id)
	return am
}

// claudeFallbackDisplayName builds a display name from a normalized model id when
// the CLI omits one. It title-cases the alias and renders a 1M-context variant's
// bracket suffix as " (1M context)" -- matching the static catalog's "Opus (1M
// context)" rather than the raw "Opus[1m]" providerkit.TitleCaseID would otherwise produce.
// It detects the variant through is1MContextVariant (the single home for the "[1m]"
// marker) rather than a literal "[1m]" suffix, so a decorated spelling like
// "opus[1m-beta]" -- which claudeContextWindowForValue already sizes at 1M -- is
// gets a consistent name instead of a garbled "Opus[1m Beta]".
func claudeFallbackDisplayName(id string) string {
	if is1MContextVariant(id) {
		// is1MContextVariant guarantees a '[' (and a trailing ']'), so the bracket
		// group is the suffix to strip; LastIndexByte cannot return -1 here.
		return providerkit.TitleCaseID(id[:strings.LastIndexByte(id, '[')], "") + " (1M context)"
	}
	return providerkit.TitleCaseID(id, "")
}

// isDefaultSentinel reports whether a raw CLI model value is the account-default
// sentinel. Case-insensitive so a "Default" spelling is still recognized; matched
// against the RAW value (not the normalized id) so a concrete model that merely
// normalizes to "default" keeps its own efforts (see convertClaudeModel).
func isDefaultSentinel(value string) bool {
	return strings.EqualFold(value, agent.DefaultModelSentinel)
}

// claudeContextWindowForValue infers a model's context window from its id. The CLI
// does not report one; the only signal it exposes is the bracketed 1M-context marker
// (is1MContextVariant). Everything else uses Claude's standard 200K window. This
// follows the same suffix rule the concrete static-catalog entries use (the "default"
// sentinel is the exception -- it has no window until it resolves), so new models stay
// correct without a per-model table.
func claudeContextWindowForValue(value string) int64 {
	// Fable 5 is always 1M. Its canonical id is "fable[1m]" (caught by the suffix
	// rule below), but a raw, un-normalized "fable" can still reach here from an API
	// model id, so accept the bare alias too rather than mis-sizing it to 200K.
	if value == "fable" || is1MContextVariant(value) {
		return claudeOneMillionContextWindow
	}
	return claudeStandardContextWindow
}

// is1MContextVariant reports whether a model id carries the CLI's 1M-context marker: a
// bracketed suffix whose content begins with "1m" (case-insensitive). This matches the
// plain "[1m]" the CLI ships today and tolerates a decorated spelling ("[1M]",
// "[1m-beta]", "[1m-preview]") so a future labelling of the 1M beta still resolves to
// the larger window instead of silently reporting the 200K standard one. Anchored on a
// trailing bracket group, so a stray "1m" elsewhere in the id can't false-positive.
// This is THE single place that recognizes the marker -- widen it here, not at call
// sites, if the CLI changes the spelling.
func is1MContextVariant(id string) bool {
	open := strings.LastIndexByte(id, '[')
	if open < 0 || !strings.HasSuffix(id, "]") {
		return false
	}
	inner := strings.ToLower(id[open+1 : len(id)-1])
	return strings.HasPrefix(inner, "1m")
}

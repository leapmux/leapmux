package claude

import (
	"slices"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// reconcileStartupFlags returns the apply_flag_settings that bring a freshly launched
// model+effort into agreement with this (dynamic) resolver. The effort was resolved
// at LAUNCH against the static catalog (the dynamic one wasn't known pre-init); the
// model/effort passed here still hold the launch values, since refreshSettingsFromAgent
// runs later. We reconstruct what launch sent as a launchEffortPlan (against the static
// catalog) and compare it against r (dynamic, with the static fallback that still
// recognizes a model the live CLI filtered out):
//
//   - Omitted: launch sent no --effort, so the CLI is at its own default. Usually
//     nothing to do -- except the S9 case handled by reconcileOmittedLaunch.
//   - Ultracode: the resolver confirms ultracode for the model, so complete the
//     combo (re-pin effortLevel:xhigh + ultracode:true) whether launch sent the
//     xhigh base (re-pin) or a lower one (upgrade). The launch path deferred the
//     ultracode boolean to here -- it is applied nowhere else -- so this also
//     re-enables a filtered model's ultracode the static fallback still vouches for.
//     Re-pinning is safe even if the live CLI no longer offers xhigh for the model:
//     Claude Code clamps an unsupported effortLevel to "high" at resolution and never
//     rejects apply_flag_settings (verified against the 2.1.170 binary), so an
//     over-pin degrades gracefully instead of stranding the session.
//   - Downgrade: the dynamic catalog runs the model at a different level than launch
//     sent (e.g. the live CLI dropped xhigh), so emit the corrected level and clear
//     ultracode defensively.
//
// Returns nil when launch and the dynamic resolution already agree (the common case)
// or when the model is unknown to both catalogs (nothing to reconcile against).
func (r effortResolver) reconcileStartupFlags(model, effort string) map[string]interface{} {
	plan := newEffortResolver(claudeCodeAvailableModels).planLaunch(model, effort)
	if plan.omitted {
		return r.reconcileOmittedLaunch(model, effort)
	}
	if _, known := r.definedEfforts(model); !known {
		// Unknown to both the dynamic catalog and the static fallback: we can't
		// reconcile against capabilities we don't have, so leave the session at
		// whatever --effort launch sent. (launchRunsUltracode implies known, so this
		// check can precede the ultracode/downgrade tail without dropping that case.)
		return nil
	}
	// Launch sent plan.level; emit only when the dynamic resolution differs from it.
	// The non-empty skipLevel also clears ultracode defensively on a downgrade.
	return r.reconciledEffortFlags(model, effort, plan.level)
}

// reconciledEffortFlags returns the apply_flag_settings that bring model/effort into
// agreement with this resolver: the xhigh+ultracode combo when the resolver confirms
// ultracode for the model, otherwise the resolved effortLevel. skipLevel suppresses
// emission when the resolved level already equals it (the startup "launch already
// sent this" no-op); pass "" to always emit a non-auto level (the omitted-launch
// path, which has no launch level to compare against). A non-empty skipLevel also
// means launch sent a level -- and so may have set an ultracode boolean a downgrade
// must undo -- so the effortLevel carries an explicit ultracode:false then; the
// omitted path (skipLevel "") sent no effort and so has no ultracode to clear.
// (clearUltracode is thus fully determined by skipLevel, not a separate knob.)
// Returns nil when there is nothing to apply (auto/empty resolution, or the level
// already matches skipLevel). Shared by reconcileStartupFlags and reconcileOmittedLaunch
// so the two can't drift on the ultracode-combo and auto-passthrough rules.
func (r effortResolver) reconciledEffortFlags(model, effort, skipLevel string) map[string]interface{} {
	if r.launchRunsUltracode(model, effort) {
		return ultracodeFlagSettings()
	}
	target := r.resolveEffort(model, effort)
	if target == "" || target == agent.EffortAuto || target == skipLevel {
		return nil
	}
	fs := map[string]interface{}{"effortLevel": target}
	if skipLevel != "" {
		fs["ultracode"] = false
	}
	return fs
}

// reconcileOmittedLaunch handles the launch-omitted-effort case. Launch sends no
// --effort for the sentinel, EffortAuto/"" (the CLI keeps its own default -- nothing
// to reconcile), and a model the STATIC catalog considers effort-less. The last is
// the only omit that leaves a CONCRETE stored effort on a real model, so it is the
// only one that can disagree with the dynamic catalog: if the live CLI actually
// offers efforts for that model (S9 -- a model effort-less in the static catalog but
// effort-bearing in the dynamic one), apply the dynamic-resolved effort so the live
// selector and the running session agree instead of silently running the CLI default.
func (r effortResolver) reconcileOmittedLaunch(model, effort string) map[string]interface{} {
	if effort == "" || effort == agent.EffortAuto || model == agent.DefaultModelSentinel {
		return nil
	}
	efforts, known := r.definedEfforts(model)
	if !known || len(efforts) == 0 {
		// The dynamic catalog agrees the model is effort-less (or doesn't know it):
		// nothing to apply.
		return nil
	}
	// Launch sent no --effort, so there is no launch level to match against (skipLevel
	// ""), which also signals there is no launch-sent ultracode boolean to clear.
	return r.reconciledEffortFlags(model, effort, "")
}

// ultracodeFlagSettings is the apply_flag_settings payload that enables the
// xhigh+ultracode combo. "ultracode" is not a real --effort value: the CLI
// models it as the boolean settings key `ultracode` layered on top of
// effortLevel:"xhigh". Shared by the startup path (buildStartupFlagSettings)
// and the live path (claudeEffortFlagSettings) so the wire encoding -- in
// particular the "xhigh base" -- is defined in exactly one place. Returns a
// fresh map each call so callers can mutate it freely.
func ultracodeFlagSettings() map[string]interface{} {
	return map[string]interface{}{"effortLevel": agent.EffortXHigh, "ultracode": true}
}

// claudeEffortFlagSettings returns the effortLevel/ultracode keys to merge into an
// apply_flag_settings payload to move curEffort -> newEffort. nil = no change.
//
// Selecting ultracode sends {effortLevel:"xhigh", ultracode:true} (see
// ultracodeFlagSettings); switching away from it sends the new level plus an
// explicit ultracode:false so the boolean is cleared.
func claudeEffortFlagSettings(newEffort, curEffort string) map[string]interface{} {
	if newEffort == "" || newEffort == agent.EffortAuto || newEffort == curEffort {
		return nil
	}
	if newEffort == EffortUltracode {
		return ultracodeFlagSettings()
	}
	fs := map[string]interface{}{"effortLevel": newEffort}
	if curEffort == EffortUltracode {
		fs["ultracode"] = false // explicitly clear when leaving ultracode
	}
	return fs
}

// updateFlagSettings builds the effort/ultracode portion of a live
// apply_flag_settings payload for moving curEffort -> the requested newEffort on
// targetModel (the model the change lands on). It resolves newEffort against the
// model first, so an unsupported combo can't be pushed to the CLI.
//
// The additional clause beyond claudeEffortFlagSettings handles the model-only change:
// when newEffort is empty there is no effort delta, so claudeEffortFlagSettings
// returns nil and the CLI's per-session `ultracode` boolean would persist even
// after switching onto a model that can't run it (e.g. opus+ultracode ->
// sonnet). We force ultracode:false whenever the session is leaving ultracode for
// a KNOWN model whose catalog doesn't offer it, so the boolean never outlives the
// model that supports it. A model unknown to BOTH catalogs is exempted (the
// `known &&` gate): like the decode side, we trust the CLI's own ultracode report
// for a model the catalog can't speak to rather than downgrading a running session.
//
// Clearing the boolean alone is not enough: the CLI's apply_flag_settings treats
// model/effortLevel/ultracode as independent keys and does NOT re-resolve
// effortLevel on a model change (verified against the Claude Code 2.1.x binary --
// the handler is three separate `if (key in settings)` blocks, and the effective
// effort is `ultracode ? "xhigh" : effortLevel`). So a bare {model, ultracode:false}
// would leave effortLevel pinned at the ultracode base "xhigh" -- a level the new
// model may not support -- and the session would keep running at xhigh. When the
// caller requested no explicit effort (so claudeEffortFlagSettings left no
// effortLevel key), we therefore also pin the level to the target model's
// xhigh-resolved fallback, which mirrors the decode side (effortFromApplied
// falls back to the xhigh base when ultracode is cleared) and lands the live path
// on the same effort a relaunch would pick. resolveEffort downgrades
// xhigh to "high" for models that don't offer it (e.g. sonnet -> "high", its
// default), so the pinned level is always one the target model can run.
//
// The UI resets effort to auto on a model change and restarts (IsEffortAutoTransition
// short-circuits UpdateSettings before this runs), so today this whole branch only
// matters for non-UI/raw callers; it is defensive so such a caller can't strand an
// unsupported effortLevel on the live session.
func (r effortResolver) updateFlagSettings(targetModel, newEffort, curEffort string) map[string]interface{} {
	if targetModel == agent.DefaultModelSentinel {
		// A session stuck on the unresolved account-default sentinel (the degraded path
		// where get_settings never echoed a concrete applied.model) has no concrete model
		// to resolve an effort against. Pushing an effortLevel here would pin a level the
		// CLI's actual resolved model may not support, so emit no effort delta -- the same
		// "the sentinel keeps the CLI's own resolution" stance the launch path takes by
		// omitting --effort. The effort settles once the model resolves.
		return nil
	}
	fs := claudeEffortFlagSettings(r.resolveEffort(targetModel, newEffort), curEffort)
	// Gate the ultracode strip on the model being KNOWN, mirroring effortFromApplied's
	// final guard (the `known &&` at the decode side): a model in NEITHER catalog
	// running ultracode is trusted as the CLI's own authoritative report (see
	// trustCLIUltracodeReport) -- the live path must not relabel it just because the
	// catalog can't confirm xhigh support. Without this guard supportsUltracode rejects
	// the unknown model, so unsupportedUltracode(curEffort, unknown) is true and an
	// unrelated/model-only update (newEffort=="") would silently push {ultracode:false,
	// effortLevel:"xhigh"} and downgrade a session the CLI is happily running at
	// ultracode -- directly contradicting the decode side that just trusted the same
	// model. What still reaches the downgrade is a switch to a known model whose
	// level set has no ultracode: Haiku, whose definedEfforts is known-but-empty.
	// Sonnet used to be the example here and no longer is -- the CLI reports
	// xhigh/ultracode for it, so the catalog does too.
	_, known := r.definedEfforts(targetModel)
	if known && r.unsupportedUltracode(curEffort, targetModel) {
		if fs == nil {
			fs = map[string]interface{}{}
		}
		fs["ultracode"] = false
		if _, ok := fs["effortLevel"]; !ok {
			fs["effortLevel"] = r.resolveEffort(targetModel, agent.EffortXHigh)
		}
	}
	return fs
}

// effortFromApplied decodes the effort/ultracode pair that get_settings reports
// back into LeapMux's internal effort value -- the decode-side inverse of
// claudeEffortFlagSettings. The CLI reports an active ultracode session as
// effortLevel:"xhigh" plus ultracode:true, so applied.ultracode==true maps to the
// internal "ultracode" -- trusted as the CLI's authoritative report, EXCEPT for a
// model the catalog KNOWS lacks ultracode (Haiku), which is never promoted.
// A model the catalog does NOT know (e.g. one the CLI reported in unavailable_models,
// so convertClaudeModels filtered it from the dynamic catalog) is trusted too: the
// CLI is the authority on what it actually applied, so a running ultracode session
// is not relabeled to xhigh just because its model dropped out of the catalog. The
// account-default sentinel is the one unknown model NOT trusted here: a session
// stuck on the literal "default" (the CLI never echoed a concrete applied.model) has
// no real model behind it, so a CLI ultracode:true report passes the effort through
// (e.g. "xhigh") rather than minting a phantom "ultracode" against the placeholder. An
// unentitled session reports ultracode:false and we keep the reported level (e.g.
// "xhigh"). When ultracode is explicitly turned off but applied.effort is omitted,
// we fall back to ultracode's "xhigh" launch base instead of leaving a stale
// "ultracode". curEffort is retained when applied.effort is omitted or empty.
//
// applied.effort is reported as a concrete effort enum or null (get_settings
// sends `typeof effort === "string" ? effort : null`), never an empty string,
// so the `!= ""` guard below is purely defensive: a malformed/empty report
// retains curEffort rather than blanking the stored effort to "".
//
// The final guard catches the remaining mislabel path the switch can't: when the
// CLI omits the ultracode field entirely (ultracode == nil) a stale
// curEffort=="ultracode" would otherwise survive onto a model the catalog KNOWS
// can't run it (e.g. a model switch that didn't touch effort), so we clear it to
// the xhigh base. An unknown model is left alone here for the same trust reason.
func (r effortResolver) effortFromApplied(appliedEffort *string, ultracode *bool, curEffort, model string) string {
	effort := curEffort
	if appliedEffort != nil && *appliedEffort != "" {
		effort = *appliedEffort
	}
	_, known := r.definedEfforts(model)
	if ultracode != nil {
		switch {
		case *ultracode && r.trustCLIUltracodeReport(model, known):
			effort = EffortUltracode // overrides the "xhigh" reported in applied.effort
		case !*ultracode && effort == EffortUltracode:
			effort = agent.EffortXHigh // ultracode cleared; fall back to its xhigh launch base
		}
	}
	if known && r.unsupportedUltracode(effort, model) {
		effort = agent.EffortXHigh
	}
	return effort
}

// trustCLIUltracodeReport reports whether a CLI applied.ultracode==true report
// should be promoted to the internal EffortUltracode for this model. The CLI is
// the authority on what it actually applied, so we trust the report both for a
// model the catalog confirms supports ultracode AND for a model the catalog does
// NOT know (e.g. one the live CLI filtered into unavailable_models but that the
// session is still running) -- a running ultracode session is not relabeled to
// xhigh just because its model dropped out of the catalog. The one exception is
// the account-default sentinel: it has no concrete model behind it, so a session
// stuck on the literal "default" must not mint a phantom ultracode against the
// placeholder. `known` is effortFromApplied's definedEfforts verdict for `model`,
// reused here for the unknown-model short-circuit; the known-model case defers to
// supportsUltracode so the "the catalog advertises ultracode" membership test lives
// in exactly one place. This is the inverse of supportsUltracode on an unknown
// model (which rejects it) -- the CLI report is trusted where the catalog is silent.
func (r effortResolver) trustCLIUltracodeReport(model string, known bool) bool {
	return model != agent.DefaultModelSentinel && (!known || r.supportsUltracode(model))
}

// Individual effort tiers, defined once and composed into the per-model slices
// below. Sharing one definition per tier keeps the descriptions single-sourced
// so a copy edit (like the one this list just received) can't drift between the
// Opus and Sonnet menus. The entries are immutable catalog data; the slices
// contain the same pointers (as opus and opus[1m] already share a whole slice).
//
// Because the pointers are shared, they MUST be treated as read-only after init:
// mutating one tier (e.g. its Description) would change it for every model slice
// that points to it. OptionGroups()'s model projection returns these without
// copying, so the read-only contract extends to its callers.
var (
	effortTierAuto      = &agent.EffortInfo{Id: agent.EffortAuto, Name: providerkit.EffortLabel(agent.EffortAuto), Description: "Let Claude decide the appropriate effort"}
	effortTierUltracode = &agent.EffortInfo{Id: "ultracode", Name: providerkit.EffortLabel("ultracode"), Description: "xhigh effort plus standing dynamic-workflow orchestration"}
	effortTierMax       = &agent.EffortInfo{Id: "max", Name: providerkit.EffortLabel("max"), Description: "Maximum capability with deepest reasoning"}
	effortTierXHigh     = &agent.EffortInfo{Id: agent.EffortXHigh, Name: providerkit.EffortLabel(agent.EffortXHigh), Description: "Deeper reasoning than high, just below maximum"}
	effortTierHigh      = &agent.EffortInfo{Id: agent.EffortHigh, Name: providerkit.EffortLabel(agent.EffortHigh), Description: "Comprehensive implementation with extensive testing and documentation"}
	effortTierMedium    = &agent.EffortInfo{Id: "medium", Name: providerkit.EffortLabel("medium"), Description: "Balanced approach with standard implementation and testing"}
	effortTierLow       = &agent.EffortInfo{Id: "low", Name: providerkit.EffortLabel("low"), Description: "Quick, straightforward implementation with minimal overhead"}
)

// claudeEffortXHighMax is used by models that support both xhigh and max, plus
// the xhigh+ultracode combo -- which, per the CLI's own initialize response, is
// every effort-capable Claude model it lists (Opus, Fable, and Sonnet alike).
// This is the FALLBACK catalog: the running session's report always wins, and
// the two must agree or the effort menu visibly grows a tier the moment the
// live catalog replaces the fallback.
var claudeEffortXHighMax = []*agent.EffortInfo{
	effortTierAuto, effortTierUltracode, effortTierMax, effortTierXHigh, effortTierHigh, effortTierMedium, effortTierLow,
}

// claudeEffortLevels lists the Claude Code CLI effort levels weakest->strongest,
// each paired with the shared AvailableEffort tier pointer. Its order IS the rank
// (claudeSupportedEfforts walks it in reverse for a strongest->weakest menu) and
// its membership defines which CLI levels we recognize, so ordering and tier
// mapping are single-sourced -- a new tier can't be added to one without the
// other (the previous parallel rank/tier maps could silently disagree, sorting an
// unmapped level as rank 0). Reusing the shared providerkit.EffortTier* pointers keeps the
// descriptions identical to the static catalog.
type claudeEffortLevel struct {
	level string
	tier  *agent.EffortInfo
}

var claudeEffortLevels = []claudeEffortLevel{
	{"low", effortTierLow},
	{"medium", effortTierMedium},
	{agent.EffortHigh, effortTierHigh},
	{agent.EffortXHigh, effortTierXHigh},
	{"max", effortTierMax},
}

// recognizedClaudeEffortLevels returns the claudeEffortLevels entries the model
// supports, ordered strongest->weakest (the rank table walked in reverse), with
// levels the model doesn't list dropped. It single-sources the reverse rank walk
// shared by claudeSupportedEfforts (which maps the entries to tier pointers) and
// claudeDefaultEffort's fallback (which takes the strongest entry's level).
func recognizedClaudeEffortLevels(supported map[string]bool) []claudeEffortLevel {
	out := make([]claudeEffortLevel, 0, len(claudeEffortLevels))
	for i := len(claudeEffortLevels) - 1; i >= 0; i-- {
		if supported[claudeEffortLevels[i].level] {
			out = append(out, claudeEffortLevels[i])
		}
	}
	return out
}

// normalizedEffortLevelSet returns the set of recognized-or-not CLI effort levels
// a model reports, lowercased so a mixed-case "XHigh" still matches. Returns nil
// when the model has no effort support (Haiku), which both claudeSupportedEfforts
// and claudeDefaultEffort read as "hide the selector / no default". Building it
// once lets both share a single scan of SupportedEffortLevels.
func normalizedEffortLevelSet(m claudeCodeModelInfo) map[string]bool {
	if !m.SupportsEffort {
		return nil
	}
	set := make(map[string]bool, len(m.SupportedEffortLevels))
	for _, lvl := range m.SupportedEffortLevels {
		set[strings.ToLower(lvl)] = true
	}
	return set
}

// claudeSupportedEfforts builds the AvailableEffort list from a model's level set
// (normalizedEffortLevelSet), reusing the shared tier pointers. Models without
// effort support, or whose reported levels we don't recognize at all, get no
// efforts -- which hides the effort selector rather than showing an auto-only stub.
// The "auto" sentinel always leads; "ultracode" follows when the model supports
// xhigh; the recognized CLI levels then follow strongest->weakest. The order comes
// from claudeEffortLevels (walked in reverse), not the CLI's reported order, so an
// unexpected ordering can't scramble the menu.
func claudeSupportedEfforts(supported map[string]bool) []*agent.EffortInfo {
	recognized := recognizedClaudeEffortLevels(supported)
	// A model that claims effort support but lists only levels we don't recognize
	// has no usable menu, so hide the selector instead of emitting a lone "auto".
	if len(recognized) == 0 {
		return nil
	}
	efforts := make([]*agent.EffortInfo, 0, len(recognized)+2)
	efforts = append(efforts, effortTierAuto)
	if supported[agent.EffortXHigh] {
		efforts = append(efforts, effortTierUltracode)
	}
	for _, lvl := range recognized {
		efforts = append(efforts, lvl.tier)
	}
	return efforts
}

// claudeDefaultEffort picks a model's default effort from its level set. The CLI
// does not report one, so we prefer xhigh, else high -- the product-chosen sweet
// spot, deliberately NOT merely the strongest level (max is overkill as a default
// for a model that also offers xhigh). Every effort-capable model in the current
// catalog reports xhigh, so the "else high" branch is the fallback for a model whose
// live level set turns out narrower, not a description of Sonnet. A model with no
// effort support gets "" -- inert, since the effort selector is hidden and the
// launch path omits --effort for it.
func claudeDefaultEffort(supported map[string]bool) string {
	if supported[agent.EffortXHigh] {
		return agent.EffortXHigh
	}
	if supported[agent.EffortHigh] {
		return agent.EffortHigh
	}
	// Neither xhigh nor high is offered (e.g. a max-only model). Fall back to the
	// strongest recognized level the model does support so an effort-bearing model
	// never ends up with a non-empty selector but an empty default -- a state the
	// frontend's effortValueForModel would otherwise have to coerce. Returns ""
	// only when no level is recognized, matching claudeSupportedEfforts hiding the
	// selector.
	if recognized := recognizedClaudeEffortLevels(supported); len(recognized) > 0 {
		return recognized[0].level // strongest-first ordering
	}
	return ""
}

// effortResolver answers model effort/ultracode capability questions against one
// catalog. The launch path constructs it over the static claudeCodeAvailableModels
// (the dynamic catalog isn't known until initialize completes); post-init paths
// construct it over the per-agent catalog via a.effortResolver. Wrapping the
// catalog once removes the trailing parameter every resolution helper used to
// thread, and makes "which catalog" an explicit choice at the few construction
// sites instead of an argument carried through the whole chain.
type effortResolver struct {
	// catalog is the primary catalog consulted first: the static one at launch, the
	// per-agent dynamic one post-init.
	catalog []*agent.ModelInfo
	// fallback is consulted only when catalog doesn't list a model, so a model the
	// live CLI dropped from the dynamic list (e.g. reported in unavailable_models)
	// but that the agent is actually running still resolves to its real
	// capabilities instead of being treated as unknown. nil for the launch resolver
	// and the single-catalog resolvers tests build via newEffortResolver.
	fallback []*agent.ModelInfo
}

func newEffortResolver(catalog []*agent.ModelInfo) effortResolver {
	return effortResolver{catalog: catalog}
}

// effortResolver returns a resolver over the agent's per-agent dynamic catalog,
// with the static claudeCodeAvailableModels as a fallback for models the dynamic
// list omits. Dynamic takes precedence, so a capability the live CLI genuinely
// dropped (the model is still listed, minus a level) still wins; the fallback only
// rescues a model the CLI filtered out entirely (e.g. into unavailable_models) but
// that the session is still running -- keeping the decode (effortFromApplied),
// live-update (updateFlagSettings), and startup (reconcileStartupEffortFlags) paths
// from downgrading a filtered session's effort/ultracode out of agreement with each
// other. availableModels is written only during the pre-registration startup
// handshake and never mutated afterward, so this read is safe without a.Mu (callers
// may already hold it).
func (a *Agent) effortResolver() effortResolver {
	return effortResolver{catalog: a.availableModels, fallback: claudeCodeAvailableModels}
}

// launchEffortPlan captures what the --effort launch flag did for a model+effort,
// resolved over the static catalog at launch: whether --effort was omitted and the
// level it was set to (the xhigh base for an ultracode launch). buildModelEffortArgs
// and reconcileStartupEffortFlags both derive it from planLaunch, so the "what launch
// sent" view is single-sourced and the launch flags can't drift from what startup
// reconciles against. Whether the launch was the xhigh+ultracode combo is deliberately
// NOT stored here: both consumers re-derive it from launchRunsUltracode (startup
// against the DYNAMIC catalog, buildModelEffortArgs against the static one), so there
// is no captured boolean to fall out of sync with the level.
type launchEffortPlan struct {
	omitted bool   // launch sent no --effort (CLI stays at its own default)
	level   string // the --effort value sent ("" when omitted; "xhigh" for the ultracode base)
}

// planLaunch resolves model+effort into the launch flag plan. See launchEffortPlan.
func (r effortResolver) planLaunch(model, effort string) launchEffortPlan {
	if r.launchOmitsEffort(model, effort) {
		return launchEffortPlan{omitted: true}
	}
	if r.launchRunsUltracode(model, effort) {
		// The --effort launch flag accepts only low|medium|high|xhigh|max: ultracode
		// has no flag value, so launch at its xhigh base and let buildStartupFlagSettings
		// layer the ultracode boolean on post-init.
		return launchEffortPlan{level: agent.EffortXHigh}
	}
	return launchEffortPlan{level: r.resolveEffort(model, effort)}
}

// buildModelEffortArgs constructs the --model and --effort CLI arguments for
// Claude Code. The DefaultModelSentinel (and the empty model) omits BOTH --model
// and --effort so the CLI resolves the account's own default model AND that model's
// own default effort (get_settings then reports the concrete model/effort, which
// refreshSettingsFromAgent stores -- the model-side analogue of EffortAuto for
// effort). Forwarding a concrete --effort here would be resolved against the
// sentinel's empty effort menu and could push an unsupported level onto whatever the
// default resolves to (e.g. --effort on a Haiku account default). Haiku does not
// support --effort at all. EffortAuto also omits --effort so the CLI picks its own
// default. A consequence: a LEAPMUX_CLAUDE_DEFAULT_EFFORT set without a concrete
// LEAPMUX_CLAUDE_DEFAULT_MODEL is not applied to a sentinel-default agent (see
// effortOrDefault). Other models each expose a subset of effort levels in the catalog;
// any effort not in the subset is downgraded to "high" as a universal safe fallback.
// Called at launch over the static catalog (the dynamic one isn't known until
// initialize completes).
func (r effortResolver) buildModelEffortArgs(model, effort string) []string {
	var args []string
	if !agent.UsesAccountDefaultModel(model) {
		args = []string{"--model", model}
	}
	plan := r.planLaunch(model, effort)
	if plan.omitted {
		return args
	}
	return append(args, "--effort", plan.level)
}

// definedEfforts returns the effort list the catalog defines for modelID and
// whether the model is known at all. It checks the primary catalog first, then the
// fallback, so a model the dynamic list omits but the static catalog still has
// resolves to its real efforts (see effortResolver). The single lookup is shared by
// supports and supportsUltracode, which differ only in how they treat an unknown
// model.
func (r effortResolver) definedEfforts(modelID string) (efforts []*agent.EffortInfo, known bool) {
	if modelID == agent.DefaultModelSentinel {
		// The account-default sentinel is a placeholder, not a concrete model: its
		// real efforts belong to whatever the CLI resolves it to. Report it as
		// unresolved so effort resolution passes the requested effort through rather
		// than clamping it against the empty effort list the sentinel's catalog entry
		// carries. Reached only in the degraded path where get_settings never echoed a
		// concrete applied.model, so a.model is stuck on "default".
		return nil, false
	}
	// Prefer a catalog entry that actually carries efforts. The dynamic catalog
	// takes precedence, but a known model can land in it with an EMPTY effort list
	// -- the live CLI reported it with supportsEffort:false, or with only effort
	// levels we don't recognize (schema drift) -- and such an empty dynamic entry
	// must not shadow a populated static-fallback entry for the same model. So we
	// keep scanning past an empty match for a populated one, while still reporting
	// the model as known. When no populated entry exists (a genuinely effort-less
	// model like Haiku, whose fallback entry is empty too) the known-but-empty
	// verdict stands. This preserves the legitimate "CLI dropped a level" case: a
	// dynamic entry with FEWER but non-empty efforts still wins over the fallback.
	for _, cat := range [][]*agent.ModelInfo{r.catalog, r.fallback} {
		for _, m := range cat {
			// Guard nil entries to match FindAvailableModel/agent.ModelOptionGroup,
			// which already treat catalogs as possibly nil-bearing; convertClaudeModels
			// never emits nil today, but this keeps the catalog-walking helpers uniform.
			if m != nil && m.Id == modelID {
				if len(m.SupportedEfforts) > 0 {
					return m.SupportedEfforts, true
				}
				known = true // matched, but no efforts here; keep looking for a populated entry
			}
		}
	}
	return nil, known
}

// contextWindow returns the context window for modelID, consulting the primary
// catalog first and then the fallback -- mirroring definedEfforts so a model the
// live CLI dropped from its list but the session is still running resolves its
// window from the static fallback instead of reporting "unknown". Returns 0 when
// neither catalog knows the model (the unresolved account-default sentinel, whose
// catalog entry carries no window) -- the usage broadcast then omits context_window.
func (r effortResolver) contextWindow(modelID string) int64 {
	if w := agent.FindAvailableModel(r.catalog, modelID).GetContextWindow(); w > 0 {
		return w
	}
	return agent.FindAvailableModel(r.fallback, modelID).GetContextWindow()
}

// effortListContains reports whether efforts holds an entry with the given ID.
func effortListContains(efforts []*agent.EffortInfo, id string) bool {
	return slices.ContainsFunc(efforts, func(e *agent.EffortInfo) bool { return e.Id == id })
}

// supports reports whether the given effort ID is in the known SupportedEfforts
// list for the given model. Unknown models are trusted (returns true) so new
// aliases work without a code change.
func (r effortResolver) supports(modelID, effort string) bool {
	efforts, known := r.definedEfforts(modelID)
	if !known {
		return true
	}
	return effortListContains(efforts, effort)
}

// supportsUltracode reports whether the model's catalog entry offers the ultracode
// tier. Unlike supports, it does NOT trust unknown models: ultracode forces
// --effort xhigh, so enabling it on a model we can't confirm supports xhigh would
// risk a level the model rejects. Because convertClaudeModels adds ultracode to any
// catalog entry whose CLI levels include xhigh, this stays consistent with the UI:
// a model is ultracode-capable here exactly when its AvailableModels entry
// advertises it (Opus, Fable, and any future xhigh model the live CLI reports).
func (r effortResolver) supportsUltracode(modelID string) bool {
	efforts, known := r.definedEfforts(modelID)
	return known && effortListContains(efforts, EffortUltracode)
}

// launchRunsUltracode reports whether launching model+effort runs the
// xhigh+ultracode combo: the requested effort is ultracode AND the model supports
// it. It is the single source of truth shared by buildModelEffortArgs (which
// launches at the xhigh base) and buildStartupFlagSettings (which layers the
// ultracode boolean back on), so the two can't disagree about whether a launch is
// an ultracode launch.
func (r effortResolver) launchRunsUltracode(model, effort string) bool {
	return effort == EffortUltracode && r.supportsUltracode(model)
}

// launchOmitsEffort reports whether launching model+effort sends no --effort flag,
// leaving the CLI at its own resolved default for the model. The account-default
// sentinel (and an empty model, which likewise sends no --model so the CLI picks
// the account default) and EffortAuto/"" always omit it; a model the catalog KNOWS
// has no effort support (Haiku) also omits it -- expressed via the catalog rather
// than a "haiku" literal, so an effort-less model the CLI introduces is handled
// without a code change. Unknown models are trusted and pass their effort through.
// Shared by buildModelEffortArgs (which omits --effort) and planLaunch, so the
// launch flags and the startup reconcile see the same "sent nothing" set.
func (r effortResolver) launchOmitsEffort(model, effort string) bool {
	if effort == "" || effort == agent.EffortAuto || agent.UsesAccountDefaultModel(model) {
		return true
	}
	efforts, known := r.definedEfforts(model)
	return known && len(efforts) == 0
}

// unsupportedUltracode reports whether effort is the ultracode tier while model
// can't run it. This is the recurring "ultracode requested/stored/being-left on a
// model whose catalog doesn't offer it" condition that the resolve (resolveEffort),
// decode (effortFromApplied), and live-update (updateFlagSettings) paths each must
// guard -- naming it once keeps a future model-capability change to a single edit
// instead of three that must be kept in agreement.
func (r effortResolver) unsupportedUltracode(effort, model string) bool {
	return effort == EffortUltracode && !r.supportsUltracode(model)
}

// resolveEffort resolves a requested effort against the model it will run under: an
// effort the model's catalog doesn't support is downgraded to the universal-safe
// EffortHigh, and "ultracode" stays "ultracode" only for a model that actually
// supports it (otherwise it too becomes EffortHigh -- this catches unknown models,
// which supports trusts but which we can't confirm are xhigh-capable). EffortAuto
// and "" pass through for the caller to handle.
func (r effortResolver) resolveEffort(model, effort string) string {
	if effort == "" || effort == agent.EffortAuto {
		return effort
	}
	if !r.supports(model, effort) {
		return agent.EffortHigh
	}
	if r.unsupportedUltracode(effort, model) {
		return agent.EffortHigh
	}
	return effort
}

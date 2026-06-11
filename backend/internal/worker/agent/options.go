package agent

import (
	"fmt"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/optionmap"
)

// nameOrID returns the trimmed display name, falling back to the id when the name is blank.
// Used to label an option (a model, an effort tier, or an ACP config-option group) from its
// own name. Lives here beside the catalog-projection callers (modelOptionGroup / effortGroupForModel)
// rather than in the ACP option-eviction file, so the general helper has a general home.
func nameOrID(name, id string) string {
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		return trimmed
	}
	return id
}

// Well-known option-group ids. Their canonical home is util/optionids -- a leaf package
// the thin remote CLI can import without the agent runtime -- aliased here so the agent
// package's many internal callers keep referencing them under the OptionID* names.
const (
	OptionIDModel          = optionids.Model
	OptionIDEffort         = optionids.Effort
	OptionIDPermissionMode = optionids.PermissionMode
	OptionIDPrimaryAgent   = optionids.PrimaryAgent
)

// isReservedOptionKey reports whether id names a well-known axis that owns a dedicated
// mapped option group (model via the model channel; permission mode / primary agent via
// the secondary mode channel). A server-driven config option must never be
// surfaced under one of these keys, or it would double-list the group the mapped channel
// already owns. Effort is deliberately NOT reserved: an ACP provider with an effort axis
// surfaces it AS a server-driven config option (it has no dedicated channel), so it legitimately
// uses the effort key.
func isReservedOptionKey(id string) bool {
	return id == OptionIDModel || id == OptionIDPermissionMode || id == OptionIDPrimaryAgent
}

// Display order for the well-known groups in the (uniform) settings popover.
// Groups are rendered ascending by order; provider-specific axes slot between
// effort and permission mode. Ties are allowed (a provider never emits two
// groups competing for the same slot).
const (
	OptionOrderModel          int32 = 10
	OptionOrderEffort         int32 = 20
	OptionOrderProviderFirst  int32 = 30
	OptionOrderProviderSecond int32 = 40
	OptionOrderProviderThird  int32 = 50
	OptionOrderProviderFourth int32 = 60
	OptionOrderPrimaryAgent   int32 = 80
	OptionOrderPermissionMode int32 = 90
	OptionOrderTrailing       int32 = 100
)

// EffortSupportedByModel reports whether effort is a selectable effort tier for model in
// the given catalog. It reads the model option's effort sub_group -- carried per model,
// independent of which model is currently active -- so it answers correctly even for a
// live catalog whose top-level effort group reflects a different (current) model. Returns
// false when the model is unlisted or has no effort axis, true when effort matches one of
// the model's tiers. Used to validate a requested effort against the model an edit settles
// on, so an unsupported tier (e.g. a CLI `--effort xhigh` against a model without it)
// isn't persisted.
//
// The model is matched by NORMALIZED id (the catalog stores canonical alias ids, but a
// caller -- notably the remote CLI -- may pass a re-spelled alias like the fully-qualified
// "claude-opus-4-8[1m]" for catalog id "opus[1m]"). Matching raw would miss the alias and
// wrongly report the model's own efforts as unsupported, resetting a valid effort to auto.
// This mirrors the normalized comparison sanitizeIncomingOptions uses to detect a real
// model switch.
func EffortSupportedByModel(groups []*leapmuxv1.AvailableOptionGroup, provider leapmuxv1.AgentProvider, model, effort string) bool {
	mg := optionids.GroupByID(groups, OptionIDModel)
	if mg == nil {
		return false
	}
	want := NormalizeModelID(provider, model)
	for _, o := range mg.GetOptions() {
		if NormalizeModelID(provider, o.GetId()) != want {
			continue
		}
		return effortListed(optionids.GroupByID(o.GetSubGroups(), OptionIDEffort), effort)
	}
	// The model is not among the SELECTABLE options. A model the session runs but the picker
	// hides -- e.g. Claude's standard-context "opus", surfaced only as the model group's
	// current value, never as a selectable option (modelOptionGroup drops Hidden models) --
	// carries no per-model sub_groups. The catalog's top-level effort group is nonetheless
	// built for that current model (modelAndEffortGroups resolves it via FindAvailableModel,
	// which does NOT filter Hidden), so when the requested model IS the current one, validate
	// against that group rather than wrongly reporting every tier unsupported and resetting a
	// valid effort to auto.
	if NormalizeModelID(provider, mg.GetCurrentValue()) == want {
		return effortListed(optionids.GroupByID(groups, OptionIDEffort), effort)
	}
	return false
}

// ModelEffortKnown reports whether the catalog describes model's effort capabilities -- so an effort
// the model does not offer can be authoritatively reset. True when model is a selectable model-group
// option (its per-model effort sub_group, present or absent, is known), or it is the model group's
// current value AND a top-level effort group is present (the hidden-current-model case
// EffortSupportedByModel validates against). FALSE when model is absent from the catalog entirely --
// a value valid in a running provider's LIVE catalog but missing from a stopped agent's static seed.
// The effort reset uses it to skip resetting against an incomplete seed, leaving an unknown model's
// effort for the running session to validate -- mirroring ValidateLaunchOptions, which deliberately
// does NOT validate model/effort against the seed.
func ModelEffortKnown(groups []*leapmuxv1.AvailableOptionGroup, provider leapmuxv1.AgentProvider, model string) bool {
	mg := optionids.GroupByID(groups, OptionIDModel)
	if mg == nil {
		return false
	}
	want := NormalizeModelID(provider, model)
	for _, o := range mg.GetOptions() {
		if NormalizeModelID(provider, o.GetId()) == want {
			return true
		}
	}
	return NormalizeModelID(provider, mg.GetCurrentValue()) == want &&
		optionids.GroupByID(groups, OptionIDEffort) != nil
}

// effortListed reports whether eg offers an option with the given effort id.
func effortListed(eg *leapmuxv1.AvailableOptionGroup, effort string) bool {
	if eg == nil {
		return false
	}
	for _, e := range eg.GetOptions() {
		if e.GetId() == effort {
			return true
		}
	}
	return false
}

// ValidateLaunchOptions reports an error when the user-supplied PERMISSION MODE in a spawn request is
// not valid for the provider, so the OpenAgent handler can REJECT a typo'd value with a clear message
// instead of letting it reach the provider and die at startup (an opaque dead agent -- Claude fails
// startup on a bad set_permission_mode).
//
// It deliberately does NOT validate model or effort. Every provider -- INCLUDING Claude/Codex/Pi --
// discovers its model catalog (and the per-model effort tiers) from the running CLI/daemon, seeding
// only a static FALLBACK until that arrives. So a model (or effort tier) valid in the live catalog
// but absent from the seed would be wrongly rejected here; model/effort are left for the running
// session to validate (and were already passable, unvalidated, before this refactor -- so not
// validating them is no regression).
//
// Permission mode, by contrast, is a FIXED CLI capability for the leapmux-managed providers
// (Claude/Codex) -- not discovered -- so an invalid one IS authoritatively rejectable here. The check
// is gated to those providers via ProviderManagesEffort (true for the CLI-backed camp Claude/Codex/Pi,
// false for ACP): an ACP provider DISCOVERS its modes from the daemon (its static group is a seed), so
// validating against that seed would false-reject a valid dynamic mode; Pi has no permission modes, so
// its empty group accepts anything. Empty requested values (an axis the user did not supply) are
// skipped. requested holds the user's raw, pre-default option values.
func ValidateLaunchOptions(provider leapmuxv1.AgentProvider, requested optionmap.Map) error {
	pm := requested.Get(OptionIDPermissionMode)
	if pm == "" {
		return nil
	}
	// Only a CLI-managed provider (Claude/Codex/Pi) has a fixed, complete permission-mode enum to
	// check against; an ACP provider's modes are daemon-discovered, so leave them to the session.
	if !ProviderManagesEffort(provider) {
		return nil
	}
	if !valueListedInGroup(AvailableOptionGroupsForProvider(provider), OptionIDPermissionMode, pm) {
		return fmt.Errorf("permission mode %q is not valid for this provider", pm)
	}
	return nil
}

// valueListedInGroup reports whether value is an option of the catalog's group for id, accepting any
// value when the group is absent or enumerates no options -- a dynamic / free-form axis the static
// catalog can't authoritatively validate (e.g. an ACP provider's session modes).
func valueListedInGroup(catalog []*leapmuxv1.AvailableOptionGroup, id, value string) bool {
	g := optionids.GroupByID(catalog, id)
	if g == nil || len(g.GetOptions()) == 0 {
		return true
	}
	for _, o := range g.GetOptions() {
		if o.GetId() == value {
			return true
		}
	}
	return false
}

// CurrentOptions extracts the chosen value of every group into a flat
// id->value map. It is the persisted/relaunch representation of an agent's
// settings: every axis the provider reports (including agent-controlled ones,
// so a relaunch reproduces e.g. a launch-fixed model) is captured. Empty values
// are omitted.
//
// This is the CATALOG view -- it captures only what OptionGroups() surfaces. A
// provider-private axis persisted as an option but deliberately NOT surfaced as a
// group (e.g. Pi's pi_provider, persisted via PersistSettingsRefresh and read back
// from launch options but never shown in the picker) is therefore absent here. Such
// values survive across a confirmed-settings write only because confirmedOptions
// overlays the confirmed map onto a BASE that already carries them; callers that
// rebuild settings purely from CurrentOptions (rather than overlaying onto the row)
// would drop them.
func CurrentOptions(groups []*leapmuxv1.AvailableOptionGroup) map[string]string {
	out := make(map[string]string, len(groups))
	for _, g := range groups {
		if v := g.GetCurrentValue(); v != "" {
			out[g.GetId()] = v
		}
	}
	return out
}

// optDef is a lightweight option spec used by selectGroup. The entry marked
// Default supplies the group's DefaultValue, so callers keep marking the default
// per option exactly as they did when AvailableOption carried IsDefault.
type optDef struct {
	Id            string
	Name          string
	Description   string
	Default       bool
	ContextWindow int64
	SubGroups     []*leapmuxv1.AvailableOptionGroup
}

// selectGroup builds a mutable (user-writable) option group from option
// specs, deriving DefaultValue from the entry flagged Default.
func selectGroup(id, label string, order int32, current string, defs []optDef) *leapmuxv1.AvailableOptionGroup {
	opts := make([]*leapmuxv1.AvailableOption, 0, len(defs))
	def := ""
	for _, d := range defs {
		opts = append(opts, &leapmuxv1.AvailableOption{
			Id:            d.Id,
			Name:          d.Name,
			Description:   d.Description,
			ContextWindow: d.ContextWindow,
			SubGroups:     d.SubGroups,
		})
		if d.Default {
			def = d.Id
		}
	}
	return &leapmuxv1.AvailableOptionGroup{
		Id:           id,
		Label:        label,
		Options:      opts,
		CurrentValue: current,
		DefaultValue: def,
		Mutable:      true,
		Order:        order,
	}
}

// liveGroup overlays an agent's confirmed current value onto a provider's static
// option-group template. Used by providers that define their option lists
// statically (Codex) and supply the current value at read time. The id, label,
// default, display order, option list, AND mutability are taken from the template
// (shared, immutable data); an empty current falls back to the template's DefaultValue,
// so a group the caller forgot to supply a current for still renders a valid (in-list)
// selection rather than a blank one -- which holds because every selectable template a
// provider registers sets a non-empty DefaultValue (a template with options but no default
// would still render blank here; the fallback can only point at what the template names).
// Honoring the template's Mutable lets a provider project an agent-controlled, read-only
// axis through liveGroup without it being shown as user-editable.
func liveGroup(static *leapmuxv1.AvailableOptionGroup, current string) *leapmuxv1.AvailableOptionGroup {
	if static == nil {
		return nil
	}
	if current == "" {
		current = static.GetDefaultValue()
	}
	g := cloneOptionGroupTemplate(static)
	g.CurrentValue = current
	return g
}

// cloneOptionGroupTemplate returns a shallow copy of a static option-group template,
// SHARING its (immutable) Options slice. It copies every scalar field explicitly here, so a
// field added to AvailableOptionGroup must be added in THIS one place -- but only here, not
// at each projection site below (liveGroup, which then overrides only CurrentValue, and
// filterGroupOptions, which overrides only Options). Centralizing the copy keeps the two
// projections from drifting in which fields they carry.
func cloneOptionGroupTemplate(static *leapmuxv1.AvailableOptionGroup) *leapmuxv1.AvailableOptionGroup {
	return &leapmuxv1.AvailableOptionGroup{
		Id:           static.GetId(),
		Label:        static.GetLabel(),
		Options:      static.GetOptions(),
		CurrentValue: static.GetCurrentValue(),
		DefaultValue: static.GetDefaultValue(),
		Mutable:      static.GetMutable(),
		Order:        static.GetOrder(),
	}
}

// filterGroupOptions returns a shallow copy of static with its Options narrowed to those
// satisfying keep, preserving id/label/default/mutability/order. Returns nil for a nil
// input. It drops an option a provider can't currently offer (e.g. Claude hiding the "auto"
// permission mode when the startup probe rejected it) WITHOUT mutating the shared static
// template, so it composes with liveGroup (which then overlays the live current value)
// instead of each caller re-implementing the template copy.
func filterGroupOptions(static *leapmuxv1.AvailableOptionGroup, keep func(*leapmuxv1.AvailableOption) bool) *leapmuxv1.AvailableOptionGroup {
	if static == nil {
		return nil
	}
	opts := make([]*leapmuxv1.AvailableOption, 0, len(static.GetOptions()))
	for _, o := range static.GetOptions() {
		if keep(o) {
			opts = append(opts, o)
		}
	}
	g := cloneOptionGroupTemplate(static)
	g.Options = opts
	return g
}

// modelSubGroupsFunc returns the model-dependent option groups a given model
// determines (its effort tiers, plus any provider-specific group whose content
// varies by model, e.g. Claude's extended-thinking label). Providers supply one
// to modelOptionGroup so each model option carries its own dependent groups,
// which the frontend swaps in the instant the model selection changes.
type modelSubGroupsFunc func(m *ModelInfo) []*leapmuxv1.AvailableOptionGroup

// modelOptionGroup projects a model catalog into the "model" option group.
// Each model becomes an option carrying its context window and -- when subGroups
// is non-nil -- its model-dependent sub_groups; the group's DefaultValue is the
// model flagged IsDefault. Returns nil for an empty catalog (e.g. a Claude
// session that hides model/effort UI), which omits the group.
func modelOptionGroup(models []*ModelInfo, current string, subGroups modelSubGroupsFunc) *leapmuxv1.AvailableOptionGroup {
	if len(models) == 0 {
		return nil
	}
	defs := make([]optDef, 0, len(models))
	for _, m := range models {
		if m == nil || m.Hidden {
			continue
		}
		def := optDef{
			Id:            m.Id,
			Name:          nameOrID(m.DisplayName, m.Id),
			Description:   m.Description,
			Default:       m.IsDefault,
			ContextWindow: m.ContextWindow,
		}
		if subGroups != nil {
			// Carry this model's dependent groups so the frontend can rebuild
			// them the instant the model selection changes (no round-trip).
			def.SubGroups = subGroups(m)
		}
		defs = append(defs, def)
	}
	if len(defs) == 0 {
		return nil
	}
	return selectGroup(OptionIDModel, ModelGroupLabel, OptionOrderModel, current, defs)
}

// ModelGroupLabel is the display label for the model option group. Defined once here so the
// selectable projection (modelOptionGroup) and the read-only projections (readOnlyModelAndEffortGroups,
// ensureModelGroup) can't drift on the label.
const ModelGroupLabel = "Model"

// EffortGroupLabel is the default display label for the model-dependent effort group.
// Providers whose effort axis is conceptually distinct override it -- Pi's CLI exposes
// a "thinking level" (set_thinking_level), so it passes PiThinkingLevelLabel instead.
const EffortGroupLabel = "Effort"

// effortSubGroups is the default modelSubGroupsFunc: a model's lone dependent
// group is its effort group (labeled "Effort"). Providers with additional model-dependent
// groups (Claude adds extended thinking) or a different effort label (Pi) wrap
// effortSubGroupsLabeled. Returns nil for an effort-less model.
func effortSubGroups(m *ModelInfo) []*leapmuxv1.AvailableOptionGroup {
	return effortSubGroupsLabeled(m, EffortGroupLabel)
}

// effortSubGroupsLabeled builds a model's effort sub_groups under a caller-chosen
// label, so a provider's model-switch swap and its top-level effort group stay
// consistently named (e.g. Pi's "Thinking Level").
func effortSubGroupsLabeled(m *ModelInfo, label string) []*leapmuxv1.AvailableOptionGroup {
	if eg := effortGroupForModel(m, "", label); eg != nil {
		return []*leapmuxv1.AvailableOptionGroup{eg}
	}
	return nil
}

// effortSubGroupsFunc returns a modelSubGroupsFunc whose lone per-model group is the model's
// effort group under the given label. It is the one constructor for the "effort-only sub_groups
// under a chosen label" shape, shared by modelAndEffortGroups' default sub_groups and Pi's
// "Thinking Level" sub_groups so the two can't drift; effortSubGroups is the unlabeled
// EffortGroupLabel default callers pass directly as a value.
func effortSubGroupsFunc(label string) modelSubGroupsFunc {
	return func(m *ModelInfo) []*leapmuxv1.AvailableOptionGroup {
		return effortSubGroupsLabeled(m, label)
	}
}

// effortGroupForModel projects a single model's supported efforts into the
// "effort" option group under the given label, with DefaultValue set to the model's
// default effort. Returns nil when the model is unknown or offers no efforts (so the
// group is omitted), matching the prior behavior of hiding effort for effort-less models.
func effortGroupForModel(m *ModelInfo, currentEffort, label string) *leapmuxv1.AvailableOptionGroup {
	if m == nil || len(m.SupportedEfforts) == 0 {
		return nil
	}
	defs := make([]optDef, 0, len(m.SupportedEfforts))
	for _, e := range m.SupportedEfforts {
		if e == nil {
			continue
		}
		defs = append(defs, optDef{
			Id:          e.Id,
			Name:        nameOrID(e.Name, e.Id),
			Description: e.Description,
			Default:     e.Id == m.DefaultEffort,
		})
	}
	if len(defs) == 0 {
		return nil
	}
	return selectGroup(OptionIDEffort, label, OptionOrderEffort, currentEffort, defs)
}

// modelThenEffort assembles the leading "model group, then effort group" slice, omitting either
// when it is nil. The model-first/effort-second ordering and the omit-when-absent rule live here so
// the mutable (modelAndEffortGroups) and read-only (readOnlyModelAndEffortGroups) builders share one
// definition and can't drift on them.
func modelThenEffort(modelGroup, effortGroup *leapmuxv1.AvailableOptionGroup) []*leapmuxv1.AvailableOptionGroup {
	var groups []*leapmuxv1.AvailableOptionGroup
	if modelGroup != nil {
		groups = append(groups, modelGroup)
	}
	if effortGroup != nil {
		groups = append(groups, effortGroup)
	}
	return groups
}

// readOnlyValueGroup builds a single-option, non-mutable group that surfaces a value
// the user cannot change -- e.g. a third-party Claude session's fixed model, which has
// no selectable catalog but should still be visible to `remote agent get`/list and the
// UI rather than rendering blank. The lone option is the current value, so the picker
// shows it as a fixed readout. name is the option's display label (e.g. a humanized
// model name); it falls back to the raw value when empty.
func readOnlyValueGroup(id, label string, order int32, value, name string) *leapmuxv1.AvailableOptionGroup {
	if name == "" {
		name = value
	}
	return &leapmuxv1.AvailableOptionGroup{
		Id:           id,
		Label:        label,
		Options:      []*leapmuxv1.AvailableOption{{Id: value, Name: name}},
		CurrentValue: value,
		DefaultValue: value,
		Mutable:      false,
		Order:        order,
	}
}

// readOnlyModelAndEffortGroups builds the read-only model group (with a humanized
// display name) and, when effort is a concrete non-auto value, the read-only effort
// group, for a session whose model/effort UI is hidden (a third-party Claude session or
// can_change_model_and_effort=false). It mirrors modelAndEffortGroups for the mutable
// case so the EffortAuto-suppression rule lives here next to its sibling rather than
// inline at the call site. modelName is the model's humanized display label.
func readOnlyModelAndEffortGroups(model, modelName, effort string) []*leapmuxv1.AvailableOptionGroup {
	var modelGroup, effortGroup *leapmuxv1.AvailableOptionGroup
	if model != "" {
		modelGroup = readOnlyValueGroup(OptionIDModel, ModelGroupLabel, OptionOrderModel, model, modelName)
	}
	if effort != "" && effort != EffortAuto {
		effortGroup = readOnlyValueGroup(OptionIDEffort, EffortGroupLabel, OptionOrderEffort, effort, "")
	}
	return modelThenEffort(modelGroup, effortGroup)
}

// modelAndEffortGroups returns the model group followed by the current model's
// effort group, omitting either when the catalog yields none. Shared by every
// provider that exposes model and effort as its two leading top-level groups
// (Codex, Pi, Claude). effortLabel names the effort axis (EffortGroupLabel for Codex
// and Claude, PiThinkingLevelLabel for Pi) and is applied to both the top-level group
// and -- via the default modelSubGroups -- the per-model sub_groups, so a model switch
// keeps the label consistent. modelSubGroups overrides the per-model sub_groups func: pass
// nil for the default (effort sub_groups only), or claudeModelSubGroups to also carry the
// model-dependent extended-thinking group Claude swaps in on a model switch.
func modelAndEffortGroups(models []*ModelInfo, model, effort, effortLabel string, modelSubGroups modelSubGroupsFunc) []*leapmuxv1.AvailableOptionGroup {
	if modelSubGroups == nil {
		modelSubGroups = effortSubGroupsFunc(effortLabel)
	}
	// effortGroupForModel resolves the current model in the catalog (FindAvailableModel) before
	// building its effort group, so a model switch carries the new model's effort tiers.
	return modelThenEffort(
		modelOptionGroup(models, model, modelSubGroups),
		effortGroupForModel(FindAvailableModel(models, model), effort, effortLabel),
	)
}

package agent

import (
	"fmt"
	"slices"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"google.golang.org/protobuf/proto"
)

// NameOrID returns the trimmed display name, falling back to the id when the name is blank.
// Used to label an option (a model, an effort tier, or an ACP config-option group) from its
// own name. Lives here beside the catalog-projection callers (ModelOptionGroup / EffortGroupForModel)
// rather than in the ACP option-eviction file, so the general helper has a general home.
func NameOrID(name, id string) string {
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
func (r *Registry) EffortSupportedByModel(groups []*leapmuxv1.AvailableOptionGroup, provider leapmuxv1.AgentProvider, model, effort string) bool {
	mg := optionids.GroupByID(groups, OptionIDModel)
	if mg == nil {
		return false
	}
	want := r.NormalizeModelID(provider, model)
	for _, o := range mg.GetOptions() {
		if r.NormalizeModelID(provider, o.GetId()) != want {
			continue
		}
		return effortListed(optionids.GroupByID(o.GetSubGroups(), OptionIDEffort), effort)
	}
	// The model is not among the SELECTABLE options. A model the session runs but the picker
	// hides -- e.g. Claude's standard-context "opus", surfaced only as the model group's
	// current value, never as a selectable option (ModelOptionGroup drops Hidden models) --
	// carries no per-model sub_groups. The catalog's top-level effort group is nonetheless
	// built for that current model (providerkit.ModelAndEffortGroups resolves it via FindAvailableModel,
	// which does NOT filter Hidden), so when the requested model IS the current one, validate
	// against that group rather than wrongly reporting every tier unsupported and resetting a
	// valid effort to auto.
	if r.NormalizeModelID(provider, mg.GetCurrentValue()) == want {
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
func (r *Registry) ModelEffortKnown(groups []*leapmuxv1.AvailableOptionGroup, provider leapmuxv1.AgentProvider, model string) bool {
	// The account default is a placeholder, not a concrete model. Its catalog entry
	// carries no efforts on purpose, so an empty effort sub-group here means "not yet
	// resolved" and never "this model offers no tier". Report it as unknown, so the
	// running session validates the effort and resetEffortToAutoIfUnsupported keeps
	// the user's choice instead of clamping it against an empty list. The Claude
	// provider's definedEfforts already answers the same way for the same input.
	if UsesAccountDefaultModel(model) {
		return false
	}
	mg := optionids.GroupByID(groups, OptionIDModel)
	if mg == nil {
		return false
	}
	want := r.NormalizeModelID(provider, model)
	for _, o := range mg.GetOptions() {
		if r.NormalizeModelID(provider, o.GetId()) == want {
			return true
		}
	}
	return r.NormalizeModelID(provider, mg.GetCurrentValue()) == want &&
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
// Permission mode, by contrast, is a FIXED capability of the providers whose permission enum
// LeapMux states itself (Claude, Codex, and native Copilot) -- not discovered -- so an invalid
// one IS authoritatively rejectable here. Registry.HasFixedPermissionModes selects those
// providers. An ACP provider DISCOVERS its modes from the daemon (its static group is a seed),
// so validating against that seed would false-reject a valid dynamic mode. Empty requested
// values (an axis the user did not supply) are skipped. requested holds the user's raw,
// pre-default option values.
func (r *Registry) ValidateLaunchOptions(provider leapmuxv1.AgentProvider, requested optionmap.Map) error {
	pm := requested.Get(OptionIDPermissionMode)
	if pm == "" {
		return nil
	}
	// Only a provider with a fixed, complete permission-mode enum can be checked here; an ACP
	// provider's modes are daemon-discovered, so leave them to the session.
	//
	// This asks its OWN question. It read Registry.ManagesEffort, which answers a different one
	// -- whether the effort tiers depend on the model -- and the two agreed only by coincidence.
	// A provider that gains a model-dependent effort catalog must not thereby gain the
	// authority to reject a permission mode.
	if !r.HasFixedPermissionModes(provider) {
		return nil
	}
	if !valueListedInGroup(r.StaticOptionGroups(provider), OptionIDPermissionMode, pm) {
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

// OptionDef is a lightweight option spec used by SelectGroup. The entry marked
// Default supplies the group's DefaultValue, so callers keep marking the default
// per option exactly as they did when AvailableOption carried IsDefault.
type OptionDef struct {
	Id            string
	Name          string
	Description   string
	Default       bool
	ContextWindow int64
	SubGroups     []*leapmuxv1.AvailableOptionGroup
	// Clears names the other option values this one settles as a side effect, so the
	// picker can say so before the click. See AvailableOption.clears in the proto.
	Clears []*leapmuxv1.OptionSideEffect
}

// SelectGroup builds a mutable (user-writable) option group from option
// specs, deriving DefaultValue from the entry flagged Default.
func SelectGroup(id, label string, order int32, current string, defs []OptionDef) *leapmuxv1.AvailableOptionGroup {
	opts := make([]*leapmuxv1.AvailableOption, 0, len(defs))
	def := ""
	for _, d := range defs {
		opts = append(opts, &leapmuxv1.AvailableOption{
			Id:            d.Id,
			Name:          d.Name,
			Description:   d.Description,
			ContextWindow: d.ContextWindow,
			SubGroups:     d.SubGroups,
			Clears:        d.Clears,
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

// ModelSubGroupsFunc returns the model-dependent option groups a given model
// determines (its effort tiers, plus any provider-specific group whose content
// varies by model, e.g. Claude's extended-thinking label). Providers supply one
// to ModelOptionGroup so each model option carries its own dependent groups,
// which the frontend swaps in the instant the model selection changes.
type ModelSubGroupsFunc func(m *ModelInfo) []*leapmuxv1.AvailableOptionGroup

// ModelOptionGroup projects a model catalog into the "model" option group.
// Each model becomes an option carrying its context window and -- when subGroups
// is non-nil -- its model-dependent sub_groups; the group's DefaultValue is the
// model flagged IsDefault. Returns nil for an empty catalog (e.g. a Claude
// session that hides model/effort UI), which omits the group.
func ModelOptionGroup(models []*ModelInfo, current string, subGroups ModelSubGroupsFunc) *leapmuxv1.AvailableOptionGroup {
	if len(models) == 0 {
		return nil
	}
	defs := make([]OptionDef, 0, len(models))
	for _, m := range models {
		if m == nil || m.Hidden {
			continue
		}
		def := OptionDef{
			Id:            m.Id,
			Name:          NameOrID(m.DisplayName, m.Id),
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
	return SelectGroup(OptionIDModel, ModelGroupLabel, OptionOrderModel, current, defs)
}

// ModelGroupLabel is the display label for the model option group. Defined once here so the
// selectable projection (ModelOptionGroup) and the read-only projections (providerkit.ReadOnlyModelAndEffortGroups,
// ensureModelGroup) can't drift on the label.
const ModelGroupLabel = "Model"

// EffortGroupLabel is the default display label for the model-dependent effort group.
// Providers whose effort axis is conceptually distinct override it -- Pi's CLI exposes
// a "thinking level" (set_thinking_level), so it passes pi.ThinkingLevelLabel instead.
const EffortGroupLabel = "Effort"

// EffortSubGroups is the default ModelSubGroupsFunc: a model's lone dependent
// group is its effort group (labeled "Effort"). Providers with additional model-dependent
// groups (Claude adds extended thinking) or a different effort label (Pi) wrap
// effortSubGroupsLabeled. Returns nil for an effort-less model.
func EffortSubGroups(m *ModelInfo) []*leapmuxv1.AvailableOptionGroup {
	return effortSubGroupsLabeled(m, EffortGroupLabel)
}

// effortSubGroupsLabeled builds a model's effort sub_groups under a caller-chosen
// label, so a provider's model-switch swap and its top-level effort group stay
// consistently named (e.g. Pi's "Thinking Level").
func effortSubGroupsLabeled(m *ModelInfo, label string) []*leapmuxv1.AvailableOptionGroup {
	if eg := EffortGroupForModel(m, "", label); eg != nil {
		return []*leapmuxv1.AvailableOptionGroup{eg}
	}
	return nil
}

// EffortSubGroupsLabeled returns a ModelSubGroupsFunc whose lone per-model group is the model's
// effort group under the given label. It is the one constructor for the "effort-only sub_groups
// under a chosen label" shape, shared by providerkit.ModelAndEffortGroups' default sub_groups and Pi's
// "Thinking Level" sub_groups so the two can't drift; EffortSubGroups is the unlabeled
// EffortGroupLabel default callers pass directly as a value.
func EffortSubGroupsLabeled(label string) ModelSubGroupsFunc {
	return func(m *ModelInfo) []*leapmuxv1.AvailableOptionGroup {
		return effortSubGroupsLabeled(m, label)
	}
}

// EffortGroupForModel projects a single model's supported efforts into the
// "effort" option group under the given label, with DefaultValue set to the model's
// default effort. Returns nil when the model is unknown or offers no efforts (so the
// group is omitted), matching the prior behavior of hiding effort for effort-less models.
func EffortGroupForModel(m *ModelInfo, currentEffort, label string) *leapmuxv1.AvailableOptionGroup {
	if m == nil || len(m.SupportedEfforts) == 0 {
		return nil
	}
	defs := make([]OptionDef, 0, len(m.SupportedEfforts))
	for _, e := range m.SupportedEfforts {
		if e == nil {
			continue
		}
		defs = append(defs, OptionDef{
			Id:          e.Id,
			Name:        NameOrID(e.Name, e.Id),
			Description: e.Description,
			Default:     e.Id == m.DefaultEffort,
		})
	}
	if len(defs) == 0 {
		return nil
	}
	return SelectGroup(OptionIDEffort, label, OptionOrderEffort, currentEffort, defs)
}

// ReadOnlyValueGroup builds a single-option, non-mutable group that surfaces a value
// the user cannot change -- e.g. a third-party Claude session's fixed model, which has
// no selectable catalog but should still be visible to `remote agent get`/list and the
// UI rather than rendering blank. The lone option is the current value, so the picker
// shows it as a fixed readout. name is the option's display label (e.g. a humanized
// model name); it falls back to the raw value when empty.
func ReadOnlyValueGroup(id, label string, order int32, value, name string) *leapmuxv1.AvailableOptionGroup {
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

// OptionGroupSetEqualExact reports whether two option-group slices are EXACTLY equal -- every
// field of every group (current/default value, label, mutability, order) plus the same option
// SET -- keyed by id and compared with optionGroupEqualExact, independent of slice order (between
// groups AND within each group's option list). apply dedups by id, so each slice has unique ids
// and a length+by-id-equal check is an exact set comparison. Contrast optionGroupSetStructureEqual,
// which compares only ids and ignores values/labels/mutability.
//
// Exported so the service layer's catalog-change detection (persistCatalogIfChanged) shares
// ONE definition of "unchanged catalog" with the ACP layer's own change detection -- otherwise
// an order-sensitive service comparator would fire a redundant option_groups write whenever a
// server merely re-sent the same groups/options in a different order (the exact churn this
// comparator was made order-insensitive to avoid).
func OptionGroupSetEqualExact(a, b []*leapmuxv1.AvailableOptionGroup) bool {
	if len(a) != len(b) {
		return false
	}
	index := make(map[string]*leapmuxv1.AvailableOptionGroup, len(a))
	for _, g := range a {
		index[g.GetId()] = g
	}
	for _, g := range b {
		prev, ok := index[g.GetId()]
		if !ok || !optionGroupEqualExact(prev, g) {
			return false
		}
	}
	return true
}

// optionGroupEqualExact compares two groups exactly EXCEPT it treats their option lists as sets: a
// server re-sending the same options in a different display order is not a meaningful change, so
// it must not be reported as a list change (which would fire a redundant status broadcast +
// catalog write). The effort/thought_level axis is already canonicalized strongest-first by
// buildOptionGroup, so this only changes the result for other selects (e.g. Reasonix tool_approval).
// The stored slice still keeps the latest order (apply assigns g.groups = groups), so a genuine
// reorder rides along on the next real change. Clone-then-sort by id, then proto.Equal compares
// every other field (current/default value, label, mutability, order) exactly.
func optionGroupEqualExact(a, b *leapmuxv1.AvailableOptionGroup) bool {
	return proto.Equal(sortGroupOptionsByID(a), sortGroupOptionsByID(b))
}

// sortGroupOptionsByID returns a clone of g with its top-level options sorted by id, so two
// groups differing only in option order compare equal under proto.Equal. Clones first so the
// caller's group (shared via OptionGroups snapshots) is never reordered in place.
func sortGroupOptionsByID(g *leapmuxv1.AvailableOptionGroup) *leapmuxv1.AvailableOptionGroup {
	c := proto.Clone(g).(*leapmuxv1.AvailableOptionGroup)
	slices.SortFunc(c.Options, func(x, y *leapmuxv1.AvailableOption) int {
		return strings.Compare(x.GetId(), y.GetId())
	})
	return c
}

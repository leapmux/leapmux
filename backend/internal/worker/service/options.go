package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"google.golang.org/protobuf/encoding/protojson"
)

// OptionMap is the agent's option id->value map -- the agents.options column, the proto
// Settings.Options map, the launch-option set, and a refresh delta are all this shape. It is an
// ALIAS for optionmap.Map, the leaf type both this layer and worker/agent share, so the
// clone-on-write + empty-value (delete-on-merge / drop-on-marshal) contract lives in ONE place
// (see the optionmap package doc) instead of being re-stated at each boundary.
type OptionMap = optionmap.Map

// parseOptions decodes the agents.options JSON column into an OptionMap, dropping empty values.
// Never nil.
func parseOptions(raw string) OptionMap { return optionmap.Parse(raw) }

// marshalOptions, mergeOptions, and resolveProviderDefaults are the canonical caller-facing
// spelling for the three OptionMap operations: callers (and tests) use these uniformly rather
// than mixing in the equivalent OptionMap methods, so there is one vocabulary to read. The
// wrappers also accept a bare map[string]string argument (assignable to OptionMap) without an
// explicit conversion, which the method form would force at every map-literal call site.

// marshalOptions encodes an OptionMap for the agents.options column (see optionmap.Map.Marshal).
func marshalOptions(options OptionMap) string { return options.Marshal() }

// mergeOptions overlays incoming onto current with the empty-deletes wire semantics (see
// optionmap.Map.Merge).
func mergeOptions(current, incoming OptionMap) OptionMap { return current.Merge(incoming) }

// resolveProviderDefaults returns a clone of options with missing well-known and provider-
// specific values filled: the model and effort defaults, plus the provider's seed defaults
// (e.g. Codex's sandbox / network / collaboration / service-tier). options is left untouched.
// It lives here, not on optionmap.Map, because filling defaults needs the worker/agent
// provider registry, which the leaf option-map package deliberately does not depend on.
func resolveProviderDefaults(options OptionMap, provider leapmuxv1.AgentProvider) OptionMap {
	out := options.Clone()
	if out[agent.OptionIDModel] == "" {
		out[agent.OptionIDModel] = agent.DefaultModel(provider)
	}
	// An explicit operator effort override (LEAPMUX_*_DEFAULT_EFFORT) is honored for
	// any provider that registers one: a catalog-effort provider (Claude/Codex/Pi)
	// pins it as the launch effort, while an ACP provider (Kilo/OpenCode) has it
	// re-pushed to the server's "effort" config option by applyStartupOptions.
	// Only the blanket EffortAuto fallback is gated to catalog-effort providers -- an
	// unset override must NOT stamp a default that would shadow a server-driven axis
	// or leave an inert "effort" key on a provider with no effort axis at all.
	if out[agent.OptionIDEffort] == "" {
		if env := agent.EffortEnvOverride(provider); env != "" {
			out[agent.OptionIDEffort] = env
		} else if agent.ProviderManagesEffort(provider) {
			out[agent.OptionIDEffort] = agent.EffortAuto
		}
	}
	// Provider-specific seed defaults, declared in the provider's registry entry so this layer
	// carries no per-provider branch.
	for id, def := range agent.ProviderOptionDefaults(provider) {
		if out[id] == "" {
			out[id] = def
		}
	}
	return out
}

// sortedOptionKeys returns the sorted union of keys across the given maps.
func sortedOptionKeys(mapsToMerge ...OptionMap) []string {
	keys := make(map[string]struct{})
	for _, options := range mapsToMerge {
		for key := range options {
			keys[key] = struct{}{}
		}
	}
	if len(keys) == 0 {
		return nil
	}
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// loadOptions parses the persisted options JSON and fills in provider defaults.
func loadOptions(raw string, provider leapmuxv1.AgentProvider) OptionMap {
	return resolveProviderDefaults(parseOptions(raw), provider)
}

// marshalOptionGroups serializes the option-group catalog for the agents.option_groups column.
// It returns an error rather than a TRUNCATED catalog when any group fails to marshal: a partial
// slice would never compare equal (optionGroupsEqual) to the full live catalog, so persisting it
// would churn the column on every push and silently hide the dropped group. Callers must skip the
// persist on error and keep the prior catalog (the next clean push re-persists a full one).
//
// NOTE: protojson.Marshal is intentionally NON-deterministic (it randomizes whitespace between
// runs), so the output must NOT be string-compared for change detection. The options-value CAS
// (PersistSettingsRefresh) compares marshalOptions output, which uses deterministic
// encoding/json; this proto-slice form is for storage only. A future "skip the option_groups
// write when unchanged" guard must diff the decoded slices (optionGroupsEqual / proto.Equal),
// not the marshaled strings, or it would churn every time.
func marshalOptionGroups(groups []*leapmuxv1.AvailableOptionGroup) (string, error) {
	if len(groups) == 0 {
		return "[]", nil
	}
	raw := make([]json.RawMessage, 0, len(groups))
	for _, g := range groups {
		b, err := protojson.Marshal(g)
		if err != nil {
			return "", fmt.Errorf("marshal AvailableOptionGroup %q: %w", g.GetId(), err)
		}
		raw = append(raw, b)
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return "", fmt.Errorf("marshal AvailableOptionGroup slice: %w", err)
	}
	return string(data), nil
}

// parseOptionGroups deserializes the agents.option_groups column. A single malformed group
// drops the WHOLE catalog (returns nil) rather than a truncated slice -- the all-or-nothing
// mirror of marshalOptionGroups, which errors rather than drop a group. A partial slice would
// be doubly harmful: the offline picker would silently omit a whole axis (a dropped group reads
// as "absent"), and it would never compare equal (optionGroupsEqual) to the full live catalog,
// churning the option_groups column on every status push. Returning nil instead falls back to
// the static reconstruction (structurally complete for the well-known axes) and lets the next
// clean live push re-persist a full catalog.
func parseOptionGroups(raw string) []*leapmuxv1.AvailableOptionGroup {
	if raw == "" || raw == "[]" {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		slog.Warn("invalid option_groups JSON", "error", err)
		return nil
	}
	groups := make([]*leapmuxv1.AvailableOptionGroup, 0, len(items))
	for _, item := range items {
		g := &leapmuxv1.AvailableOptionGroup{}
		if err := protojson.Unmarshal(item, g); err != nil {
			slog.Warn("invalid option_groups: dropping whole catalog on malformed group", "error", err)
			return nil
		}
		groups = append(groups, g)
	}
	return groups
}

// optionGroupsEqual reports whether two catalogs are equivalent, delegating to
// agent.OptionGroupSetEqualExact so this layer and the ACP layer share ONE definition of
// "unchanged catalog": keyed by group id (group order-INSENSITIVE) with option lists compared as
// sets (option order-insensitive), via proto.Equal per group. Used to decide whether a freshly-
// built live catalog differs from the persisted one before writing it back -- order-insensitive
// so a server re-sending the same groups/options in a different order doesn't churn the
// option_groups column, and proto.Equal is immune to the non-deterministic whitespace
// marshalOptionGroups (protojson) emits.
func optionGroupsEqual(a, b []*leapmuxv1.AvailableOptionGroup) bool {
	return agent.OptionGroupSetEqualExact(a, b)
}

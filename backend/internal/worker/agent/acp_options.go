package agent

// acp_options.go holds the optionState config-option state machine -- the bookkeeping for an
// ACP provider's server-driven config options (the selectors the model and mode channels do not
// claim: effort, reasoning_effort, allow_all, ...). Split out of acp_common.go so the option
// lifecycle reads in one place; every method here assumes the caller holds the owning acpBase.mu.

import (
	"container/list"
	"maps"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
)

// optionState bundles the bookkeeping for an ACP provider's server-driven config option
// config options -- the selectors the model and mode channels do not claim (effort,
// reasoning_effort, allow_all, ...). It is a field of acpBase, NOT a standalone locked
// object: every method here assumes the caller holds the OWNING acpBase.mu (they are the
// former *Locked methods). Carrying its own mutex would break the single-snapshot atomicity
// applySessionRefresh relies on (it pairs an option change with the model/secondary under one
// acpBase.mu critical section), so the type deliberately has none.
type optionState struct {
	// groups holds the surfaced config-option selectors -- a thought_level axis (OpenCode/Kilo
	// "effort", Copilot "reasoning_effort"), a permissions axis (Copilot "allow_all"), or any
	// other select. They are surfaced as MUTABLE groups: displayed via OptionGroups(),
	// kept in sync at handshake / runtime / ClearContext, and written back via
	// session/set_config_option (applyOptionUpdates). nil when the provider emits none.
	groups []*leapmuxv1.AvailableOptionGroup
	// values maps each config option's id to its current value, so the live selection rides
	// along in the persisted option values and tells UpdateSettings which ids are writable
	// config options. nil when none; never an empty map (the keep-nil guard in apply preserves nil).
	values map[string]string
	// pendingValues is set ONLY for the duration of apply: it aliases the in-flight payload's
	// surfaced-values map while that map is still being built, before apply commits it to `values`
	// at the end. valued() consults it so an id first valued in THIS payload is protected from LRU
	// eviction by a LATER id in the same payload -- markKnown/markSurfaced run inside apply's loop,
	// where `values` still holds the PRIOR payload, so without this a same-payload first-sighting
	// valued id could be evicted, stranding its value, its template, and its pending "" delete. nil
	// outside apply.
	pendingValues map[string]string
	// known records every config option id the server has ADVERTISED this session (whether or
	// not it surfaced a concrete value). It gates the two write paths: setConfigOption
	// accepts a write only for an advertised id, and applyStartupOptions re-pushes a
	// persisted preference only for one -- so an option advertised with an empty current at
	// handshake stays writable/re-pushable. LRU-bounded (boundedIDSet) so a non-conforming
	// server cycling distinct ids can't grow it without bound.
	known *boundedIDSet
	// surfaced records every config option id this session has surfaced with a CONCRETE value.
	// When a later (complete) configOptions payload drops one -- reports a smaller option set
	// than before -- mergeOptionValues emits the dropped id as an explicit "" so the uniform refresh
	// merge DELETES its stale stored value instead of preserving it (an absent key is
	// preserved). It is a strict subset of `known`: an id advertised but never valued has no
	// stored value to delete, so emitting "" for it would be a redundant no-op (and could wipe
	// a persisted preference awaiting re-push). LRU-bounded, like `known`.
	surfaced *boundedIDSet
	// templates holds the last-advertised acpConfigOption for each known id, so a write the
	// server accepts but does NOT echo in a refreshed configOptions (off-spec) can still
	// synthesize a proper option group in recordOptimistic -- with the real option list, not a
	// degenerate one. Kept in lockstep with `known`: markKnown stores the template and drops the
	// one for any id `known` evicts, so it inherits the same bound.
	templates map[string]acpConfigOption
	// structureGen is a monotonic counter bumped every time a fold changes the group-set STRUCTURE
	// (the set of group ids and their option ids -- optionGroupSetStructureEqual, ignoring current/
	// default values and labels). A live UpdateSettings reads it before and after its writes to tell
	// whether THIS span saw a structural fold, WITHOUT racing the reader goroutine on the `groups`
	// slice: comparing the slice itself (snapshot-before vs the shared field after) can read a
	// reader's concurrent reassignment as the "after", spuriously firing or -- when the reader
	// reverts a structure this span changed -- suppressing the broadcast. The counter increments on
	// every structural fold from either goroutine, so a difference across the span means a structural
	// change happened during it (a reader-only change merely fires a harmless idempotent broadcast,
	// as it did before). Bumped under the owning acpBase.mu, like the rest of optionState.
	structureGen uint64
}

// maxOptionStateIDs caps the known/surfaced config-option id sets. The ids are a tiny fixed
// set for conforming providers (model, mode, effort, reasoning_effort, allow_all), so this is
// a safety net against a non-conforming server cycling distinct config-option ids without
// bound -- the backend mirror of the frontend settingsLabelCache's per-group cap.
const maxOptionStateIDs = 256

// boundedIDSet is an insertion-ordered string set with LRU eviction at maxOptionStateIDs:
// adding (or re-adding) an id makes it most-recently-used, and the least-recently-used id is
// evicted once the cap is exceeded. It mirrors settingsLabelCache.setLabelWithCap -- each
// configOptions payload re-marks its current ids (keeping them fresh), so an id that stops
// appearing drifts to the front and is evicted first. It carries no lock of its own; the
// owning acpBase.mu guards it like every other optionState field. The read methods are
// nil-safe so a never-populated set reads as empty.
type boundedIDSet struct {
	order *list.List               // front = least-recently-used, back = most-recently-used; values are ids
	elems map[string]*list.Element // id -> its element in `order`
}

func newBoundedIDSet() *boundedIDSet {
	return &boundedIDSet{order: list.New(), elems: make(map[string]*list.Element)}
}

// add marks id present and most-recently-used, evicting the least-recently-used id that is NOT
// protected when the cap is exceeded. `protected` reports ids that must never be evicted -- a
// config option carrying a live value, whose eviction would strand a persisted selection (a
// stale value mergeOptionValues can no longer delete, a live edit rejected as "unknown config
// option", a degenerate synthesized group); pass nil to protect nothing. Scanning past protected
// ids lets a pathological all-valued set grow past the cap, which is correct: those ids are
// genuinely live (and `values`, the unbounded source of truth, already holds them), so the bound
// only sheds the UNVALUED advertisement churn it exists to cap. Returns the evicted id (and true)
// so a caller mirroring the set in a side table -- e.g. optionState.templates -- can prune the
// same id; ("", false) when nothing was evicted. The caller has lazily created the set, so a nil
// receiver is a programmer error.
func (s *boundedIDSet) add(id string, protected func(string) bool) (evicted string, didEvict bool) {
	if e, ok := s.elems[id]; ok {
		s.order.MoveToBack(e)
		return "", false
	}
	s.elems[id] = s.order.PushBack(id)
	if s.order.Len() <= maxOptionStateIDs {
		return "", false
	}
	// Evict the least-recently-used UNPROTECTED id (scan front=LRU toward back). Never evict the
	// id just added (it sits at the back); if everything is protected, allow temporary over-cap
	// growth rather than dropping a live id.
	for e := s.order.Front(); e != nil; e = e.Next() {
		evictedID := e.Value.(string)
		if evictedID == id || (protected != nil && protected(evictedID)) {
			continue
		}
		delete(s.elems, evictedID)
		s.order.Remove(e)
		return evictedID, true
	}
	return "", false
}

// has reports whether id is in the set.
func (s *boundedIDSet) has(id string) bool {
	if s == nil {
		return false
	}
	_, ok := s.elems[id]
	return ok
}

// keys returns a snapshot of the ids in least-to-most-recently-used order.
func (s *boundedIDSet) keys() []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.elems))
	for e := s.order.Front(); e != nil; e = e.Next() {
		out = append(out, e.Value.(string))
	}
	return out
}

// len reports how many ids the set holds. nil-safe (a never-populated set is empty).
func (s *boundedIDSet) len() int {
	if s == nil {
		return 0
	}
	return len(s.elems)
}

// markKnown records that the server has advertised a config option, so
// setConfigOption (which gates writes on `known`) will accept a write for it -- even
// one apply declines to surface yet. It also stashes the option's template (its real option
// list) so recordOptimistic can synthesize a proper group for an off-spec write the server
// doesn't echo, and drops the template for any id the LRU evicts so the two stay in lockstep.
// Caller holds the owning acpBase.mu.
func (g *optionState) markKnown(option acpConfigOption) {
	if g.known == nil {
		g.known = newBoundedIDSet()
	}
	if g.templates == nil {
		g.templates = make(map[string]acpConfigOption)
	}
	if evicted, didEvict := g.known.add(option.ID, g.valued); didEvict {
		delete(g.templates, evicted)
	}
	g.templates[option.ID] = option
}

// valued reports whether a config-option id currently carries a value (so it has a persisted
// selection at stake). markKnown/markSurfaced pass it as boundedIDSet.add's eviction guard, so a
// live id is never evicted from `known`/`surfaced` -- which would strand its stale value
// (mergeOptionValues could not emit the delete), reject a live edit to it as "unknown config
// option" (setConfigOption gates on `known`), or drop its template (recordOptimistic would
// synthesize a degenerate single-option group). `values` is the unbounded source of truth, so an
// evicted UNvalued id loses nothing. During apply it also consults `pendingValues` (the in-flight
// payload's not-yet-committed values) so an id first valued in THAT payload is protected too.
func (g *optionState) valued(id string) bool {
	if _, ok := g.values[id]; ok {
		return true
	}
	_, ok := g.pendingValues[id]
	return ok
}

// markSurfaced records that a config option surfaced a concrete value this session,
// so mergeOptionValues emits an explicit "" delete if a later complete payload drops it. Distinct
// from markKnown (advertised, not necessarily valued): only a once-valued id can leave a stale
// persisted value worth deleting. Caller holds the owning acpBase.mu.
func (g *optionState) markSurfaced(id string) {
	if g.surfaced == nil {
		g.surfaced = newBoundedIDSet()
	}
	g.surfaced.add(id, g.valued)
}

// payloadAuthority selects, when a config-option id appears in BOTH the incoming payload and
// the stored in-memory option values, which one wins -- replacing a bare bool that flipped this
// precedence at a distance.
type payloadAuthority int

const (
	// authoritativePayload: the payload's CurrentValue wins (handshake, a server-initiated
	// config_option_update, or a write's own response), falling back to the stored value only
	// when the payload's value is empty (a transient partial update).
	authoritativePayload payloadAuthority = iota
	// preferStoredValue: the stored in-memory value wins over the payload's (the ClearContext
	// refresh, whose captured snapshot predates the reapply re-push), while a stored value the
	// payload no longer offers still falls through to the payload's value.
	preferStoredValue
)

// resolveCurrent resolves a config option's current value and whether to surface it
// as a group now. A select always has a current value, so an empty CurrentValue is a transient
// server quirk (a partial config_option_update), not a deliberate clear: fall back to the
// prior in-memory selection rather than recording the empty (which mergeOptionValues would propagate
// as a delete, wiping the user's choice). When nothing is stored either -- a first sighting
// with an empty current -- the value is "not yet known", so it is not surfaced (the frontend
// has no default to fall back on); a later payload with a concrete current, or
// applyStartupOptions re-pushing the persisted preference, surfaces it for real. Either
// way the id is recorded as known, so the re-push is accepted (`values` is never seeded from
// the DB, so this is the only guard for the first-handshake case). Caller holds the owning
// acpBase.mu.
func (g *optionState) resolveCurrent(option acpConfigOption, authority payloadAuthority) (current string, surface bool) {
	g.markKnown(option)
	// On a ClearContext refresh (preferStoredValue) the captured session/new snapshot predates
	// reapplyOptions' re-push, so its CurrentValue is the server default, stale
	// relative to the value the re-push just confirmed (folded in from each
	// set_config_option response). Prefer the in-memory value so a context clear keeps the
	// user's choice instead of reverting it. An option the new session no longer reports is
	// still dropped -- it simply won't appear in this payload. For the authoritative paths
	// (handshake, a server-initiated config_option_update, a write's own response) the
	// payload's CurrentValue wins, falling back to the stored value only when it is empty (a
	// transient partial update).
	if authority == preferStoredValue {
		// The in-memory value wins over the snapshot's (re-push-predating) CurrentValue --
		// but only while it is still a selectable option of THIS payload (storedIfOffered). A
		// ClearContext re-push the server rejected can leave a stored value the new session no
		// longer offers; surfacing it would render a current absent from its own option list
		// (the model and mode channels already guard this via reconcileCurrentOptionID, but the
		// option channel did not). When the stored value isn't selectable, fall through to the
		// payload's authoritative CurrentValue.
		if stored, ok := g.storedIfOffered(option); ok {
			return stored, true
		}
		if option.CurrentValue != "" {
			return option.CurrentValue, true
		}
		return "", false
	}
	current = option.CurrentValue
	if current == "" {
		// A transient partial update (an empty CurrentValue on a select that always has one)
		// falls back to the prior in-memory selection -- but only while that value is still
		// offered by THIS payload's option list (storedIfOffered). A stored value the new list
		// no longer offers must not be surfaced as a current absent from its own options
		// (buildOptionValues does NOT inject the current, so an out-of-list current would render
		// a selection with no matching radio). Treat it as not-yet-known instead; a later
		// payload carrying a concrete current surfaces it for real.
		if stored, ok := g.storedIfOffered(option); ok {
			return stored, true
		}
		return "", false
	}
	return current, current != ""
}

// storedIfOffered returns the in-memory selection for option's id and true when it is non-empty
// AND still a selectable value of THIS payload, so a stored value the new option list dropped is
// never surfaced as a current absent from its own options. Shared by resolveCurrent's two
// authority branches so the "stored only while still offered" guard lives in one place.
// Caller holds the owning acpBase.mu.
func (g *optionState) storedIfOffered(option acpConfigOption) (string, bool) {
	if stored := g.values[option.ID]; stored != "" && configOptionOffers(option, stored) {
		return stored, true
	}
	return "", false
}

// configOptionOffers reports whether value is one of the option's selectable values.
// Used to reconcile a stored current against a freshly-reported option list so a
// ClearContext refresh never surfaces a selection the new session no longer offers.
func configOptionOffers(option acpConfigOption, value string) bool {
	return configOptionValuesContain(option.Options, value)
}

// configOptionValuesContain reports whether value is one of the selectable values. Split from
// configOptionOffers so the value-slice form is reusable where only the option list is in hand
// (appendConfigOptionValueIfMissing).
func configOptionValuesContain(values []acpConfigOptionValue, value string) bool {
	for _, o := range values {
		if o.Value == value {
			return true
		}
	}
	return false
}

// appendConfigOptionValueIfMissing returns options with value appended as a selectable entry when
// value is non-empty and not already offered, copying the slice first so a shared/stored option
// list is never mutated. A no-op (returns options unchanged) for an empty or already-present value.
// Shared by buildOptionGroup (a server's authoritative current absent from its own list) and
// recordOptimistic (a value the server accepted but did not echo in its option list), so the
// "current always has a matching option" invariant is established in one place.
func appendConfigOptionValueIfMissing(options []acpConfigOptionValue, value string) []acpConfigOptionValue {
	if value == "" || configOptionValuesContain(options, value) {
		return options
	}
	out := make([]acpConfigOptionValue, len(options), len(options)+1)
	copy(out, options)
	return append(out, acpConfigOptionValue{Value: value})
}

// offersValue reports whether the config option id's last-advertised option list contains
// value. Returns true when the id has no template or an empty option list -- there is nothing
// to validate against (an option advertised with an empty current, or a degenerate
// synthesized template), so a persisted preference can still be re-pushed for a not-yet-listed
// option. setConfigOption uses it to drop a write whose value the current model does not offer
// (e.g. a stale effort tier inherited across a model switch) rather than force-pushing a value
// the daemon would reject -- which would fail the live edit and bounce it into a relaunch.
// Caller holds the owning acpBase.mu.
func (g *optionState) offersValue(id, value string) bool {
	tmpl, ok := g.templates[id]
	if !ok || len(tmpl.Options) == 0 {
		return true
	}
	return configOptionOffers(tmpl, value)
}

// buildOptionGroup builds the mutable option group for an unclaimed config-option
// selector (effort / reasoning_effort / allow_all, ...), reordering a reasoning-effort
// (thought_level) axis strongest-first since servers report it weakest-first.
func buildOptionGroup(option acpConfigOption, current string) *leapmuxv1.AvailableOptionGroup {
	// Guarantee the current value is a selectable option of the group. A (non-conforming) server
	// can report a CurrentValue absent from its own option list, and buildOptionValues builds the
	// list from option.Options ONLY -- it does not inject the current. Without this the group would
	// carry a CurrentValue with no matching radio, and CurrentOptions/mergeOptionValues would
	// persist that out-of-list value. resolveCurrent's stored-value branches already guard via
	// storedIfOffered; injecting here closes the same gap for the paths that surface the server's
	// OWN authoritative current (the authoritative non-empty path and the prefer-stored fallback).
	// option is passed by value and appendConfigOptionValueIfMissing copies the slice, so the
	// caller's payload option is untouched. A no-op for a conforming server (current already
	// listed). This also subsumes recordOptimistic's off-spec injection, so it lives in one place.
	option.Options = appendConfigOptionValueIfMissing(option.Options, current)
	opts := buildOptionValues(option, nil)
	if isEffortConfigOption(option) {
		sortEffortOptionsDescending(opts)
	}
	return &leapmuxv1.AvailableOptionGroup{
		Id:           option.ID,
		Label:        nameOrID(option.Name, option.ID),
		Options:      opts,
		CurrentValue: current,
		// ACP reports no separate default for a config option, so the default tracks the
		// current value (there is nothing else to point at).
		DefaultValue: current,
		// Writable via session/set_config_option (applyOptionUpdates): these are
		// user-selectable selects, not agent-owned read-only state.
		Mutable: true,
		Order:   OptionOrderTrailing,
	}
}

// applyOptionGroupsLocked surfaces the config-option selectors the model and
// mode channels did not claim as additional, mutable option groups, recording
// each one's current value. It mirrors applyConfigOptionModelsLocked: the caller
// holds b.mu, and it reports whether a current value changed (valueChanged) versus
// only the option set changed (listChanged) so handleACPConfigOptionUpdate can route
// a value change through broadcastSettingsRefresh (which persists it) and a list-only
// change through a status refresh.
//
// The claimed model and mode options are excluded by identity (the matched entry's
// id), so the permission-mode/primary-agent group -- the claimed "mode" -- is never
// double-rendered as a option group.
//
// Complete-snapshot semantics: every configOptions payload is the COMPLETE set of the
// options that currently apply -- verified across all six ACP providers (Goose, Kilo,
// OpenCode, Cursor, Copilot, Reasonix); none emits a partial/delta payload, and an option
// is omitted ONLY when it no longer applies (e.g. Copilot drops reasoning_effort for a
// model without effort support; OpenCode/Kilo drop effort for a model without variants).
// So an option absent from a NON-EMPTY payload no longer applies and is dropped --
// optionState.mergeOptionValues then deletes its stale persisted value via its `surfaced` set.
// The only preserve case is an EMPTY payload (len(options) == 0): no configOptions were
// delivered at all (e.g. a session response before the model inventory resolved), so there
// is no information and the stored options are left untouched.
//
// The payload's CurrentValue is authoritative here (handshake, a server-initiated
// config_option_update, or a write's own response). The ClearContext refresh instead calls
// applyOptionGroupsKeepingStoredLocked, whose captured snapshot predates the reapply
// re-push and so must NOT override the just-re-applied value.
func (b *acpBase) applyOptionGroupsLocked(options []acpConfigOption) (valueChanged, listChanged bool) {
	return b.options.apply(options, authoritativePayload, b.modeChannel)
}

// applyOptionGroupsKeepingStoredLocked folds a ClearContext session-refresh payload
// but keeps the in-memory value of any option still present, so a context clear doesn't
// revert a value reapplyOptions just re-pushed (the user's choice) to the captured
// snapshot's server default. An option the new session no longer reports is still dropped
// (it won't appear in the payload). Caller holds b.mu.
func (b *acpBase) applyOptionGroupsKeepingStoredLocked(options []acpConfigOption) (valueChanged, listChanged bool) {
	return b.options.apply(options, preferStoredValue, b.modeChannel)
}

// apply is the shared core of the two acpBase wrappers above; authority picks the value
// precedence when an id is in BOTH the payload and the in-memory option values (see
// resolveCurrent). modeChannel tells it which mode option (if any) the provider already
// consumes, so a claimed mode isn't double-rendered as an option. Caller holds the owning
// acpBase.mu.
func (g *optionState) apply(options []acpConfigOption, authority payloadAuthority, modeChannel acpModeChannel) (valueChanged, listChanged bool) {
	// No configOptions delivered at all -> no information; leave the stored options
	// untouched rather than wiping them. A non-empty payload is authoritative and complete
	// (see the doc above), so it MAY legitimately drop an option that no longer applies.
	if len(options) == 0 {
		return false, false
	}
	// Capture the claimed model/mode options by identity so we exclude exactly those
	// entries -- an unclaimed selector with a coincidental id is still surfaced.
	claimedModelID, claimedModel := "", false
	if option, ok := acpConfigOptionByCategory(options, acpConfigOptionCategoryModel, acpConfigOptionIDModel); ok {
		claimedModelID, claimedModel = option.ID, true
	}
	// The mode option is "claimed" only by a provider that actually consumes it (a
	// permission-mode or primary-agent provider). A provider whose mode channel is
	// unmapped would otherwise exclude a configOptions `mode` here without
	// applying it anywhere -- silently dropping it. Surfacing it as a option group is
	// the safe fallback.
	claimedModeID, claimedMode := "", false
	if modeChannel != modeChannelUnmapped {
		if option, ok := acpConfigOptionByCategory(options, acpConfigOptionCategoryMode, acpConfigOptionIDMode); ok {
			claimedModeID, claimedMode = option.ID, true
		}
	}

	var groups []*leapmuxv1.AvailableOptionGroup
	values := make(map[string]string)
	// Expose the payload's surfaced values to the eviction guard (valued) as they accrue, so an
	// id valued earlier in THIS loop is protected when a later id triggers LRU eviction. Without
	// it, valued reads only the prior payload (committed to g.values at the end), so a same-payload
	// first-sighting valued id could be evicted -- stranding its value, template, and pending ""
	// delete. Cleared once the loop returns; markKnown/markSurfaced run only within it.
	g.pendingValues = values
	defer func() { g.pendingValues = nil }()
	// First pass: choose, per id, the single config option to surface. A (non-conforming) server
	// may report the same id more than once; resolve the duplicate to the content-smallest occurrence
	// DETERMINISTICALLY (acpConfigOptionContentLess) -- regardless of the order the server lists the
	// duplicates -- so the surfaced group can't flip its value/options between two payloads that list
	// the same duplicates in different orders (which would fire a redundant status broadcast + catalog
	// write each push). This mirrors acpSelectableConfigOptionByID's tie-break for the claimed
	// model/mode axes. A single winner per id is required regardless: two groups sharing a key corrupt
	// the frontend's <For each={groupIds()}> reconciliation (it keys rows by id). First-occurrence
	// order is kept for display; the change comparators below are order-INSENSITIVE, so only the per-id
	// CHOICE is significant.
	order := make([]string, 0, len(options))
	winner := make(map[string]acpConfigOption, len(options))
	for _, option := range options {
		if option.ID == "" || !isSelectableConfigOption(option) {
			continue
		}
		if (claimedModel && option.ID == claimedModelID) || (claimedMode && option.ID == claimedModeID) {
			continue
		}
		// Never surface a option group under a reserved proto key: the mapped model and
		// permission-mode/primary-agent groups already own those keys, and a second group
		// with the same key would double-list it in AvailableOptionGroups.
		if isReservedOptionKey(option.ID) {
			continue
		}
		if existing, ok := winner[option.ID]; ok {
			if acpConfigOptionContentLess(option, existing) {
				winner[option.ID] = option
			}
			continue
		}
		winner[option.ID] = option
		order = append(order, option.ID)
	}
	// Second pass: surface each id's winning option once, in first-occurrence order. resolveCurrent
	// (markKnown) and markSurfaced run exactly once per surviving id, as before.
	for _, id := range order {
		option := winner[id]
		current, surface := g.resolveCurrent(option, authority)
		if !surface {
			continue
		}
		groups = append(groups, buildOptionGroup(option, current))
		values[id] = current
		g.markSurfaced(id)
	}

	// A complete payload with no surfaced options genuinely has none now, so rebuild to the
	// empty set -- this clears an option the latest payload dropped (and mergeOptionValues then
	// deletes its persisted value). Keep `values` nil rather than an empty map so the
	// "never an empty map" invariant downstream readers rely on holds.
	if len(values) == 0 {
		values = nil
	}

	// maps.Equal tells an idempotent re-send from a real value change; OptionGroupSetEqualExact
	// (proto.Equal per group) compares key, label, and nested options, so a future field added
	// to AvailableOptionGroup is included automatically rather than silently ignored by a
	// hand-rolled key/label-only comparison. The list compare is keyed by id (order-INSENSITIVE,
	// matching the order-insensitive value compare): every ACP config group shares
	// OptionOrderTrailing, so a server re-sending the same options in a different order is not a
	// meaningful change, and treating it as one would fire a redundant status broadcast +
	// catalog write. The latest slice order is still stored (g.groups = groups), so a genuine
	// reorder is picked up on the next broadcast.
	valueChanged = !maps.Equal(g.values, values)
	listChanged = !OptionGroupSetEqualExact(g.groups, groups)
	// Bump the structure generation when the group SET structure changes (structure-only, ignoring
	// current/default values -- the same comparison the live UpdateSettings broadcast is gated on),
	// so that broadcast can detect its own structural fold race-free (see structureGen). Computed
	// against the OLD g.groups, before the reassignment below.
	if !optionGroupSetStructureEqual(g.groups, groups) {
		g.structureGen++
	}
	g.groups = groups
	g.values = values
	return valueChanged, listChanged
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
// buildOptionGroup, so this only changes the result for other selects (e.g. Copilot allow_all).
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

// optionGroupSetStructureEqual reports whether two option-group sets are structurally identical
// -- the same group ids, each offering the same SET of option ids -- IGNORING current/default
// values, labels, and mutability. Contrast OptionGroupSetEqualExact, which compares those too.
// The live UpdateSettings path uses it to decide whether a change
// needs a status refresh: the frontend rebuilds its option-group catalog only from statusChange
// events, so a group appearing/disappearing or an option list changing (e.g. switching to a model
// whose reasoning variants differ) must be pushed, but a pure current-value change must NOT --
// that rides the settings reply, and broadcasting the catalog for it would be redundant
// (and would fire on every effort/mode edit). Group and option order are both insensitive.
func optionGroupSetStructureEqual(a, b []*leapmuxv1.AvailableOptionGroup) bool {
	structure := func(groups []*leapmuxv1.AvailableOptionGroup) map[string][]string {
		out := make(map[string][]string, len(groups))
		for _, g := range groups {
			ids := make([]string, 0, len(g.GetOptions()))
			for _, o := range g.GetOptions() {
				ids = append(ids, o.GetId())
			}
			slices.Sort(ids)
			out[g.GetId()] = ids
		}
		return out
	}
	return maps.EqualFunc(structure(a), structure(b), slices.Equal)
}

// mergeOptionValues overlays the current values of the config options onto a base option-values
// map. The base map (e.g. primaryAgentOptions) owns its keys: the options are written first
// and the base overlaid on top, so a config option that coincidentally shares a base key
// (primaryAgent) can never clobber it. The caller must hold the owning acpBase.mu.
//
// Returns nil only when nothing is being reported -- no base AND no surfaced option
// options -- so the caller omits those keys entirely and PersistSettingsRefresh preserves
// whatever is stored. When config options ARE surfaced, every surfaced id is included,
// INCLUDING ones whose current value is empty: this is an optionmap.Map DELTA, so an empty
// entry DELETES that key on merge (see the optionmap package doc), clearing the stale stored
// value of an axis the agent cleared rather than preserving it (which omitting the key would do).
func (g *optionState) mergeOptionValues(base map[string]string) optionmap.Map {
	// Return nil (preserve stored) ONLY when there is genuinely nothing to report: no base,
	// no current values, AND no surfaced id owing a "" delete. Omitting the surfaced check
	// would drop the deletes when a complete payload clears EVERY option at once -- g.values
	// goes empty, but a once-surfaced id still needs its stale stored value deleted, and the
	// `len(g.values) == 0` guard would short-circuit past the delete-emission loop below.
	if len(base) == 0 && len(g.values) == 0 && g.surfaced.len() == 0 {
		return nil
	}
	merged := make(optionmap.Map, len(base)+len(g.values))
	// An option id this session once surfaced with a value but that is no longer current was
	// dropped by the server (a later complete payload reported a smaller set); emit it as ""
	// so the merge DELETES the stale stored value rather than preserving it. The `!ok` guard
	// emits "" ONLY for ids absent from g.values, so a still-current option is never written as
	// a delete -- the result is independent of these two loops' order (the base loop below
	// must still run last, since a base key legitimately owns a coincidental id). Iterate
	// `surfaced` (once-valued), NOT `known` (merely advertised): an advertised-but-never-valued
	// id has no stored value to delete, so emitting "" for it would be a redundant no-op -- and
	// would wipe a persisted preference still awaiting re-push.
	//
	// KNOWN LIMIT (deliberate): delete-emission is scoped by `surfaced`, an LRU set bounded at
	// maxOptionStateIDs. `valued` protects every id currently holding a value (in g.values /
	// g.pendingValues) from eviction, so a live option can't be dropped here -- and within the
	// SINGLE payload that drops an id, apply has not yet reassigned g.values, so `valued` still reads
	// the pre-drop map and shields the just-dropped id from eviction even against a >maxOptionStateIDs
	// burst in that same payload. The only uncovered case spans MULTIPLE payloads: one payload drops a
	// previously-valued id (apply then commits the smaller g.values, so it is no longer `valued`), and
	// LATER payloads churn >maxOptionStateIDs OTHER distinct ids, evicting the now-unvalued id from
	// `surfaced` before its "" delete is emitted -- stranding one stale stored value. Closing it fully
	// would require an UNBOUNDED record of once-valued ids, reintroducing the memory-exhaustion risk
	// the bound exists to prevent, so we accept the strand for that pathological sequence as the
	// bound's trade-off.
	for _, id := range g.surfaced.keys() {
		if _, ok := g.values[id]; !ok {
			merged[id] = ""
		}
	}
	for id, value := range g.values {
		merged[id] = value
	}
	for k, v := range base {
		merged[k] = v
	}
	return merged
}

// recordOptimistic records an option value the server accepted but did not echo in a
// refreshed configOptions list. It always updates the stored value and marks the id surfaced
// (so the value persists and a relaunch's applyStartupOptions re-pushes it). It also makes the
// value visible to OptionGroups()/CurrentOptions(): when a group for the id is already surfaced
// it updates that group's CurrentValue (replacing the group proto rather than mutating it, so a
// concurrently-returned OptionGroups slice keeps its snapshot); for a known-but-unsurfaced id
// (no matching group -- the server advertised it with an empty current and accepted this write
// without echoing it) it SYNTHESIZES a group from the advertised template. Surfacing it matters
// for applySettingsLive: its readback (CurrentOptions) drives the orphan reconcile, which would
// otherwise DROP this just-accepted axis as one the live session no longer carries. A no-op
// only for an empty value. Caller holds the owning acpBase.mu.
func (gs *optionState) recordOptimistic(configID, value string) {
	if value == "" {
		return
	}
	if gs.values == nil {
		gs.values = make(map[string]string)
	}
	gs.values[configID] = value
	gs.markSurfaced(configID)
	for i, g := range gs.groups {
		if g.GetId() == configID {
			if g.GetCurrentValue() != value {
				c := proto.Clone(g).(*leapmuxv1.AvailableOptionGroup)
				c.CurrentValue = value
				gs.groups[i] = c
			}
			return
		}
	}
	// No surfaced group yet for this id. Synthesize one so the readback reflects the accepted
	// value -- from the advertised template (the real option list) when we have it, else a
	// minimal group carrying just this id. The template is present for any live id: setConfigOption
	// gates on `known`, markKnown stores the template alongside it, and -- now that this write makes
	// the id valued -- the eviction guard (optionState.valued) keeps it (and its template) from
	// being dropped. The templateless fallback only covers the pathological case of a non-conforming
	// server churning enough distinct ids in one payload to evict an id before it is valued.
	// buildOptionGroup injects `value` as a selectable option when the template doesn't already
	// offer it (an off-spec value the server accepted but never listed, or this templateless id), so
	// the synthesized group's CurrentValue always has a matching option.
	tmpl, ok := gs.templates[configID]
	if !ok {
		tmpl = acpConfigOption{ID: configID}
	}
	gs.groups = append(gs.groups, buildOptionGroup(tmpl, value))
	// A newly synthesized group grows the set -- a structural change the live UpdateSettings
	// broadcast must see (the update-existing-value branch above returns first, as a value-only
	// change that the structure-only comparison correctly ignores).
	gs.structureGen++
}

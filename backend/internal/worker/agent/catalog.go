package agent

import (
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
)

// ModelInfo is the worker-internal, rich catalog entry for a model. The flat
// proto AvailableOption cannot carry per-model data (effort tiers, default
// effort), so each provider keeps its model catalog as []*ModelInfo and projects
// it into the "model" and "effort" option groups at the OptionGroups()
// boundary (see ModelOptionGroup / EffortGroupForModel in options.go).
//
// Field names mirror the former proto AvailableModel so the catalog/effort
// resolver machinery that predates the config option model is unchanged. The
// nil-safe Get* accessors below are the ones production code actually calls
// (model id and context window); the other fields are read directly.
type ModelInfo struct {
	Id               string
	DisplayName      string
	IsDefault        bool
	DefaultEffort    string
	SupportedEfforts []*EffortInfo
	Description      string
	ContextWindow    int64
	// Hidden keeps a catalog entry available for capability resolution (effort
	// tiers, context window) while excluding it from the model picker. Used for
	// models the live CLI no longer offers but that a session might still run.
	Hidden bool
}

// AccountDefaultModelEntry builds the leading catalog entry that means "let the
// CLI pick the account's own default model". A provider whose CLI resolves the
// model itself puts this first in its static catalog, and DefaultModel then
// returns the sentinel for a new agent of that provider.
//
// The entry carries no efforts and no context window on purpose. The effort menu
// appears once the concrete model is known, which keeps a fresh launch from
// forwarding an effort the resolved model may not offer. One helper for every
// provider makes that omission impossible to lose: a caller states the wording of
// the description and nothing else.
func AccountDefaultModelEntry(description string) *ModelInfo {
	return &ModelInfo{
		Id:          DefaultModelSentinel,
		DisplayName: "Default (recommended)",
		Description: description,
		IsDefault:   true,
	}
}

func (m *ModelInfo) GetId() string {
	if m == nil {
		return ""
	}
	return m.Id
}

func (m *ModelInfo) GetContextWindow() int64 {
	if m == nil {
		return 0
	}
	return m.ContextWindow
}

// equal reports whether two model entries carry identical catalog data,
// including their supported-effort lists. Used to detect a genuine catalog
// change vs an idempotent re-report.
func (m *ModelInfo) equal(o *ModelInfo) bool {
	if m == nil || o == nil {
		return m == o
	}
	return m.Id == o.Id && m.DisplayName == o.DisplayName && m.IsDefault == o.IsDefault &&
		m.DefaultEffort == o.DefaultEffort && m.Description == o.Description &&
		m.ContextWindow == o.ContextWindow && m.Hidden == o.Hidden &&
		slices.EqualFunc(m.SupportedEfforts, o.SupportedEfforts, (*EffortInfo).equal)
}

// ModelInfosEqual reports whether two model catalogs are element-wise equal.
func ModelInfosEqual(a, b []*ModelInfo) bool {
	return slices.EqualFunc(a, b, (*ModelInfo).equal)
}

// EffortInfo is the worker-internal catalog entry for a reasoning effort tier
// supported by a model. Mirrors the former proto AvailableEffort.
type EffortInfo struct {
	Id          string
	Name        string
	Description string
}

func (e *EffortInfo) GetId() string {
	if e == nil {
		return ""
	}
	return e.Id
}

func (e *EffortInfo) GetName() string {
	if e == nil {
		return ""
	}
	return e.Name
}

func (e *EffortInfo) GetDescription() string {
	if e == nil {
		return ""
	}
	return e.Description
}

// equal reports whether two effort entries carry identical catalog data. Extracted from
// ModelInfo.equal (which compares the effort lists via slices.EqualFunc) so a future
// EffortInfo field is compared in this one obvious place rather than silently missed by an
// inline per-field comparison.
func (e *EffortInfo) equal(o *EffortInfo) bool {
	if e == nil || o == nil {
		return e == o
	}
	return e.Id == o.Id && e.Name == o.Name && e.Description == o.Description
}

// EffortAuto is the LeapMux-side sentinel meaning "let the CLI pick its own
// default reasoning effort". When an agent's Effort is this value, the
// provider layer omits the CLI flag / wire field entirely so older CLIs
// that don't recognize newer effort names (e.g. "xhigh") still work.
const EffortAuto = contracts.EffortAuto

// DefaultModelSentinel is the model id that means "let the agent select the
// account's default model" -- the model-side analogue of EffortAuto above. Claude
// Code reports the sentinel in its own model list. LeapMux stores the sentinel for
// a new Codex session, and Codex replaces it with the concrete model that the
// thread/start lifecycle response reports.
const DefaultModelSentinel = contracts.DefaultModelSentinel

// UsesAccountDefaultModel reports whether the model lets the agent select the
// account default. An empty value and DefaultModelSentinel have this meaning.
//
// Every site that puts a model on the wire must call this. One two-clause check
// keeps the omit-the-model decision the same at every site, so a forgotten
// sentinel clause becomes a missing call rather than a silent wrong branch.
func UsesAccountDefaultModel(model string) bool {
	return model == "" || model == DefaultModelSentinel
}

// EffortXHigh is the "xhigh" effort level. It is also the launch/wire base for
// the ultracode combo (which layers the `ultracode` boolean on top of xhigh),
// so it is a load-bearing value shared by the encode path (ultracodeFlagSettings,
// buildModelEffortArgs) and the decode path (effortFromApplied). Naming it
// once keeps those sites from drifting to inconsistent literals.
const EffortXHigh = "xhigh"

// EffortHigh is the "high" effort level. It is the universal-safe fallback every
// model supports, so resolveEffort downgrades any unsupported
// effort to it. Like EffortXHigh it is load-bearing (the fallback target and the
// Sonnet/Haiku catalog default), so naming it once keeps those sites from
// drifting to inconsistent literals.
const EffortHigh = "high"

// FindAvailableModel returns the AvailableModel with the given ID, or nil if
// none matches. Callers typically use this to resolve per-model metadata
// (e.g. DefaultEffort) from a catalog returned by the CLI.
func FindAvailableModel(models []*ModelInfo, id string) *ModelInfo {
	for _, m := range models {
		// Guard nil entries: callers (ModelOptionGroup, the effort resolver) already
		// treat the slice as possibly nil-bearing, so this must too.
		if m != nil && m.Id == id {
			return m
		}
	}
	return nil
}

// IsEffortAutoTransition reports whether a settings update is switching
// effort from a concrete value to EffortAuto. Both providers' UpdateSettings
// must handle this by requesting a restart, since apply-in-place paths do
// not accept "auto" as a live effortLevel / reasoning_effort value.
func IsEffortAutoTransition(newEffort, curEffort string) bool {
	return newEffort == EffortAuto && curEffort != EffortAuto
}

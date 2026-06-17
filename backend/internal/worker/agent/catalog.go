package agent

import "slices"

// ModelInfo is the worker-internal, rich catalog entry for a model. The flat
// proto AvailableOption cannot carry per-model data (effort tiers, default
// effort), so each provider keeps its model catalog as []*ModelInfo and projects
// it into the "model" and "effort" option groups at the OptionGroups()
// boundary (see modelOptionGroup / effortGroupForModel in options.go).
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

// clone returns a deep copy: the SupportedEfforts slice and its *EffortInfo elements
// are copied too, not aliased. Today callers only re-badge the scalar IsDefault, but a
// shallow copy would let a future caller that mutates an effort through the clone
// corrupt the shared static catalog every model with that effort list points at.
func (m *ModelInfo) clone() *ModelInfo {
	if m == nil {
		return nil
	}
	c := *m
	if m.SupportedEfforts != nil {
		c.SupportedEfforts = make([]*EffortInfo, len(m.SupportedEfforts))
		for i, e := range m.SupportedEfforts {
			if e != nil {
				ec := *e
				c.SupportedEfforts[i] = &ec
			}
		}
	}
	return &c
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

// modelInfosEqual reports whether two model catalogs are element-wise equal.
func modelInfosEqual(a, b []*ModelInfo) bool {
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

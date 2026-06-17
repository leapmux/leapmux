// Package optionids defines the well-known agent option-group ids and small,
// dependency-light lookups over an option-group catalog. It is a leaf package (importing
// only the generated proto types), so the thin remote CLI can reference option ids and
// resolve a current value WITHOUT importing the worker-agent runtime. This is their canonical
// home: the worker/agent package re-exposes the ids under OptionID* constants for its many
// internal callers and calls GroupByID/CurrentValue directly.
package optionids

import leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

// Well-known option-group ids. Every agent configuration axis is a generic
// AvailableOptionGroup; these ids let the callers that genuinely need a specific axis
// (e.g. "the current model") fetch it by id. Provider-specific axes (fast mode, sandbox
// policy, ...) use their own ids and need no constant here.
const (
	Model          = "model"
	Effort         = "effort"
	PermissionMode = "permissionMode"
	PrimaryAgent   = "primaryAgent"
)

// GroupByID returns the group with the given id, or nil.
func GroupByID(groups []*leapmuxv1.AvailableOptionGroup, id string) *leapmuxv1.AvailableOptionGroup {
	for _, g := range groups {
		if g.GetId() == id {
			return g
		}
	}
	return nil
}

// CurrentValue returns the current value of the group with the given id, or "".
func CurrentValue(groups []*leapmuxv1.AvailableOptionGroup, id string) string {
	return GroupByID(groups, id).GetCurrentValue()
}

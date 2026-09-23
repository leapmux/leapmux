package agenttest

import leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

// OptionIDs returns the id of each item, in order, for an assertion about ORDER.
//
// It is generic over the constraint that providerkit.SortEffortsDescending takes, because two
// element types reach the tests: `*EffortInfo` for a catalog entry and
// `*leapmuxv1.AvailableOption` for an option of a group.
func OptionIDs[T interface{ GetId() string }](items []T) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.GetId())
	}
	return ids
}

// OptionByID returns the option with the given id in the group, or nil.
func OptionByID(g *leapmuxv1.AvailableOptionGroup, id string) *leapmuxv1.AvailableOption {
	for _, o := range g.GetOptions() {
		if o.GetId() == id {
			return o
		}
	}
	return nil
}

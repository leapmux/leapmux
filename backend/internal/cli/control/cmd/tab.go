package cmd

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// filterTabsByType drops tabs whose type doesn't match wanted. A zero
// wanted (TAB_TYPE_UNSPECIFIED) returns the input slice unchanged, so
// callers can pass the parsed flag through without a nil check.
// The non-unspecified path allocates a fresh slice rather than reusing
// the input's backing array — `in` aliases the response's `tabs` slice
// and overwriting it in place while iterating would corrupt any future
// reader of the response.
func filterTabsByType(in []*leapmuxv1.WorkspaceTab, wanted leapmuxv1.TabType) []*leapmuxv1.WorkspaceTab {
	if wanted == leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED {
		return in
	}
	out := make([]*leapmuxv1.WorkspaceTab, 0, len(in))
	for _, t := range in {
		if t.GetTabType() == wanted {
			out = append(out, t)
		}
	}
	return out
}

package service

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// tabHydrationVerdicts builds the per-tab answer a hydration batch owes its
// caller.
//
// The client hydrates CRDT-projected tabs by asking a worker for the records
// behind a set of tab ids. Before this existed the reply carried only the
// records it could produce, so an OMITTED tab was ambiguous and the client had
// to guess -- and the only safe guess is "transient, ask again", which pins a
// retry timer per unanswerable tab for the life of the page.
//
// `requested` is the caller's tab_ids, and is never empty in practice: both
// callers answer an empty request with an empty response and return before
// reaching this (agent.go, terminal.go). The guard below is a defensive
// backstop for a third caller, not a supported request shape. `found` names
// the ids present in the response body; anything else the caller named has no
// record on this worker at all, and nothing the client does changes that.
func tabHydrationVerdicts(requested []string, found map[string]bool) []*leapmuxv1.TabHydrationVerdict {
	if len(requested) == 0 {
		return nil
	}
	out := make([]*leapmuxv1.TabHydrationVerdict, 0, len(requested))
	for _, id := range requested {
		status := leapmuxv1.TabHydrationStatus_TAB_HYDRATION_STATUS_ABSENT
		if found[id] {
			status = leapmuxv1.TabHydrationStatus_TAB_HYDRATION_STATUS_FOUND
		}
		out = append(out, &leapmuxv1.TabHydrationVerdict{TabId: id, Status: status})
	}
	return out
}

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
// backstop for a third caller, not a supported request shape. `found`
// names the ids present in the response body. `hidden` names ids this worker DOES
// hold a record for but whose workspace this channel may not see yet -- a
// channel's accessible set is seeded at open time and grows only through
// AddAccessibleWorkspaceID, which runs when someone calls PrepareWorkspaceAccess.
// Re-asking this worker therefore cannot clear it; asking the hub for access can,
// which is why the two are distinguished. Anything the caller named that is in
// neither map has no record here at all, and nothing the client does changes that.
//
// `found` wins over `hidden`: an id served from the in-memory manager is FOUND
// even if a stale DB row for it would have been filtered.
func tabHydrationVerdicts(requested []string, found, hidden map[string]bool) []*leapmuxv1.TabHydrationVerdict {
	if len(requested) == 0 {
		return nil
	}
	out := make([]*leapmuxv1.TabHydrationVerdict, 0, len(requested))
	for _, id := range requested {
		status := leapmuxv1.TabHydrationStatus_TAB_HYDRATION_STATUS_ABSENT
		switch {
		case found[id]:
			status = leapmuxv1.TabHydrationStatus_TAB_HYDRATION_STATUS_FOUND
		case hidden[id]:
			status = leapmuxv1.TabHydrationStatus_TAB_HYDRATION_STATUS_NOT_ACCESSIBLE
		}
		out = append(out, &leapmuxv1.TabHydrationVerdict{TabId: id, Status: status})
	}
	return out
}

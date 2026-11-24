package crdt

import (
	"cmp"
	"slices"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// LayoutEventWake changes when this manager commits retained layout facts.
// Capture it before reading the journal to avoid missing a concurrent commit.
func (m *Manager) LayoutEventWake() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.layoutEventWake == nil {
		m.layoutEventWake = make(chan struct{})
	}
	return m.layoutEventWake
}

func (m *Manager) signalLayoutEvents() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.layoutEventWake != nil {
		close(m.layoutEventWake)
		m.layoutEventWake = nil
	}
}

func projectionDiffAndLayoutFacts(before, after *leapmuxv1.UserCrdtState, ops []*leapmuxv1.CrdtOp) (IndexDiff, []*leapmuxv1.LayoutEventFact) {
	var facts []*leapmuxv1.LayoutEventFact
	diff := diffProjectionForBatch(before, after, ops, func(previous, next TabIndexRow) {
		if previous.WorkspaceID == next.WorkspaceID && previous.WorkerID == next.WorkerID {
			return
		}
		identity := next
		if identity.TabID == "" {
			identity = previous
		}
		kind := leapmuxv1.LayoutTabChangeKind_LAYOUT_TAB_CHANGE_KIND_MOVED
		switch {
		case previous.TabID == "" && !layoutTabIsLive(before.GetTabs()[identity.TabID]):
			kind = leapmuxv1.LayoutTabChangeKind_LAYOUT_TAB_CHANGE_KIND_OPENED
		case next.TabID == "" && !layoutTabIsLive(after.GetTabs()[identity.TabID]):
			kind = leapmuxv1.LayoutTabChangeKind_LAYOUT_TAB_CHANGE_KIND_CLOSED
		}
		facts = append(facts, &leapmuxv1.LayoutEventFact{Event: &leapmuxv1.LayoutEventFact_Tab{
			Tab: &leapmuxv1.LayoutTabChange{
				Tab:  &leapmuxv1.TabIdent{TabType: identity.TabType, TabId: identity.TabID},
				Kind: kind, Before: layoutPlacement(previous), After: layoutPlacement(next),
			},
		}})
	})
	slices.SortFunc(facts, func(a, b *leapmuxv1.LayoutEventFact) int {
		return cmp.Compare(a.GetTab().GetTab().GetTabId(), b.GetTab().GetTab().GetTabId())
	})
	return diff, facts
}

func layoutTabIsLive(tab *leapmuxv1.TabRecord) bool {
	return tab != nil && HLCIsZero(tab.GetTombstoneAt())
}

func layoutPlacement(row TabIndexRow) *leapmuxv1.LayoutTabPlacement {
	if row.TabID == "" {
		return nil
	}
	return &leapmuxv1.LayoutTabPlacement{WorkspaceId: row.WorkspaceID, TileId: row.TileID, WorkerId: row.WorkerID}
}

func workspaceLayoutFact(workspaceID, title string, operation LifecycleOpType) *leapmuxv1.LayoutEventFact {
	return &leapmuxv1.LayoutEventFact{Event: &leapmuxv1.LayoutEventFact_Workspace{
		Workspace: &leapmuxv1.LayoutWorkspaceChange{
			WorkspaceId: workspaceID, Title: title, Operation: leapmuxv1.WorkspaceLifecycleOp(operation),
		},
	}}
}

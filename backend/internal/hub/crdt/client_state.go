package crdt

import (
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TabTransitionKind classifies how a tab changed between two scoped OwnedTab
// projections.
type TabTransitionKind uint8

const (
	// TabTransitionOpened reports a tab that entered the projected scope.
	TabTransitionOpened TabTransitionKind = iota + 1
	// TabTransitionClosed reports a tab that left the projected scope while
	// closed, orphaned, or missing from a post-gap snapshot.
	TabTransitionClosed
	// TabTransitionMoved reports a tab that changed workspace inside the
	// watched scope.
	TabTransitionMoved
	// TabTransitionLeftScope reports a tab that left the watched scope via
	// EntityRemoved.
	TabTransitionLeftScope
)

// String returns a readable label for k.
func (k TabTransitionKind) String() string {
	switch k {
	case TabTransitionOpened:
		return "opened"
	case TabTransitionClosed:
		return "closed"
	case TabTransitionMoved:
		return "moved"
	case TabTransitionLeftScope:
		return "left_scope"
	default:
		return fmt.Sprintf("TabTransitionKind(%d)", k)
	}
}

// TabTransition describes one tab change between two scoped OwnedTab
// projections.
//
// For TabTransitionOpened, Current holds the new tab and Previous is zero.
// For TabTransitionClosed, Previous holds the removed tab and Current is zero.
// For TabTransitionMoved, Previous and Current both hold the tab.
// For TabTransitionLeftScope, Previous holds the tab and Current is zero.
type TabTransition struct {
	Kind       TabTransitionKind
	TabID      string
	Previous   OwnedTab
	Current    OwnedTab
	Reconciled bool
}

// StateFromMaterialized builds a client-side UserCrdtState from a materialized
// snapshot so subsequent op batches apply to it as the hub applies them. It
// deep-clones MaxHlc and every record into fresh maps so subsequent crdt.Apply
// calls cannot mutate caller-owned proto messages. It skips nil record entries.
func StateFromMaterialized(m *leapmuxv1.UserMaterialized) *leapmuxv1.UserCrdtState {
	if m == nil {
		return NewState("")
	}
	maxHlc := HLCClone(m.GetMaxHlc())
	if maxHlc == nil {
		maxHlc = &leapmuxv1.HLC{}
	}
	state := &leapmuxv1.UserCrdtState{
		UserId:          m.GetUserId(),
		Nodes:           make(map[string]*leapmuxv1.NodeRecord, len(m.GetNodes())),
		Tabs:            make(map[string]*leapmuxv1.TabRecord, len(m.GetTabs())),
		FloatingWindows: make(map[string]*leapmuxv1.FloatingWindowRecord, len(m.GetFloatingWindows())),
		Workspaces:      make(map[string]*leapmuxv1.WorkspaceContentsRecord, len(m.GetWorkspaces())),
		MaxHlc:          maxHlc,
		CurrentEpoch:    m.GetCurrentEpoch(),
	}
	for id, node := range m.GetNodes() {
		if node == nil {
			continue
		}
		state.Nodes[id] = cloneNode(node)
	}
	for id, tab := range m.GetTabs() {
		if tab == nil {
			continue
		}
		state.Tabs[id] = cloneTab(tab)
	}
	for id, fw := range m.GetFloatingWindows() {
		if fw == nil {
			continue
		}
		state.FloatingWindows[id] = cloneFloatingWindow(fw)
	}
	for id, ws := range m.GetWorkspaces() {
		if ws == nil {
			continue
		}
		state.Workspaces[id] = proto.Clone(ws).(*leapmuxv1.WorkspaceContentsRecord)
	}
	return state
}

// ApplyEntityMaterialized replaces or inserts a deep clone of the record for an
// entity that entered the subscriber's scope. Deep-cloning prevents subsequent
// crdt.Apply calls from mutating caller-owned proto messages.
func ApplyEntityMaterialized(state *leapmuxv1.UserCrdtState, m *leapmuxv1.EntityMaterialized) {
	if state == nil || m == nil {
		return
	}
	switch e := m.GetEntity().(type) {
	case *leapmuxv1.EntityMaterialized_Tab:
		if id := e.Tab.GetTabId(); id != "" {
			if state.Tabs == nil {
				state.Tabs = map[string]*leapmuxv1.TabRecord{}
			}
			state.Tabs[id] = cloneTab(e.Tab)
		}
	case *leapmuxv1.EntityMaterialized_Node:
		if id := e.Node.GetNodeId(); id != "" {
			if state.Nodes == nil {
				state.Nodes = map[string]*leapmuxv1.NodeRecord{}
			}
			state.Nodes[id] = cloneNode(e.Node)
		}
	case *leapmuxv1.EntityMaterialized_FloatingWindow:
		if id := e.FloatingWindow.GetWindowId(); id != "" {
			if state.FloatingWindows == nil {
				state.FloatingWindows = map[string]*leapmuxv1.FloatingWindowRecord{}
			}
			state.FloatingWindows[id] = cloneFloatingWindow(e.FloatingWindow)
		}
	}
}

// ApplyEntityRemoved drops the record of an entity that left the subscriber's
// scope.
func ApplyEntityRemoved(state *leapmuxv1.UserCrdtState, r *leapmuxv1.EntityRemoved) {
	if state == nil || r == nil {
		return
	}
	switch e := r.GetEntity().(type) {
	case *leapmuxv1.EntityRemoved_Tab:
		delete(state.Tabs, e.Tab.GetTabId())
	case *leapmuxv1.EntityRemoved_NodeId:
		delete(state.Nodes, e.NodeId)
	case *leapmuxv1.EntityRemoved_WindowId:
		delete(state.FloatingWindows, e.WindowId)
	}
}

// OpAffectsTabProjection reports whether op can change the projected set of
// owned tabs or their workspace or Worker ownership. Layout-only edits such as
// split ratios or floating-window geometry cannot change OwnedTabs.
func OpAffectsTabProjection(op *leapmuxv1.CrdtOp) bool {
	if op == nil || op.GetBody() == nil {
		return false
	}
	switch body := op.GetBody().(type) {
	case *leapmuxv1.CrdtOp_SetTabRegister:
		switch body.SetTabRegister.GetField().(type) {
		case *leapmuxv1.SetTabRegisterOp_TileId, *leapmuxv1.SetTabRegisterOp_WorkerId:
			return true
		default:
			return false
		}
	case *leapmuxv1.CrdtOp_TombstoneTab, *leapmuxv1.CrdtOp_ReviveTab,
		*leapmuxv1.CrdtOp_CreateWorkspace, *leapmuxv1.CrdtOp_TombstoneWorkspace,
		*leapmuxv1.CrdtOp_TombstoneNode, *leapmuxv1.CrdtOp_TombstoneFloatingWindow:
		return true
	case *leapmuxv1.CrdtOp_SetNodeRegister:
		_, isParent := body.SetNodeRegister.GetField().(*leapmuxv1.SetNodeRegisterOp_ParentId)
		return isParent
	case *leapmuxv1.CrdtOp_SetFloatingWindowRegister:
		switch body.SetFloatingWindowRegister.GetField().(type) {
		case *leapmuxv1.SetFloatingWindowRegisterOp_RootNodeId, *leapmuxv1.SetFloatingWindowRegisterOp_WorkspaceId:
			return true
		default:
			return false
		}
	default:
		return true
	}
}

// DiffOwnedTabs computes how next differs from prev in sorted tab-ID order.
//
// A tab that left the projection closed when state still holds its record: a
// close tombstones the record, and a tab whose tile went away keeps it. A tab
// whose record left state reports TabTransitionLeftScope, which only
// EntityRemoved causes. A snapshot after a gap holds no record of a closed tab,
// so when reconciled is true any tab that left reports TabTransitionClosed: the
// snapshot does not distinguish a close from a move to an unwatched workspace.
func DiffOwnedTabs(state *leapmuxv1.UserCrdtState, prev, next map[string]OwnedTab, reconciled bool) []TabTransition {
	if len(prev) == 0 && len(next) == 0 {
		return nil
	}
	var out []TabTransition
	tabs := state.GetTabs()
	for id, before := range prev {
		after, isIn := next[id]
		if !isIn {
			_, recorded := tabs[id]
			if reconciled || recorded {
				out = append(out, TabTransition{
					Kind:       TabTransitionClosed,
					TabID:      id,
					Previous:   before,
					Reconciled: reconciled,
				})
				continue
			}
			out = append(out, TabTransition{
				Kind:     TabTransitionLeftScope,
				TabID:    id,
				Previous: before,
			})
			continue
		}
		if before.WorkspaceID != after.WorkspaceID {
			out = append(out, TabTransition{
				Kind:       TabTransitionMoved,
				TabID:      id,
				Previous:   before,
				Current:    after,
				Reconciled: reconciled,
			})
		}
	}
	for id, after := range next {
		if _, wasIn := prev[id]; !wasIn {
			out = append(out, TabTransition{
				Kind:       TabTransitionOpened,
				TabID:      id,
				Current:    after,
				Reconciled: reconciled,
			})
		}
	}
	if len(out) > 1 {
		slices.SortFunc(out, func(a, b TabTransition) int {
			return strings.Compare(a.TabID, b.TabID)
		})
	}
	return out
}

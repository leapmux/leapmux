package crdt

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// liveRecordSnapshot returns a deep-cloned EntityMaterialized payload for
// `ref` from `state`, or nil when the record is missing, tombstoned, or the
// kind has no EntityMaterialized variant. This is the SINGLE source of the
// "what record ships in a materialize frame" rule: both the live broadcast
// (buildEntityMaterializedEvent) and the commit-time encoder
// (AffectedEntitiesToProto) call it, so resume/live cannot drift on
// tombstone-omit or kind coverage.
func liveRecordSnapshot(state *leapmuxv1.UserCrdtState, ref EntityRef) *leapmuxv1.EntityMaterialized {
	switch ref.Kind {
	case EntityKindNode:
		n := state.GetNodes()[ref.NodeID]
		if n == nil || !HLCIsZero(n.GetTombstoneAt()) {
			return nil
		}
		return &leapmuxv1.EntityMaterialized{
			Entity: &leapmuxv1.EntityMaterialized_Node{Node: cloneNode(n)},
		}
	case EntityKindTab:
		t := state.GetTabs()[ref.TabID]
		if t == nil || !HLCIsZero(t.GetTombstoneAt()) {
			return nil
		}
		return &leapmuxv1.EntityMaterialized{
			Entity: &leapmuxv1.EntityMaterialized_Tab{Tab: cloneTab(t)},
		}
	case EntityKindFloatingWindow:
		fw := state.GetFloatingWindows()[ref.WindowID]
		if fw == nil || !HLCIsZero(fw.GetTombstoneAt()) {
			return nil
		}
		return &leapmuxv1.EntityMaterialized{
			Entity: &leapmuxv1.EntityMaterialized_FloatingWindow{FloatingWindow: cloneFloatingWindow(fw)},
		}
	default:
		// EntityKindUnknown and EntityKindWorkspaceRoot have no
		// EntityMaterialized variant.
		return nil
	}
}

// AffectedEntitiesToProto encodes a batch's AffectedEntities
// (map[EntityRef]EntityWorkspaceTransition — the ValidateBatch result the live
// broadcast consumes) as the storage-shaped BatchTransitions proto the journal
// persists alongside the OpBatch. The encoding is one-for-one: exactly one
// identity field (node_id / tab / window_id / workspace_id) is set per entry,
// and its position implies the EntityKind TransitionsFromProto recovers.
//
// For Node / Tab / FloatingWindow entries whose workspace CHANGED (Pre != Post)
// it also captures the entity's POST-batch record snapshot from `working`, so
// the resume path's materialized frame reads the batch-era record instead of
// CURRENT live state. Stable-visibility edits (Pre == Post) never produce a
// materialized frame for any subscriber, so their records are omitted --
// persisting them would bloat every in-place edit's journal row for no resume
// consumer. A tombstoned record is omitted via liveRecordSnapshot: a tombstone
// is projection-ignored client-side, and the live broadcast path uses the same
// helper so both catch-up paths skip the shell together.
//
// Used by the commit path (Manager.commit) so the per-batch transitions
// ValidateBatch already computes are written once and read back by resume,
// making the two catch-up paths read the SAME data through the SAME predicates.
func AffectedEntitiesToProto(m map[EntityRef]EntityWorkspaceTransition, working *leapmuxv1.UserCrdtState) *leapmuxv1.BatchTransitions {
	out := &leapmuxv1.BatchTransitions{Entries: make([]*leapmuxv1.BatchTransition, 0, len(m))}
	for ref, trans := range m {
		entry := &leapmuxv1.BatchTransition{
			PreWorkspace:  trans.Pre,
			PostWorkspace: trans.Post,
		}
		switch ref.Kind {
		case EntityKindNode:
			entry.Identity = &leapmuxv1.BatchTransition_NodeId{NodeId: ref.NodeID}
		case EntityKindTab:
			entry.Identity = &leapmuxv1.BatchTransition_Tab{Tab: &leapmuxv1.TabIdent{TabType: ref.TabType, TabId: ref.TabID}}
		case EntityKindFloatingWindow:
			entry.Identity = &leapmuxv1.BatchTransition_WindowId{WindowId: ref.WindowID}
		case EntityKindWorkspaceRoot:
			entry.Identity = &leapmuxv1.BatchTransition_WorkspaceId{WorkspaceId: ref.WorkspaceID}
			// No EntityMaterialized variant for WorkspaceRoot; no record snapshot.
		default:
			// EntityKindUnknown carries no identity and resolves to no
			// workspace, so it has nothing to persist. Skip it rather
			// than emit an entry whose kind TransitionsFromProto could
			// not recover.
			continue
		}
		// Record snapshots are only useful for becoming-visible materialize
		// frames, which require a workspace change (Pre != Post). Capturing
		// them for stable in-place edits would store full protos the resume
		// path never reads.
		if trans.Pre != trans.Post {
			if snap := liveRecordSnapshot(working, ref); snap != nil {
				switch e := snap.GetEntity().(type) {
				case *leapmuxv1.EntityMaterialized_Node:
					entry.Record = &leapmuxv1.BatchTransition_NodeRecord{NodeRecord: e.Node}
				case *leapmuxv1.EntityMaterialized_Tab:
					entry.Record = &leapmuxv1.BatchTransition_TabRecord{TabRecord: e.Tab}
				case *leapmuxv1.EntityMaterialized_FloatingWindow:
					entry.Record = &leapmuxv1.BatchTransition_FloatingWindowRecord{FloatingWindowRecord: e.FloatingWindow}
				}
			}
		}
		out.Entries = append(out.Entries, entry)
	}
	return out
}

// transitionEntryRef decodes one entry's identity oneof into the EntityRef it
// names, reporting ok=false for an entry that names no entity (unset oneof, or
// an empty identity string). The SINGLE definition of that mapping: both
// TransitionsFromProto and MissingTransitionOp read it, so the "which ops does a
// transitions_payload cover" question cannot be answered two different ways.
func transitionEntryRef(e *leapmuxv1.BatchTransition) (EntityRef, bool) {
	var ref EntityRef
	switch id := e.GetIdentity().(type) {
	case *leapmuxv1.BatchTransition_NodeId:
		ref = EntityRef{Kind: EntityKindNode, NodeID: id.NodeId}
	case *leapmuxv1.BatchTransition_Tab:
		ref = EntityRef{Kind: EntityKindTab, TabType: id.Tab.GetTabType(), TabID: id.Tab.GetTabId()}
	case *leapmuxv1.BatchTransition_WindowId:
		ref = EntityRef{Kind: EntityKindFloatingWindow, WindowID: id.WindowId}
	case *leapmuxv1.BatchTransition_WorkspaceId:
		ref = EntityRef{Kind: EntityKindWorkspaceRoot, WorkspaceID: id.WorkspaceId}
	default:
		return EntityRef{}, false
	}
	// One rule for "names a real entity", shared with the submit-path validator
	// via EntityRef.HasIdentity -- see its doc for why the two must agree.
	if !ref.HasIdentity() {
		return EntityRef{}, false
	}
	return ref, true
}

// MissingTransitionOp reports the first op in batch whose target names a real
// entity that the persisted transitions do NOT cover, so a caller can treat the
// row as corrupt.
//
// It exists because unmarshal success is not a completeness witness. proto3
// fields are length-delimited and repeated, so a transitions_payload truncated
// at an entry boundary -- or truncated to zero bytes, the degenerate case --
// decodes cleanly into a BatchTransitions holding only the entries that
// survived. Every op whose entry was lost then resolves to the zero transition
// {Pre:"", Post:""}; IsAllowed("") is false on both sides, so filterVisibleOps
// drops it, and if that accounts for the whole batch the subscriber receives
// only BatchEnd -- which still advances its resume cursor past ops it never
// got. That is indistinguishable, on the wire, from the batch never existing,
// and no later resume re-requests it.
//
// Ops whose target is EntityKindUnknown carry no identity and are deliberately
// absent from a well-formed payload (AffectedEntitiesToProto drops them for the
// same reason), so they are not evidence of loss.
func MissingTransitionOp(batch *leapmuxv1.OpBatch, p *leapmuxv1.BatchTransitions) (EntityRef, bool) {
	covered := make(map[EntityRef]struct{}, len(p.GetEntries()))
	for _, e := range p.GetEntries() {
		if ref, ok := transitionEntryRef(e); ok {
			covered[ref] = struct{}{}
		}
	}
	for _, op := range batch.GetOps() {
		ref := OpTarget(op)
		if ref.Kind == EntityKindUnknown {
			continue
		}
		if _, ok := covered[ref]; !ok {
			return ref, true
		}
	}
	return EntityRef{}, false
}

// TransitionsFromProto is the resume-side inverse of AffectedEntitiesToProto:
// it rebuilds the per-batch transition map the resume path feeds into the SAME
// orderedAffectedRefs / buildEntityRemovedEvent helpers the live broadcast
// uses, plus each entry's captured post-batch record snapshot (in
// `records`, keyed by EntityRef) so the materialized frame reads batch-era
// state instead of cloning CURRENT live state. EntityKind is recovered from
// WHICH identity oneof arm is set. An entry whose identity is empty / unset is
// dropped (it cannot name an entity, and AffectedEntitiesToProto never emits
// one).
func TransitionsFromProto(p *leapmuxv1.BatchTransitions) (transitions map[EntityRef]EntityWorkspaceTransition, records map[EntityRef]*leapmuxv1.EntityMaterialized) {
	if p == nil {
		return map[EntityRef]EntityWorkspaceTransition{}, map[EntityRef]*leapmuxv1.EntityMaterialized{}
	}
	transitions = make(map[EntityRef]EntityWorkspaceTransition, len(p.GetEntries()))
	records = make(map[EntityRef]*leapmuxv1.EntityMaterialized, len(p.GetEntries()))
	for _, e := range p.GetEntries() {
		ref, ok := transitionEntryRef(e)
		if !ok {
			continue
		}
		transitions[ref] = EntityWorkspaceTransition{Pre: e.GetPreWorkspace(), Post: e.GetPostWorkspace()}
		// Recover the captured record snapshot into the EntityMaterialized
		// payload the resume frame ships. Absent ⇒ no materialized frame
		// (tombstoned, stable-visibility, or a kind with no materialized variant).
		switch r := e.GetRecord().(type) {
		case *leapmuxv1.BatchTransition_NodeRecord:
			records[ref] = &leapmuxv1.EntityMaterialized{Entity: &leapmuxv1.EntityMaterialized_Node{Node: r.NodeRecord}}
		case *leapmuxv1.BatchTransition_TabRecord:
			records[ref] = &leapmuxv1.EntityMaterialized{Entity: &leapmuxv1.EntityMaterialized_Tab{Tab: r.TabRecord}}
		case *leapmuxv1.BatchTransition_FloatingWindowRecord:
			records[ref] = &leapmuxv1.EntityMaterialized{Entity: &leapmuxv1.EntityMaterialized_FloatingWindow{FloatingWindow: r.FloatingWindowRecord}}
		}
	}
	return transitions, records
}

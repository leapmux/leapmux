package crdt

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TestAffectedEntitiesToProto_TransitionsFromProto_RoundTrip locks in the
// storage encoding of AffectedEntities (the per-batch {pre, post} workspace
// transitions ValidateBatch produces): every EntityKind round-trips through
// AffectedEntitiesToProto → TransitionsFromProto with its identity fields and
// workspaces intact, and the EntityKind is recovered from WHICH identity field
// the encoder set (node_id / tab / window_id / workspace_id). This is the
// load-bearing contract the resume path relies on to replay the same
// transitions broadcast applies — a drift here would misclassify every entity
// of the affected kind on resume.
func TestAffectedEntitiesToProto_TransitionsFromProto_RoundTrip(t *testing.T) {
	original := map[EntityRef]EntityWorkspaceTransition{
		{Kind: EntityKindNode, NodeID: "n1"}:                                          {Pre: "wA", Post: "wB"},
		{Kind: EntityKindTab, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "t1"}: {Pre: "", Post: "wA"},
		{Kind: EntityKindFloatingWindow, WindowID: "fw1"}:                             {Pre: "wA", Post: ""},
		{Kind: EntityKindWorkspaceRoot, WorkspaceID: "ws1"}:                           {Pre: "ws1", Post: "ws1"},
	}

	encoded := AffectedEntitiesToProto(original, &leapmuxv1.UserCrdtState{})
	decoded, _ := TransitionsFromProto(encoded)

	require.Len(t, decoded, len(original), "every entity must survive the round-trip")
	for ref, trans := range original {
		got, ok := decoded[ref]
		require.True(t, ok, "entity %v must be present after round-trip", ref)
		assert.Equal(t, trans, got, "transition for %v must match after round-trip", ref)
	}
}

// TestAffectedEntitiesToProto_OmitsUnknownKind pins that EntityKindUnknown
// (which carries no identity field) is dropped by the encoder rather than
// emitted as an entry TransitionsFromProto could not recover a kind for.
func TestAffectedEntitiesToProto_OmitsUnknownKind(t *testing.T) {
	encoded := AffectedEntitiesToProto(map[EntityRef]EntityWorkspaceTransition{
		{Kind: EntityKindUnknown}:              {Pre: "wA", Post: "wB"},
		{Kind: EntityKindNode, NodeID: "kept"}: {Pre: "wA", Post: "wB"},
	}, &leapmuxv1.UserCrdtState{})
	assert.Len(t, encoded.GetEntries(), 1, "EntityKindUnknown must be dropped; only the Node entry survives")
	assert.Equal(t, "kept", encoded.GetEntries()[0].GetNodeId())
}

// TestTransitionsFromProto_NilInputReturnsEmpty pins the nil-safety of the
// decoder: a nil BatchTransitions (a journal row whose transitions were never
// set, or an empty-map decode) yields empty maps, not a panic. The resume
// path feeds the result into orderedAffectedRefs and workspace lookups, both
// of which must handle the empty case gracefully.
func TestTransitionsFromProto_NilInputReturnsEmpty(t *testing.T) {
	trans, rec := TransitionsFromProto(nil)
	assert.Empty(t, trans, "nil BatchTransitions must decode to an empty transition map")
	assert.Empty(t, rec, "nil BatchTransitions must decode to an empty records map")
	trans, rec = TransitionsFromProto(&leapmuxv1.BatchTransitions{})
	assert.Empty(t, trans, "empty BatchTransitions must decode to an empty transition map")
	assert.Empty(t, rec, "empty BatchTransitions must decode to an empty records map")
}

// TestTransitionsFromProto_DropsEntryWithNoIdentityField pins the decoder's
// defensive drop: an entry whose identity fields are all empty cannot name an
// entity, so it is skipped rather than decoded into an EntityKindUnknown ref
// that no downstream classification would match.
func TestTransitionsFromProto_DropsEntryWithNoIdentityField(t *testing.T) {
	decoded, _ := TransitionsFromProto(&leapmuxv1.BatchTransitions{
		Entries: []*leapmuxv1.BatchTransition{
			{PreWorkspace: "wA", PostWorkspace: "wB"}, // no identity oneof arm
			{Identity: &leapmuxv1.BatchTransition_NodeId{NodeId: "n1"}, PreWorkspace: "wA", PostWorkspace: "wB"},
		},
	})
	assert.Len(t, decoded, 1, "the identity-less entry must be dropped")
	_, hasN1 := decoded[EntityRef{Kind: EntityKindNode, NodeID: "n1"}]
	assert.True(t, hasN1, "the valid Node entry must survive")
}

// TestAffectedEntitiesToProto_CapturesRecordSnapshot pins that the encoder
// captures each non-tombstoned Node/Tab/FloatingWindow entity's POST-batch
// record from `working` into the entry's `record` oneof, and that
// TransitionsFromProto recovers it as the EntityMaterialized payload the
// resume materialized frame ships. This is the load-bearing contract that lets
// resume read batch-era state instead of cloning current live state.
func TestAffectedEntitiesToProto_CapturesRecordSnapshot(t *testing.T) {
	working := &leapmuxv1.UserCrdtState{
		Nodes: map[string]*leapmuxv1.NodeRecord{
			"n1": {NodeId: "n1"},
		},
		Tabs: map[string]*leapmuxv1.TabRecord{
			"t1": {TabId: "t1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
		},
	}

	encoded := AffectedEntitiesToProto(map[EntityRef]EntityWorkspaceTransition{
		{Kind: EntityKindNode, NodeID: "n1"}:                                          {Pre: "wA", Post: "wB"},
		{Kind: EntityKindTab, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "t1"}: {Pre: "", Post: "wA"},
		// WorkspaceRoot has no EntityMaterialized variant → no record snapshot.
		{Kind: EntityKindWorkspaceRoot, WorkspaceID: "ws1"}: {Pre: "ws1", Post: "ws1"},
	}, working)

	_, records := TransitionsFromProto(encoded)

	n1Rec, ok := records[EntityRef{Kind: EntityKindNode, NodeID: "n1"}]
	require.True(t, ok, "the Node record snapshot must be recovered")
	require.NotNil(t, n1Rec.GetNode(), "the recovered snapshot must carry the NodeRecord")
	assert.Equal(t, "n1", n1Rec.GetNode().GetNodeId())

	t1Rec, ok := records[EntityRef{Kind: EntityKindTab, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "t1"}]
	require.True(t, ok, "the Tab record snapshot must be recovered")
	require.NotNil(t, t1Rec.GetTab(), "the recovered snapshot must carry the TabRecord")
	assert.Equal(t, "t1", t1Rec.GetTab().GetTabId())

	_, hasWS := records[EntityRef{Kind: EntityKindWorkspaceRoot, WorkspaceID: "ws1"}]
	assert.False(t, hasWS, "WorkspaceRoot must have no record snapshot (no EntityMaterialized variant)")
}

// TestAffectedEntitiesToProto_CapturesFloatingWindowRecordSnapshot extends the
// Node/Tab coverage to FloatingWindow: the encode/decode switches are hand-
// written inverses over EntityKind, and the FloatingWindow record-snapshot arm
// (encode transitions_proto.go, decode :162-164) was not previously exercised
// end-to-end. A drift here would silently drop the materialized frame for a
// floating window that crossed into visibility during a disconnect gap.
func TestAffectedEntitiesToProto_CapturesFloatingWindowRecordSnapshot(t *testing.T) {
	working := &leapmuxv1.UserCrdtState{
		FloatingWindows: map[string]*leapmuxv1.FloatingWindowRecord{
			"fw1": {WindowId: "fw1", WorkspaceId: &leapmuxv1.LWWString{Value: "wA", Hlc: &leapmuxv1.HLC{Physical: 1, Logical: 0, ClientId: "c"}}},
		},
	}

	encoded := AffectedEntitiesToProto(map[EntityRef]EntityWorkspaceTransition{
		{Kind: EntityKindFloatingWindow, WindowID: "fw1"}: {Pre: "", Post: "wA"},
	}, working)

	_, records := TransitionsFromProto(encoded)

	fwRec, ok := records[EntityRef{Kind: EntityKindFloatingWindow, WindowID: "fw1"}]
	require.True(t, ok, "the FloatingWindow record snapshot must be recovered")
	require.NotNil(t, fwRec.GetFloatingWindow(), "the recovered snapshot must carry the FloatingWindowRecord")
	assert.Equal(t, "fw1", fwRec.GetFloatingWindow().GetWindowId())
}

// TestAffectedEntitiesToProto_OmitsTombstonedRecordSnapshot pins that a
// tombstoned record is NOT captured into the snapshot: a tombstone is
// projection-ignored client-side, and liveRecordSnapshot (shared with
// buildEntityMaterializedEvent) returns nil for it, so both catch-up paths
// skip the shell together. The transition is still recorded (so the resume
// classifies visibility correctly); only the record snapshot is omitted.
func TestAffectedEntitiesToProto_OmitsTombstonedRecordSnapshot(t *testing.T) {
	working := &leapmuxv1.UserCrdtState{
		Nodes: map[string]*leapmuxv1.NodeRecord{
			"dead": {NodeId: "dead", TombstoneAt: &leapmuxv1.HLC{Physical: 1, Logical: 0, ClientId: "c"}},
		},
	}

	// Pre != Post so the encoder considers a record snapshot; the tombstone
	// check must still omit it.
	encoded := AffectedEntitiesToProto(map[EntityRef]EntityWorkspaceTransition{
		{Kind: EntityKindNode, NodeID: "dead"}: {Pre: "wA", Post: "wB"},
	}, working)

	require.Len(t, encoded.GetEntries(), 1, "the transition must still be recorded")
	assert.Nil(t, encoded.GetEntries()[0].GetRecord(), "a tombstoned record must have no snapshot (projection-ignored)")

	_, records := TransitionsFromProto(encoded)
	_, hasDead := records[EntityRef{Kind: EntityKindNode, NodeID: "dead"}]
	assert.False(t, hasDead, "a tombstoned entity must yield no materialized-frame record")
}

// TestAffectedEntitiesToProto_OmitsStableVisibilityRecordSnapshot pins that
// Pre == Post (in-place edit) entries do not capture a record snapshot —
// stable-visibility batches never emit a materialized frame for any
// subscriber, so persisting the full proto would be pure journal bloat.
func TestAffectedEntitiesToProto_OmitsStableVisibilityRecordSnapshot(t *testing.T) {
	working := &leapmuxv1.UserCrdtState{
		Nodes: map[string]*leapmuxv1.NodeRecord{
			"n1": {NodeId: "n1"},
		},
	}
	encoded := AffectedEntitiesToProto(map[EntityRef]EntityWorkspaceTransition{
		{Kind: EntityKindNode, NodeID: "n1"}: {Pre: "wA", Post: "wA"},
	}, working)
	require.Len(t, encoded.GetEntries(), 1)
	assert.Nil(t, encoded.GetEntries()[0].GetRecord(), "stable-visibility edits must not persist a record snapshot")
}

// TestTransitionsFromProto_DropsEmptyTabIdentity pins that a TabIdent with an
// empty tab_id is dropped the same way an empty node_id is (cannot name an
// entity).
func TestTransitionsFromProto_DropsEmptyTabIdentity(t *testing.T) {
	decoded, _ := TransitionsFromProto(&leapmuxv1.BatchTransitions{
		Entries: []*leapmuxv1.BatchTransition{
			{Identity: &leapmuxv1.BatchTransition_Tab{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: ""}}, PreWorkspace: "wA", PostWorkspace: "wB"},
			{Identity: &leapmuxv1.BatchTransition_NodeId{NodeId: "n1"}, PreWorkspace: "wA", PostWorkspace: "wB"},
		},
	})
	assert.Len(t, decoded, 1, "empty tab_id entry must be dropped")
	_, hasN1 := decoded[EntityRef{Kind: EntityKindNode, NodeID: "n1"}]
	assert.True(t, hasN1)
}

// TestLiveRecordSnapshot_OmitsTombstone pins that buildEntityMaterializedEvent
// (via liveRecordSnapshot) returns nil for a tombstoned record — matching the
// commit-time encoder so resume/live cannot diverge on become-visible+tombstone.
func TestLiveRecordSnapshot_OmitsTombstone(t *testing.T) {
	state := &leapmuxv1.UserCrdtState{
		Nodes: map[string]*leapmuxv1.NodeRecord{
			"dead": {NodeId: "dead", TombstoneAt: &leapmuxv1.HLC{Physical: 1, Logical: 0, ClientId: "c"}},
			"live": {NodeId: "live"},
		},
	}
	assert.Nil(t, buildEntityMaterializedEvent(state, EntityRef{Kind: EntityKindNode, NodeID: "dead"}, &leapmuxv1.HLC{Physical: 2}))
	assert.NotNil(t, buildEntityMaterializedEvent(state, EntityRef{Kind: EntityKindNode, NodeID: "live"}, &leapmuxv1.HLC{Physical: 2}))
}

// TestMissingTransitionOp_CoversTheBatchsResolvableOps pins the completeness
// witness directly. The journal-level cases exercise it through the resume
// scan; these fix the rule itself, including the two ways an op is legitimately
// absent from a well-formed payload.
func TestMissingTransitionOp_CoversTheBatchsResolvableOps(t *testing.T) {
	nodeOp := func(id string) *leapmuxv1.CrdtOp {
		return &leapmuxv1.CrdtOp{Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
			NodeId: id,
			Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: "p"},
		}}}
	}
	entryFor := func(id string) *leapmuxv1.BatchTransition {
		return &leapmuxv1.BatchTransition{
			Identity:      &leapmuxv1.BatchTransition_NodeId{NodeId: id},
			PreWorkspace:  "w1",
			PostWorkspace: "w1",
		}
	}

	t.Run("covered batch reports nothing missing", func(t *testing.T) {
		batch := &leapmuxv1.OpBatch{Ops: []*leapmuxv1.CrdtOp{nodeOp("n1"), nodeOp("n2")}}
		transitions := &leapmuxv1.BatchTransitions{Entries: []*leapmuxv1.BatchTransition{entryFor("n1"), entryFor("n2")}}
		_, missing := MissingTransitionOp(batch, transitions)
		assert.False(t, missing)
	})

	t.Run("names the first uncovered op", func(t *testing.T) {
		batch := &leapmuxv1.OpBatch{Ops: []*leapmuxv1.CrdtOp{nodeOp("n1"), nodeOp("n2")}}
		transitions := &leapmuxv1.BatchTransitions{Entries: []*leapmuxv1.BatchTransition{entryFor("n1")}}
		ref, missing := MissingTransitionOp(batch, transitions)
		require.True(t, missing)
		assert.Equal(t, EntityRef{Kind: EntityKindNode, NodeID: "n2"}, ref)
	})

	t.Run("an empty payload against a resolvable batch is missing", func(t *testing.T) {
		// The degenerate truncation: a payload cut to zero bytes decodes
		// cleanly into an empty message, which the unmarshal guard cannot see.
		batch := &leapmuxv1.OpBatch{Ops: []*leapmuxv1.CrdtOp{nodeOp("n1")}}
		_, missing := MissingTransitionOp(batch, &leapmuxv1.BatchTransitions{})
		assert.True(t, missing)
	})

	t.Run("an op with no body is not evidence of loss", func(t *testing.T) {
		// OpTarget yields EntityKindUnknown for an unset oneof, and
		// AffectedEntitiesToProto deliberately drops those, so a well-formed
		// payload has no entry for them either. Treating that as corruption
		// would FALLBACK every resume whose tail contains one.
		batch := &leapmuxv1.OpBatch{Ops: []*leapmuxv1.CrdtOp{{OpId: "bodyless"}}}
		_, missing := MissingTransitionOp(batch, &leapmuxv1.BatchTransitions{})
		assert.False(t, missing)
	})

	t.Run("an entry whose identity is empty covers nothing", func(t *testing.T) {
		batch := &leapmuxv1.OpBatch{Ops: []*leapmuxv1.CrdtOp{nodeOp("n1")}}
		transitions := &leapmuxv1.BatchTransitions{Entries: []*leapmuxv1.BatchTransition{
			{Identity: &leapmuxv1.BatchTransition_NodeId{NodeId: ""}},
		}}
		_, missing := MissingTransitionOp(batch, transitions)
		assert.True(t, missing)
	})

	t.Run("an empty batch is trivially covered", func(t *testing.T) {
		_, missing := MissingTransitionOp(&leapmuxv1.OpBatch{}, &leapmuxv1.BatchTransitions{})
		assert.False(t, missing)
	})
}

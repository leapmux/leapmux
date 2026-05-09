package crdt

import (
	"maps"
	"math"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// PruneTombstonesAtOrBelow drops every node / tab / floating window
// record whose tombstone_at is at or below `watermark`. Mutates state
// in place; returns the number of records pruned across all three
// kinds.
//
// Safety: callers MUST hold the manager mutex (write lock) AND must
// only invoke this with `watermark <= state.CompactionWatermark`.
// Pruning is safe under those preconditions because:
//
//   - HLC monotonicity: new ops carry canonical HLCs strictly above
//     `maxHlc >= watermark`, so the pre-apply tombstone check can never
//     reference a pruned record. The check is short-circuit-friendly
//     ("record exists AND record is tombstoned" → reject); a missing
//     record is treated as not-yet-created, which is exactly the
//     desired outcome.
//   - Stale-epoch retries: rejected before reaching Apply via the
//     existing `BATCH_REJECTION_STALE_EPOCH` gate.
//   - Id collision: client-minted ids are 48-char nanoids (62^48 search
//     space); a legitimate retry of a pre-tombstone batch hits the
//     dedup row (when still cached) or fails the epoch check.
//   - Subscribers connected before the pruning still hold the original
//     tombstone records locally and continue to apply remote ops
//     correctly. New subscribers bootstrap from the pruned state and
//     see the entity as if it never existed.
func PruneTombstonesAtOrBelow(state *leapmuxv1.OrgCrdtState, watermark *leapmuxv1.HLC) int {
	if state == nil || HLCIsZero(watermark) {
		return 0
	}
	pruned := 0
	for id, n := range state.GetNodes() {
		ts := n.GetTombstoneAt()
		if HLCIsZero(ts) {
			continue
		}
		if HLCCmp(ts, watermark) <= 0 {
			delete(state.GetNodes(), id)
			pruned++
		}
	}
	for id, t := range state.GetTabs() {
		ts := t.GetTombstoneAt()
		if HLCIsZero(ts) {
			continue
		}
		if HLCCmp(ts, watermark) <= 0 {
			delete(state.GetTabs(), id)
			pruned++
		}
	}
	for id, fw := range state.GetFloatingWindows() {
		ts := fw.GetTombstoneAt()
		if HLCIsZero(ts) {
			continue
		}
		if HLCCmp(ts, watermark) <= 0 {
			delete(state.GetFloatingWindows(), id)
			pruned++
		}
	}
	return pruned
}

// NewState returns an empty OrgCrdtState seeded with the given org id.
// The workspaces map is initialized empty; lifecycle paths add entries
// via manager-internal mutation, not via the op log.
func NewState(orgID string) *leapmuxv1.OrgCrdtState {
	return &leapmuxv1.OrgCrdtState{
		OrgId:           orgID,
		Nodes:           map[string]*leapmuxv1.NodeRecord{},
		Tabs:            map[string]*leapmuxv1.TabRecord{},
		FloatingWindows: map[string]*leapmuxv1.FloatingWindowRecord{},
		Workspaces:      map[string]*leapmuxv1.WorkspaceContentsRecord{},
		MaxHlc:          &leapmuxv1.HLC{},
		CurrentEpoch:    1,
	}
}

// Apply mutates state in place, applying op with its canonical_hlc
// already set. Apply assumes the op has passed validation and that
// canonical_hlc is set; behavior is otherwise undefined.
//
// The transition function is:
//
//   - Tombstone op: write tombstone_at if hlc > current; clear all
//     non-tombstone registers on the entity.
//   - Set op on a tombstoned entity: drop (remove-wins).
//   - Set op on a live entity: replace the targeted register if
//     (op.canonical_hlc, op.origin_client_id) > current register HLC.
//   - parent_id (set-once): write only if pre-state is "" (zero value).
//
// Apply also normalizes -0.0 to +0.0 for every double register so
// byte-equal comparison across permutations is tractable.
func Apply(state *leapmuxv1.OrgCrdtState, op *leapmuxv1.OrgOp) {
	canon := op.GetCanonicalHlc()
	if canon == nil {
		return
	}
	advanceMaxHLC(state, canon)

	switch body := op.GetBody().(type) {
	case *leapmuxv1.OrgOp_SetNodeRegister:
		applySetNodeRegister(state, body.SetNodeRegister, canon)
	case *leapmuxv1.OrgOp_TombstoneNode:
		applyTombstoneNode(state, body.TombstoneNode, canon)
	case *leapmuxv1.OrgOp_SetTabRegister:
		applySetTabRegister(state, body.SetTabRegister, canon)
	case *leapmuxv1.OrgOp_TombstoneTab:
		applyTombstoneTab(state, body.TombstoneTab, canon)
	case *leapmuxv1.OrgOp_SetFloatingWindowRegister:
		applySetFloatingWindowRegister(state, body.SetFloatingWindowRegister, canon)
	case *leapmuxv1.OrgOp_TombstoneFloatingWindow:
		applyTombstoneFloatingWindow(state, body.TombstoneFloatingWindow, canon)
	case *leapmuxv1.OrgOp_SetWorkspaceRootNode:
		applySetWorkspaceRootNode(state, body.SetWorkspaceRootNode)
	}
}

func advanceMaxHLC(state *leapmuxv1.OrgCrdtState, hlc *leapmuxv1.HLC) {
	if HLCCmp(hlc, state.GetMaxHlc()) > 0 {
		state.MaxHlc = HLCClone(hlc)
	}
}

// canonicalizeZero collapses -0.0 to +0.0 so byte-equal output is
// well-defined. IEEE-754 makes -0.0 == 0.0 evaluate true, so we have
// to inspect the sign bit explicitly.
func canonicalizeZero(v float64) float64 {
	if math.Signbit(v) && v == 0 {
		return 0
	}
	return v
}

func canonicalizeZeros(values []float64) []float64 {
	out := make([]float64, len(values))
	for i, v := range values {
		out[i] = canonicalizeZero(v)
	}
	return out
}

// shouldWrite reports whether a write at hlc / clientID should
// supersede the current register HLC. The tie-break rule is
// (physical, logical, client_id) — but the comparison is folded into
// HLCCmp so the same rule is applied consistently.
func shouldWrite(currentHLC, opHLC *leapmuxv1.HLC) bool {
	return HLCCmp(opHLC, currentHLC) > 0
}

// --- LWW write helpers ---
//
// Each helper owns the canonical "compare HLC, replace whole wrapper"
// body for one LWW shape. They take a **wrapper so the caller can
// pass &rec.Field directly; the helper rewrites the slot only when
// the op's HLC strictly succeeds the current wrapper's HLC. Doubles
// canonicalise -0 → +0 so two clients that emit -0 vs +0 don't disagree
// on otherwise-identical state.

func setLWWString(slot **leapmuxv1.LWWString, hlc *leapmuxv1.HLC, value string) {
	if shouldWrite((*slot).GetHlc(), hlc) {
		*slot = &leapmuxv1.LWWString{Value: value, Hlc: HLCClone(hlc)}
	}
}

func setLWWInt32(slot **leapmuxv1.LWWInt32, hlc *leapmuxv1.HLC, value int32) {
	if shouldWrite((*slot).GetHlc(), hlc) {
		*slot = &leapmuxv1.LWWInt32{Value: value, Hlc: HLCClone(hlc)}
	}
}

func setLWWUint32(slot **leapmuxv1.LWWUint32, hlc *leapmuxv1.HLC, value uint32) {
	if shouldWrite((*slot).GetHlc(), hlc) {
		*slot = &leapmuxv1.LWWUint32{Value: value, Hlc: HLCClone(hlc)}
	}
}

func setLWWDouble(slot **leapmuxv1.LWWDouble, hlc *leapmuxv1.HLC, value float64) {
	if shouldWrite((*slot).GetHlc(), hlc) {
		*slot = &leapmuxv1.LWWDouble{Value: canonicalizeZero(value), Hlc: HLCClone(hlc)}
	}
}

func setLWWDoubles(slot **leapmuxv1.LWWDoubles, hlc *leapmuxv1.HLC, values []float64) {
	if shouldWrite((*slot).GetHlc(), hlc) {
		*slot = &leapmuxv1.LWWDoubles{
			Value: &leapmuxv1.DoubleList{Values: canonicalizeZeros(values)},
			Hlc:   HLCClone(hlc),
		}
	}
}

func setLWWDirection(slot **leapmuxv1.LWWDirection, hlc *leapmuxv1.HLC, value leapmuxv1.SplitDirection) {
	if shouldWrite((*slot).GetHlc(), hlc) {
		*slot = &leapmuxv1.LWWDirection{Value: value, Hlc: HLCClone(hlc)}
	}
}

func setLWWNodeKind(slot **leapmuxv1.LWWNodeKind, hlc *leapmuxv1.HLC, value leapmuxv1.NodeKind) {
	if shouldWrite((*slot).GetHlc(), hlc) {
		*slot = &leapmuxv1.LWWNodeKind{Value: value, Hlc: HLCClone(hlc)}
	}
}

// --- Node register transitions ---

func applySetNodeRegister(state *leapmuxv1.OrgCrdtState, op *leapmuxv1.SetNodeRegisterOp, hlc *leapmuxv1.HLC) {
	id := op.GetNodeId()
	rec, ok := state.Nodes[id]
	if !ok {
		rec = &leapmuxv1.NodeRecord{NodeId: id}
		state.Nodes[id] = rec
	}
	if !HLCIsZero(rec.GetTombstoneAt()) {
		// Remove-wins: every later op on a tombstoned node is dropped.
		return
	}
	switch field := op.GetField().(type) {
	case *leapmuxv1.SetNodeRegisterOp_Kind:
		setLWWNodeKind(&rec.Kind, hlc, field.Kind)
	case *leapmuxv1.SetNodeRegisterOp_ParentId:
		// Set-once: only writes when slot is empty.
		if rec.GetParentId() == "" {
			rec.ParentId = field.ParentId
		}
	case *leapmuxv1.SetNodeRegisterOp_Position:
		setLWWString(&rec.Position, hlc, field.Position)
	case *leapmuxv1.SetNodeRegisterOp_Direction:
		setLWWDirection(&rec.Direction, hlc, field.Direction)
	case *leapmuxv1.SetNodeRegisterOp_Ratios:
		setLWWDoubles(&rec.Ratios, hlc, field.Ratios.GetValues())
	case *leapmuxv1.SetNodeRegisterOp_Rows:
		setLWWUint32(&rec.Rows, hlc, field.Rows)
	case *leapmuxv1.SetNodeRegisterOp_Cols:
		setLWWUint32(&rec.Cols, hlc, field.Cols)
	case *leapmuxv1.SetNodeRegisterOp_RowRatios:
		setLWWDoubles(&rec.RowRatios, hlc, field.RowRatios.GetValues())
	case *leapmuxv1.SetNodeRegisterOp_ColRatios:
		setLWWDoubles(&rec.ColRatios, hlc, field.ColRatios.GetValues())
	}
}

func applyTombstoneNode(state *leapmuxv1.OrgCrdtState, op *leapmuxv1.TombstoneNodeOp, hlc *leapmuxv1.HLC) {
	id := op.GetNodeId()
	rec := state.Nodes[id]
	// Materialize the cleared tombstoned record both when no record
	// exists (so future Set ops drop under remove-wins) and when the
	// incoming HLC dominates the existing tombstone.
	if rec == nil || HLCCmp(hlc, rec.GetTombstoneAt()) > 0 {
		state.Nodes[id] = &leapmuxv1.NodeRecord{NodeId: id, TombstoneAt: HLCClone(hlc)}
	}
}

// --- Tab register transitions ---

func applySetTabRegister(state *leapmuxv1.OrgCrdtState, op *leapmuxv1.SetTabRegisterOp, hlc *leapmuxv1.HLC) {
	id := op.GetTabId()
	rec, ok := state.Tabs[id]
	if !ok {
		rec = &leapmuxv1.TabRecord{TabType: op.GetTabType(), TabId: id}
		state.Tabs[id] = rec
	}
	if rec.GetTabType() != op.GetTabType() {
		// Defense in depth: validator already rejected this. Drop.
		return
	}
	if !HLCIsZero(rec.GetTombstoneAt()) {
		return
	}
	switch field := op.GetField().(type) {
	case *leapmuxv1.SetTabRegisterOp_TileId:
		setLWWString(&rec.TileId, hlc, field.TileId)
	case *leapmuxv1.SetTabRegisterOp_Position:
		setLWWString(&rec.Position, hlc, field.Position)
	case *leapmuxv1.SetTabRegisterOp_WorkerId:
		setLWWString(&rec.WorkerId, hlc, field.WorkerId)
	case *leapmuxv1.SetTabRegisterOp_DisplayMode:
		setLWWInt32(&rec.DisplayMode, hlc, field.DisplayMode)
	case *leapmuxv1.SetTabRegisterOp_FileViewMode:
		setLWWInt32(&rec.FileViewMode, hlc, field.FileViewMode)
	case *leapmuxv1.SetTabRegisterOp_FileDiffBase:
		setLWWString(&rec.FileDiffBase, hlc, field.FileDiffBase)
	}
}

func applyTombstoneTab(state *leapmuxv1.OrgCrdtState, op *leapmuxv1.TombstoneTabOp, hlc *leapmuxv1.HLC) {
	id := op.GetTabId()
	rec := state.Tabs[id]
	if rec == nil || HLCCmp(hlc, rec.GetTombstoneAt()) > 0 {
		// Preserve the existing tab_type when one is on file (it's
		// immutable on the record); fall back to the op's tab_type
		// when materializing a tombstone for a never-seen tab.
		tabType := op.GetTabType()
		if rec != nil {
			tabType = rec.GetTabType()
		}
		state.Tabs[id] = &leapmuxv1.TabRecord{
			TabType:     tabType,
			TabId:       id,
			TombstoneAt: HLCClone(hlc),
		}
	}
}

// --- Floating window register transitions ---

func applySetFloatingWindowRegister(state *leapmuxv1.OrgCrdtState, op *leapmuxv1.SetFloatingWindowRegisterOp, hlc *leapmuxv1.HLC) {
	id := op.GetWindowId()
	rec, ok := state.FloatingWindows[id]
	if !ok {
		rec = &leapmuxv1.FloatingWindowRecord{WindowId: id}
		state.FloatingWindows[id] = rec
	}
	if !HLCIsZero(rec.GetTombstoneAt()) {
		return
	}
	switch field := op.GetField().(type) {
	case *leapmuxv1.SetFloatingWindowRegisterOp_WorkspaceId:
		setLWWString(&rec.WorkspaceId, hlc, field.WorkspaceId)
	case *leapmuxv1.SetFloatingWindowRegisterOp_X:
		setLWWDouble(&rec.X, hlc, field.X)
	case *leapmuxv1.SetFloatingWindowRegisterOp_Y:
		setLWWDouble(&rec.Y, hlc, field.Y)
	case *leapmuxv1.SetFloatingWindowRegisterOp_Width:
		setLWWDouble(&rec.Width, hlc, field.Width)
	case *leapmuxv1.SetFloatingWindowRegisterOp_Height:
		setLWWDouble(&rec.Height, hlc, field.Height)
	case *leapmuxv1.SetFloatingWindowRegisterOp_Opacity:
		setLWWDouble(&rec.Opacity, hlc, field.Opacity)
	case *leapmuxv1.SetFloatingWindowRegisterOp_RootNodeId:
		// Set-once.
		if rec.GetRootNodeId() == "" {
			rec.RootNodeId = field.RootNodeId
		}
	}
}

func applyTombstoneFloatingWindow(state *leapmuxv1.OrgCrdtState, op *leapmuxv1.TombstoneFloatingWindowOp, hlc *leapmuxv1.HLC) {
	id := op.GetWindowId()
	rec := state.FloatingWindows[id]
	if rec == nil || HLCCmp(hlc, rec.GetTombstoneAt()) > 0 {
		state.FloatingWindows[id] = &leapmuxv1.FloatingWindowRecord{
			WindowId:    id,
			TombstoneAt: HLCClone(hlc),
		}
	}
}

// --- Workspace-root assignment ---

func applySetWorkspaceRootNode(state *leapmuxv1.OrgCrdtState, op *leapmuxv1.SetWorkspaceRootNodeOp) {
	wsID := op.GetWorkspaceId()
	rec, ok := state.Workspaces[wsID]
	if !ok {
		// Live-session path: the lifecycle outbox seeds an empty entry
		// via MutateInternal before submitting this op. Bootstrap-replay
		// path: that MutateInternal is non-journaled, so on restart the
		// op arrives with no placeholder; we must create the record here
		// or `state.Workspaces[wsID]` stays absent and the subscriber's
		// projection never surfaces the workspace's root_node_id (the
		// frontend's awaitWorkspaceBootstrap then polls forever and
		// surfaces the 30s "Workspace load timed out" toast).
		//
		// A dangling entry after a restart-then-delete-then-restart
		// cycle is harmless: SQL `workspaces.is_deleted` is authoritative
		// for visibility, ListAccessibleWorkspaces excludes deleted rows,
		// and SubscriberFilter is built from that list -- so the
		// dangling CRDT record can't reach any subscriber's projection.
		rec = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: wsID}
		state.Workspaces[wsID] = rec
	}
	if rec.GetRootNodeId() == "" {
		rec.RootNodeId = op.GetRootNodeId()
	}
}

// CloneState returns a deep copy of an OrgCrdtState. Used by paths
// that mutate every record (manager bootstrap, tests). `proto.Clone`
// is deep on maps + nested messages so a single call covers every
// record kind plus the workspaces map. We re-allocate any nil maps
// because callers (Apply → ensureNode/Tab/FloatingWindow) assign into
// them directly and proto.Clone preserves nil-vs-empty.
//
// Most validator hot paths should use CloneStateForBatch instead — it
// deep-clones only the records the batch touches and shares refs for
// the rest, dropping per-batch cost from O(total entities) to
// O(|touched|).
func CloneState(state *leapmuxv1.OrgCrdtState) *leapmuxv1.OrgCrdtState {
	if state == nil {
		return nil
	}
	out := proto.Clone(state).(*leapmuxv1.OrgCrdtState)
	ensureStateMaps(out)
	return out
}

// CloneStateForBatch returns a working copy of `pre` suitable for the
// validator's Apply pass. The cost scales with the *kinds* of entities
// the batch touches — not the full state — because Apply only writes
// to the one entity map matching each op's body type:
//
//   - For each entity kind that the batch can write to (Nodes for any
//     SetNodeRegister/TombstoneNode op, Tabs for Set/TombstoneTab, …),
//     the corresponding map is cloned via `maps.Clone` and every
//     touched record's slot is deep-cloned so Apply's in-place
//     mutations land on the clone.
//   - For an entity kind no op in the batch can write to, the map is
//     shared with `pre`. A tab-only batch in a 50k-node org skips
//     three O(N) map copies.
//
// Safety invariant: Apply must only ever mutate records (and the map
// slot) of the entity kind matching the op's body. The unit tests in
// state_clone_for_batch_test.go pin this contract so future op
// additions can't silently regress.
//
// Top-level HLC fields (max_hlc, compaction_watermark) are deep-
// cloned because Apply may bump max_hlc on every op.
func CloneStateForBatch(pre *leapmuxv1.OrgCrdtState, batch []*leapmuxv1.OrgOp) *leapmuxv1.OrgCrdtState {
	if pre == nil {
		return nil
	}
	touched := batchTouchedIDs(batch)
	out := &leapmuxv1.OrgCrdtState{
		OrgId:               pre.GetOrgId(),
		MaxHlc:              HLCClone(pre.GetMaxHlc()),
		CompactionWatermark: HLCClone(pre.GetCompactionWatermark()),
		CurrentEpoch:        pre.GetCurrentEpoch(),
		EpochStartedAt:      pre.GetEpochStartedAt(),
	}

	if len(touched.nodes) == 0 {
		out.Nodes = pre.GetNodes()
	} else {
		out.Nodes = maps.Clone(pre.GetNodes())
		if out.Nodes == nil {
			out.Nodes = map[string]*leapmuxv1.NodeRecord{}
		}
		for id := range touched.nodes {
			if n, ok := out.Nodes[id]; ok {
				out.Nodes[id] = cloneNode(n)
			}
		}
	}

	if len(touched.tabs) == 0 {
		out.Tabs = pre.GetTabs()
	} else {
		out.Tabs = maps.Clone(pre.GetTabs())
		if out.Tabs == nil {
			out.Tabs = map[string]*leapmuxv1.TabRecord{}
		}
		for id := range touched.tabs {
			if t, ok := out.Tabs[id]; ok {
				out.Tabs[id] = cloneTab(t)
			}
		}
	}

	if len(touched.windows) == 0 {
		out.FloatingWindows = pre.GetFloatingWindows()
	} else {
		out.FloatingWindows = maps.Clone(pre.GetFloatingWindows())
		if out.FloatingWindows == nil {
			out.FloatingWindows = map[string]*leapmuxv1.FloatingWindowRecord{}
		}
		for id := range touched.windows {
			if fw, ok := out.FloatingWindows[id]; ok {
				out.FloatingWindows[id] = cloneFloatingWindow(fw)
			}
		}
	}

	if len(touched.workspaces) == 0 {
		out.Workspaces = pre.GetWorkspaces()
	} else {
		out.Workspaces = maps.Clone(pre.GetWorkspaces())
		if out.Workspaces == nil {
			out.Workspaces = map[string]*leapmuxv1.WorkspaceContentsRecord{}
		}
		for id := range touched.workspaces {
			if ws, ok := out.Workspaces[id]; ok {
				// WorkspaceContentsRecord is a thin struct (no LWW
				// envelopes), proto.Clone is overkill but works.
				out.Workspaces[id] = proto.Clone(ws).(*leapmuxv1.WorkspaceContentsRecord)
			}
		}
	}

	return out
}

// touchedIDs is the per-batch set of ids whose records the validator
// may mutate via Apply. Membership is derived from each op's
// EntityRef; ops with EntityKindUnknown contribute nothing.
type touchedIDs struct {
	nodes      map[string]bool
	tabs       map[string]bool
	windows    map[string]bool
	workspaces map[string]bool
}

func batchTouchedIDs(batch []*leapmuxv1.OrgOp) touchedIDs {
	t := touchedIDs{
		nodes:      map[string]bool{},
		tabs:       map[string]bool{},
		windows:    map[string]bool{},
		workspaces: map[string]bool{},
	}
	for _, op := range batch {
		ref := OpTarget(op)
		switch ref.Kind {
		case EntityKindNode:
			t.nodes[ref.NodeID] = true
		case EntityKindTab:
			t.tabs[ref.TabID] = true
		case EntityKindFloatingWindow:
			t.windows[ref.WindowID] = true
		case EntityKindWorkspaceRoot:
			t.workspaces[ref.WorkspaceID] = true
		}
	}
	return t
}

func ensureStateMaps(s *leapmuxv1.OrgCrdtState) {
	if s.Nodes == nil {
		s.Nodes = map[string]*leapmuxv1.NodeRecord{}
	}
	if s.Tabs == nil {
		s.Tabs = map[string]*leapmuxv1.TabRecord{}
	}
	if s.FloatingWindows == nil {
		s.FloatingWindows = map[string]*leapmuxv1.FloatingWindowRecord{}
	}
	if s.Workspaces == nil {
		s.Workspaces = map[string]*leapmuxv1.WorkspaceContentsRecord{}
	}
}

func cloneNode(n *leapmuxv1.NodeRecord) *leapmuxv1.NodeRecord {
	if n == nil {
		return nil
	}
	return proto.Clone(n).(*leapmuxv1.NodeRecord)
}

func cloneTab(t *leapmuxv1.TabRecord) *leapmuxv1.TabRecord {
	if t == nil {
		return nil
	}
	return proto.Clone(t).(*leapmuxv1.TabRecord)
}

func cloneFloatingWindow(f *leapmuxv1.FloatingWindowRecord) *leapmuxv1.FloatingWindowRecord {
	if f == nil {
		return nil
	}
	return proto.Clone(f).(*leapmuxv1.FloatingWindowRecord)
}

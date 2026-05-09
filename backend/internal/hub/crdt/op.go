// Package crdt implements the per-org commutative CRDT for workspace
// layouts and tabs. The model is documented in the plan at
// `~/.config/leapmux/solo/worker/plans/2026/05/`. In short:
//
//   - Every CRDT op is a single LWW register write or a tombstone.
//   - The hub assigns canonical HLCs on commit; client-supplied HLCs
//     are advisory.
//   - Apply is "merge max" per non-tombstone register on live entities,
//     or "canonical cleared form" on tombstoned entities.
//   - Higher-level intents (split a tile, make a grid, ...) are
//     client-side batches that share a contiguous canonical HLC range.
//
// The Apply transition function is byte-equal-deterministic over any
// permutation of a validated committed op log; this property is
// asserted by the commute/parity tests.
package crdt

import (
	"crypto/sha256"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"google.golang.org/protobuf/proto"
)

// HLCCmp compares two HLC values lex by (physical, logical, client_id).
// Returns -1 / 0 / +1 in the usual sense.
func HLCCmp(a, b *leapmuxv1.HLC) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return -1
	}
	if b == nil {
		return 1
	}
	if a.GetPhysical() < b.GetPhysical() {
		return -1
	}
	if a.GetPhysical() > b.GetPhysical() {
		return 1
	}
	if a.GetLogical() < b.GetLogical() {
		return -1
	}
	if a.GetLogical() > b.GetLogical() {
		return 1
	}
	switch {
	case a.GetClientId() < b.GetClientId():
		return -1
	case a.GetClientId() > b.GetClientId():
		return 1
	}
	return 0
}

// HLCIsZero reports whether an HLC is unset (nil or all-zero).
func HLCIsZero(h *leapmuxv1.HLC) bool {
	return h == nil || (h.GetPhysical() == 0 && h.GetLogical() == 0 && h.GetClientId() == "")
}

// HLCClone returns a deep copy of an HLC. Returns nil for nil input.
func HLCClone(h *leapmuxv1.HLC) *leapmuxv1.HLC {
	if h == nil {
		return nil
	}
	return &leapmuxv1.HLC{
		Physical: h.GetPhysical(),
		Logical:  h.GetLogical(),
		ClientId: h.GetClientId(),
	}
}

// Clock is a hybrid logical clock. The hub holds one Clock per org,
// keyed by an `originClientID` that distinguishes the hub-side
// allocator from any client. Tick and Observe are NOT thread-safe;
// callers must serialize through the per-org manager goroutine.
type Clock struct {
	clientID string
	maxPhys  int64
	maxLog   int64
}

// NewClock returns a Clock that stamps ops with the given client id.
func NewClock(clientID string) *Clock {
	return &Clock{clientID: clientID}
}

// Now is overridable in tests; defaults to time.Now().UnixMilli() at
// the call site that wires Tick.
type NowFunc func() int64

// Tick produces a new HLC strictly greater than every HLC the clock
// has previously produced or observed.
func (c *Clock) Tick(now int64) *leapmuxv1.HLC {
	if now > c.maxPhys {
		c.maxPhys = now
		c.maxLog = 0
	} else {
		c.maxLog++
	}
	return &leapmuxv1.HLC{
		Physical: c.maxPhys,
		Logical:  c.maxLog,
		ClientId: c.clientID,
	}
}

// Observe absorbs a remote HLC so the clock's next Tick is strictly
// greater than the observed value. Used at bootstrap time to seed the
// clock from state.max_hlc.
func (c *Clock) Observe(remote *leapmuxv1.HLC) {
	if remote == nil {
		return
	}
	if remote.GetPhysical() > c.maxPhys {
		c.maxPhys = remote.GetPhysical()
		c.maxLog = remote.GetLogical()
		return
	}
	if remote.GetPhysical() == c.maxPhys && remote.GetLogical() > c.maxLog {
		c.maxLog = remote.GetLogical()
	}
}

// Current returns the highest HLC the clock has produced or observed
// (without advancing it).
func (c *Clock) Current() *leapmuxv1.HLC {
	return &leapmuxv1.HLC{
		Physical: c.maxPhys,
		Logical:  c.maxLog,
		ClientId: c.clientID,
	}
}

// BatchBodyHash returns the SHA-256 of the marshaled batch body (op
// bodies only, with each op's hub-assigned identity fields stripped).
// Used as the dedup key for principal-aware idempotent retries.
//
// The stripped fields are op_id (assigned by client + observed by hub,
// but irrelevant for body equality), origin_client_id (overwritten by
// the hub at commit), client_hlc (advisory hint only), and canonical_hlc
// (assigned by the hub). A retry that carries the same semantic ops in
// the same order MUST hash to the same body_hash regardless of which
// client minted it or when.
func BatchBodyHash(batch *leapmuxv1.OpBatch) ([]byte, error) {
	if batch == nil {
		return nil, fmt.Errorf("nil batch")
	}
	bodyOps := make([]*leapmuxv1.OrgOp, len(batch.GetOps()))
	for i, op := range batch.GetOps() {
		bodyOps[i] = &leapmuxv1.OrgOp{Body: op.GetBody()}
	}
	// batch_id is NOT part of the body hash — clients may retry the
	// same semantic batch under a different batch_id, but dedup must
	// detect it as the same body.
	body := &leapmuxv1.OpBatch{Ops: bodyOps}
	bytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal batch body: %w", err)
	}
	sum := sha256.Sum256(bytes)
	return sum[:], nil
}

// MarshalOp returns the deterministic binary encoding of a fully
// canonical-stamped op (canonical_hlc set).
func MarshalOp(op *leapmuxv1.OrgOp) ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(op)
}

// UnmarshalOp parses a binary-encoded op.
func UnmarshalOp(data []byte) (*leapmuxv1.OrgOp, error) {
	op := &leapmuxv1.OrgOp{}
	if err := proto.Unmarshal(data, op); err != nil {
		return nil, fmt.Errorf("unmarshal op: %w", err)
	}
	return op, nil
}

// EntityKind classifies the target of an op.
type EntityKind int

const (
	EntityKindUnknown EntityKind = iota
	EntityKindNode
	EntityKindTab
	EntityKindFloatingWindow
	EntityKindWorkspaceRoot
)

// EntityRef identifies the target of an op. For tabs, TabType is
// also part of identity; the validator enforces tab_id uniqueness
// across TabTypes inside a single org doc.
type EntityRef struct {
	Kind        EntityKind
	NodeID      string
	TabType     leapmuxv1.TabType
	TabID       string
	WindowID    string
	WorkspaceID string
}

// IsTombstoneOp reports whether `op` is one of the three entity-tombstone
// ops. Tombstone ops strip the registers used to resolve the entity's
// owning workspace (parent_id / tile_id / workspace_id), so callers that
// drive visibility transitions need to handle them specially — the
// post-state workspace can't be recovered from the entity record.
func IsTombstoneOp(op *leapmuxv1.OrgOp) bool {
	switch op.GetBody().(type) {
	case *leapmuxv1.OrgOp_TombstoneNode, *leapmuxv1.OrgOp_TombstoneTab, *leapmuxv1.OrgOp_TombstoneFloatingWindow:
		return true
	}
	return false
}

// ToJSON renders the EntityRef's identifying fields as a JSON object,
// keyed by the proto field names callers expect on the wire. The shape
// is sparse — only the IDs relevant to the entity kind are included —
// and never carries the op-kind ("set_node_register" etc): callers that
// need an op-name label add their own "type" key alongside the result.
// Returns an empty map for EntityKindUnknown so JSON output stays a
// stable object shape across all op variants.
func (r EntityRef) ToJSON() map[string]any {
	switch r.Kind {
	case EntityKindNode:
		return map[string]any{"node_id": r.NodeID}
	case EntityKindTab:
		return map[string]any{"tab_type": r.TabType.String(), "tab_id": r.TabID}
	case EntityKindFloatingWindow:
		return map[string]any{"window_id": r.WindowID}
	case EntityKindWorkspaceRoot:
		return map[string]any{"workspace_id": r.WorkspaceID}
	}
	return map[string]any{}
}

// OpTarget extracts the EntityRef an op acts on.
func OpTarget(op *leapmuxv1.OrgOp) EntityRef {
	switch body := op.GetBody().(type) {
	case *leapmuxv1.OrgOp_SetNodeRegister:
		return EntityRef{Kind: EntityKindNode, NodeID: body.SetNodeRegister.GetNodeId()}
	case *leapmuxv1.OrgOp_TombstoneNode:
		return EntityRef{Kind: EntityKindNode, NodeID: body.TombstoneNode.GetNodeId()}
	case *leapmuxv1.OrgOp_SetTabRegister:
		return EntityRef{
			Kind:    EntityKindTab,
			TabType: body.SetTabRegister.GetTabType(),
			TabID:   body.SetTabRegister.GetTabId(),
		}
	case *leapmuxv1.OrgOp_TombstoneTab:
		return EntityRef{
			Kind:    EntityKindTab,
			TabType: body.TombstoneTab.GetTabType(),
			TabID:   body.TombstoneTab.GetTabId(),
		}
	case *leapmuxv1.OrgOp_SetFloatingWindowRegister:
		return EntityRef{Kind: EntityKindFloatingWindow, WindowID: body.SetFloatingWindowRegister.GetWindowId()}
	case *leapmuxv1.OrgOp_TombstoneFloatingWindow:
		return EntityRef{Kind: EntityKindFloatingWindow, WindowID: body.TombstoneFloatingWindow.GetWindowId()}
	case *leapmuxv1.OrgOp_SetWorkspaceRootNode:
		return EntityRef{Kind: EntityKindWorkspaceRoot, WorkspaceID: body.SetWorkspaceRootNode.GetWorkspaceId()}
	}
	return EntityRef{}
}

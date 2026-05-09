package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
)

// stateBuilder accumulates a minimal OrgMaterialized covering the
// shapes preflightTile / preflightTab inspect: workspaces with root
// node ids, live + tombstoned nodes parented in a tree, and live +
// tombstoned tabs anchored to specific tiles. Used by the
// table-driven preflight tests below.
type stateBuilder struct {
	st *leapmuxv1.OrgMaterialized
}

func newStateBuilder() *stateBuilder {
	return &stateBuilder{
		st: &leapmuxv1.OrgMaterialized{
			Nodes:      map[string]*leapmuxv1.NodeRecord{},
			Tabs:       map[string]*leapmuxv1.TabRecord{},
			Workspaces: map[string]*leapmuxv1.WorkspaceContentsRecord{},
		},
	}
}

func (b *stateBuilder) workspace(workspaceID, rootNodeID string) *stateBuilder {
	b.st.Workspaces[workspaceID] = &leapmuxv1.WorkspaceContentsRecord{
		WorkspaceId: workspaceID,
		RootNodeId:  rootNodeID,
	}
	return b
}

func (b *stateBuilder) node(nodeID, parentID string) *stateBuilder {
	b.st.Nodes[nodeID] = &leapmuxv1.NodeRecord{NodeId: nodeID, ParentId: parentID}
	return b
}

// leafNode registers a live leaf with an explicit lexorank position.
// Used by `firstLiveLeaf` tests where the DFS ordering depends on the
// child position field that the bare `node` helper leaves unset.
func (b *stateBuilder) leafNode(nodeID, parentID, position string) *stateBuilder {
	b.st.Nodes[nodeID] = &leapmuxv1.NodeRecord{
		NodeId:   nodeID,
		ParentId: parentID,
		Kind:     &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF},
		Position: &leapmuxv1.LWWString{Value: position},
	}
	return b
}

// splitNode registers a live SPLIT node with an explicit lexorank
// position. Children are added via leafNode/splitNode parented to this
// id.
func (b *stateBuilder) splitNode(nodeID, parentID, position string) *stateBuilder {
	b.st.Nodes[nodeID] = &leapmuxv1.NodeRecord{
		NodeId:   nodeID,
		ParentId: parentID,
		Kind:     &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_SPLIT},
		Position: &leapmuxv1.LWWString{Value: position},
	}
	return b
}

func (b *stateBuilder) tombstonedNode(nodeID, parentID string) *stateBuilder {
	b.st.Nodes[nodeID] = &leapmuxv1.NodeRecord{
		NodeId: nodeID, ParentId: parentID,
		TombstoneAt: &leapmuxv1.HLC{Physical: 1, Logical: 0, ClientId: "x"},
	}
	return b
}

func (b *stateBuilder) tab(tabID, tileID string, tabType leapmuxv1.TabType) *stateBuilder {
	b.st.Tabs[tabID] = &leapmuxv1.TabRecord{
		TabId:   tabID,
		TabType: tabType,
		TileId:  &leapmuxv1.LWWString{Value: tileID},
	}
	return b
}

// tabAt is the position-aware variant used by resolvePositionSpec
// tests. The base `tab` helper leaves Position nil, which is fine for
// preflight tests but indistinguishable from the empty-rank case the
// LexoRank math relies on.
func (b *stateBuilder) tabAt(tabID, tileID, position string, tabType leapmuxv1.TabType) *stateBuilder {
	b.st.Tabs[tabID] = &leapmuxv1.TabRecord{
		TabId:    tabID,
		TabType:  tabType,
		TileId:   &leapmuxv1.LWWString{Value: tileID},
		Position: &leapmuxv1.LWWString{Value: position},
	}
	return b
}

func (b *stateBuilder) tombstonedTab(tabID, tileID string, tabType leapmuxv1.TabType) *stateBuilder {
	b.st.Tabs[tabID] = &leapmuxv1.TabRecord{
		TabId:       tabID,
		TabType:     tabType,
		TileId:      &leapmuxv1.LWWString{Value: tileID},
		TombstoneAt: &leapmuxv1.HLC{Physical: 1, Logical: 0, ClientId: "x"},
	}
	return b
}

// captureEmit redirects remote.Out to a buffer, runs fn, restores
// remote.Out, then returns the captured error envelope's code+message
// (empty strings on the happy path). Necessary because
// remote.EmitError* writes the structured envelope to stdout, not to
// the returned error.
func captureEmit(t *testing.T, fn func() error) (code string, message string) {
	t.Helper()
	prev := remote.Out
	buf := &bytes.Buffer{}
	remote.Out = buf
	defer func() { remote.Out = prev }()
	if err := fn(); err == nil {
		return "", ""
	}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("envelope did not parse: %v\nbuf=%s", err, buf.String())
	}
	return env.Error.Code, env.Error.Message
}

func TestPreflightTile_AcceptsLiveTileInWorkspace(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		node("root-1", "").
		node("child-A", "root-1").
		st
	code, msg := captureEmit(t, func() error {
		return preflightTile(state, "ws-1", "child-A")
	})
	assert.Empty(t, code, "live tile in workspace must pass preflight: %s", msg)
}

func TestPreflightTile_RejectsEmptyTileID(t *testing.T) {
	state := newStateBuilder().workspace("ws-1", "root-1").node("root-1", "").st
	code, _ := captureEmit(t, func() error {
		return preflightTile(state, "ws-1", "")
	})
	assert.Equal(t, "invalid_request", code)
}

func TestPreflightTile_RejectsUnknownTile(t *testing.T) {
	state := newStateBuilder().workspace("ws-1", "root-1").node("root-1", "").st
	code, msg := captureEmit(t, func() error {
		return preflightTile(state, "ws-1", "ghost")
	})
	assert.Equal(t, "not_found", code)
	assert.Contains(t, msg, "ghost")
}

func TestPreflightTile_RejectsTombstonedTile(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		node("root-1", "").
		tombstonedNode("dead", "root-1").
		st
	code, msg := captureEmit(t, func() error {
		return preflightTile(state, "ws-1", "dead")
	})
	assert.Equal(t, "not_found", code)
	assert.Contains(t, msg, "tombstoned")
}

func TestPreflightTile_RejectsWrongWorkspace(t *testing.T) {
	// child-A lives under ws-1; asking for it under ws-2 must fail.
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		workspace("ws-2", "root-2").
		node("root-1", "").
		node("root-2", "").
		node("child-A", "root-1").
		st
	code, msg := captureEmit(t, func() error {
		return preflightTile(state, "ws-2", "child-A")
	})
	assert.Equal(t, "not_found", code)
	assert.Contains(t, msg, "ws-2")
}

func TestPreflightTile_EmptyWorkspaceSkipsPlacementCheck(t *testing.T) {
	// `tab move` accepts cross-workspace destinations and passes
	// workspaceID="" to skip the placement assertion.
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		workspace("ws-2", "root-2").
		node("root-1", "").
		node("root-2", "").
		node("child-A", "root-1").
		st
	code, _ := captureEmit(t, func() error {
		return preflightTile(state, "", "child-A")
	})
	assert.Empty(t, code, "empty workspaceID must skip the cross-workspace placement check")
}

func TestPreflightTab_AcceptsLiveAgent(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		node("root-1", "").
		tab("agent-A", "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	code, msg := captureEmit(t, func() error {
		return preflightTab(state, "ws-1", "agent-A", leapmuxv1.TabType_TAB_TYPE_AGENT)
	})
	assert.Empty(t, code, "live agent tab must pass preflight: %s", msg)
}

func TestPreflightTab_RejectsTypeMismatch(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		node("root-1", "").
		tab("agent-A", "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	code, msg := captureEmit(t, func() error {
		return preflightTab(state, "ws-1", "agent-A", leapmuxv1.TabType_TAB_TYPE_TERMINAL)
	})
	assert.Equal(t, "invalid_request", code)
	assert.Contains(t, msg, "type agent")
	assert.Contains(t, msg, "expected terminal")
}

func TestPreflightTab_RejectsTombstoned(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		node("root-1", "").
		tombstonedTab("agent-A", "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	code, msg := captureEmit(t, func() error {
		return preflightTab(state, "ws-1", "agent-A", leapmuxv1.TabType_TAB_TYPE_AGENT)
	})
	assert.Equal(t, "not_found", code)
	assert.Contains(t, msg, "tombstoned")
}

func TestPreflightTab_RejectsUnknownTab(t *testing.T) {
	state := newStateBuilder().workspace("ws-1", "root-1").node("root-1", "").st
	code, _ := captureEmit(t, func() error {
		return preflightTab(state, "ws-1", "ghost", leapmuxv1.TabType_TAB_TYPE_AGENT)
	})
	assert.Equal(t, "not_found", code)
}

func TestPreflightTab_RejectsTabInOtherWorkspace(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		workspace("ws-2", "root-2").
		node("root-1", "").
		node("root-2", "").
		tab("agent-A", "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	code, msg := captureEmit(t, func() error {
		return preflightTab(state, "ws-2", "agent-A", leapmuxv1.TabType_TAB_TYPE_AGENT)
	})
	assert.Equal(t, "not_found", code)
	assert.Contains(t, msg, "ws-2")
}

func TestPreflightTab_UnspecifiedTypeSkipsTypeCheck(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		node("root-1", "").
		tab("agent-A", "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	code, _ := captureEmit(t, func() error {
		return preflightTab(state, "ws-1", "agent-A", leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED)
	})
	assert.Empty(t, code, "passing TAB_TYPE_UNSPECIFIED must skip the type-match assertion")
}

func TestNodeWorkspaceFromState_WalksParentChain(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		node("root-1", "").
		node("mid", "root-1").
		node("leaf", "mid").
		st
	assert.Equal(t, "ws-1", nodeWorkspaceFromState(state, "leaf"))
	assert.Equal(t, "ws-1", nodeWorkspaceFromState(state, "mid"))
	assert.Equal(t, "ws-1", nodeWorkspaceFromState(state, "root-1"))
}

func TestNodeWorkspaceFromState_EmptyForOrphan(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		node("root-1", "").
		node("orphan", "missing-parent").
		st
	assert.Empty(t, nodeWorkspaceFromState(state, "orphan"))
}

func TestNodeWorkspaceFromState_BreaksCycleSafely(t *testing.T) {
	// A malformed state where two nodes parent each other must not
	// loop forever; the visited-set bails out and returns "".
	state := newStateBuilder().
		node("a", "b").
		node("b", "a").
		st
	assert.Empty(t, nodeWorkspaceFromState(state, "a"))
}

// TestIsWorkerUnreachable_ExistenceAuthClass guards the close-path
// fallback that lets `agent close` / `terminal close` tombstone a
// tab whose worker is gone. We MUST match the four existence/auth
// connect codes (NotFound, PermissionDenied, Unauthenticated,
// Unavailable) and ONLY those — transient failures like Internal
// or DeadlineExceeded would tombstone an agent that's actually
// alive on the worker, which is far worse than the user pressing
// retry.
func TestIsWorkerUnreachable_ExistenceAuthClass(t *testing.T) {
	cases := []struct {
		name string
		code connect.Code
		want bool
	}{
		{"not found", connect.CodeNotFound, true},
		{"permission denied", connect.CodePermissionDenied, true},
		{"unauthenticated", connect.CodeUnauthenticated, true},
		{"unavailable", connect.CodeUnavailable, true},
		{"internal stays hard", connect.CodeInternal, false},
		{"deadline exceeded stays hard", connect.CodeDeadlineExceeded, false},
		{"unknown stays hard", connect.CodeUnknown, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := &codedRPCError{
				Code:  "channel_open_failed",
				Cause: connect.NewError(tc.code, errors.New("worker gone")),
			}
			assert.Equal(t, tc.want, isWorkerUnreachable(err))
		})
	}
}

// TestIsWorkerUnreachable_OnlyChannelOpenFailures pins the
// codedRPCError gating: a NotFound surfaced through some path OTHER
// than channel_open_failed must NOT trigger the fallback. The
// fallback's whole purpose is "worker side of an inner RPC isn't
// reachable"; an unmarshal error that happens to wrap NotFound (it
// wouldn't, but defensively) shouldn't tombstone a live agent.
func TestIsWorkerUnreachable_OnlyChannelOpenFailures(t *testing.T) {
	other := &codedRPCError{
		Code:  "unmarshal_failed",
		Cause: connect.NewError(connect.CodeNotFound, errors.New("bytes")),
	}
	assert.False(t, isWorkerUnreachable(other),
		"non-channel-open coded errors must not be treated as worker-unreachable")
}

// TestIsWorkerUnreachable_PlainConnectErrorAlsoMatches covers the
// local-IPC path where the error reaches us as a raw connect.Error
// (not wrapped in codedRPCError because localIPC translates server
// errors directly).
func TestIsWorkerUnreachable_PlainConnectErrorAlsoMatches(t *testing.T) {
	err := connect.NewError(connect.CodeNotFound, errors.New("worker absent"))
	assert.True(t, isWorkerUnreachable(err))

	transient := connect.NewError(connect.CodeInternal, errors.New("boom"))
	assert.False(t, isWorkerUnreachable(transient))
}

// TestIsWorkerUnreachable_NilSafe is the obvious negative — nil err
// must report "reachable" so callers can use the predicate in the
// post-call branch without a nil check.
func TestIsWorkerUnreachable_NilSafe(t *testing.T) {
	assert.False(t, isWorkerUnreachable(nil))
	assert.False(t, isWorkerUnreachable(fmt.Errorf("bare error with no code")))
}

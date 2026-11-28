package crdt

import (
	"slices"
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestCompareLayoutStringMatchesUTF16CodeUnitOrder(t *testing.T) {
	t.Parallel()

	samples := []string{
		"",
		"a",
		"ab",
		"b",
		"0",
		"10",
		"2",
		// BMP code point U+E000 sorts AFTER supplementary plane U+1F600 in UTF-16
		// (0xE000 > high surrogate 0xD83D), even though U+E000 < U+1F600 in UTF-8/runes.
		"\uE000",
		"\U0001F600",
		"\U0001F601",
		"\U00010000",
		"\uFFFD",
		"prefix-\U0001F600-a",
		"prefix-\U0001F600-b",
		"prefix-\uE000",
		string([]byte{0xff, 'a'}),
	}

	for _, a := range samples {
		for _, b := range samples {
			want := slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b)))
			assert.Equal(t, want, compareLayoutString(a, b), "a=%q b=%q", a, b)
		}
	}
}

func TestGridChildIDsOrderTieBreakAndValidation(t *testing.T) {
	t.Parallel()

	node := func(pos string) *leapmuxv1.NodeRecord {
		return &leapmuxv1.NodeRecord{
			Position: &leapmuxv1.LWWString{Value: pos},
		}
	}
	nodes := map[string]*leapmuxv1.NodeRecord{
		"b":       node("0,1"),
		"a":       node("0,1"), // tie with "b": lower UTF-16 ID wins
		"bottom":  node("1,2"),
		"leading": node("00,0"), // non-canonical leading zero ignored
		"oob":     node("2,0"),  // out of row range ignored
		"bad":     node("1,x"),  // malformed ignored
		"space":   node(" 0,0"), // whitespace ignored
	}
	children := []string{"b", "a", "bottom", "leading", "oob", "bad", "space"}

	got := gridChildIDs(nodes, children, 2, 3)
	assert.Equal(t, []string{"", "a", "", "", "", "bottom"}, got)

	assert.Nil(t, gridChildIDs(nodes, children, MaxGridDimension+1, 1))
	assert.Empty(t, gridChildIDs(nodes, children, 0, 3))
}

func TestLocateTileWorkspace_ResolvesLiveFloatingWindowTilesAndRejectsTombstoned(t *testing.T) {
	t.Parallel()

	mgr := &Manager{
		state: &leapmuxv1.UserCrdtState{
			Workspaces: map[string]*leapmuxv1.WorkspaceContentsRecord{
				"ws-1": {WorkspaceId: "ws-1", RootNodeId: "ws-root"},
			},
			FloatingWindows: map[string]*leapmuxv1.FloatingWindowRecord{
				"fw-live": {
					WindowId:    "fw-live",
					WorkspaceId: &leapmuxv1.LWWString{Value: "ws-1"},
					RootNodeId:  "fw-root",
				},
				"fw-dead": {
					WindowId:    "fw-dead",
					WorkspaceId: &leapmuxv1.LWWString{Value: "ws-1"},
					RootNodeId:  "dead-root",
					TombstoneAt: &leapmuxv1.HLC{Physical: 10, Logical: 0, ClientId: "c1"},
				},
			},
			Nodes: map[string]*leapmuxv1.NodeRecord{
				"ws-root":   {NodeId: "ws-root"},
				"ws-leaf":   {NodeId: "ws-leaf", ParentId: "ws-root"},
				"fw-root":   {NodeId: "fw-root"},
				"fw-split":  {NodeId: "fw-split", ParentId: "fw-root"},
				"fw-leaf":   {NodeId: "fw-leaf", ParentId: "fw-split"},
				"dead-root": {NodeId: "dead-root"},
				"dead-leaf": {NodeId: "dead-leaf", ParentId: "dead-root"},
			},
		},
	}

	assert.Equal(t, "ws-1", mgr.LocateTileWorkspace("ws-leaf"))
	assert.Equal(t, "ws-1", mgr.LocateTileWorkspace("fw-root"), "live floating window root must resolve to its workspace")
	assert.Equal(t, "ws-1", mgr.LocateTileWorkspace("fw-leaf"), "nested descendant inside a live floating window must resolve to its workspace")
	assert.Empty(t, mgr.LocateTileWorkspace("dead-root"), "tombstoned floating window root must not resolve")
	assert.Empty(t, mgr.LocateTileWorkspace("dead-leaf"), "descendant of tombstoned floating window must not resolve")
}

func TestFindNodeRootAndFindRootWorkspace(t *testing.T) {
	t.Parallel()

	workspaces := map[string]*leapmuxv1.WorkspaceContentsRecord{
		"ws-1": {WorkspaceId: "ws-1", RootNodeId: "ws-root"},
	}
	floating := map[string]*leapmuxv1.FloatingWindowRecord{
		"fw-live": {
			WindowId:    "fw-live",
			WorkspaceId: &leapmuxv1.LWWString{Value: "ws-1"},
			RootNodeId:  "fw-root",
		},
		"fw-collide": {
			WindowId:    "fw-collide",
			WorkspaceId: &leapmuxv1.LWWString{Value: "ws-other"},
			RootNodeId:  "ws-root",
		},
		"fw-dead": {
			WindowId:    "fw-dead",
			WorkspaceId: &leapmuxv1.LWWString{Value: "ws-1"},
			RootNodeId:  "dead-root",
			TombstoneAt: &leapmuxv1.HLC{Physical: 5, Logical: 0, ClientId: "c1"},
		},
	}
	nodes := map[string]*leapmuxv1.NodeRecord{
		"ws-root":   {NodeId: "ws-root"},
		"ws-leaf":   {NodeId: "ws-leaf", ParentId: "ws-root"},
		"fw-root":   {NodeId: "fw-root"},
		"fw-grid":   {NodeId: "fw-grid", ParentId: "fw-root"},
		"fw-cell":   {NodeId: "fw-cell", ParentId: "fw-grid"},
		"dead-root": {NodeId: "dead-root"},
		"dead-leaf": {NodeId: "dead-leaf", ParentId: "dead-root"},
		"orphan":    {NodeId: "orphan", ParentId: "missing"},
		"cyc-a":     {NodeId: "cyc-a", ParentId: "cyc-b"},
		"cyc-b":     {NodeId: "cyc-b", ParentId: "cyc-a"},
	}

	ws, root, isFloat := FindNodeRoot(nodes, workspaces, floating, "ws-leaf")
	assert.Equal(t, "ws-1", ws)
	assert.Equal(t, "ws-root", root)
	assert.False(t, isFloat, "workspace root must outrank colliding floating window root")

	ws, root, isFloat = FindNodeRoot(nodes, workspaces, floating, "fw-cell")
	assert.Equal(t, "ws-1", ws)
	assert.Equal(t, "fw-root", root)
	assert.True(t, isFloat)
	assert.Equal(t, "ws-1", FindRootWorkspace(nodes, workspaces, floating, "fw-cell"))

	for _, id := range []string{"", "dead-root", "dead-leaf", "orphan", "cyc-a", "missing"} {
		ws, root, isFloat = FindNodeRoot(nodes, workspaces, floating, id)
		assert.Empty(t, ws, "node %q", id)
		assert.Empty(t, root, "node %q", id)
		assert.False(t, isFloat, "node %q", id)
		assert.Empty(t, FindRootWorkspace(nodes, workspaces, floating, id), "node %q", id)
	}
}

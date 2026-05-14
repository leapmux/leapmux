package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TestCallingTabID_ReadsEnvAndTrims verifies the spawn anchor read
// path: empty env yields "", a populated env yields the trimmed value
// (trailing newline / whitespace from shell substitutions must not
// foil the equality comparisons the guards do later).
func TestCallingTabID_ReadsEnvAndTrims(t *testing.T) {
	t.Setenv("LEAPMUX_REMOTE_TAB_ID", "")
	assert.Equal(t, "", callingTabID(), "empty env must yield empty anchor")

	t.Setenv("LEAPMUX_REMOTE_TAB_ID", "  tab-123\n")
	assert.Equal(t, "tab-123", callingTabID(), "anchor must be trimmed")
}

// TestCallingTileFromState verifies the CRDT-state lookup path: an
// unset anchor, a missing tab, a tombstoned tab, and a live tab all
// behave as documented. The live case is the only one that returns a
// non-empty tile id; the rest fail open so guards don't block real
// operations on stale anchors.
func TestCallingTileFromState(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		node("root-1", "").
		node("tile-A", "root-1").
		tab("live-tab", "tile-A", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		tombstonedTab("dead-tab", "tile-A", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st

	t.Run("no anchor -> empty", func(t *testing.T) {
		t.Setenv("LEAPMUX_REMOTE_TAB_ID", "")
		assert.Equal(t, "", callingTileFromState(state))
	})

	t.Run("nil state -> empty", func(t *testing.T) {
		t.Setenv("LEAPMUX_REMOTE_TAB_ID", "live-tab")
		assert.Equal(t, "", callingTileFromState(nil))
	})

	t.Run("anchor not in state -> empty", func(t *testing.T) {
		t.Setenv("LEAPMUX_REMOTE_TAB_ID", "ghost")
		assert.Equal(t, "", callingTileFromState(state))
	})

	t.Run("tombstoned anchor -> empty", func(t *testing.T) {
		t.Setenv("LEAPMUX_REMOTE_TAB_ID", "dead-tab")
		assert.Equal(t, "", callingTileFromState(state))
	})

	t.Run("live anchor -> tile id", func(t *testing.T) {
		t.Setenv("LEAPMUX_REMOTE_TAB_ID", "live-tab")
		assert.Equal(t, "tile-A", callingTileFromState(state))
	})
}

// TestRejectIfSelfWorkspace covers the pure-decision core of
// guardWorkspaceDelete. The guard's RPC half is exercised indirectly
// via locateCallingTab; this test pins the comparison logic without
// needing a hub client.
func TestRejectIfSelfWorkspace(t *testing.T) {
	t.Run("no anchor -> proceed", func(t *testing.T) {
		assert.NoError(t, rejectIfSelfWorkspace("", "ws-1"))
	})
	t.Run("anchor in different workspace -> proceed", func(t *testing.T) {
		assert.NoError(t, rejectIfSelfWorkspace("ws-other", "ws-1"))
	})
	t.Run("empty target -> proceed (resolver would have failed earlier)", func(t *testing.T) {
		assert.NoError(t, rejectIfSelfWorkspace("ws-1", ""))
	})
	t.Run("anchor in target workspace -> refuse", func(t *testing.T) {
		code, msg := captureEmit(t, func() error {
			return rejectIfSelfWorkspace("ws-1", "ws-1")
		})
		assert.Equal(t, "self_target_refused", code)
		assert.Contains(t, msg, "ws-1")
		assert.Contains(t, msg, "--force")
	})
}

// TestGuardTabClose covers the four cases for `tab close`: no anchor,
// anchor on a different tab, anchor on the target tab without --force
// (refuse), and anchor on the target tab with --force (proceed).
func TestGuardTabClose(t *testing.T) {
	t.Run("no anchor -> proceed", func(t *testing.T) {
		t.Setenv("LEAPMUX_REMOTE_TAB_ID", "")
		assert.NoError(t, guardTabClose("tab-1", false))
	})
	t.Run("different tab -> proceed", func(t *testing.T) {
		t.Setenv("LEAPMUX_REMOTE_TAB_ID", "tab-self")
		assert.NoError(t, guardTabClose("tab-other", false))
	})
	t.Run("self target without force -> refuse", func(t *testing.T) {
		t.Setenv("LEAPMUX_REMOTE_TAB_ID", "tab-self")
		code, msg := captureEmit(t, func() error {
			return guardTabClose("tab-self", false)
		})
		assert.Equal(t, "self_target_refused", code)
		assert.Contains(t, msg, "tab-self")
		assert.Contains(t, msg, "--force")
	})
	t.Run("self target with force -> proceed", func(t *testing.T) {
		t.Setenv("LEAPMUX_REMOTE_TAB_ID", "tab-self")
		assert.NoError(t, guardTabClose("tab-self", true))
	})
	t.Run("empty target -> proceed", func(t *testing.T) {
		t.Setenv("LEAPMUX_REMOTE_TAB_ID", "tab-self")
		assert.NoError(t, guardTabClose("", false))
	})
}

// TestGuardTileClose mirrors TestGuardTabClose for `tile close`: only
// rejects when the calling tab sits on the exact target tile.
func TestGuardTileClose(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		node("root-1", "").
		node("tile-A", "root-1").
		node("tile-B", "root-1").
		tab("tab-self", "tile-A", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		st

	t.Run("no anchor -> proceed", func(t *testing.T) {
		t.Setenv("LEAPMUX_REMOTE_TAB_ID", "")
		assert.NoError(t, guardTileClose(state, "tile-A", false))
	})
	t.Run("anchor on different tile -> proceed", func(t *testing.T) {
		t.Setenv("LEAPMUX_REMOTE_TAB_ID", "tab-self")
		assert.NoError(t, guardTileClose(state, "tile-B", false))
	})
	t.Run("anchor on target tile without force -> refuse", func(t *testing.T) {
		t.Setenv("LEAPMUX_REMOTE_TAB_ID", "tab-self")
		code, msg := captureEmit(t, func() error {
			return guardTileClose(state, "tile-A", false)
		})
		assert.Equal(t, "self_target_refused", code)
		assert.Contains(t, msg, "tile-A")
		assert.Contains(t, msg, "--force")
	})
	t.Run("anchor on target tile with force -> proceed", func(t *testing.T) {
		t.Setenv("LEAPMUX_REMOTE_TAB_ID", "tab-self")
		assert.NoError(t, guardTileClose(state, "tile-A", true))
	})
}

// TestGuardTileRemoveGrid covers the grid-subtree case: refuse when
// the calling tab is on any tile in the doomed set (grid + every
// descendant), proceed otherwise, --force overrides.
func TestGuardTileRemoveGrid(t *testing.T) {
	// grid-1 has two child leaves; tab-self sits on the second child.
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		node("root-1", "").
		node("grid-1", "root-1").
		node("cell-A", "grid-1").
		node("cell-B", "grid-1").
		node("tile-outside", "root-1").
		tab("tab-self", "cell-B", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		st

	gridDoomed := []string{"cell-A", "cell-B", "grid-1"} // mirrors descendantsLeavesFirst output
	otherDoomed := []string{"tile-outside"}              // grid sibling, unrelated to the anchor
	emptyDoomed := []string{}

	t.Run("no anchor -> proceed", func(t *testing.T) {
		t.Setenv("LEAPMUX_REMOTE_TAB_ID", "")
		assert.NoError(t, guardTileRemoveGrid(state, gridDoomed, false))
	})
	t.Run("anchor outside the doomed subtree -> proceed", func(t *testing.T) {
		t.Setenv("LEAPMUX_REMOTE_TAB_ID", "tab-self")
		assert.NoError(t, guardTileRemoveGrid(state, otherDoomed, false))
	})
	t.Run("anchor inside the doomed subtree -> refuse", func(t *testing.T) {
		t.Setenv("LEAPMUX_REMOTE_TAB_ID", "tab-self")
		code, msg := captureEmit(t, func() error {
			return guardTileRemoveGrid(state, gridDoomed, false)
		})
		assert.Equal(t, "self_target_refused", code)
		assert.Contains(t, msg, "cell-B")
		assert.Contains(t, msg, "--force")
	})
	t.Run("anchor inside doomed subtree with force -> proceed", func(t *testing.T) {
		t.Setenv("LEAPMUX_REMOTE_TAB_ID", "tab-self")
		assert.NoError(t, guardTileRemoveGrid(state, gridDoomed, true))
	})
	t.Run("empty doomed set -> proceed", func(t *testing.T) {
		t.Setenv("LEAPMUX_REMOTE_TAB_ID", "tab-self")
		assert.NoError(t, guardTileRemoveGrid(state, emptyDoomed, false))
	})
}

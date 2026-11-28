package cmd

import (
	"fmt"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// TestCallingTabID_ReadsEnvAndTrims verifies the spawn anchor read
// path: empty env yields "", a populated env yields the trimmed value
// (trailing newline / whitespace from shell substitutions must not
// foil the equality comparisons the guards do later).
func TestCallingTabID_ReadsEnvAndTrims(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_TAB_ID", "")
	assert.Equal(t, "", callingTabID(), "empty env must yield empty anchor")

	t.Setenv("LEAPMUX_CONTROL_TAB_ID", "  tab-123\n")
	assert.Equal(t, "tab-123", callingTabID(), "anchor must be trimmed")
}

// TestRejectIfSelfWorkspace covers the workspace comparison without a Hub.
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
		env := failureOf(t, rejectIfSelfWorkspace("ws-1", "ws-1"))
		assert.Equal(t, "self_target_refused", env.Error["code"])
		assert.Contains(t, env.Error["message"], "ws-1")
		assert.Contains(t, env.Error["hints"], "--allow-self", "the refusal states the flag that overrides it")
	})
}

func TestGuardWorkspaceDeleteDistinguishesMissingTabFromFailedLookup(t *testing.T) {
	const workspaceID = "ws-1"
	for _, tc := range []struct {
		name     string
		code     connect.Code
		wantCode cli.Code
	}{
		{name: "confirmed absent", code: connect.CodeNotFound},
		{name: "lookup unavailable", code: connect.CodeUnavailable, wantCode: cli.CodeUnavailable},
		{name: "lookup forbidden", code: connect.CodePermissionDenied, wantCode: cli.CodePermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := &recordingHub{locateTabErr: connect.NewError(tc.code, "lookup result")}
			startSpawnIPC(t, hub, &closeDispatcher{})
			t.Setenv("LEAPMUX_CONTROL_TAB_ID", "agent-1")
			c, err := control.NewClientFromEnv("")
			require.NoError(t, err)
			assert.NoError(t, guardWorkspaceDelete(t.Context(), c, workspaceID, true))
			assert.Empty(t, hub.called(), "the explicit override skips the lookup")

			err = guardWorkspaceDelete(t.Context(), c, workspaceID, false)
			assert.Equal(t, []string{leapmuxv1connect.WorkspaceServiceLocateTabProcedure}, hub.called())
			if tc.wantCode == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			reported := cli.FromError(err)
			assert.Equal(t, tc.wantCode, reported.Code)
			assert.Contains(t, reported.Message, "lookup result")
		})
	}
}

// TestGuardTabClose covers the four cases for `tab close`: no anchor,
// anchor on a different tab, anchor on the target tab without --allow-self
// (refuse), and anchor on the target tab with --allow-self (proceed).
func TestGuardTabClose(t *testing.T) {
	t.Run("no anchor -> proceed", func(t *testing.T) {
		t.Setenv("LEAPMUX_CONTROL_TAB_ID", "")
		assert.NoError(t, guardTabClose("tab-1", false))
	})
	t.Run("different tab -> proceed", func(t *testing.T) {
		t.Setenv("LEAPMUX_CONTROL_TAB_ID", "tab-self")
		assert.NoError(t, guardTabClose("tab-other", false))
	})
	t.Run("self target without --allow-self -> refuse", func(t *testing.T) {
		t.Setenv("LEAPMUX_CONTROL_TAB_ID", "tab-self")
		env := failureOf(t, guardTabClose("tab-self", false))
		assert.Equal(t, "self_target_refused", env.Error["code"])
		assert.Contains(t, env.Error["message"], "tab-self")
		assert.Contains(t, env.Error["hints"], "--allow-self", "the refusal states the flag that overrides it")
	})
	t.Run("self target with --allow-self -> proceed", func(t *testing.T) {
		t.Setenv("LEAPMUX_CONTROL_TAB_ID", "tab-self")
		assert.NoError(t, guardTabClose("tab-self", true))
	})
	t.Run("empty target -> proceed", func(t *testing.T) {
		t.Setenv("LEAPMUX_CONTROL_TAB_ID", "tab-self")
		assert.NoError(t, guardTabClose("", false))
	})
}

func TestGuardLayoutClose(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root").
		splitNode("root", "", "m").
		leafNode("left", "root", "a").
		leafNode("right", "root", "b").
		tab("tab-self", "left", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		tab("tab-other", "right", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tombstonedTab("tab-closed", "left", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	tabsInRoot := crdt.LiveTabsInSubtree(state, "root")
	for _, tc := range []struct {
		name     string
		caller   string
		tabs     []crdt.TabRef
		override bool
		refused  bool
	}{
		{name: "outside a tab", tabs: tabsInRoot},
		{name: "missing caller", caller: "ghost", tabs: tabsInRoot},
		{name: "closed caller", caller: "tab-closed", tabs: tabsInRoot},
		{name: "caller on another tile", caller: "tab-self", tabs: crdt.TabsOnTile(state, "right")},
		{name: "empty close set", caller: "tab-self"},
		{name: "caller included", caller: "tab-self", tabs: tabsInRoot, refused: true},
		{name: "caller included with override", caller: "tab-self", tabs: tabsInRoot, override: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LEAPMUX_CONTROL_TAB_ID", tc.caller)
			err := guardLayoutClose("grid root", tc.tabs, tc.override)
			if !tc.refused {
				require.NoError(t, err)
				return
			}
			reported := failureOf(t, err)
			assert.Equal(t, "self_target_refused", reported.Error["code"])
			assert.Contains(t, reported.Error["message"], "grid root")
			assert.Contains(t, reported.Error["message"], tc.caller)
			assert.Contains(t, reported.Error["hints"], "--allow-self")
		})
	}
}

func TestGuardWorkspaceDelete_UsesHubClientURLInAuthHint(t *testing.T) {
	hub := &recordingHub{
		locateTabErr: connect.NewError(connect.CodeUnauthenticated, "expired session"),
	}
	h := startHubChannelHarness(t, hub, &closeDispatcher{})
	t.Setenv("LEAPMUX_CONTROL_TAB_ID", "agent-1")

	client, err := control.NewClientForHub(h.url)
	require.NoError(t, err)
	client.SetErrorDecorator(decoratePeerError)
	err = guardWorkspaceDelete(t.Context(), client, "ws-1", false)
	require.Error(t, err)
	ce := cli.FromError(err)
	assert.Equal(t, cli.CodeNotLoggedIn, ce.Code)
	wantHint := fmt.Sprintf(`run "leapmux auth login --hub %s" to sign in again`, client.HubURL)
	assert.Equal(t, 1, countStrings(ce.Hints, wantHint), "auth login hint must appear once without duplication")
	assert.Contains(t, ce.Hints, "pass --allow-self to delete the workspace even if the command cannot verify the calling tab's location")
}

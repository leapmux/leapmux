package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TestTabTypeName_AllValues pins the lowercase short labels the CLI
// emits in JSON envelopes. The strings have to match the --type flag
// values that `tab open` accepts (parseTabType in tab.go) and the
// $LEAPMUX_REMOTE_TAB_TYPE env var the resolver reads, so a script
// that reads a tab_id from one envelope and feeds it back as a flag
// doesn't need a translation table.
func TestTabTypeName_AllValues(t *testing.T) {
	cases := map[leapmuxv1.TabType]string{
		leapmuxv1.TabType_TAB_TYPE_AGENT:       "agent",
		leapmuxv1.TabType_TAB_TYPE_TERMINAL:    "terminal",
		leapmuxv1.TabType_TAB_TYPE_FILE:        "file",
		leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED: "",
	}
	for in, want := range cases {
		assert.Equal(t, want, tabTypeName(in), "tabTypeName(%v)", in)
	}
}

// TestAgentProviderName_UsesDisplayName guards the alignment between
// the CLI's envelope and the frontend's picker labels. agentlabels.
// DisplayName already owns the canonical strings ("Claude Code",
// "Codex", ...); a regression that diverged would surface as a UX
// inconsistency between the CLI and the UI.
func TestAgentProviderName_UsesDisplayName(t *testing.T) {
	cases := map[leapmuxv1.AgentProvider]string{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE:    "Claude Code",
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX:          "Codex",
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI:     "Gemini CLI",
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR:         "Cursor",
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT: "GitHub Copilot",
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO:           "Kilo",
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE:       "OpenCode",
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE:          "Goose",
		leapmuxv1.AgentProvider_AGENT_PROVIDER_PI:             "Pi",
		leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED:    "",
	}
	for in, want := range cases {
		assert.Equal(t, want, agentProviderName(in), "agentProviderName(%v)", in)
	}
}

// TestAgentStatusName_AllValues pins the human-readable strings for
// the agent lifecycle. AGENT_STATUS_UNSPECIFIED returns "" rather
// than a literal "unspecified" so the JSON envelope simply omits the
// field via the surrounding map's empty-string handling.
func TestAgentStatusName_AllValues(t *testing.T) {
	cases := map[leapmuxv1.AgentStatus]string{
		leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE:         "active",
		leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE:       "inactive",
		leapmuxv1.AgentStatus_AGENT_STATUS_STARTING:       "starting",
		leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED: "startup_failed",
		leapmuxv1.AgentStatus_AGENT_STATUS_UNSPECIFIED:    "",
	}
	for in, want := range cases {
		assert.Equal(t, want, agentStatusName(in), "agentStatusName(%v)", in)
	}
}

// TestTerminalStatusName_AllValues mirrors TestAgentStatusName for
// terminals. Five live values + UNSPECIFIED.
func TestTerminalStatusName_AllValues(t *testing.T) {
	cases := map[leapmuxv1.TerminalStatus]string{
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING:       "starting",
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY:          "ready",
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED: "startup_failed",
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_DISCONNECTED:   "disconnected",
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED:         "exited",
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_UNSPECIFIED:    "",
	}
	for in, want := range cases {
		assert.Equal(t, want, terminalStatusName(in), "terminalStatusName(%v)", in)
	}
}

// TestWorkspaceTabToMap_RendersEnumAsString pins the projection used
// by `tab list` / `tab get`. The enum field has to surface as the
// short string label, and the other fields must round-trip unchanged.
func TestWorkspaceTabToMap_RendersEnumAsString(t *testing.T) {
	in := &leapmuxv1.WorkspaceTab{
		TabId:       "tab-1",
		TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
		Position:    "n",
		TileId:      "tile-1",
		WorkerId:    "worker-A",
		WorkspaceId: "ws-1",
	}
	got := workspaceTabToMap(in)
	assert.Equal(t, "agent", got["tab_type"], "tab_type must be a string, not an int ordinal")
	assert.Equal(t, "tab-1", got["tab_id"])
	assert.Equal(t, "n", got["position"])
	assert.Equal(t, "tile-1", got["tile_id"])
	assert.Equal(t, "worker-A", got["worker_id"])
	assert.Equal(t, "ws-1", got["workspace_id"])
}

// TestWorkspaceTabsToList_EmptyInputYieldsEmptySlice pins that an
// empty / nil input renders as `[]` rather than `null` in JSON, so
// scripts that iterate `.data.tabs` don't choke on a null when a
// workspace has zero tabs of the requested type.
func TestWorkspaceTabsToList_EmptyInputYieldsEmptySlice(t *testing.T) {
	got := workspaceTabsToList(nil)
	assert.NotNil(t, got)
	assert.Empty(t, got)

	got = workspaceTabsToList([]*leapmuxv1.WorkspaceTab{})
	assert.NotNil(t, got)
	assert.Empty(t, got)
}

// TestAgentInfoToMap_RendersEnumsAsStrings pins status and
// agent_provider projection. The other fields go straight through so
// we only spot-check a couple to confirm the projection isn't
// dropping anything unexpected.
func TestAgentInfoToMap_RendersEnumsAsStrings(t *testing.T) {
	in := &leapmuxv1.AgentInfo{
		Id:            "ag-1",
		Title:         "demo",
		Model:         "claude-sonnet-4",
		Status:        leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		WorkerId:      "worker-A",
	}
	got := agentInfoToMap(in)
	assert.Equal(t, "active", got["status"])
	assert.Equal(t, "Claude Code", got["agent_provider"])
	assert.Equal(t, "ag-1", got["id"])
	assert.Equal(t, "demo", got["title"])
	assert.Equal(t, "worker-A", got["worker_id"])
}

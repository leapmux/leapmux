package main

import (
	"io"

	cmdremote "github.com/leapmux/leapmux/internal/cli/remote/cmd"
)

// remoteCmdCtxAdapter exposes adminCmdCtx through the cmd subpackage's
// Ctx interface. Decoupled from the main package's adminCmdCtx to avoid
// an import cycle.
type remoteCmdCtxAdapter struct {
	PathStr        string
	DescriptionStr string
}

func (a remoteCmdCtxAdapter) Path() string        { return a.PathStr }
func (a remoteCmdCtxAdapter) Description() string { return a.DescriptionStr }

// remoteRun bridges the cmd subpackage's signature to the admin
// dispatcher's `func(adminCmdCtx, []string) error`.
func remoteRun(fn func(any, []string) error) func(adminCmdCtx, []string) error {
	return func(c adminCmdCtx, args []string) error {
		return fn(remoteCmdCtxAdapter{PathStr: c.Path, DescriptionStr: c.Description}, args)
	}
}

// remoteTree is the command tree under `leapmux remote`.
var remoteTree = adminGroup{
	Name:    "remote",
	Summary: "Remotely control LeapMux from a script or another LeapMux agent",
	Subgroups: []adminGroup{
		{
			Name:    "auth",
			Summary: "Manage hub credentials",
			Commands: []adminCommand{
				{Name: "login", Summary: "Authorize this CLI against a hub", Run: remoteRun(cmdremote.RunAuthLogin)},
				{Name: "logout", Summary: "Revoke + remove local credentials", Run: remoteRun(cmdremote.RunAuthLogout)},
				{Name: "list", Summary: "List configured hubs", Run: remoteRun(cmdremote.RunAuthList)},
				{Name: "status", Summary: "Show user, expiry, scope for the active hub", Run: remoteRun(cmdremote.RunAuthStatus)},
			},
		},
		{
			Name:    "workspace",
			Summary: "Workspace management",
			Commands: []adminCommand{
				{Name: "list", Summary: "List workspaces", Run: remoteRun(cmdremote.RunWorkspaceList)},
				{Name: "get", Summary: "Show one workspace", Run: remoteRun(cmdremote.RunWorkspaceGet)},
				{Name: "create", Summary: "Create a workspace", Run: remoteRun(cmdremote.RunWorkspaceCreate)},
				{Name: "rename", Summary: "Rename a workspace", Run: remoteRun(cmdremote.RunWorkspaceRename)},
				{Name: "delete", Summary: "Delete a workspace", Run: remoteRun(cmdremote.RunWorkspaceDelete)},
			},
		},
		{
			Name:    "tab",
			Summary: "Tab management (generic open/close/list/rename across agent / terminal / file)",
			Commands: []adminCommand{
				{Name: "list", Summary: "List tabs in a workspace", Run: remoteRun(cmdremote.RunTabList)},
				{Name: "get", Summary: "Show one tab", Run: remoteRun(cmdremote.RunTabGet)},
				{Name: "open", Summary: "Open a new tab (--type agent|terminal|file)", Run: remoteRun(cmdremote.RunTabOpen)},
				{Name: "close", Summary: "Close a tab (worker close + hub tombstone)", Run: remoteRun(cmdremote.RunTabClose)},
				{Name: "rename", Summary: "Rename an agent or terminal tab", Run: remoteRun(cmdremote.RunTabRename)},
				{Name: "move", Summary: "Move a tab to a different tile or workspace", Run: remoteRun(cmdremote.RunTabMove)},
			},
		},
		{
			Name:    "worker",
			Summary: "Worker management",
			Commands: []adminCommand{
				{Name: "list", Summary: "List accessible workers", Run: remoteRun(cmdremote.RunWorkerList)},
				{Name: "get", Summary: "Show metadata for one worker", Run: remoteRun(cmdremote.RunWorkerGet)},
			},
			Subgroups: []adminGroup{
				{
					Name:    "pins",
					Summary: "Manage TOFU worker key pins",
					Commands: []adminCommand{
						{Name: "list", Summary: "List every pinned worker", Run: remoteRun(cmdremote.RunWorkerPinsList)},
						{Name: "show", Summary: "Show one recorded pin (--worker-id)", Run: remoteRun(cmdremote.RunWorkerPinsShow)},
						{Name: "remove", Summary: "Remove a pin so the next connect re-prompts (--worker-id)", Run: remoteRun(cmdremote.RunWorkerPinsRemove)},
					},
				},
			},
		},
		{
			Name:    "agent",
			Summary: "Agent-specific operations (use `tab open/close/list/rename` for the generic surface)",
			Commands: []adminCommand{
				{Name: "send", Summary: "Send a user message to an agent", Run: remoteRun(cmdremote.RunAgentSend)},
				{Name: "interrupt", Summary: "Abort an agent's current turn", Run: remoteRun(cmdremote.RunAgentInterrupt)},
				{Name: "get", Summary: "Show one agent (settings, status, available models)", Run: remoteRun(cmdremote.RunAgentGet)},
				{Name: "providers", Summary: "List available providers on the resolved worker", Run: remoteRun(cmdremote.RunAgentProviders)},
				{Name: "messages", Summary: "Page or follow an agent's message log", Run: remoteRun(cmdremote.RunAgentMessages)},
				{Name: "set", Summary: "Update agent settings (model/effort/permission-mode/extras)", Run: remoteRun(cmdremote.RunAgentSet)},
				{Name: "send-control-response", Summary: "Forward a raw control_response payload (Claude-Code-style)", Run: remoteRun(cmdremote.RunAgentSendControlResponse)},
			},
		},
		{
			Name:    "tile",
			Summary: "Tile-tree mutations within a workspace layout",
			Commands: []adminCommand{
				{Name: "list", Summary: "List leaf tiles with their parent path", Run: remoteRun(cmdremote.RunTileList)},
				{Name: "split", Summary: "Split a tile horizontally or vertically", Run: remoteRun(cmdremote.RunTileSplit)},
				{Name: "close", Summary: "Close a tile and collapse its parent", Run: remoteRun(cmdremote.RunTileClose)},
				{Name: "make-grid", Summary: "Convert a leaf tile into a grid", Run: remoteRun(cmdremote.RunTileMakeGrid)},
				{Name: "remove-grid", Summary: "Remove a grid (destroy subtree, or with --with-tabs=move collapse it back to a single tile)", Run: remoteRun(cmdremote.RunTileRemoveGrid)},
				{Name: "set-ratios", Summary: "Update the ratios on a SPLIT node", Run: remoteRun(cmdremote.RunTileSetRatios)},
				{Name: "set-grid-ratios", Summary: "Update row and/or column ratios on a GRID node", Run: remoteRun(cmdremote.RunTileSetGridRatios)},
			},
		},
		{
			Name:    "layout",
			Summary: "Workspace layout snapshot",
			Commands: []adminCommand{
				{Name: "get", Summary: "Print the current layout snapshot as JSON", Run: remoteRun(cmdremote.RunLayoutGet)},
				{Name: "set", Summary: "Replace the layout (--file PATH or --stdin)", Run: remoteRun(cmdremote.RunLayoutSet)},
			},
		},
		{
			Name:    "file",
			Summary: "Filesystem inspection on a worker",
			Commands: []adminCommand{
				{Name: "list", Summary: "List a directory", Run: remoteRun(cmdremote.RunFileList)},
				{Name: "read", Summary: "Read a file (with optional --offset/--limit)", Run: remoteRun(cmdremote.RunFileRead)},
				{Name: "stat", Summary: "Stat a path", Run: remoteRun(cmdremote.RunFileStat)},
			},
		},
		{
			Name:    "git",
			Summary: "Git inspection on a worker",
			Commands: []adminCommand{
				{Name: "status", Summary: "Show git info + per-file change list", Run: remoteRun(cmdremote.RunGitStatus)},
				{Name: "branches", Summary: "List git branches", Run: remoteRun(cmdremote.RunGitBranches)},
				{Name: "worktrees", Summary: "List git worktrees", Run: remoteRun(cmdremote.RunGitWorktrees)},
				{Name: "read", Summary: "Read a file at HEAD or in the index", Run: remoteRun(cmdremote.RunGitRead)},
			},
		},
		{
			Name:    "terminal",
			Summary: "Terminal-specific operations (use `tab open/close/list/rename` for the generic surface)",
			Commands: []adminCommand{
				{Name: "send", Summary: "Send input to a terminal", Run: remoteRun(cmdremote.RunTerminalSend)},
				{Name: "get", Summary: "Show one terminal (geometry, shell, working dir)", Run: remoteRun(cmdremote.RunTerminalGet)},
				{Name: "shells", Summary: "List available shells on a worker", Run: remoteRun(cmdremote.RunTerminalShells)},
			},
		},
		{
			Name:    "events",
			Summary: "Stream workspace events as JSON-lines",
			Commands: []adminCommand{
				{Name: "watch", Summary: "Subscribe to a workspace's event stream", Run: remoteRun(cmdremote.RunEvents)},
			},
		},
	},
	Commands: []adminCommand{
		{Name: "whoami", Summary: "Show resolved identity for this CLI", Run: remoteRun(cmdremote.RunWhoami)},
		{Name: "version", Summary: "Print CLI build version (and the --hub's version when set)", Run: remoteRun(cmdremote.RunVersion)},
	},
}

// handleRemoteArgs walks remoteTree to validate args before dispatch.
// Returns (code, true) when help/error fully handled the request.
func handleRemoteArgs(args []string, stdout, stderr io.Writer) (int, bool) {
	return walkAdminArgs(remoteTree, []string{"remote"}, args, stdout, stderr)
}

func runRemote(args []string) error {
	return dispatchAdminGroup(remoteTree, args, []string{"remote"})
}

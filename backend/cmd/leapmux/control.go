package main

import (
	"io"

	cmdcontrol "github.com/leapmux/leapmux/internal/cli/control/cmd"
)

// controlCmdCtxAdapter exposes adminCmdCtx through the cmd subpackage's
// Ctx interface. Decoupled from the main package's adminCmdCtx to avoid
// an import cycle.
type controlCmdCtxAdapter struct {
	PathStr        string
	DescriptionStr string
}

func (a controlCmdCtxAdapter) Path() string        { return a.PathStr }
func (a controlCmdCtxAdapter) Description() string { return a.DescriptionStr }

// controlRun bridges the cmd subpackage's signature to the admin
// dispatcher's `func(adminCmdCtx, []string) error`.
func controlRun(fn func(any, []string) error) func(adminCmdCtx, []string) error {
	return func(c adminCmdCtx, args []string) error {
		return fn(controlCmdCtxAdapter{PathStr: c.Path, DescriptionStr: c.Description}, args)
	}
}

// controlTree is the command tree under `leapmux control`.
var controlTree = adminGroup{
	Name:    "control",
	Summary: "Remotely control LeapMux from a script or another LeapMux agent",
	Subgroups: []adminGroup{
		{
			Name:    "auth",
			Summary: "Manage hub credentials",
			Commands: []adminCommand{
				{Name: "login", Summary: "Authorize this CLI against a hub", Run: controlRun(cmdcontrol.RunAuthLogin)},
				{Name: "logout", Summary: "Revoke + remove local credentials", Run: controlRun(cmdcontrol.RunAuthLogout)},
				{Name: "list", Summary: "List configured hubs", Run: controlRun(cmdcontrol.RunAuthList)},
				{Name: "status", Summary: "Show user, expiry, scope for the active hub", Run: controlRun(cmdcontrol.RunAuthStatus)},
			},
		},
		{
			Name:    "workspace",
			Summary: "Workspace management",
			Commands: []adminCommand{
				{Name: "list", Summary: "List workspaces", Run: controlRun(cmdcontrol.RunWorkspaceList)},
				{Name: "get", Summary: "Show one workspace", Run: controlRun(cmdcontrol.RunWorkspaceGet)},
				{Name: "create", Summary: "Create a workspace", Run: controlRun(cmdcontrol.RunWorkspaceCreate)},
				{Name: "rename", Summary: "Rename a workspace", Run: controlRun(cmdcontrol.RunWorkspaceRename)},
				{Name: "delete", Summary: "Delete a workspace", Run: controlRun(cmdcontrol.RunWorkspaceDelete)},
			},
		},
		{
			Name:    "tab",
			Summary: "Tab management (generic open/close/list/rename across agent / terminal / file)",
			Commands: []adminCommand{
				{Name: "list", Summary: "List tabs in a workspace", Run: controlRun(cmdcontrol.RunTabList)},
				{Name: "get", Summary: "Show one tab", Run: controlRun(cmdcontrol.RunTabGet)},
				{Name: "open", Summary: "Open a new tab (--type agent|terminal|file)", Run: controlRun(cmdcontrol.RunTabOpen)},
				{Name: "close", Summary: "Close a tab (worker close + hub tombstone)", Run: controlRun(cmdcontrol.RunTabClose)},
				{Name: "rename", Summary: "Rename an agent or terminal tab", Run: controlRun(cmdcontrol.RunTabRename)},
				{Name: "move", Summary: "Move a tab to a different tile or workspace", Run: controlRun(cmdcontrol.RunTabMove)},
			},
		},
		{
			Name:    "worker",
			Summary: "Worker management",
			Commands: []adminCommand{
				{Name: "list", Summary: "List accessible workers", Run: controlRun(cmdcontrol.RunWorkerList)},
				{Name: "get", Summary: "Show metadata for one worker", Run: controlRun(cmdcontrol.RunWorkerGet)},
			},
			Subgroups: []adminGroup{
				{
					Name:    "pins",
					Summary: "Manage TOFU worker key pins",
					Commands: []adminCommand{
						{Name: "list", Summary: "List every pinned worker", Run: controlRun(cmdcontrol.RunWorkerPinsList)},
						{Name: "show", Summary: "Show one recorded pin (--worker-id)", Run: controlRun(cmdcontrol.RunWorkerPinsShow)},
						{Name: "remove", Summary: "Remove a pin so the next connect re-prompts (--worker-id)", Run: controlRun(cmdcontrol.RunWorkerPinsRemove)},
					},
				},
			},
		},
		{
			Name:    "agent",
			Summary: "Agent-specific operations (use `tab open/close/list/rename` for the generic surface)",
			Commands: []adminCommand{
				{Name: "send", Summary: "Send a user message to an agent", Run: controlRun(cmdcontrol.RunAgentSend)},
				{Name: "interrupt", Summary: "Abort an agent's current turn", Run: controlRun(cmdcontrol.RunAgentInterrupt)},
				{Name: "get", Summary: "Show one agent (settings, status, available models)", Run: controlRun(cmdcontrol.RunAgentGet)},
				{Name: "providers", Summary: "List available providers on the resolved worker", Run: controlRun(cmdcontrol.RunAgentProviders)},
				{Name: "messages", Summary: "Page or follow an agent's message log", Run: controlRun(cmdcontrol.RunAgentMessages)},
				{Name: "set", Summary: "Update agent settings (model/effort/permission-mode/extras)", Run: controlRun(cmdcontrol.RunAgentSet)},
				{Name: "send-control-response", Summary: "Forward a raw control_response payload (Claude-Code-style)", Run: controlRun(cmdcontrol.RunAgentSendControlResponse)},
			},
		},
		{
			Name:    "tile",
			Summary: "Tile-tree mutations within a workspace layout",
			Commands: []adminCommand{
				{Name: "list", Summary: "List leaf tiles with their parent path", Run: controlRun(cmdcontrol.RunTileList)},
				{Name: "split", Summary: "Split a tile horizontally or vertically", Run: controlRun(cmdcontrol.RunTileSplit)},
				{Name: "close", Summary: "Close a tile and collapse its parent", Run: controlRun(cmdcontrol.RunTileClose)},
				{Name: "make-grid", Summary: "Convert a leaf tile into a grid", Run: controlRun(cmdcontrol.RunTileMakeGrid)},
				{Name: "remove-grid", Summary: "Remove a grid (destroy subtree, or with --with-tabs=move collapse it back to a single tile)", Run: controlRun(cmdcontrol.RunTileRemoveGrid)},
				{Name: "set-ratios", Summary: "Update the ratios on a SPLIT node", Run: controlRun(cmdcontrol.RunTileSetRatios)},
				{Name: "set-grid-ratios", Summary: "Update row and/or column ratios on a GRID node", Run: controlRun(cmdcontrol.RunTileSetGridRatios)},
			},
		},
		{
			Name:    "layout",
			Summary: "Workspace layout snapshot",
			Commands: []adminCommand{
				{Name: "get", Summary: "Print the current layout snapshot as JSON", Run: controlRun(cmdcontrol.RunLayoutGet)},
				{Name: "set", Summary: "Replace the layout (--file PATH or --stdin)", Run: controlRun(cmdcontrol.RunLayoutSet)},
			},
		},
		{
			Name:    "file",
			Summary: "Filesystem inspection on a worker",
			Commands: []adminCommand{
				{Name: "list", Summary: "List a directory", Run: controlRun(cmdcontrol.RunFileList)},
				{Name: "read", Summary: "Read a file (with optional --offset/--limit)", Run: controlRun(cmdcontrol.RunFileRead)},
				{Name: "stat", Summary: "Stat a path", Run: controlRun(cmdcontrol.RunFileStat)},
			},
		},
		{
			Name:    "git",
			Summary: "Git inspection on a worker",
			Commands: []adminCommand{
				{Name: "status", Summary: "Show git info + per-file change list", Run: controlRun(cmdcontrol.RunGitStatus)},
				{Name: "branches", Summary: "List git branches", Run: controlRun(cmdcontrol.RunGitBranches)},
				{Name: "worktrees", Summary: "List git worktrees", Run: controlRun(cmdcontrol.RunGitWorktrees)},
				{Name: "read", Summary: "Read a file at HEAD or in the index", Run: controlRun(cmdcontrol.RunGitRead)},
			},
		},
		{
			Name:    "terminal",
			Summary: "Terminal-specific operations (use `tab open/close/list/rename` for the generic surface)",
			Commands: []adminCommand{
				{Name: "send", Summary: "Send input to a terminal", Run: controlRun(cmdcontrol.RunTerminalSend)},
				{Name: "get", Summary: "Show one terminal (geometry, shell, working dir)", Run: controlRun(cmdcontrol.RunTerminalGet)},
				{Name: "shells", Summary: "List available shells on a worker", Run: controlRun(cmdcontrol.RunTerminalShells)},
			},
		},
		{
			Name:    "events",
			Summary: "Stream workspace events as JSON-lines",
			Commands: []adminCommand{
				{Name: "watch", Summary: "Subscribe to a workspace's event stream", Run: controlRun(cmdcontrol.RunEvents)},
			},
		},
	},
	Commands: []adminCommand{
		{Name: "whoami", Summary: "Show resolved identity for this CLI", Run: controlRun(cmdcontrol.RunWhoami)},
		{Name: "version", Summary: "Print CLI build version (and the --hub's version when set)", Run: controlRun(cmdcontrol.RunVersion)},
	},
}

// handleControlArgs walks controlTree to validate args before dispatch.
// Returns (code, true) when help/error fully handled the request.
func handleControlArgs(args []string, stdout, stderr io.Writer) (int, bool) {
	return walkAdminArgs(controlTree, []string{"control"}, args, stdout, stderr)
}

func runControl(args []string) error {
	return dispatchAdminGroup(controlTree, args, []string{"control"})
}

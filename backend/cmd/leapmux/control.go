package main

import (
	"io"

	cmdcontrol "github.com/leapmux/leapmux/internal/cli/control/cmd"
)

// controlCmdCtxAdapter exposes cmdCtx through the cmd subpackage's
// Ctx interface. Decoupled from the main package's cmdCtx to avoid
// an import cycle.
type controlCmdCtxAdapter struct {
	PathStr        string
	DescriptionStr string
}

func (a controlCmdCtxAdapter) Path() string        { return a.PathStr }
func (a controlCmdCtxAdapter) Description() string { return a.DescriptionStr }

// controlRun bridges the cmd subpackage's signature to the tree
// dispatcher's `func(cmdCtx, []string) error`.
func controlRun(fn func(any, []string) error) func(cmdCtx, []string) error {
	return func(c cmdCtx, args []string) error {
		return fn(controlCmdCtxAdapter{PathStr: c.Path, DescriptionStr: c.Description}, args)
	}
}

// controlTree is the command tree under `leapmux control`.
var controlTree = cmdGroup{
	Name:    "control",
	Summary: "Remotely control LeapMux from a script or another LeapMux agent",
	Subgroups: []cmdGroup{
		{
			Name:    "auth",
			Summary: "Manage hub credentials",
			Commands: []cmdLeaf{
				{Name: "login", Summary: "Authorize this CLI against a hub", Run: controlRun(cmdcontrol.RunAuthLogin)},
				{Name: "logout", Summary: "Revoke + remove local credentials", Run: controlRun(cmdcontrol.RunAuthLogout)},
				{Name: "list", Summary: "List configured hubs", Run: controlRun(cmdcontrol.RunAuthList)},
				{Name: "status", Summary: "Show user, expiry, scope for the active hub", Run: controlRun(cmdcontrol.RunAuthStatus)},
			},
		},
		{
			Name:    "workspace",
			Summary: "Workspace management",
			Commands: []cmdLeaf{
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
			Commands: []cmdLeaf{
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
			Commands: []cmdLeaf{
				{Name: "list", Summary: "List accessible workers", Run: controlRun(cmdcontrol.RunWorkerList)},
				{Name: "get", Summary: "Show metadata for one worker", Run: controlRun(cmdcontrol.RunWorkerGet)},
			},
			Subgroups: []cmdGroup{
				{
					Name:    "pins",
					Summary: "Manage TOFU worker key pins",
					Commands: []cmdLeaf{
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
			Commands: []cmdLeaf{
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
			Commands: []cmdLeaf{
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
			Commands: []cmdLeaf{
				{Name: "get", Summary: "Print the current layout snapshot as JSON", Run: controlRun(cmdcontrol.RunLayoutGet)},
				{Name: "set", Summary: "Replace the layout (--file PATH or --stdin)", Run: controlRun(cmdcontrol.RunLayoutSet)},
			},
		},
		{
			Name:    "file",
			Summary: "Filesystem inspection on a worker",
			Commands: []cmdLeaf{
				{Name: "list", Summary: "List a directory", Run: controlRun(cmdcontrol.RunFileList)},
				{Name: "read", Summary: "Read a file (with optional --offset/--limit)", Run: controlRun(cmdcontrol.RunFileRead)},
				{Name: "stat", Summary: "Stat a path", Run: controlRun(cmdcontrol.RunFileStat)},
			},
		},
		{
			Name:    "git",
			Summary: "Git inspection on a worker",
			Commands: []cmdLeaf{
				{Name: "status", Summary: "Show git info + per-file change list", Run: controlRun(cmdcontrol.RunGitStatus)},
				{Name: "branches", Summary: "List git branches", Run: controlRun(cmdcontrol.RunGitBranches)},
				{Name: "worktrees", Summary: "List git worktrees", Run: controlRun(cmdcontrol.RunGitWorktrees)},
				{Name: "read", Summary: "Read a file at HEAD or in the index", Run: controlRun(cmdcontrol.RunGitRead)},
			},
		},
		{
			Name:    "terminal",
			Summary: "Terminal-specific operations (use `tab open/close/list/rename` for the generic surface)",
			Commands: []cmdLeaf{
				{Name: "send", Summary: "Send input to a terminal", Run: controlRun(cmdcontrol.RunTerminalSend)},
				{Name: "get", Summary: "Show one terminal (geometry, shell, working dir)", Run: controlRun(cmdcontrol.RunTerminalGet)},
				{Name: "shells", Summary: "List available shells on a worker", Run: controlRun(cmdcontrol.RunTerminalShells)},
			},
		},
		{
			Name:    "events",
			Summary: "Stream workspace events as JSON-lines",
			Commands: []cmdLeaf{
				{Name: "watch", Summary: "Subscribe to a workspace's event stream", Run: controlRun(cmdcontrol.RunEvents)},
			},
		},
		{
			// The online, authenticated face of hub administration — the
			// successor of the removed offline admin tree. These leaves
			// call the Admin*Service RPCs directly (never the worker-IPC
			// bridge); the break-glass offline subset lives in `leapmux
			// recover`.
			Name:    "admin",
			Summary: "Administer the hub over RPC (requires an admin login; offline break-glass: `leapmux recover`)",
			Subgroups: []cmdGroup{
				{
					Name:    "settings",
					Summary: "Manage the hub's DB-backed instance settings (auth policy, SMTP, timeouts, limits, captcha, rate limits)",
					Commands: []cmdLeaf{
						{Name: "list", Summary: "List every setting key with its effective value, propagation, and default marker", Run: controlRun(cmdcontrol.RunAdminSettingsList)},
						{Name: "get", Summary: "Show one key's effective value and shape", Run: controlRun(cmdcontrol.RunAdminSettingsGet)},
						{Name: "set", Summary: "Write one key's public half from a JSON document (or bare scalar)", Run: controlRun(cmdcontrol.RunAdminSettingsSet)},
						{Name: "set-secret", Summary: "Write one key's secret half (encrypted at rest)", Run: controlRun(cmdcontrol.RunAdminSettingsSetSecret)},
						{Name: "reset", Summary: "Remove one key's row, returning it to its default", Run: controlRun(cmdcontrol.RunAdminSettingsReset)},
					},
				},
				{
					Name:    "user",
					Summary: "Manage users",
					Commands: []cmdLeaf{
						{Name: "list", Summary: "List users", Run: controlRun(cmdcontrol.RunAdminUserList)},
						{Name: "get", Summary: "Get user details", Run: controlRun(cmdcontrol.RunAdminUserGet)},
						{Name: "create", Summary: "Create a new user", Run: controlRun(cmdcontrol.RunAdminUserCreate)},
						{Name: "update", Summary: "Update user fields", Run: controlRun(cmdcontrol.RunAdminUserUpdate)},
						{Name: "delete", Summary: "Delete a user", Run: controlRun(cmdcontrol.RunAdminUserDelete)},
						{Name: "grant-admin", Summary: "Grant admin privileges", Run: func(c cmdCtx, args []string) error {
							return cmdcontrol.RunAdminUserSetAdmin(controlCmdCtxAdapter{PathStr: c.Path, DescriptionStr: c.Description}, args, true)
						}},
						{Name: "revoke-admin", Summary: "Revoke admin privileges", Run: func(c cmdCtx, args []string) error {
							return cmdcontrol.RunAdminUserSetAdmin(controlCmdCtxAdapter{PathStr: c.Path, DescriptionStr: c.Description}, args, false)
						}},
						{Name: "reset-password", Summary: "Set a user's password and revoke every credential they hold", Run: controlRun(cmdcontrol.RunAdminUserResetPassword)},
						{Name: "list-sessions", Summary: "List a user's active sessions", Run: controlRun(cmdcontrol.RunAdminUserListSessions)},
					},
				},
				{
					Name:    "session",
					Summary: "Manage sessions",
					Commands: []cmdLeaf{
						{Name: "list", Summary: "List all active sessions", Run: controlRun(cmdcontrol.RunAdminSessionList)},
						{Name: "revoke", Summary: "Revoke a session by ID", Run: controlRun(cmdcontrol.RunAdminSessionRevoke)},
						{Name: "revoke-user", Summary: "Revoke all sessions for a user", Run: controlRun(cmdcontrol.RunAdminSessionRevokeUser)},
						{Name: "purge-expired", Summary: "Delete all expired sessions", Run: controlRun(cmdcontrol.RunAdminSessionPurgeExpired)},
					},
				},
				{
					Name:    "worker",
					Summary: "Cross-user worker administration",
					Commands: []cmdLeaf{
						{Name: "list", Summary: "List workers (all users)", Run: controlRun(cmdcontrol.RunAdminWorkerList)},
						{Name: "get", Summary: "Get worker details", Run: controlRun(cmdcontrol.RunAdminWorkerGet)},
						{Name: "deregister", Summary: "Deregister a worker", Run: controlRun(cmdcontrol.RunAdminWorkerDeregister)},
					},
					Subgroups: []cmdGroup{
						{
							Name:    "reg-key",
							Summary: "Manage worker registration keys",
							Commands: []cmdLeaf{
								{Name: "list", Summary: "List worker registration keys", Run: controlRun(cmdcontrol.RunAdminWorkerRegKeyList)},
								{Name: "revoke", Summary: "Revoke a registration key by ID", Run: controlRun(cmdcontrol.RunAdminWorkerRegKeyRevoke)},
								{Name: "purge-expired", Summary: "Hard-delete all expired or revoked keys", Run: controlRun(cmdcontrol.RunAdminWorkerRegKeyPurgeExpired)},
							},
						},
					},
				},
				{
					Name:    "oauth-provider",
					Summary: "Manage OAuth/OIDC providers",
					Commands: []cmdLeaf{
						{Name: "add", Summary: "Add an OAuth/OIDC provider", Run: controlRun(cmdcontrol.RunAdminOAuthProviderAdd)},
						{Name: "list", Summary: "List configured providers", Run: controlRun(cmdcontrol.RunAdminOAuthProviderList)},
						{Name: "remove", Summary: "Remove a provider", Run: controlRun(cmdcontrol.RunAdminOAuthProviderRemove)},
						{Name: "enable", Summary: "Enable a provider", Run: func(c cmdCtx, args []string) error {
							return cmdcontrol.RunAdminOAuthProviderSetEnabled(controlCmdCtxAdapter{PathStr: c.Path, DescriptionStr: c.Description}, args, true)
						}},
						{Name: "disable", Summary: "Disable a provider", Run: func(c cmdCtx, args []string) error {
							return cmdcontrol.RunAdminOAuthProviderSetEnabled(controlCmdCtxAdapter{PathStr: c.Path, DescriptionStr: c.Description}, args, false)
						}},
					},
				},
				{
					Name:    "captcha",
					Summary: "Manage captcha bot protection: ALTCHA, reCAPTCHA v3, or Turnstile (a solo-mode hub never enforces captcha)",
					Commands: []cmdLeaf{
						{Name: "show", Summary: "Show the effective captcha configuration", Run: controlRun(cmdcontrol.RunAdminCaptchaShow)},
						{Name: "set", Summary: "Update captcha settings and/or switch the active provider", Run: controlRun(cmdcontrol.RunAdminCaptchaSet)},
						{Name: "enable", Summary: "Enable captcha verification", Run: func(c cmdCtx, args []string) error {
							return cmdcontrol.RunAdminCaptchaSetEnabled(controlCmdCtxAdapter{PathStr: c.Path, DescriptionStr: c.Description}, args, true)
						}},
						{Name: "disable", Summary: "Disable captcha verification (the honeypot check stays active)", Run: func(c cmdCtx, args []string) error {
							return cmdcontrol.RunAdminCaptchaSetEnabled(controlCmdCtxAdapter{PathStr: c.Path, DescriptionStr: c.Description}, args, false)
						}},
						{Name: "reset", Summary: "Reset captcha configuration to defaults (all providers, or --provider X)", Run: controlRun(cmdcontrol.RunAdminCaptchaReset)},
					},
				},
				{
					Name:    "rate-limit",
					Summary: "Manage per-user rate limits",
					Commands: []cmdLeaf{
						{Name: "list", Summary: "List rate limits for all known operations", Run: controlRun(cmdcontrol.RunAdminRateLimitList)},
						{Name: "set", Summary: "Update an operation's rate limit", Run: controlRun(cmdcontrol.RunAdminRateLimitSet)},
						{Name: "enable", Summary: "Enable an operation's rate limit", Run: func(c cmdCtx, args []string) error {
							return cmdcontrol.RunAdminRateLimitSetEnabled(controlCmdCtxAdapter{PathStr: c.Path, DescriptionStr: c.Description}, args, true)
						}},
						{Name: "disable", Summary: "Disable an operation's rate limit", Run: func(c cmdCtx, args []string) error {
							return cmdcontrol.RunAdminRateLimitSetEnabled(controlCmdCtxAdapter{PathStr: c.Path, DescriptionStr: c.Description}, args, false)
						}},
						{Name: "reset", Summary: "Reset an operation's rate limit to defaults", Run: controlRun(cmdcontrol.RunAdminRateLimitReset)},
					},
				},
				{
					Name:    "api-token",
					Summary: "Manage durable API tokens (CLI / integrations)",
					Commands: []cmdLeaf{
						{Name: "list", Summary: "List API tokens", Run: controlRun(cmdcontrol.RunAdminAPITokenList)},
						{Name: "issue", Summary: "Issue a new API token (e.g. for headless service accounts)", Run: controlRun(cmdcontrol.RunAdminAPITokenIssue)},
						{Name: "revoke", Summary: "Revoke an API token by id", Run: controlRun(cmdcontrol.RunAdminAPITokenRevoke)},
					},
				},
				{
					Name:    "delegation-token",
					Summary: "Manage worker-minted delegation tokens",
					Commands: []cmdLeaf{
						{Name: "list", Summary: "List delegation tokens", Run: controlRun(cmdcontrol.RunAdminDelegationTokenList)},
						{Name: "revoke", Summary: "Revoke a delegation token by id", Run: controlRun(cmdcontrol.RunAdminDelegationTokenRevoke)},
					},
				},
			},
		},
	},
	Commands: []cmdLeaf{
		{Name: "whoami", Summary: "Show resolved identity for this CLI", Run: controlRun(cmdcontrol.RunWhoami)},
		{Name: "version", Summary: "Print CLI build version (and the --hub's version when set)", Run: controlRun(cmdcontrol.RunVersion)},
	},
}

// handleControlArgs walks controlTree to validate args before dispatch.
// Returns (code, true) when help/error fully handled the request.
func handleControlArgs(args []string, stdout, stderr io.Writer) (int, bool) {
	return walkGroupArgs(controlTree, []string{"control"}, args, stdout, stderr)
}

func runControl(args []string) error {
	return dispatchGroup(controlTree, args, []string{"control"})
}

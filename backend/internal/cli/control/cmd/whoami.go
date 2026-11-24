package cmd

import (
	"os"
	"strings"

	"github.com/spf13/cobra"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
)

func newWhoamiCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:              "whoami",
		Short:            "Show the identity and tab context that this CLI acts as",
		QuietUnsupported: true,
		Examples: []cli.Example{
			{Command: "leapmux whoami"},
			{Comment: "Inside a tab, print the id of this tab.", Command: "leapmux whoami --json | jq -r .data.tab_id"},
		},
		Run: runWhoami,
	})
}

// whoamiResult is the result of `whoami`. A hub-bound client fills the
// hub fields; a client inside a tab fills the tab and workspace context fields.
type whoamiResult struct {
	HubURL      string `json:"hub_url,omitempty"`
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name,omitempty"`
	// IsAdmin is nil when the hub could not confirm it. The fallback prints
	// null rather than a false that would be wrong for an administrator.
	IsAdmin            *bool  `json:"is_admin,omitempty"`
	ElevationExpiresAt string `json:"elevation_expires_at,omitempty"`
	WorkerID           string `json:"worker_id,omitempty"`
	WorkerName         string `json:"worker_name,omitempty"`
	WorkspaceID        string `json:"workspace_id,omitempty"`
	WorkspaceTitle     string `json:"workspace_title,omitempty"`
	TileID             string `json:"tile_id,omitempty"`
	TabID              string `json:"tab_id,omitempty"`
	TabType            string `json:"tab_type,omitempty"`
	TabTitle           string `json:"tab_title,omitempty"`
	Provider           string `json:"provider,omitempty"`
	WorkingDir         string `json:"working_dir,omitempty"`
	GitBranch          string `json:"git_branch,omitempty"`
	ParentAgentID      string `json:"parent_agent_id,omitempty"`
	Warning            string `json:"warning,omitempty"`
}

func runWhoami(inv *cli.Invocation) (any, error) {
	c, err := newClient(inv)
	if err != nil {
		return nil, err
	}
	if c.IsWorkerIPC() {
		ipc, err := c.ControlIPCService()
		if err != nil {
			return nil, err
		}
		ctx, cancel := rpcDeadline(inv.Context)
		defer cancel()
		resp, err := ipc.Whoami(ctx, &leapmuxv1.WhoamiRequest{})
		if err != nil {
			return nil, (workerCall{c: c}).withVersion(ctx, "", err)
		}
		out := whoamiResult{
			HubURL:   resp.GetHubUrl(),
			UserID:   resp.GetUserId(),
			Username: resp.GetUsername(),
			WorkerID: resp.GetWorkerId(),
			TabID:    resp.GetTabId(),
			TabType:  tabTypeName(resp.GetTabType()),
		}
		if out.HubURL == "" {
			out.HubURL = strings.TrimSpace(os.Getenv("LEAPMUX_HUB"))
		}
		// Best-effort account metadata (display_name, is_admin, elevation_expires_at).
		var meResp leapmuxv1.GetCurrentUserResponse
		if err := hubCallUnary(ctx, c, leapmuxv1connect.AuthServiceGetCurrentUserProcedure,
			&leapmuxv1.GetCurrentUserRequest{}, &meResp); err == nil {
			if sr := meResp.GetSessionReport(); sr != nil {
				if u := sr.GetUser(); u != nil {
					if out.Username == "" {
						out.Username = u.GetUsername()
					}
					out.DisplayName = u.GetDisplayName()
					admin := u.GetIsAdmin()
					out.IsAdmin = &admin
				}
				if ts := sr.GetElevationExpiresAt(); ts != nil && ts.IsValid() {
					out.ElevationExpiresAt = ts.AsTime().UTC().Format("2006-01-02T15:04:05Z")
				}
			}
		}
		// Best-effort single-call orientation enrichment: locate the tab and
		// read its Worker details so agents inside a tab learn workspace, tile,
		// directory, git branch, provider, and parent in one command.
		if resp.GetTabId() != "" {
			var loc leapmuxv1.LocateTabResponse
			if err := hubCallUnary(ctx, c, leapmuxv1connect.WorkspaceServiceLocateTabProcedure,
				&leapmuxv1.LocateTabRequest{TabId: resp.GetTabId(), TabType: resp.GetTabType()}, &loc); err == nil && loc.GetTab() != nil {
				row := loc.GetTab()
				out.WorkspaceID = row.GetWorkspaceId()
				out.TileID = row.GetTileId()
				if out.WorkspaceID != "" {
					var wsResp leapmuxv1.GetWorkspaceResponse
					if err := hubCallUnary(ctx, c, leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure,
						&leapmuxv1.GetWorkspaceRequest{WorkspaceId: out.WorkspaceID}, &wsResp); err == nil {
						out.WorkspaceTitle = wsResp.GetWorkspace().GetTitle()
					}
				}
				details, names := fetchTabDetailsAndWorkers(ctx, c, []*leapmuxv1.WorkspaceTab{row}, true)
				if name := names[row.GetWorkerId()]; name != "" {
					out.WorkerName = name
				}
				if d, ok := details[resp.GetTabId()]; ok {
					out.TabTitle = d.Title
					if d.Agent != nil {
						out.Provider = agentlabels.CLIAlias(d.Agent.GetAgentProvider())
						out.WorkingDir = d.Agent.GetWorkingDir()
						out.ParentAgentID = d.Agent.GetParentAgentId()
						if g := d.Agent.GetGitStatus(); g != nil {
							out.GitBranch = g.GetBranch()
						}
					} else if d.Terminal != nil {
						out.WorkingDir = d.Terminal.GetWorkingDir()
						if g := d.Terminal.GetGitStatus(); g != nil {
							out.GitBranch = g.GetBranch()
						}
					}
				}
			}
		}
		return out, nil
	}

	/*
		The hub check, for the same reason `auth status` runs one: printing the
		client's cached identity answers what the credential FILE says, and a
		revoked credential's file keeps its username until something asks the
		hub. hubCheckSession (shared with `auth status`) settles it -- confirmed
		answers carry the hub's own username and admin flag; a refusal is an
		honest not-logged-in; an unreachable or account:read-less credential
		falls back to the cached identity with a warning that states what was
		not verified.
	*/
	report, warning, outcome, err := hubCheckSession(c)
	switch outcome {
	case hubCheckRefused, hubCheckFailed:
		return nil, err
	case hubCheckLocalFallback:
		return whoamiResult{HubURL: c.HubURL, UserID: c.UserID, Username: c.Username, Warning: warning}, nil
	case hubCheckConfirmed:
		// The hub's own answer prints below.
	}
	user := report.GetUser()
	isAdmin := user.GetIsAdmin()
	return whoamiResult{
		HubURL:             c.HubURL,
		UserID:             user.GetId(),
		Username:           user.GetUsername(),
		DisplayName:        user.GetDisplayName(),
		IsAdmin:            &isAdmin,
		ElevationExpiresAt: formatTime(report.GetElevationExpiresAt()),
	}, nil
}

package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
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
	// IsAdmin is nil when the hub could not confirm it. JSON output emits
	// null rather than a false that would be wrong for an administrator.
	IsAdmin            *bool  `json:"is_admin"`
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
	p, err := newClient(inv)
	if err != nil {
		return nil, err
	}
	switch c := p.(type) {
	case *control.WorkerIPCClient:
		return whoamiOverIPC(inv, c)
	case *control.HubClient:
		return whoamiOverHub(inv, c)
	default:
		return nil, fmt.Errorf("unsupported control peer %T", p)
	}
}

func whoamiOverIPC(inv *cli.Invocation, c *control.WorkerIPCClient) (whoamiResult, error) {
	ipc, err := c.ControlIPCService()
	if err != nil {
		return whoamiResult{}, err
	}
	ctx, cancel := rpcDeadline(inv.Context)
	defer cancel()
	resp, err := ipc.Whoami(ctx, &leapmuxv1.WhoamiRequest{})
	if err != nil {
		return whoamiResult{}, (workerCall{ipc: c}).withVersion(ctx, "", err)
	}
	// Account metadata (display_name, is_admin, elevation_expires_at) is
	// unavailable over tab IPC: AuthService/GetCurrentUser is not in
	// hubrpc.Registry and delegation bearers cannot hold account:read.
	out := whoamiResult{
		HubURL:   resp.GetHubUrl(),
		UserID:   resp.GetUserId(),
		Username: resp.GetUsername(),
		WorkerID: resp.GetWorkerId(),
		TabID:    resp.GetTabId(),
		TabType:  resolve.TabTypeWireName(resp.GetTabType()),
	}
	if out.HubURL == "" {
		out.HubURL = strings.TrimSpace(os.Getenv("LEAPMUX_HUB"))
	}
	if resp.GetTabId() != "" {
		enrichWhoamiTab(ctx, c, resp, &out)
	}
	return out, nil
}

// enrichWhoamiTab adds workspace, tile, and Worker metadata to out on a
// best-effort basis so an agent inside a tab gets full orientation in one call.
func enrichWhoamiTab(ctx context.Context, c *control.WorkerIPCClient, resp *leapmuxv1.WhoamiResponse, out *whoamiResult) {
	var (
		workspaceID    string
		tileID         string
		workspaceTitle string
		details        map[string]tabDetail
		workerNames    map[string]string
		workerID       = resp.GetWorkerId()
	)
	fetchWorkspaceTitle := func(wsID string) string {
		if wsID == "" {
			return ""
		}
		var wsResp leapmuxv1.GetWorkspaceResponse
		if err := hubCallUnary(ctx, c, leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure,
			&leapmuxv1.GetWorkspaceRequest{WorkspaceId: wsID}, &wsResp); err == nil {
			return wsResp.GetWorkspace().GetTitle()
		}
		return ""
	}

	if workerID != "" {
		lightTab := &leapmuxv1.WorkspaceTab{
			Tab:      &leapmuxv1.TabIdent{TabId: resp.GetTabId(), TabType: resp.GetTabType()},
			WorkerId: workerID,
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			var loc leapmuxv1.LocateTabResponse
			if err := hubCallUnary(ctx, c, leapmuxv1connect.WorkspaceServiceLocateTabProcedure,
				&leapmuxv1.LocateTabRequest{TabId: resp.GetTabId(), TabType: resp.GetTabType()}, &loc); err == nil && loc.GetTab() != nil {
				row := loc.GetTab()
				workspaceID = row.GetWorkspaceId()
				tileID = row.GetTileId()
				workspaceTitle = fetchWorkspaceTitle(workspaceID)
			}
		}()
		go func() {
			defer wg.Done()
			details, workerNames = fetchTabDetailsAndWorkers(ctx, c, []*leapmuxv1.WorkspaceTab{lightTab}, true)
		}()
		wg.Wait()
	} else {
		var loc leapmuxv1.LocateTabResponse
		if err := hubCallUnary(ctx, c, leapmuxv1connect.WorkspaceServiceLocateTabProcedure,
			&leapmuxv1.LocateTabRequest{TabId: resp.GetTabId(), TabType: resp.GetTabType()}, &loc); err != nil || loc.GetTab() == nil {
			return
		}
		row := loc.GetTab()
		workspaceID = row.GetWorkspaceId()
		tileID = row.GetTileId()
		workerID = row.GetWorkerId()
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			workspaceTitle = fetchWorkspaceTitle(workspaceID)
		}()
		go func() {
			defer wg.Done()
			details, workerNames = fetchTabDetailsAndWorkers(ctx, c, []*leapmuxv1.WorkspaceTab{row}, true)
		}()
		wg.Wait()
	}

	out.WorkspaceID = workspaceID
	out.TileID = tileID
	out.WorkspaceTitle = workspaceTitle
	if name := workerNames[workerID]; name != "" {
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

func whoamiOverHub(inv *cli.Invocation, c *control.HubClient) (whoamiResult, error) {
	// Confirm the session with the hub for the same reason `auth status` does:
	// a revoked credential's file still holds its old username until something
	// asks the hub. checkHubSession settles the result:
	//   - confirmed: return the hub's username, admin flag, and elevation state.
	//   - refused: return a not_logged_in error.
	//   - unreachable or missing account:read: return the cached identity with a
	//     warning stating what could not be verified.
	check, err := checkHubSession(inv.Context, c)
	if err != nil {
		return whoamiResult{}, err
	}
	if check.Fallback {
		warning := check.Warning
		if check.Unreachable && c.UserID == "" && c.Username == "" {
			warning = localHubUnreachableWarning
		}
		return whoamiResult{
			HubURL:   c.HubURL,
			UserID:   c.UserID,
			Username: c.Username,
			Warning:  warning,
		}, nil
	}
	user := check.Report.GetUser()
	isAdmin := user.GetIsAdmin()
	return whoamiResult{
		HubURL:             c.HubURL,
		UserID:             user.GetId(),
		Username:           user.GetUsername(),
		DisplayName:        user.GetDisplayName(),
		IsAdmin:            &isAdmin,
		ElevationExpiresAt: formatTime(check.Report.GetElevationExpiresAt()),
	}, nil
}

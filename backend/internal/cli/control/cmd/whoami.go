package cmd

import (
	"github.com/spf13/cobra"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
)

func newWhoamiCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:   "whoami",
		Short: "Show the identity that this CLI acts as",
		Examples: []cli.Example{
			{Command: "leapmux control whoami"},
			{Comment: "Inside a tab, print the id of this tab.", Command: "leapmux control whoami --json | jq -r .data.tab_id"},
		},
		Run: runWhoami,
	})
}

// whoamiResult is the result of `whoami`. A hub-bound client fills the
// hub fields; a client inside a tab fills the tab fields.
type whoamiResult struct {
	HubURL   string `json:"hub_url,omitempty"`
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	// IsAdmin is nil when the hub could not confirm it. The fallback prints
	// null rather than a false that would be wrong for an administrator.
	IsAdmin  *bool  `json:"is_admin,omitempty"`
	WorkerID string `json:"worker_id,omitempty"`
	TabID    string `json:"tab_id,omitempty"`
	TabType  string `json:"tab_type,omitempty"`
	Warning  string `json:"warning,omitempty"`
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
		// No workspace_id and no scope: the bearer is scoped to the user and
		// to the minting worker, not to a workspace, and the spawn context
		// carries the stable tab id instead -- `tab get self` derives the
		// workspace from it and stays correct across a tab move.
		return whoamiResult{
			UserID:   resp.GetUserId(),
			Username: resp.GetUsername(),
			WorkerID: resp.GetWorkerId(),
			TabID:    resp.GetTabId(),
			TabType:  tabTypeName(resp.GetTabType()),
		}, nil
	}

	/*
		The hub check, for the same reason `auth status` runs one: printing the
		client's cached identity answers what the credential FILE says, and a
		revoked credential's file keeps its username until something asks the
		hub. hubCheck (shared with `auth status`) settles it -- confirmed
		answers carry the hub's own username and admin flag; a refusal is an
		honest not-logged-in; an unreachable or account:read-less credential
		falls back to the cached identity with a warning that states what was
		not verified.

		is_admin is absent on the fallback: the hub-confirmed and fallback
		endings carry the same identity fields, and leaving out the one this
		ending cannot know is honest -- a fabricated false would tell an
		administrator's own machine the wrong flag during an outage. The
		worker-IPC ending answers from the spawn context the worker holds,
		which carries no hub_url and no admin flag.
	*/
	user, warning, outcome, err := hubCheck(c)
	switch outcome {
	case hubCheckRefused, hubCheckFailed:
		return nil, err
	case hubCheckLocalFallback:
		return whoamiResult{HubURL: c.HubURL, UserID: c.UserID, Username: c.Username, Warning: warning}, nil
	case hubCheckConfirmed:
		// The hub's own answer prints below.
	}
	isAdmin := user.GetIsAdmin()
	return whoamiResult{HubURL: c.HubURL, UserID: user.GetId(), Username: user.GetUsername(), IsAdmin: &isAdmin}, nil
}

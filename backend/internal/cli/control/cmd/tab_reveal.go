package cmd

import (
	"github.com/spf13/cobra"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// tabRevealLong is the help of `tab reveal`.
const tabRevealLong = `Show a tab in every LeapMux window that you have open: each window switches
to the tab's workspace, and the tab becomes the active tab of its tile, as a
click on the tab in the sidebar does.

Nothing stores the reveal. Which tab a window shows stays the window's own
state, so a window that you open later shows what it showed before, and a
window that does not know the tab yet ignores the command.`

// newTabRevealLeaf builds `tab reveal`, which asks every open frontend of the
// user to show one tab. The tab's Worker relays the request on its
// private-event stream, as `terminal quake` relays a panel command.
func newTabRevealLeaf() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "reveal <tab>",
		Short:   "Show a tab in every LeapMux window that you have open",
		Long:    tabRevealLong,
		Mutates: true,
		Examples: []cli.Example{
			{Command: "leapmux control tab reveal 87a19d5f"},
			{Comment: "Bring the tab that runs this command into view.", Command: "leapmux control tab reveal self"},
		},
		Complete: completeTargets,
		Run:      runTabReveal,
	})
}

// tabRevealResult is the result of `tab reveal`.
type tabRevealResult struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

// runTabReveal sends RevealTab to the Worker that hosts the tab.
func runTabReveal(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindTab, inv.Arg("tab"), resolve.Need{WorkerID: true, Want: resolve.Wants{TabType: true, WorkspaceID: true}})
	if err != nil {
		return nil, err
	}
	if err := maybePreflightWorker(s.ctx, s.c, got.WorkerID); err != nil {
		return nil, err
	}
	req := &leapmuxv1.RevealTabRequest{TabId: got.ID}
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerPrivateServiceRevealTabProcedure, req, nil); err != nil {
		return nil, err
	}
	return tabRevealResult{ID: got.ID, Type: tabTypeName(got.Type), WorkspaceID: got.WorkspaceID}, nil
}

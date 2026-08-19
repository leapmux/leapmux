package cmd

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// `tab focus` is intentionally absent: active-tab + focused-tile are
// purely client-local under the CRDT model (sessionStorage), so there
// is no remote command to issue.

// RunTabRename retitles an agent or terminal tab. The resolver
// figures out the tab's type by passing TAB_TYPE_UNSPECIFIED to
// LocateTab when the user didn't disambiguate — the server treats
// 0 as a wildcard and returns the matched type. Replaces the prior
// per-type `agent rename` / `terminal set-title` commands. Agent
// rename dispatches to RenameAgent; terminal rename is still a
// worker-private E2EE call not yet wired through the new IPC layer.
func RunTabRename(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub, title string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{})
	fs.StringVar(&title, "title", "", "new title (required)")
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	if title == "" {
		return control.EmitError("invalid_request", "--title is required")
	}
	return resolveAndEmit(hub, resolve.Need{TabID: true, WorkerID: true}, in, func(ctx context.Context, c *control.Client, got resolve.Resolved) error {
		switch got.TabType {
		case leapmuxv1.TabType_TAB_TYPE_AGENT:
			// Report the worker's title, not the one this command sent. The
			// worker cleans it with the rule that validate.CleanName
			// documents. A title that cleans to nothing leaves the tab with
			// the name it already had. Echoing --title back therefore printed
			// a name that the tab does not carry.
			var resp leapmuxv1.RenameAgentResponse
			if err := callInnerRPC(ctx, c, got.WorkerID, "RenameAgent", &leapmuxv1.RenameAgentRequest{AgentId: got.TabID, Title: title}, &resp); err != nil {
				return err
			}
			return control.EmitData(map[string]string{"tab_id": got.TabID, "tab_type": "agent", "title": resp.GetTitle()})
		case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
			return renameTerminalTab(ctx, c, got, title)
		default:
			return control.EmitError("not_found", "no tab with id "+got.TabID+" in any accessible workspace")
		}
	})
}

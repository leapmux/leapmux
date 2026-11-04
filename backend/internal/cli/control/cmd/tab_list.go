package cmd

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// `tab list` and `tab get` are read-only views over the rendered rows of
// workspace_tab_owned, served by the WorkspaceService ListTabs and LocateTab
// RPCs.

// RunTabList enumerates rendered tabs. Tabs are always scoped to the
// authenticated session's own user, so no user id is resolved or
// sent. When --workspace-id is supplied (directly, or derived from
// --tab-id / --tile-id), ListTabs scopes to that one workspace;
// otherwise it returns every tab the caller owns.
//
// --tab-type, unlike on `tab get` / `tab close` / `tab rename`, is an
// OUTPUT filter, not a resolver constraint. A user inside a terminal
// spawn running `tab list --tab-type agent` wants to see agent tabs;
// they don't want the resolver to LocateTab(AGENT, $LEAPMUX_CONTROL_TAB_ID)
// and 404 because the env tab is a terminal. HideTabType keeps the
// resolver away from the flag; ListTabs runs unconstrained, then we
// drop non-matching rows client-side.
func RunTabList(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub, filterType string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{HideTabType: true})
	fs.StringVar(&filterType, "tab-type", "", `filter output by tab type ("agent" | "terminal" | "file"; default: no filter)`)
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	var wantedType leapmuxv1.TabType
	if filterType != "" {
		var ok bool
		wantedType, ok = resolve.ParseTabType(filterType)
		if !ok {
			return control.EmitError("invalid_request", `--tab-type must be "agent", "terminal", or "file"`)
		}
	}
	return resolveAndEmit(hub, resolve.Need{Want: resolve.Wants{WorkspaceID: true}}, in, func(ctx context.Context, c *control.Client, got resolve.Resolved) error {
		req := &leapmuxv1.ListTabsRequest{}
		if got.WorkspaceID != "" {
			req.WorkspaceIds = []string{got.WorkspaceID}
		}
		var resp leapmuxv1.ListTabsResponse
		return hubCallUnaryEmitOn(ctx, c, leapmuxv1connect.WorkspaceServiceListTabsProcedure, req, &resp, func() any {
			tabs := filterTabsByType(resp.GetTabs(), wantedType)
			return map[string]any{"tabs": workspaceTabsToList(tabs)}
		})
	})
}

// RunTabGet returns the rendered-tab row for a single tab: the row that the
// resolver's LocateTab answered, so the command asks the hub once. The
// resolver derives workspace_id from --tab-id when only the tab id is
// supplied; tab_type may be left unset and the wildcard LocateTab will fill it
// (via the resolver's UNSPECIFIED-to-matched backfill). A workspace that the
// caller specifies but that does not hold the tab answers not_found, as a
// lookup scoped to that workspace would.
//
// The resolver hands back no row when it tolerated an ambient tab id that the
// hub cannot place (see resolve.ErrTabNotLocatable), and that answers
// not_found too: there is no row to print.
func RunTabGet(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{})
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	return resolveAndEmit(hub, resolve.Need{TabID: true, WorkspaceID: true, Want: resolve.Wants{TabType: true}}, in, func(_ context.Context, _ *control.Client, got resolve.Resolved) error {
		// An explicit --workspace-id shadows the workspace that an ambient tab
		// id derives, so the resolved workspace can differ from the row's.
		if got.Tab == nil || got.Tab.GetWorkspaceId() != got.WorkspaceID {
			return control.EmitError("not_found", "tab "+got.TabID+" is not in workspace "+got.WorkspaceID)
		}
		return control.EmitData(workspaceTabToMap(got.Tab))
	})
}

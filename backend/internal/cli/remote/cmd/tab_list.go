package cmd

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
	"github.com/leapmux/leapmux/internal/cli/remote/resolve"
)

// `tab list` and `tab get` are read-only views over
// workspace_tab_rendered, served by the (still-extant) WorkspaceService
// ListTabs / GetTab RPCs.

// RunTabList enumerates rendered tabs. Org filter comes from
// --org-id, --workspace-id, or any flag the resolver can derive
// org_id from (e.g. --tab-id, --worker-id). When --workspace-id is
// supplied, ListTabs scopes to that one workspace; otherwise it
// returns every tab in the resolved org.
//
// --tab-type, unlike on `tab get` / `tab close` / `tab rename`, is an
// OUTPUT filter, not a resolver constraint. A user inside a terminal
// spawn running `tab list --tab-type agent` wants to see agent tabs;
// they don't want the resolver to LocateTab(AGENT, $LEAPMUX_REMOTE_TAB_ID)
// and 404 because the env tab is a terminal. HideTabType keeps the
// resolver away from the flag; ListTabs runs unconstrained, then we
// drop non-matching rows client-side.
func RunTabList(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub, filterType string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{HideUser: true, HideTabType: true})
	fs.StringVar(&filterType, "tab-type", "", `filter output by tab type ("agent" | "terminal" | "file"; default: no filter)`)
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	var wantedType leapmuxv1.TabType
	if filterType != "" {
		var ok bool
		wantedType, ok = resolve.ParseTabType(filterType)
		if !ok {
			return remote.EmitError("invalid_request", `--tab-type must be "agent", "terminal", or "file"`)
		}
	}
	return resolveAndEmit(hub, resolve.Need{OrgID: true}, in, func(ctx context.Context, c *remote.Client, got resolve.Resolved) error {
		req := &leapmuxv1.ListTabsRequest{OrgId: got.OrgID}
		if got.WorkspaceID != "" {
			req.WorkspaceIds = []string{got.WorkspaceID}
		}
		var resp leapmuxv1.ListTabsResponse
		return hubCallUnaryEmitOn(ctx, c, "ListTabs", got.WorkspaceID, req, &resp, func() any {
			tabs := filterTabsByType(resp.GetTabs(), wantedType)
			return map[string]any{"tabs": workspaceTabsToList(tabs)}
		})
	})
}

// RunTabGet returns the rendered-tab row for a single tab. The
// resolver derives workspace_id from --tab-id when only the tab id
// is supplied; tab_type may be left unset and the wildcard
// LocateTab will fill it (via the resolver's UNSPECIFIED-to-matched
// backfill).
func RunTabGet(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{HideOrg: true, HideUser: true})
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	return resolveAndEmit(hub, resolve.Need{TabID: true, WorkspaceID: true}, in, func(ctx context.Context, c *remote.Client, got resolve.Resolved) error {
		req := &leapmuxv1.GetTabRequest{
			WorkspaceId: got.WorkspaceID,
			TabId:       got.TabID,
			TabType:     got.TabType,
		}
		var resp leapmuxv1.GetTabResponse
		return hubCallUnaryEmitOn(ctx, c, "GetTab", got.WorkspaceID, req, &resp,
			func() any { return workspaceTabToMap(resp.GetTab()) })
	})
}

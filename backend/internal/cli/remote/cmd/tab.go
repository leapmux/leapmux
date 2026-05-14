package cmd

import (
	"context"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
)

// filterTabsByType drops tabs whose type doesn't match wanted. A zero
// wanted (TAB_TYPE_UNSPECIFIED) returns the input slice unchanged, so
// callers can pass the parsed flag through without a nil check.
// The non-unspecified path allocates a fresh slice rather than reusing
// the input's backing array — `in` aliases the response's `tabs` slice
// and overwriting it in place while iterating would corrupt any future
// reader of the response.
func filterTabsByType(in []*leapmuxv1.WorkspaceTab, wanted leapmuxv1.TabType) []*leapmuxv1.WorkspaceTab {
	if wanted == leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED {
		return in
	}
	out := make([]*leapmuxv1.WorkspaceTab, 0, len(in))
	for _, t := range in {
		if t.GetTabType() == wanted {
			out = append(out, t)
		}
	}
	return out
}

// resolveOrgID looks up the org_id for a workspace via GetWorkspace.
// Returns an error if the workspace is not found or the response is
// missing org_id.
func resolveOrgID(ctx context.Context, c *remote.Client, workspaceID string) (string, error) {
	if workspaceID == "" {
		return "", fmt.Errorf("workspace_id required")
	}
	req := &leapmuxv1.GetWorkspaceRequest{WorkspaceId: workspaceID}
	var resp leapmuxv1.GetWorkspaceResponse
	if err := hubCallUnary(ctx, c, "GetWorkspace", workspaceID, req, &resp); err != nil {
		return "", err
	}
	orgID := resp.GetWorkspace().GetOrgId()
	if orgID == "" {
		return "", fmt.Errorf("workspace %s has no org_id", workspaceID)
	}
	return orgID, nil
}

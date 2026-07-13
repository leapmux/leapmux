package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
)

// delegationProcedureScope records, for every procedure a delegation bearer may
// call (delegationAllowedProcedures), how that handler constrains the request
// to the bearer's pinned workspace scope. Scope enforcement is split between
// this coarse interceptor allowlist and per-handler guards
// (delegation_scope.go), so a procedure that is allowlisted but forgets its
// guard would leak cross-workspace data to a scoped bearer.
//
// The interceptor cannot enforce the fine-grained check itself: the workspace
// id lives in each RPC's request body, which the interceptor never decodes.
// This map is the tripwire that keeps the two in lock-step -- TestDelegation...
// below fails the build if a procedure joins the allowlist without a scope
// justification here, forcing the author to decide (and record) how the new
// delegation-reachable RPC is scoped.
var delegationProcedureScope = map[string]string{
	// Worker-scoped public key material only; the workspace narrowing happens
	// at the paired OpenChannel, so there is no workspace to leak here.
	leapmuxv1connect.ChannelServiceGetWorkerHandshakeParamsProcedure: "worker-scoped handshake material; workspace narrowing happens at OpenChannel",
	// Narrows the announced accessible-workspace set to WorkspaceScopeID and
	// re-verifies the pin against current grants (ChannelService.OpenChannel).
	leapmuxv1connect.ChannelServiceOpenChannelProcedure: "OpenChannel pins accessible workspaces to WorkspaceScopeID",
	// userCanUseChannel requires a delegation caller to match the exact bearer
	// that opened the channel, so a bearer can only close its own channels.
	leapmuxv1connect.ChannelServiceCloseChannelProcedure: "CloseChannel is limited to channels opened by the same delegation bearer",
	// delegationWorkspaceMismatch rejects a workspace other than the scope
	// (ChannelService.PrepareWorkspaceAccess).
	leapmuxv1connect.ChannelServicePrepareWorkspaceAccessProcedure: "PrepareWorkspaceAccess rejects a workspace outside the delegation scope",
	// delegationScopedWorkspaceRequest narrows the listing to the scope.
	leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure: "ListWorkspaces narrows the result to the delegation scope",
	// requireDelegationWorkspaceOrNotFound rejects a different workspace.
	leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure: "GetWorkspace rejects a workspace outside the delegation scope",
	// resolveAllowedWorkspacesForUser -> delegationScopedWorkspaceRequest narrows.
	leapmuxv1connect.WorkspaceServiceListTabsProcedure: "ListTabs narrows the requested workspaces to the delegation scope",
	leapmuxv1connect.WorkspaceServiceGetTabProcedure:   "GetTab rejects a workspace outside the delegation scope",
	// Pins the lookup to WorkspaceScopeID (workspace_tabs.LocateTab).
	leapmuxv1connect.WorkspaceServiceLocateTabProcedure: "LocateTab is pinned to the delegation scope workspace",
	// requireDelegationWorkspaceOrNotFound guards the resolved workspace.
	leapmuxv1connect.WorkspaceServiceLocateTileProcedure: "LocateTile rejects a tile outside the delegation scope workspace",
	// delegationScopedWorkspaceRequest narrows the submitted ops' workspaces.
	leapmuxv1connect.OrgCRDTSubmitOpsProcedure: "SubmitOps narrows the requested workspaces to the delegation scope",
	// resolveAllowedWorkspacesSetForUser -> delegationScopedWorkspaceRequest.
	leapmuxv1connect.OrgCRDTGetMaterializedProcedure: "GetMaterialized narrows the materialized set to the delegation scope",
	// requireDelegationWorkspace rejects a different workspace (CRDT.UpdatePresence).
	leapmuxv1connect.OrgCRDTUpdatePresenceProcedure: "UpdatePresence rejects a workspace outside the delegation scope",
}

// TestDelegationAllowedProceduresAreScopeClassified is a tripwire coupling the
// delegation allowlist to a recorded scope justification. If it fails because a
// procedure is unclassified, the fix is NOT to blindly add it here: confirm the
// handler actually constrains the request to the bearer's WorkspaceScopeID
// (reject / narrow), THEN record how.
func TestDelegationAllowedProceduresAreScopeClassified(t *testing.T) {
	for procedure := range delegationAllowedProcedures {
		note, ok := delegationProcedureScope[procedure]
		assert.Truef(t, ok,
			"delegation-allowed procedure %q is not scope-classified: a delegation bearer can reach it, so record how it is constrained to WorkspaceScopeID (or why it is scope-independent) in delegationProcedureScope",
			procedure)
		assert.NotEmptyf(t, note, "delegation-allowed procedure %q has an empty scope justification", procedure)
	}
	for procedure := range delegationProcedureScope {
		assert.Truef(t, delegationAllowedProcedures[procedure],
			"procedure %q is scope-classified but no longer in delegationAllowedProcedures; remove the stale entry", procedure)
	}
}

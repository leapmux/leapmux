package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
)

// delegationProcedureScope records, for every procedure a delegation bearer may
// call (delegationAllowedProcedures), what bounds the request.
//
// A delegation bearer authenticates AS its user and carries no workspace pin,
// so for most of these the answer is "the same owner-only check a session
// gets" -- which is exactly why the entry has to be written down rather than
// assumed. The one bound that is NOT the user's own reach is the MINTING
// WORKER (auth.DelegationWorkerScope), and the procedures that can bind a tab
// to a machine say so here.
//
// This map is the tripwire that keeps the allowlist honest -- TestDelegation...
// below fails the build if a procedure joins the allowlist without a scope
// justification here, forcing the author to decide (and record) how the new
// delegation-reachable RPC is bounded.
var delegationProcedureScope = map[string]string{
	// Worker-scoped public key material only; the worker narrowing happens
	// at the paired OpenChannel.
	leapmuxv1connect.ChannelServiceGetWorkerHandshakeParamsProcedure: "worker-scoped handshake material; the minter bound is applied at OpenChannel",
	// verifyDelegationWorkerScope refuses a bearer aimed at a worker its
	// minter is not entitled to reach.
	leapmuxv1connect.ChannelServiceOpenChannelProcedure: "OpenChannel refuses a worker outside the token's DelegationWorkerScope",
	// userCanUseChannel requires a delegation caller to match the exact bearer
	// that opened the channel, so a bearer can only close its own channels.
	leapmuxv1connect.ChannelServiceCloseChannelProcedure: "CloseChannel is limited to channels opened by the same delegation bearer",
	// Owner-only reads: the bearer sees exactly what its user owns, no more.
	leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure: "owner-only listing; a delegation bearer reads exactly its user's workspaces",
	leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure:   "loadWorkspaceForRead is owner-only against the bearer's user",
	leapmuxv1connect.WorkspaceServiceListTabsProcedure:       "resolveAllowedWorkspacesForUser narrows to the bearer's user's workspaces",
	leapmuxv1connect.WorkspaceServiceGetTabProcedure:         "owner-only workspace read before the tab lookup",
	leapmuxv1connect.WorkspaceServiceLocateTabProcedure:      "LocateAccessibleRendered binds the bearer's user id",
	leapmuxv1connect.WorkspaceServiceLocateTileProcedure:     "the resolved workspace goes through the owner-only read",
	// workerScopePredicate narrows which WORKER ids the ops may bind a tab to.
	leapmuxv1connect.UserCRDTSubmitOpsProcedure: "SubmitOps applies the token's DelegationWorkerScope to every worker id its ops name",
	// Owner-only materialization: same set a session gets for that user.
	leapmuxv1connect.UserCRDTGetMaterializedProcedure: "resolveAllowedWorkspacesForUser narrows to the bearer's user's workspaces",
	leapmuxv1connect.UserCRDTUpdatePresenceProcedure:  "WorkspaceCanAccess is owner-only against the bearer's user",
}

// TestDelegationAllowedProceduresAreScopeClassified is a tripwire coupling the
// delegation allowlist to a recorded scope justification. If it fails because a
// procedure is unclassified, the fix is NOT to blindly add it here: confirm the
// handler actually bounds the request (owner-only, or the minter bound where a
// worker id is involved), THEN record how.
func TestDelegationAllowedProceduresAreScopeClassified(t *testing.T) {
	for procedure := range delegationAllowedProcedures {
		note, ok := delegationProcedureScope[procedure]
		assert.Truef(t, ok,
			"delegation-allowed procedure %q is not scope-classified: a delegation bearer can reach it, so record what bounds it (owner-only, or DelegationWorkerScope) in delegationProcedureScope",
			procedure)
		assert.NotEmptyf(t, note, "delegation-allowed procedure %q has an empty scope justification", procedure)
	}
	for procedure := range delegationProcedureScope {
		assert.Truef(t, delegationAllowedProcedures[procedure],
			"procedure %q is scope-classified but no longer in delegationAllowedProcedures; remove the stale entry", procedure)
	}
}

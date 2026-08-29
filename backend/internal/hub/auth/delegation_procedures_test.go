package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// formerDelegationAllowlist is the EXACT procedure set the deleted
// delegationAllowedProcedures map admitted, copied verbatim.
//
// It is the audit of removing a live gate. The allowlist was the only thing
// standing between a worker-spawned agent -- a process that reads untrusted
// input -- and the whole hub, and CeilingFor(BearerKindDelegation) replaced it
// with a bound on the GRANT. That is a strictly stronger construction, but it
// is only equivalent if the two admit the same procedures, and "equivalent" is
// not something prose can assert. This list is what makes the swap checkable
// line by line, so it must NOT be regenerated from the ceiling.
//
// A procedure legitimately added to the delegation surface later goes here as
// well, with a note. A procedure that drops off it is a deliberate narrowing
// and its line is deleted with one.
var formerDelegationAllowlist = []string{
	leapmuxv1connect.ChannelServiceGetWorkerHandshakeParamsProcedure,
	leapmuxv1connect.ChannelServiceOpenChannelProcedure,
	leapmuxv1connect.ChannelServiceCloseChannelProcedure,
	leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure,
	leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure,
	leapmuxv1connect.WorkspaceServiceListTabsProcedure,
	leapmuxv1connect.WorkspaceServiceGetTabProcedure,
	leapmuxv1connect.WorkspaceServiceLocateTabProcedure,
	leapmuxv1connect.WorkspaceServiceLocateTileProcedure,
	leapmuxv1connect.UserCRDTSubmitOpsProcedure,
	leapmuxv1connect.UserCRDTGetMaterializedProcedure,
	leapmuxv1connect.UserCRDTUpdatePresenceProcedure,
}

// delegationProcedureScope records, for every procedure a delegation bearer may
// call, what bounds the request.
//
// A delegation bearer authenticates AS its user and carries no workspace pin,
// so for most of these the answer is "the same owner-only check a session
// gets" -- which is exactly why the entry has to be written down rather than
// assumed. The one bound that is NOT the user's own reach is the MINTING
// WORKER (auth.DelegationWorkerScope), and the procedures that can bind a tab
// to a machine say so here.
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

// maximalDelegationBearer is the widest credential a delegation row could ever
// produce: an unscoped stored grant, narrowed by the read-time ceiling exactly
// as loadBearer narrows it.
//
// The UNSCOPED starting point is the point. It is the value a mint bug, a
// hand-edited row or a restored backup would produce, and the ceiling has to
// contain it -- so every assertion below runs against the worst case rather
// than against a well-formed one.
func maximalDelegationBearer() *UserInfo {
	return &UserInfo{
		ID: userid.MustNew("u-delegation"),
		// IsAdmin true, deliberately: the account behind a delegation bearer
		// may well be an administrator, and the ceiling must hold regardless.
		IsAdmin:    true,
		Credential: DelegationCredential("d-1", "w-1"),
		Scopes:     authscope.UnscopedGrant().NarrowTo(CeilingFor(BearerKindDelegation)),
	}
}

// newlyDelegationReachable records every hub procedure the CEILING admits that
// the old allowlist refused, with the reason it is acceptable.
//
// This table is the whole audit. A ceiling is a set of scopes and a scope
// covers a family, so replacing an allowlist with one necessarily widens the
// surface -- the question is by exactly what, and whether each addition was
// looked at. An entry here is a reviewed answer; a MISSING entry fails the
// suite, so no widening arrives unnoticed.
//
// It must never be regenerated from the ceiling. The whole point is that the
// two are written independently and compared.
var newlyDelegationReachable = map[string]string{
	// A REAL widening, and it is forced by the vocabulary rather than chosen:
	// worker:read is the scope that OPENS a channel, which the old allowlist
	// already admitted and which a delegation bearer exists to do. The same
	// scope names the worker LISTING, so admitting the one admits the other,
	// and no narrower scope exists to split them.
	//
	// What it exposes is the account's own worker names and ids -- to a bearer
	// that already authenticates AS that account. What it does not widen is
	// reach: verifyDelegationWorkerScope still refuses an OpenChannel aimed at
	// any worker outside the token's DelegationWorkerScope, so enumerating the
	// fleet does not make a second machine reachable.
	leapmuxv1connect.WorkerManagementServiceListWorkersProcedure: "worker:read is the scope that opens a channel, which a delegation bearer must do; the listing shares it and no narrower scope splits them",
	leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure:   "shares worker:read with OpenChannel; see ListWorkers above",

	// The workspace and sidebar lifecycle. SubmitOps was already admitted, and
	// it mutates the whole layout document -- closing tabs, moving them, and
	// rewriting every tile -- so these verbs are the same authority reached
	// through a named RPC instead of an op body. workspace:write is the honest
	// unit for both, and splitting them would need a scope whose only member
	// is "delete a workspace", which no consent screen could explain.
	leapmuxv1connect.WorkspaceServiceCreateWorkspaceProcedure: "SubmitOps already mutates the whole layout document; workspace:write is the honest unit",
	leapmuxv1connect.WorkspaceServiceRenameWorkspaceProcedure: "SubmitOps already mutates the whole layout document; workspace:write is the honest unit",
	leapmuxv1connect.WorkspaceServiceDeleteWorkspaceProcedure: "SubmitOps already mutates the whole layout document; workspace:write is the honest unit",
	leapmuxv1connect.SectionServiceListSectionsProcedure:      "the sidebar is layout, which SubmitOps already reaches",
	leapmuxv1connect.SectionServiceCreateSectionProcedure:     "the sidebar is layout, which SubmitOps already reaches",
	leapmuxv1connect.SectionServiceRenameSectionProcedure:     "the sidebar is layout, which SubmitOps already reaches",
	leapmuxv1connect.SectionServiceDeleteSectionProcedure:     "the sidebar is layout, which SubmitOps already reaches",
	leapmuxv1connect.SectionServiceMoveSectionProcedure:       "the sidebar is layout, which SubmitOps already reaches",
	leapmuxv1connect.SectionServiceMoveWorkspaceProcedure:     "the sidebar is layout, which SubmitOps already reaches",
}

// TestDelegationCeilingAdmitsTheFormerAllowlistAndOnlyRecordedAdditions is the
// audit.
//
// It asserts the swap in BOTH directions: every procedure the allowlist
// admitted is still reachable, and every procedure that became reachable is
// one somebody wrote a reason for.
func TestDelegationCeilingAdmitsTheFormerAllowlistAndOnlyRecordedAdditions(t *testing.T) {
	bearer := maximalDelegationBearer()
	former := make(map[string]bool, len(formerDelegationAllowlist))
	for _, procedure := range formerDelegationAllowlist {
		former[procedure] = true
		assert.NoErrorf(t, enforceScope(procedure, bearer),
			"%q was delegation-allowed and the ceiling must still admit it", procedure)
	}

	for procedure, requirement := range procedureScopes {
		scope, named := requirement.Scope()
		if !named || former[procedure] {
			continue
		}
		if !bearer.Scopes.Allows(scope) {
			assert.NotContainsf(t, newlyDelegationReachable, procedure,
				"%q is recorded as newly delegation-reachable but the ceiling refuses it; remove the stale entry", procedure)
			continue
		}
		reason, recorded := newlyDelegationReachable[procedure]
		assert.Truef(t, recorded,
			"%q was NOT delegation-allowed but the ceiling now admits it through %s; "+
				"either narrow delegationCeiling or record why in newlyDelegationReachable",
			procedure, scope)
		assert.NotEmptyf(t, reason, "%q has an empty widening reason", procedure)
	}
}

// TestDelegationProceduresAreScopeClassified is a tripwire coupling the
// delegation surface to a recorded scope justification. If it fails because a
// procedure is unclassified, the fix is NOT to blindly add it here: confirm the
// handler actually bounds the request (owner-only, or the minter bound where a
// worker id is involved), THEN record how.
func TestDelegationProceduresAreScopeClassified(t *testing.T) {
	for _, procedure := range formerDelegationAllowlist {
		note, ok := delegationProcedureScope[procedure]
		assert.Truef(t, ok,
			"delegation-reachable procedure %q is not scope-classified: a delegation bearer can reach it, so record what bounds it (owner-only, or DelegationWorkerScope) in delegationProcedureScope",
			procedure)
		assert.NotEmptyf(t, note, "delegation-reachable procedure %q has an empty scope justification", procedure)
	}
	reachable := make(map[string]bool, len(formerDelegationAllowlist))
	for _, procedure := range formerDelegationAllowlist {
		reachable[procedure] = true
	}
	for procedure := range delegationProcedureScope {
		assert.Truef(t, reachable[procedure],
			"procedure %q is scope-classified but no longer delegation-reachable; remove the stale entry", procedure)
	}
}

// TestDelegationCeilingExcludesTheThreeDangerousFamilies states the guarantee
// in the vocabulary an auditor reads, rather than as a consequence of the
// procedure walk above.
//
// A worker mints a delegation token for an agent acting on a prompt it did not
// write. Such a credential must never administer the hub, never touch the
// account's own profile or credentials, and never administer the account's
// workers -- whatever the row says, because the row is not what bounds it.
func TestDelegationCeilingExcludesTheThreeDangerousFamilies(t *testing.T) {
	ceiling := CeilingFor(BearerKindDelegation)
	require.False(t, ceiling.IsUnscoped(), "the delegation ceiling must be finite, or it narrows nothing")
	forbidden := []leapmuxv1.Scope{
		leapmuxv1.Scope_SCOPE_ACCOUNT_READ,
		leapmuxv1.Scope_SCOPE_ACCOUNT_WRITE,
		leapmuxv1.Scope_SCOPE_WORKER_ADMIN,
		leapmuxv1.Scope_SCOPE_ADMIN_READ,
		leapmuxv1.Scope_SCOPE_ADMIN_USERS,
		leapmuxv1.Scope_SCOPE_ADMIN_SETTINGS,
		leapmuxv1.Scope_SCOPE_ADMIN_WORKERS,
	}
	for _, scope := range forbidden {
		assert.Falsef(t, ceiling.Allows(scope), "a delegation bearer must never hold %s", scope)
	}
}

// TestAPICeilingIsFiniteAndExcludesTheReservedValues pins the OTHER ceiling.
//
// An app's ceiling is the whole grantable vocabulary, because what an app
// actually holds is what its owner consented to. What it must NOT be is the
// UNSCOPED grant: NarrowTo returns the receiver unchanged against an unscoped
// ceiling, so an unscoped ceiling would make the read-time narrowing a no-op
// and a hand-edited row could then authenticate as unscoped.
func TestAPICeilingIsFiniteAndExcludesTheReservedValues(t *testing.T) {
	ceiling := CeilingFor(BearerKindAPI)
	require.False(t, ceiling.IsUnscoped(),
		"the api-token ceiling must be FINITE; an unscoped one makes loadBearer's narrowing a no-op")
	for _, scope := range authscope.Grantable() {
		assert.Truef(t, ceiling.Allows(scope), "an app may be granted %s", scope)
	}
	assert.False(t, authscope.UnscopedGrant().NarrowTo(ceiling).IsUnscoped(),
		"narrowing an unscoped stored grant must produce a finite one")
}

// TestUnknownBearerKindReachesNothing pins the defence-in-depth arm. ParseBearer
// rejects an unknown kind first, so this is unreachable today -- which is
// exactly why it needs a test: an unreachable arm that fails OPEN is invisible
// until the day something makes it reachable.
func TestUnknownBearerKindReachesNothing(t *testing.T) {
	ceiling := CeilingFor(BearerKind('z'))
	assert.True(t, ceiling.IsEmpty())
	assert.False(t, ceiling.IsUnscoped())
	for _, scope := range authscope.Grantable() {
		assert.Falsef(t, ceiling.Allows(scope), "an unknown bearer kind must not reach %s", scope)
	}
}

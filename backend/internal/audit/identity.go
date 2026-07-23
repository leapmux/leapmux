package audit

// identityComparisonSites classifies every function IN THE REPOSITORY that
// compares a caller identity against a stored one -- `userid.UserID.Matches` or
// `auth.IsOwner`. The value names the test that pins its zero-id denial.
//
// Keys are directory- and receiver-qualified ("dir.(*Recv).Method"). The
// directory rather than the package NAME is load-bearing: internal/hub/service
// and internal/worker/service are both `package service`, so a name-keyed table
// would let a worker-side function be born carrying a hub-side blessing.
//
// This is the counterpart to hub/auth's zeroUserIDDenyFuncs net, and it is
// keyed on the COMPARISON rather than on a parameter type on purpose. Several
// of the decisions below take their identity from the request context rather
// than a parameter -- (*UserService).GetUser and the delegation-mint handler --
// so a parameter-keyed table would classify neither, and the net would read as
// complete while missing the sharpest sites. Keying on the comparison follows
// the rule instead of the signature.
//
// TestRepoInvariants fails on any comparison whose enclosing function is not
// listed here, AND on any listed test that no longer exists -- so neither
// adding an unguarded comparison nor deleting the coverage for an existing one
// can pass silently.
//
// internal/hub/auth is IN scope, despite having its own fixture-driven net.
// That net keys off exported functions carrying an identity PARAMETER, so the
// package's three eviction-path comparisons -- unexported, and taking a bare
// `userID string` -- were covered by neither table. They are the sites where
// getting it wrong is worst: see the eviction-polarity block below.
var identityComparisonSites = map[string]string{
	// ---- hub/service: grant polarity (false means "deny") ----

	// Workspace access is owner-only, and this is the single door onto it.
	"internal/hub/service.loadOwnedWorkspaceOr403": "TestZeroCallerCannotLoadBlankOwnedWorkspace",
	// The package's other resource-ownership predicate.
	"internal/hub/service.(*SectionService).requireOwnedSection": "TestMoveSectionDeniesZeroCallerOnBlankOwnedSection",
	// Decides whether a caller may reuse an already-registered channel.
	"internal/hub/service.userCanUseChannel": "TestUserCanUseChannelRequiresMatchingIdentity",
	// Self-vs-other gate on profile reads; identity comes from the context.
	// Matches is defence in depth here -- the empty-target 400 above it is what
	// makes a zero caller unable to self-match -- so the named test pins that
	// boundary, which is the layer a future edit could actually remove.
	"internal/hub/service.(*UserService).GetUser": "TestGetUserRejectsEmptyTargetBeforeSelfMatch",
	// Decides whether a delegation token may be minted for a worker. The id is
	// minted (and a blank one 403'd) before the comparison, so again the named
	// test pins the boundary rather than the comparison behind it.
	"internal/hub/service.(*WorkerDelegationHandler).handleMint": "TestWorkerDelegation_Mint_RejectsBlankUserID",

	// ---- hub/store ----

	// The store's own ownership helper, invisible to this rule while it was
	// scoped to hub/service. Its Matches has no prologue in front of it, so the
	// empty-vs-empty refusal is the only thing guarding a blank registrant.
	"internal/hub/store.GetOwnedWorker": "TestGetOwnedWorker_EmptyUserIDDenied",

	// ---- hub/auth: grant polarity ----

	// The shared owner predicate every workspace read funnels through, plus the
	// four predicates that call it and the worker-ownership twin. All are
	// exported and all are cases in hub/auth's fixture-driven net, which seeds a
	// real owner and asserts both the deny and the owner-side control.
	// WorkspaceCanAccessInOrg is deliberately absent: it delegates to
	// WorkspaceCanRead rather than comparing itself, so there is one
	// implementation of the owner-only rule and one entry for it here.
	"internal/hub/auth.IsOwner":                       "TestZeroUserIDDenies",
	"internal/hub/auth.WorkspaceCanRead":              "TestZeroUserIDDenies",
	"internal/hub/auth.WorkspaceReadableByUsersInOrg": "TestZeroUserIDDenies",
	"internal/hub/auth.WorkspacesReadableByUser":      "TestZeroUserIDDenies",
	"internal/hub/auth.WorkerCanUse":                  "TestZeroUserIDDenies",
	"internal/hub/auth.ResolveDelegationWorkerScope":  "TestZeroUserIDDenies",

	// ---- hub/auth: EVICTION polarity (false means "do not revoke") ----
	//
	// These three are the reason this package is no longer exempt. Matches is
	// tuned for grants, so on an eviction path its false is a fail-OPEN: a blank
	// id would skip every cached session, bearer, and lease and report a
	// revocation that evicted nothing. What keeps the polarity right is the
	// hand-written `userID == ""` prologue on the exported entrypoint -- not the
	// type -- and deleting that prologue as "redundant with userid.UserID" used
	// to leave the whole suite green.
	"internal/hub/auth.(*AuthContextRegistry).RevokeUserAuthContextAtGeneration": "TestBlankUserIDRevocationEvictsNothingAndBumpsNoGeneration",
	"internal/hub/auth.(*AuthContextRegistry).evictSessionsByUserGeneration":     "TestBlankUserIDRevocationEvictsNothingAndBumpsNoGeneration",
	"internal/hub/auth.(*AuthContextRegistry).evictBearersByUserGeneration":      "TestBlankUserIDRevocationEvictsNothingAndBumpsNoGeneration",

	// ---- worker/service: grant polarity ----
	//
	// Both of these compare two TYPED ids rather than a typed one against a
	// string, so they were reached through UserID.Equal -- and while that method
	// was called Equal the net could not see them. `Equal` is Go's most
	// overloaded method name, so no syntax-level rule can tell a UserID
	// comparison from a time.Time one, and this rule therefore did not scan it
	// at all. The worker-side ownership gate, the sharpest decision in the whole
	// worker process, was outside the net for that reason alone. Renaming the
	// method MatchesUser is what brought both sites in; see userid.MatchesUser.

	// The machine-scoped gate: only the worker's owner may reach the filesystem,
	// git, tunnel, and sysinfo families. A zero id on EITHER side must refuse,
	// because an unpopulated RegisteredBy and an unnamed caller are the same
	// empty string.
	"internal/worker/service.requireWorkerOwner": "TestRequireWorkerOwnerRefusesEmptyIdentities",
	// Not a grant: it decides whether the Hub pushed a DIFFERENT owner than the
	// one already recorded, and only logs. It is listed because the comparison
	// is the same one, and because the guard that keeps it honest -- refusing an
	// empty push rather than storing it -- is what stops the gate above from
	// ever comparing against a blank owner.
	"internal/worker/service.(*Service).UpdateRegisteredBy": "TestUpdateRegisteredByIgnoresEmptyOwner",
}

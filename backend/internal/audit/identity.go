package audit

// identityComparisonSites classifies every function IN THE REPOSITORY that
// compares a caller identity against a stored one -- `userid.UserID.Matches` or
// `auth.IsOwner`. The value identifies the test that pins its zero-id denial.
//
// Keys are directory- and receiver-qualified ("dir.(*Recv).Method"). The
// directory rather than the package NAME is load-bearing: internal/hub/service
// and internal/worker/service are both `package service`, so a name-keyed table
// would apply a hub-side entry to a worker-side function.
//
// This is the counterpart to hub/auth's zeroUserIDDenyFuncs net, and it is
// keyed on the COMPARISON rather than on a parameter type on purpose. Several
// of the decisions below take their identity from the request context rather
// than a parameter -- the delegation-mint handler, for one -- so a
// parameter-keyed table would not classify them, and the net would read as
// complete while missing the sharpest sites. Keying on the comparison follows
// the rule instead of the signature.
//
// TestRepoInvariants fails on any comparison whose enclosing function is not
// listed here, AND on any listed test that no longer exists -- so neither
// adding an unguarded comparison nor deleting the coverage for an existing one
// can pass silently.
//
// internal/hub/auth is IN scope, despite having its own fixture-driven net.
// That net keys off exported functions carrying an identity PARAMETER, so
// neither table covered the package's three eviction-path comparisons --
// unexported, and taking a bare `userID string`. They are the sites where
// getting it wrong is worst: see the eviction-polarity block below.
var identityComparisonSites = map[string]string{
	// ---- hub/service: grant polarity (false means "deny") ----

	// Workspace access is owner-only, and this is the single door onto it.
	"internal/hub/service.loadOwnedWorkspaceOr403": "TestZeroCallerCannotLoadBlankOwnedWorkspace",
	// Decides whether the credential-issued notice reads as an ALARM ("an
	// administrator issued this, you did not authorize it") or as a receipt.
	// Not a grant: it picks the wording of a mail the hub already decided to
	// send. It fails closed -- a blank id keeps the alarm -- because the
	// stronger notice is the safe answer for an actor the hub cannot identify.
	"internal/hub/service.issuedByAnotherPerson": "TestIssuedByAnotherPerson_BlankIdKeepsTheAlarm",
	// The package's other resource-ownership predicate.
	"internal/hub/service.requireOwnedSection": "TestMoveSectionDeniesZeroCallerOnBlankOwnedSection",
	// Decides whether a request may see one registered app. A PRIVATE app is
	// visible to its owner alone, so a zero caller must resolve none: an
	// unauthenticated stage (the device-code first hop) reaches hub-wide apps
	// and nothing else, which is exactly what a blank comparison would break.
	"internal/hub/service.(*OAuthServerHandler).resolveApp": "TestResolveApp_ZeroCallerReachesNoPrivateApp",
	// Decides whether a caller may EDIT one registered app. It is the early
	// refusal with a message; the store statement carries the same predicate,
	// so this is the second of two independent checks rather than the only one.
	"internal/hub/service.assertAppOwner": "TestAssertAppOwner_ZeroCallerOwnsNothing",
	// Decides whether a caller may reuse an already-registered channel.
	"internal/hub/service.userCanUseChannel": "TestUserCanUseChannelRequiresMatchingIdentity",
	// Decides whether a delegation token may be minted for a worker. The id is
	// minted (and a blank one 403'd) before the comparison, so again the listed
	// test pins the boundary rather than the comparison behind it.
	"internal/hub/service.(*WorkerDelegationHandler).handleMint": "TestWorkerDelegation_Mint_RejectsBlankUserID",
	// The mint's second, independent ownership check: the tab-propagation poll
	// re-verifies that the row it waits on is the caller's own, so the
	// mint's safety does not rest solely on the user_id predicate in a SQL file
	// three packages away. It was previously a raw `row.UserID == userID.String()`
	// -- fail-OPEN for blank-vs-blank, and invisible here, because a .String()
	// unwrap defeats the detector.
	"internal/hub/service.(*WorkerDelegationHandler).waitForTabOwnership": "TestWorkerDelegation_Mint_RejectsForeignOwnerTabRow",
	// The tab-sync reconciler's tombstone target. Not a grant: it compares the
	// row-level owner against the owner the query was BOUND with, so a
	// mismatch is a broken predicate rather than an unauthorized caller, and it
	// reports the mismatch rather than silently routing it. It appears here
	// because the comparison is the same one, and because this used to be the last
	// production path that reached crdt.Registry.Get with an unvalidated column
	// string -- the manager is now derived from the bound owner instead.
	"internal/hub/service.(*WorkerConnectorService).handleWorkspaceTabsSync": "TestHandleWorkspaceTabsSync_TombstonesOnlyTheRegistrant",

	// ---- hub/crdt: grant polarity ----

	// The CRDT's only remaining tenancy comparison. A submit is no longer one:
	// SubmitInput carries no user id, so the manager it lands on IS the tenant
	// and processSubmit has nothing to compare (Registry.Get validates the user
	// id and keys the manager by it; the factory builds it from that key).
	//
	// Bootstrap is different because the user_state payload carries its OWN
	// user_id, and adopting it would let the BLOB override the key the row was
	// fetched by. The reach is outside the CRDT -- CompactBatch keys the next
	// user_state row by state.GetUserId(), so a foreign payload rewrites another
	// tenant's row. (The derived workspace_tab_owned rows were the other half of
	// that reach; they now take their owner from the committing tenant and the
	// column carries a users(id) FK, so this is the layer that still needs the
	// check.) Matches rather than `!=` for the usual reason: a blank-tenant
	// manager must not match a blank-tenant payload.
	"internal/hub/crdt.(*Manager).requireOwnState": "TestBootstrapRefusesAStatePayloadNamingAnotherTenant",

	// ---- hub/store ----

	// The store's own ownership helper, invisible to this rule while it was
	// scoped to hub/service. Its Matches has no prologue in front of it, so the
	// empty-vs-empty refusal is the only thing guarding a blank registrant.
	"internal/hub/store.GetOwnedWorker": "TestGetOwnedWorker_EmptyUserIDDenied",

	// ---- hub/auth: grant polarity ----

	// The shared owner predicate every workspace read funnels through, plus the
	// predicates that call it and the worker-ownership twin. All are exported
	// and all are cases in hub/auth's fixture-driven net, which seeds a real
	// owner and asserts both the deny and the owner-side control.
	//
	// IsOwner is the canonical comparison the rest route through, but routing
	// through it does NOT excuse a caller from this table: isIdentityComparison
	// counts a CALL to auth.IsOwner as an identity comparison in its own right,
	// exactly as it counts the userid.Matches inside IsOwner. So the three
	// predicates that call it -- WorkspaceCanAccess, WorkspaceReadableByUsers,
	// WorkspacesReadableByUser -- are detected sites and must each be listed.
	// Deleting one as "duplicating IsOwner" does not de-duplicate anything; it
	// fails TestRepoInvariants with an unclassified-comparison error.
	"internal/hub/auth.IsOwner":                      "TestZeroUserIDDenies",
	"internal/hub/auth.WorkspaceCanAccess":           "TestZeroUserIDDenies",
	"internal/hub/auth.WorkspaceReadableByUsers":     "TestZeroUserIDDenies",
	"internal/hub/auth.WorkspacesReadableByUser":     "TestZeroUserIDDenies",
	"internal/hub/auth.WorkerCanUse":                 "TestZeroUserIDDenies",
	"internal/hub/auth.ResolveDelegationWorkerScope": "TestZeroUserIDDenies",

	// ---- hub/auth: EVICTION polarity (false means "do not revoke") ----
	//
	// These three are the reason this package is no longer exempt. Matches is
	// tuned for grants, so on an eviction path its false is a fail-OPEN: a blank
	// id would skip every cached session, bearer, and lease and report a
	// revocation that evicted nothing. What keeps the polarity right is the
	// hand-written `userID == ""` prologue on the exported entrypoint -- not the
	// type -- and deleting that prologue as "redundant with userid.UserID" used
	// to leave the whole suite passing.
	"internal/hub/auth.(*AuthContextRegistry).RevokeUserAuthContextAtGeneration": "TestBlankUserIDRevocationEvictsNothingAndBumpsNoGeneration",
	"internal/hub/auth.(*AuthContextRegistry).evictSessionsByUserGeneration":     "TestBlankUserIDRevocationEvictsNothingAndBumpsNoGeneration",
	"internal/hub/auth.(*AuthContextRegistry).evictBearersByUserGeneration":      "TestBlankUserIDRevocationEvictsNothingAndBumpsNoGeneration",

	// The relay-disconnect sweep. Not a grant: it selects which channels a
	// disconnecting connection takes down with it. The polarity still matters
	// and still points the same way -- a raw `ch.UserID != userID` counted
	// blank-vs-blank as a match, so an unidentified caller would sweep
	// every blank-owner channel; Matches refuses instead.
	"internal/hub/channelmgr.(*Manager).UnbindUserAndCleanup": "TestUnbindUserAndCleanup_BlankUserClosesNothing",

	// ---- worker/service: grant polarity ----
	//
	// Both of these compare two TYPED ids rather than a typed one against a
	// string, so they called UserID.Equal -- and while that method
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
	//
	// One entry, not two: the unary and streaming gates (requireWorkerOwner /
	// requireWorkerOwnerStream) both delegate the comparison here and differ only
	// in how they encode the refusal, so this is the single place that makes the
	// decision. Keeping the predicate in one function is also what stops a change to
	// it landing in one gate and missing the other.
	"internal/worker/service.callerIsWorkerOwner": "TestRequireWorkerOwnerRefusesEmptyIdentities",
	// Not a grant: it decides whether the Hub pushed a DIFFERENT owner than the
	// one already recorded, and only logs. It appears here because the comparison
	// is the same one, and because the guard that keeps it correct -- refusing an
	// empty push rather than storing it -- is what stops the gate above from
	// ever comparing against a blank owner.
	"internal/worker/service.(*Service).UpdateRegisteredBy": "TestUpdateRegisteredByIgnoresEmptyOwner",
	// The reap gate, and the most destructive comparison in either process: a
	// false negative here closes a live agent/terminal and drops a worktree
	// link. It decides whether an ABSENCE from the hub's owner-scoped response
	// may count as an orphan for this local row. It was previously spelled as
	// a bespoke `ownerScope.covers` (`s.owner != "" && s.owner == userID`) --
	// semantically identical to Matches, but a hand-written twin that no
	// syntax-level rule could recognise, so the sharpest decision in the worker
	// sat outside this net while appearing to be inside it.
	"internal/worker/service.(*OrphanReconciler).reconcileTabPayloads": "TestOrphanReconciler_FileTab_SharedTabIDStaysWithItsOwner",
	// The reconnect report's owner filter, and the mirror of the reap gate above:
	// the same (user_id, tab_id) uniqueness rule, applied on the OTHER half of the
	// same comparison. The worker's tab report carries no user axis on the wire, so
	// the Hub attributes all of it to the connecting registrant -- meaning a row
	// left behind by a previous owner (ClearState keeps worker.db, and
	// workers.registered_by is never UPDATEd) would be claimed by whoever connects
	// next, and a colliding client-minted id would suppress a tombstone the real
	// owner's tab was due. BuildTabSync refuses an unminted owner outright rather
	// than reporting every row on the machine.
	"internal/worker/bootstrap.payloadTabIDsByType": "TestBuildTabSync_RefusesWithoutARegisteredOwner",
}

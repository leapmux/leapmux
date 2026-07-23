package audit

// orgCheckSkipSites classifies every function IN THE REPOSITORY that skips the
// organization binding by calling auth.AnyOrg(). The value records why that
// skip is correct there.
//
// AnyOrg is the one constructor that opts a workspace lookup out of the org
// rule, so the set of functions calling it IS the set of org-check carve-outs.
// Until this table existed the rule was enforced by prose: a comment in
// workspace_tabs.go told the reader that `rg AnyOrg` finds every skip and named
// the other two sites by hand -- a claim that goes stale the moment a fourth
// appears, silently and while every test stays green. The same class of net as
// workerReachSites and identityComparisonSites, for the same reason.
//
// Keys are directory- and receiver-qualified ("dir.(*Recv).Method"); see
// workerReachSites for why the directory rather than the package NAME.
var orgCheckSkipSites = map[string]string{
	// A delegation bearer is pinned to ONE workspace, which may live outside
	// the caller's home org (a worker delegated into another org's workspace).
	// Binding to the home org there would hide the caller's own pinned
	// workspace, i.e. deny the exact thing the credential names.
	"internal/hub/service.(*ChannelService).accessibleWorkspaceIDs": "delegation credential pinned to one workspace, which may live outside the caller's home org",
	"internal/hub/service.(*WorkerDelegationHandler).handleMint":    "mints against the workspace the delegation request names, which may live outside the caller's home org",
	// The only CONDITIONAL skip: every other site passes AnyOrg() as a fixed
	// argument, this one decides. It is a named function rather than a handler
	// prologue so a unit test can reach the decision without building a request.
	"internal/hub/service.listTabsOrgBinding": "delegation callers listing every readable tab, whose pinned workspace may live outside their home org; an explicit org_id still binds",
}

package audit

// mustNewNonLiteralSites are the production call sites allowed to pass
// something other than a string literal to userid.MustNew, each with why it is
// safe.
//
// Keys are "file#EnclosingFunc". The enclosing function is part of the key
// because a file-wide exemption blesses every OTHER call site in the same file
// too -- including ones the recorded reason below says nothing about. This file
// has three subcommand handlers; only one of them earns the exemption.
//
// The bar is MustNew's own documented contract: "the caller already knows this
// is non-empty." A literal satisfies that by inspection. Anything else has to
// earn it, and the only thing that does is a LOCAL VARIABLE this function has
// already refused when empty -- never a struct field read out of a database
// row, because a column's contents are data, not a program invariant.
//
// That bar is checked, not trusted: TestRepoInvariants requires each exempted
// site to pass an identifier that an earlier `if x == "" { return ... }` in the
// same function refuses. An exemption whose recorded reason is prose and whose
// code has no guard fails, the same way store.OwnerFilter's ok result has to
// gate a return rather than merely be called.
//
// This rule exists because the class recurred twice in one change: three mint
// sites reading a users/oauth_tokens column, then four more from a mechanical
// retype -- one of them on the unauthenticated OAuth token endpoint, where a
// blank cli_authorization_codes.user_id panicked the handler instead of
// answering invalid_grant. Both times the sites read as correct, because "is
// this column ever blank?" is a question about the database, not about the code
// in front of you.
var mustNewNonLiteralSites = map[string]string{
	"cmd/leapmux/admin_api_token.go#runAPITokenIssue": "the --user CLI flag, refused as empty by the guard on the line above the mint",
}

// typedIdentityPackages are the packages whose in-process structs must carry a
// user identity as userid.UserID rather than string. Keys are repo-relative
// directories, not package names, so two packages that share a name cannot
// opt each other in.
//
// The rule is stated as a REQUIREMENT ("must be userid.UserID") rather than as
// a ban on `string`. Banning one spelling let `*string`, `[]string`, and a
// local `type raw = string` alias through -- the same untyped identity wearing
// a hat.
//
// This is an allowlist of PACKAGES, not of fields, and deliberately so. Row
// structs, proto messages, and CRDT actor ids stay string-keyed across the repo
// -- roughly thirty fields -- because they are wire and storage shapes whose
// identity is data in flight, and typing them would demand a conversion at
// every marshal boundary for no added guarantee. Listing packages inverts that:
// a package opts IN when its structs are purely in-process credential records,
// and then EVERY UserID field in it must be typed.
var typedIdentityPackages = map[string]string{
	"internal/worker/remoteipc": "TokenInfo is the worker's in-process credential record -- no struct tags, never marshalled -- and Router.UserID already holds the same identity typed, so a string copy here could drift from it and would compare with a fail-open `!= \"\"`",
}

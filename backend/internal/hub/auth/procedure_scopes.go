package auth

import (
	"fmt"

	"connectrpc.com/connect/v2"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/rpcaccess"
)

// ScopeRequirement is what one Connect procedure demands of a SCOPED
// credential.
//
// The polarity is the whole design. adminProcedures is an ALLOWLIST, so a miss
// there fails OPEN -- which is why enforceAdmin had to move to a name prefix.
// A REQUIREMENT map's miss is the strictest possible answer instead: the zero
// value is "nobody classified this", and enforceScope refuses it to every
// scoped credential. So a procedure added tomorrow is unreachable by an app
// until somebody assigns it, and no prefix trick is needed here.
//
// It is a struct rather than a bare leapmuxv1.Scope because three of the four
// answers are not scopes at all -- unassigned, public and never -- and folding
// them into the enum would put values in scope.proto that no account can grant.
type ScopeRequirement struct {
	kind  requirementKind
	scope leapmuxv1.Scope
}

type requirementKind uint8

const (
	// reqUnassigned is the ZERO VALUE, which is what a map miss yields. It
	// denies every scoped credential, so a new procedure is fail-closed from
	// the moment it is declared.
	reqUnassigned requirementKind = iota
	// reqScope demands one named scope.
	reqScope
	// reqPublic records a procedure the interceptor waives entirely: the
	// handler authenticates itself with a credential of its own (a worker
	// registration key, a worker auth_token), or the procedure bootstraps a
	// session. The scope rung is UNREACHABLE for these, because
	// publicProcedures short-circuits first -- so this value is a record. It
	// still denies, which is the fail-closed answer if that ordering ever
	// changes: an app credential has no business calling Login or Register.
	reqPublic
	// reqNever records a deliberate refusal. See ScopeNever.
	reqNever
	// reqNotHubServed records a procedure the Hub's Connect mux never mounts.
	// See ScopeNotHubServed. It denies exactly as reqNever does; the separate
	// value keeps the two RECORDS apart, so a test can assert what the hub
	// refuses on purpose without also matching what the hub never serves.
	reqNotHubServed
)

// Requires states that a procedure demands one named scope. It panics on a
// non-grantable value, so a bad (leapmux.v1.access) option is a boot failure
// rather than a runtime denial nobody notices.
func Requires(scope leapmuxv1.Scope) ScopeRequirement {
	if !authscope.IsGrantable(scope) {
		panic(fmt.Sprintf("procedure scope %s is not grantable", scope))
	}
	return ScopeRequirement{kind: reqScope, scope: scope}
}

// ScopePublic records a procedure on publicProcedures. See reqPublic.
var ScopePublic = ScopeRequirement{kind: reqPublic}

// ScopeNever records a procedure NO grant reaches, whatever an account ticks
// on a consent screen.
//
// It marks the account's AUTHENTICATOR and CREDENTIAL surface, and the line is
// this: a scope may reach an action that changes a factor the account already
// has, because that action separately demands proof of the existing factor. A
// scope may NEVER reach an action that ADDS a factor, that manages ANOTHER
// app's credential, or that MINTS a credential -- each of those creates
// authority which outlives the app's connection, so disconnecting the app
// would no longer withdraw what it was given.
//
// It is a recorded value rather than an absence because an absence cannot
// state a reason. A reader of the proto learns the method was considered
// and refused, not that somebody forgot it.
var ScopeNever = ScopeRequirement{kind: reqNever}

// ScopeNotHubServed records a procedure the Hub's Connect mux does not mount,
// so no request ever reaches the scope rung through it.
//
// Every method of a service whose (leapmux.v1.host) is not HUB is in this
// state, and legitimately: a WORKER service is dispatched inside a Noise
// channel, and the WORKER_IPC service is the Worker's own local unix-socket
// surface. Recording them keeps procedureScopes TOTAL over the declared
// methods, and server_scope_coverage_test.go cross-checks the claim against
// the real mount list, so mounting one of these on the hub turns this record
// into a test failure instead of an unclassified procedure.
var ScopeNotHubServed = ScopeRequirement{kind: reqNotHubServed}

// Scope reports the named scope this requirement demands, if it demands one.
func (r ScopeRequirement) Scope() (leapmuxv1.Scope, bool) {
	return r.scope, r.kind == reqScope
}

// IsAssigned reports whether somebody classified this procedure.
func (r ScopeRequirement) IsAssigned() bool { return r.kind != reqUnassigned }

// IsPublic reports whether this procedure is recorded on publicProcedures.
func (r ScopeRequirement) IsPublic() bool { return r.kind == reqPublic }

// IsNever reports the deliberate refusal. It is false for ScopeNotHubServed,
// which denies for a different reason and is checked against the mount list.
func (r ScopeRequirement) IsNever() bool { return r.kind == reqNever }

// IsHubServed reports whether the Hub's Connect mux is expected to mount this
// procedure. server_scope_coverage_test.go checks the claim against the real
// mount list.
func (r ScopeRequirement) IsHubServed() bool { return r.kind != reqNotHubServed }

// procedureScopes is the requirement of every declared leapmux.v1 procedure,
// derived at init from the (leapmux.v1.host) and (leapmux.v1.access) options in
// the protos (see proto/leapmux/v1/access.proto and internal/rpcaccess). The
// proto is the one source of each classification and of its rationale, which
// sits beside the method; this map is a lookup table built from it and never
// written afterwards.
//
// The derivation:
//   - a method of a service whose host is not HUB is ScopeNotHubServed;
//   - a HUB method with no MethodAccess, or one with no requirement selected,
//     stays unassigned, which denies every scoped credential;
//   - `public` is ScopePublic;
//   - SCOPE_NEVER is ScopeNever;
//   - any other scope is Requires(scope), which panics on a non-grantable
//     value, so a bad annotation is a boot failure.
var procedureScopes = deriveProcedureScopes()

func deriveProcedureScopes() map[string]ScopeRequirement {
	out := make(map[string]ScopeRequirement)
	for _, m := range rpcaccess.All() {
		out[m.Procedure] = requirementOf(m)
	}
	return out
}

// requirementOf maps one declared method onto its ScopeRequirement.
func requirementOf(m rpcaccess.Method) ScopeRequirement {
	if m.Host != leapmuxv1.ServiceHost_SERVICE_HOST_HUB {
		return ScopeNotHubServed
	}
	switch req := m.Access.GetRequirement().(type) {
	case *leapmuxv1.MethodAccess_Public:
		return ScopePublic
	case *leapmuxv1.MethodAccess_Scope:
		if req.Scope == leapmuxv1.Scope_SCOPE_NEVER {
			return ScopeNever
		}
		return Requires(req.Scope)
	default:
		return ScopeRequirement{}
	}
}

// ScopeRequirementFor returns what a procedure demands of a scoped credential.
// A procedure nobody classified answers the zero value, which denies.
func ScopeRequirementFor(procedure string) ScopeRequirement {
	return procedureScopes[procedure]
}

// enforceScope refuses a SCOPED credential a procedure its grant does not
// reach.
//
// It runs FIRST among the authorization rungs, and the order is deliberate.
// It is the cheapest, it applies to every credential kind, and running it
// before enforceAdmin means an app with no admin:read is refused for its own
// grant rather than told whether its user is an administrator.
//
// An UNSCOPED credential passes unconditionally. That is the composition rule
// as code: a scope SUBTRACTS from the user's own authority and never adds to
// it, so this rung can only ever deny. Every other rung -- ownership, the
// admin gate, the email gate, elevation -- still applies afterwards.
func enforceScope(procedure string, userInfo *UserInfo) error {
	if userInfo == nil {
		return connect.Errorf(connect.CodeUnauthenticated, "not authenticated")
	}
	if userInfo.Scopes.IsUnscoped() {
		return nil
	}
	requirement := ScopeRequirementFor(procedure)
	scope, explicit := requirement.Scope()
	if !explicit {
		// reqUnassigned, reqPublic, reqNever and reqNotHubServed all land
		// here, and every one of them is a refusal for a scoped credential.
		// The message stays uniform, so a denial never discloses WHICH of the
		// four it was -- an app learns that it may not call the procedure, not
		// whether the hub serves it at all.
		return connect.Errorf(connect.CodePermissionDenied,
			"this app's authorization does not cover %s", procedure)
	}
	if !userInfo.Scopes.Allows(scope) {
		return connect.NewError(connect.CodePermissionDenied,
			authscope.NotGrantedDenial(scope))
	}
	return nil
}

// CeilingFor is the widest grant a credential of this kind may EVER carry.
//
// It is applied in loadBearer, at the moment the row is read, rather than only
// at the mint. So a mint bug, a hand-edited row or a restored backup cannot
// produce an over-scoped credential that authenticates: the ceiling is a
// property of the credential kind at every validation.
//
// This is the same argument the AbsoluteTokenLifetime ceiling already makes in
// apiTokenExpired -- read the limit at validation, so no issuer, present or
// future, can write past it.
func CeilingFor(kind BearerKind) authscope.ScopeSet {
	switch kind {
	case BearerKindAPI:
		// An app's ceiling is the whole grantable vocabulary: what it actually
		// holds is what its owner consented to, and the consent screen is the
		// place that narrows it.
		return authscope.EveryGrantableScope()
	case BearerKindDelegation:
		return delegationCeiling
	}
	// An unknown kind reaches nothing. ParseBearer rejects one before this is
	// called, so the branch is defence in depth.
	return authscope.ScopeSet{}
}

// delegationCeiling is what a worker-minted delegation bearer may reach,
// however wide the credential that spawned it was.
//
// It carries the guarantee the deleted delegationAllowedProcedures allowlist
// used to carry: a bearer minted for a process that reads UNTRUSTED INPUT --
// a coding agent acting on a prompt -- can never administer the hub, never
// touch the account's own profile or credentials, and never administer the
// account's workers. What it keeps from that allowlist is the hub surface:
// reading and writing the layout document, reading workspaces and tabs, and
// opening a channel to a worker.
//
// The terminal, file, git, agent and tunnel families widen it for the OTHER
// door this credential opens: the cross-worker channel. A sibling worker's
// dispatcher authenticates that channel with this bearer and restricts every
// inner method to the scope it declares, so a ceiling without them broke
// every cross-worker inner call -- a spawned agent could no longer close a
// tab, read a file or push a branch on another worker, exactly the surface
// the delegation path exists to serve. No hub Connect procedure declares any
// of those scopes (only a HUB method derives a scope in procedureScopes), so
// they widen no hub reach. The families that DO widen it -- worker:read and
// workspace:write, which reach the worker-management and workspace verbs --
// are recorded in delegation_procedures_test.go's newlyDelegationReachable
// table: the test pins the delegation surface as the old allowlist PLUS that
// recorded widening, and nothing else.
var delegationCeiling = authscope.MustNew(
	leapmuxv1.Scope_SCOPE_WORKSPACE_READ,
	leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE,
	leapmuxv1.Scope_SCOPE_WORKER_READ,
	leapmuxv1.Scope_SCOPE_AGENT_READ,
	leapmuxv1.Scope_SCOPE_AGENT_WRITE,
	leapmuxv1.Scope_SCOPE_TERMINAL_READ,
	leapmuxv1.Scope_SCOPE_TERMINAL_WRITE,
	leapmuxv1.Scope_SCOPE_FILE_READ,
	leapmuxv1.Scope_SCOPE_GIT_READ,
	leapmuxv1.Scope_SCOPE_GIT_WRITE,
	leapmuxv1.Scope_SCOPE_TUNNEL_OPEN,
)

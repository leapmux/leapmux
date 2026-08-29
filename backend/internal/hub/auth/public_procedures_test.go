package auth

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
)

// publicProcedureRationale records, for every procedure the auth interceptor
// waves through (publicProcedures), WHAT AUTHENTICATES IT INSTEAD.
//
// publicProcedures is the most dangerous list in this package: an entry skips
// the interceptor entirely, so the handler is the only thing between the
// caller and the store. Two kinds of entry are legitimate --
//
//   - genuinely unauthenticated by design (login, sign-up, system info); or
//   - authenticated by the handler itself against a credential the interceptor
//     cannot parse (a worker's auth_token or registration key).
//
// -- and nothing else. Writing the reason down is what forces an author to
// decide which one a new entry is, and gives a reviewer something to check
// against the handler.
//
// This is the sibling of delegationProcedureScope; the two lists guard
// opposite ends of the same interceptor.
var publicProcedureRationale = map[string]string{
	// Unauthenticated by design: these are how a caller ACQUIRES a credential,
	// so requiring one would be circular.
	leapmuxv1connect.AuthServiceLoginProcedure:                 "credential-acquiring: verifies username/password itself",
	leapmuxv1connect.AuthServiceSignUpProcedure:                "credential-acquiring: gated by the signup-enabled config and first-admin rules",
	leapmuxv1connect.AuthServiceGetSystemInfoProcedure:         "discloses only pre-login system facts (auth modes, setup state)",
	leapmuxv1connect.AuthServiceGetOAuthProvidersProcedure:     "discloses only which OAuth providers are configured, pre-login",
	leapmuxv1connect.AuthServiceGetPendingOAuthSignupProcedure: "keyed by the single-use pending-signup id issued by the OAuth callback",
	leapmuxv1connect.AuthServiceCompleteOAuthSignupProcedure:   "keyed by the single-use pending-signup id issued by the OAuth callback",
	leapmuxv1connect.AuthServiceGetAltchaChallengeProcedure:    "discloses only a self-authenticating PoW challenge (no secret); needed pre-login so the captcha widget can arm Login/SignUp",
	leapmuxv1connect.AuthServiceBeginPasskeyLoginProcedure:     "credential-acquiring: starts a passkey assertion ceremony",
	leapmuxv1connect.AuthServiceFinishPasskeyLoginProcedure:    "credential-acquiring: verifies a passkey assertion and mints a session",
	leapmuxv1connect.AuthServiceBeginPasskeySignUpProcedure:    "credential-acquiring: starts passkey registration during sign-up",
	leapmuxv1connect.AuthServiceFinishPasskeySignUpProcedure:   "credential-acquiring: verifies passkey registration and creates the account",
	leapmuxv1connect.AuthServiceRequestPasswordResetProcedure:  "self-service break-glass: issues a reset email when SMTP is configured",
	leapmuxv1connect.AuthServiceCompletePasswordResetProcedure: "self-service break-glass: consumes a single-use reset token and sets a new password",

	// Self-authenticating against a worker credential the interceptor cannot
	// parse. Each handler resolves the caller itself and refuses without it.
	leapmuxv1connect.WorkerConnectorServiceRegisterProcedure:                "handler validates the registration key in its own Authorization header",
	leapmuxv1connect.WorkerConnectorServiceConnectProcedure:                 "handler validates the worker auth_token in the stream's Authorization header",
	leapmuxv1connect.WorkerReconcilerServiceListOwnedTabsForWorkerProcedure: "AuthenticateWorkerBearer resolves the worker and its registrant from the auth_token, and scopes the response to that owner",
}

// TestPublicProceduresAreRationaleClassified is a tripwire coupling the
// interceptor bypass list to a recorded reason.
//
// If it fails because a procedure is unclassified, the fix is NOT to blindly
// add it here: confirm the handler either needs no credential at all or
// authenticates one itself, THEN record which. The reverse direction matters
// just as much -- a stale rationale for a procedure that left the list is a
// reader's false assurance that a bypass is still reviewed.
func TestPublicProceduresAreRationaleClassified(t *testing.T) {
	for procedure := range publicProcedures {
		note, ok := publicProcedureRationale[procedure]
		assert.Truef(t, ok,
			"public procedure %q is not classified: it bypasses the auth interceptor, so record what authenticates it (nothing, by design -- or the handler itself) in publicProcedureRationale",
			procedure)
		assert.NotEmptyf(t, note, "public procedure %q has an empty rationale", procedure)
	}
	for procedure := range publicProcedureRationale {
		assert.Truef(t, publicProcedures[procedure],
			"%q has a public-procedure rationale but is no longer public; drop the entry so the list keeps meaning what it says",
			procedure)
	}
}

// TestPublicAndDelegationListsAreDisjoint pins that the two allowlists never
// name the same procedure.
//
// They answer different questions -- "may an unauthenticated request through?"
// versus "what does a scoped credential need to call this?" -- and the scope
// rung runs only for an AUTHENTICATED caller, which a public procedure
// short-circuits before reaching. An entry in both would therefore read as a
// reviewed scope requirement while the interceptor was already waving every
// caller through.
//
// ScopePublic is exactly that record, and this asserts the two sets are the
// SAME set rather than merely disjoint: a public procedure with a named scope
// would document a bound nothing enforces, and a ScopePublic record for a
// procedure that is not public would document a waiver that does not exist.
func TestPublicProceduresAreRecordedAsScopePublic(t *testing.T) {
	for procedure := range publicProcedures {
		assert.Truef(t, ScopeRequirementFor(procedure).IsPublic(),
			"%q is public but not recorded as ScopePublic in procedureScopes", procedure)
	}
	for procedure, requirement := range procedureScopes {
		if requirement.IsPublic() {
			assert.Truef(t, publicProcedures[procedure],
				"%q is recorded as ScopePublic but the interceptor does not waive it", procedure)
		}
	}
}

// TestEveryWorkerBoundProcedureIsExempt derives the exempt set from the side that
// actually needs it: the WORKER's own hub client.
//
// Every RPC the worker calls with its own credential must bypass the interceptor,
// because the interceptor cannot parse a worker auth_token -- and the failure mode
// is silent and permanent, a handler that answers `unauthenticated` forever. That
// is exactly how ListOwnedTabsForWorker shipped with the orphan reconciler never
// once running.
//
// It scans `internal/worker/hub`, the package holding the worker's OWN connection,
// rather than scanning the hub's handlers for a credential-resolving call. The
// handler-side scan this replaces matched one literal (AuthenticateWorkerBearer) on
// one receiver shape in one directory, while interceptor.go itself names THREE
// mechanisms -- a registration key, Workers().GetByAuthToken, and the bearer -- so
// two of the three were held only by the hand-maintained rationale map above. A new
// worker->hub RPC must be called from this package whatever mechanism it
// authenticates with, so deriving from here covers all of them and any fourth.
//
// Deliberately NOT scanned: internal/worker/controlipc, whose HubBridge and
// HubEventStreamer call hub RPCs on a USER's behalf with a delegation bearer. Those
// carry an ordinary user credential the interceptor parses, so exempting them would
// widen the bypass rather than describe it.
func TestEveryWorkerBoundProcedureIsExempt(t *testing.T) {
	const workerHubDir = "../../worker/hub"
	entries, err := os.ReadDir(workerHubDir)
	require.NoError(t, err)

	fset := token.NewFileSet()
	called := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(workerHubDir, name), nil, 0)
		require.NoError(t, perr, "parsing %s", name)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// A method invoked on one of the client fields, e.g.
			// c.reconciler.ListOwnedTabsForWorker(...) / client.Register(...).
			if recv, ok := sel.X.(*ast.SelectorExpr); ok && workerHubClientFields[recv.Sel.Name] {
				called[sel.Sel.Name] = true
			}
			if id, ok := sel.X.(*ast.Ident); ok && workerHubClientLocals[id.Name] {
				called[sel.Sel.Name] = true
			}
			return true
		})
	}
	require.NotEmpty(t, called,
		"scanned no worker->hub calls; the scan is broken, not the code")

	var missing []string
	for method := range called {
		if !anyPublicProcedureEndsWith(method + "Procedure") {
			missing = append(missing, fmt.Sprintf(
				"internal/worker/hub calls %s on its hub client, but no publicProcedures entry ends in %sProcedure -- the interceptor will reject the worker's credential before the handler runs",
				method, method))
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Error(m)
	}
}

// workerHubClientFields / workerHubClientLocals name the Client fields and local
// variables that hold generated Connect clients for the hub. Listed rather than
// type-resolved so the scan needs no type checker; a field added here without a
// matching entry simply is not scanned, which the NotEmpty guard above keeps from
// silently emptying the whole set.
var workerHubClientFields = map[string]bool{
	"connector":  true,
	"reconciler": true,
}

var workerHubClientLocals = map[string]bool{
	"client": true,
}

// anyPublicProcedureEndsWith reports whether an exempt procedure exists whose
// constant name ends in the given suffix.
//
// Suffix rather than exact match because a call site knows only the METHOD
// (ListOwnedTabsForWorker) while the constant carries its service
// (WorkerReconcilerServiceListOwnedTabsForWorkerProcedure). Membership still
// resolves through knownProcedurePaths to the generated PATH, so a proto rename
// cannot make this pass vacuously.
func anyPublicProcedureEndsWith(suffix string) bool {
	for constant := range knownProcedurePaths {
		if strings.HasSuffix(constant, suffix) && publicProceduresContainsConstant(constant) {
			return true
		}
	}
	return false
}

// publicProceduresContainsConstant checks membership by the procedure's
// generated PATH, resolved through the same leapmuxv1connect constants
// publicProcedures is keyed by -- so a proto rename cannot make this test pass
// vacuously.
func publicProceduresContainsConstant(constant string) bool {
	path, ok := knownProcedurePaths[constant]
	if !ok {
		// An unmapped constant is a FAILURE to resolve, not a pass: returning
		// true here would let a new self-authenticating service slip through.
		return false
	}
	return publicProcedures[path]
}

// knownProcedurePaths maps the constant names this test derives to the generated
// procedure paths. Kept explicit because Go has no reflection over package-level
// constants; a service added here without an entry fails the test above, which
// is the intended nudge.
var knownProcedurePaths = map[string]string{
	"WorkerReconcilerServiceListOwnedTabsForWorkerProcedure": leapmuxv1connect.WorkerReconcilerServiceListOwnedTabsForWorkerProcedure,
	"WorkerConnectorServiceRegisterProcedure":                leapmuxv1connect.WorkerConnectorServiceRegisterProcedure,
	"WorkerConnectorServiceConnectProcedure":                 leapmuxv1connect.WorkerConnectorServiceConnectProcedure,
}

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
	leapmuxv1connect.AuthServiceLoginProcedure:                           "credential-acquiring: verifies username/password itself",
	leapmuxv1connect.AuthServiceSetInitialSoloPasswordProcedure:          "credential-acquiring: atomically claims a passwordless solo account and creates a session",
	leapmuxv1connect.AuthServiceSignUpProcedure:                          "credential-acquiring: requires the signup-enabled config and the first-admin rules",
	leapmuxv1connect.SystemServiceGetSystemInfoProcedure:                 "discloses only pre-login system facts (auth modes, setup state)",
	leapmuxv1connect.AuthServiceGetOAuthProvidersProcedure:               "discloses only which OAuth providers are configured, pre-login",
	leapmuxv1connect.AuthServiceGetPendingOAuthSignupProcedure:           "keyed by the single-use pending-signup id issued by the OAuth callback",
	leapmuxv1connect.AuthServiceCompleteOAuthSignupProcedure:             "keyed by the single-use pending-signup id issued by the OAuth callback",
	leapmuxv1connect.AuthServiceGetAltchaChallengeProcedure:              "discloses only a self-authenticating PoW challenge (no secret); needed pre-login so the captcha widget can arm Login/SignUp",
	leapmuxv1connect.AuthServiceBeginPasskeyLoginProcedure:               "credential-acquiring: starts a passkey assertion ceremony",
	leapmuxv1connect.AuthServiceFinishPasskeyLoginProcedure:              "credential-acquiring: verifies a passkey assertion and mints a session",
	leapmuxv1connect.AuthServiceBeginPasskeySignUpProcedure:              "credential-acquiring: starts passkey registration during sign-up",
	leapmuxv1connect.AuthServiceFinishPasskeySignUpProcedure:             "credential-acquiring: verifies passkey registration and creates the account",
	leapmuxv1connect.AuthServiceRequestAccountRecoveryProcedure:          "self-service break-glass: issues an account-recovery email when SMTP is configured",
	leapmuxv1connect.AuthServiceCompleteAccountRecoveryPasswordProcedure: "self-service break-glass: consumes a single-use recovery link and sets a new password",
	leapmuxv1connect.AuthServiceBeginAccountRecoveryPasskeyProcedure:     "self-service break-glass: consumes a single-use recovery link and starts a passkey re-enrollment ceremony",
	leapmuxv1connect.AuthServiceFinishAccountRecoveryPasskeyProcedure:    "self-service break-glass: consumes a single-use recovery link (re-checked at write time) and the ceremony session Begin minted",

	// Self-authenticating against a worker credential the interceptor cannot
	// parse. Each handler resolves the caller itself and refuses without it.
	leapmuxv1connect.WorkerConnectorServiceRegisterProcedure:               "handler validates the registration key in its own Authorization header",
	leapmuxv1connect.WorkerConnectorServiceConnectProcedure:                "handler validates the worker auth_token in the stream's Authorization header",
	leapmuxv1connect.WorkerConnectorServiceListOwnedTabsForWorkerProcedure: "AuthenticateWorkerBearer resolves the worker and its registrant from the auth_token, and scopes the response to that owner",
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
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(workerHubDir, name), nil, 0)
		require.NoError(t, perr, "parsing %s", name)
		files = append(files, f)
	}
	fields, locals := hubClientHolders(files)
	require.NotEmpty(t, fields,
		"found no struct field that holds a generated hub client; the scan is broken, not the code")

	called := map[string]bool{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// A method invoked on a client field, e.g.
			// c.connectorUnary.ListOwnedTabsForWorker(...).
			if recv, ok := sel.X.(*ast.SelectorExpr); ok && fields[recv.Sel.Name] {
				called[sel.Sel.Name] = true
			}
			// A method invoked on a client variable, e.g. client.Register(...).
			if id, ok := sel.X.(*ast.Ident); ok && locals[id.Name] {
				called[sel.Sel.Name] = true
			}
			return true
		})
	}
	require.NotEmpty(t, called,
		"scanned no worker->hub calls; the scan is broken, not the code")
	require.True(t, called["ListOwnedTabsForWorker"],
		"the scan missed the orphan reconciler's ListOwnedTabsForWorker call, the one this test exists for; the scan is broken, not the code")

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

// hubClientHolders finds the names that hold a generated Connect client in the
// parsed files:
//   - fields: struct fields typed leapmuxv1connect.<Service>Client
//   - locals: parameters and results of that type, and variables assigned from a
//     leapmuxv1connect.New<Service>Client call
//
// The names come from the types, not from a hand-kept list, because a renamed
// field drops out of a list with no failure: that is how the orphan
// reconciler's ListOwnedTabsForWorker call left this scan once. The scan stays
// syntactic, so it needs no type checker; a local of another type that shares
// a holder's name only adds methods to check, never hides one.
func hubClientHolders(files []*ast.File) (fields, locals map[string]bool) {
	fields, locals = map[string]bool{}, map[string]bool{}
	addNames := func(into map[string]bool, names []*ast.Ident) {
		for _, name := range names {
			into[name.Name] = true
		}
	}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.StructType:
				for _, field := range n.Fields.List {
					if isGeneratedClientType(field.Type) {
						addNames(fields, field.Names)
					}
				}
			case *ast.FuncType:
				for _, list := range []*ast.FieldList{n.Params, n.Results} {
					if list == nil {
						continue
					}
					for _, field := range list.List {
						if isGeneratedClientType(field.Type) {
							addNames(locals, field.Names)
						}
					}
				}
			case *ast.ValueSpec:
				if isGeneratedClientType(n.Type) {
					addNames(locals, n.Names)
				}
				for i, value := range n.Values {
					if i < len(n.Names) && isGeneratedClientConstructor(value) {
						locals[n.Names[i].Name] = true
					}
				}
			case *ast.AssignStmt:
				for i, rhs := range n.Rhs {
					if i >= len(n.Lhs) || !isGeneratedClientConstructor(rhs) {
						continue
					}
					if id, ok := n.Lhs[i].(*ast.Ident); ok {
						locals[id.Name] = true
					}
				}
			}
			return true
		})
	}
	return fields, locals
}

// isGeneratedClientType reports whether expr spells leapmuxv1connect.<X>Client.
// A stream type such as <X>ConnectClientStream does not match: its Send and
// Receive are not RPCs.
func isGeneratedClientType(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "leapmuxv1connect" && strings.HasSuffix(sel.Sel.Name, "Client")
}

// isGeneratedClientConstructor reports whether expr calls
// leapmuxv1connect.New<X>Client.
func isGeneratedClientConstructor(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "leapmuxv1connect" &&
		strings.HasPrefix(sel.Sel.Name, "New") && strings.HasSuffix(sel.Sel.Name, "Client")
}

// anyPublicProcedureEndsWith reports whether an exempt procedure exists whose
// constant name ends in the given suffix.
//
// Suffix rather than exact match because a call site knows only the METHOD
// (ListOwnedTabsForWorker) while the constant carries its service
// (WorkerConnectorServiceListOwnedTabsForWorkerProcedure). Membership still
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
	"WorkerConnectorServiceListOwnedTabsForWorkerProcedure": leapmuxv1connect.WorkerConnectorServiceListOwnedTabsForWorkerProcedure,
	"WorkerConnectorServiceRegisterProcedure":               leapmuxv1connect.WorkerConnectorServiceRegisterProcedure,
	"WorkerConnectorServiceConnectProcedure":                leapmuxv1connect.WorkerConnectorServiceConnectProcedure,
}

package auth_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// useridPkgPath is the import path the completeness scan resolves a file's
// userid identifier from, rather than assuming the conventional spelling.
const useridPkgPath = "github.com/leapmux/leapmux/internal/util/userid"

// zeroUserIDDenyFuncs names every exported auth predicate that carries a caller
// identity -- as a userid.UserID or inside a *UserInfo -- and must fail closed
// on the zero value. The AST completeness test below requires this table to
// stay in lockstep with the package's exported surface -- the same shape as
// Dispatcher.Methods() vs the gate map. Names are receiver-qualified.
var zeroUserIDDenyFuncs = []string{
	"IsOwner",
	"WorkspaceCanAccess",
	"WorkspaceReadableByUsers",
	"WorkspacesReadableByUser",
	"WorkerCanUse",
	"CreateSession",
	"ResolveDelegationWorkerScope",
	"CheckDelegationWorkerScope",
	"(*DelegationScopeCache).Resolve",
	"RevokeAllUserCredentials",
}

// TestZeroUserIDDenies exercises the deny-closed contract for every entry in
// zeroUserIDDenyFuncs. A zero UserID must never grant access, independent of
// whether the other arguments are well-formed.
//
// Every case runs against a REAL seeded workspace/worker owned by a REAL user,
// and every case pairs its deny assertion with a control proving that same row
// IS reachable by its owner. Both halves are load-bearing: an earlier version
// of this test passed ids that were never seeded ("ws", "worker") into an empty
// store, so each predicate denied because the row did not exist rather than
// because the id was zero -- stripping Matches down to `u.id == stored` AND
// deleting all three IsZero() prologues left the whole package green. The
// control assertion is what makes "denied" mean "denied for the right reason".
func TestZeroUserIDDenies(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	var zero userid.UserID

	owner := storetest.SeedUser(t, st, "owner")
	ownerID := userid.MustNew(owner.ID)
	workspaceID := storetest.SeedWorkspace(t, st, owner.ID, "ws")
	worker := storetest.SeedWorker(t, st, owner.ID)

	// The blank-owner fixture these cases used to build is gone, and what that
	// costs is worth stating rather than leaving to be rediscovered.
	//
	// owner_user_id and registered_by are `NOT NULL REFERENCES users(id)`, and
	// CreateUserParams.Validate now refuses a blank users.id -- so no blank-owner
	// workspace and no blank-registrant worker can exist. That removes the
	// empty-vs-empty pairing (a zero caller against a blank STORED id) from
	// everything below that reads a row, which is the pairing userid.UserID
	// exists to close.
	//
	// It is still pinned, just not from here: userid_test.go asserts
	// `UserID{}.Matches("")` is false directly, and the IsOwner case below
	// asserts it against an in-memory blank-owner workspace that needs no row at
	// all. What the store-backed cases pin now is the other half -- that a zero
	// caller is refused against a REAL owner's rows -- which stays reachable and
	// still fails if a predicate stops comparing at all.
	//
	// A second real user's worker stands in for the delegation cases, where the
	// question is whether a principal inherits the minter's owner reach.
	stranger := storetest.SeedUser(t, st, "stranger")
	strangerWorker := storetest.SeedWorker(t, st, stranger.ID)

	for _, name := range zeroUserIDDenyFuncs {
		t.Run(name, func(t *testing.T) {
			switch name {
			case "IsOwner":
				ws := &store.Workspace{OwnerUserID: owner.ID}
				require.True(t, auth.IsOwner(ws, ownerID), "control: the owner must match")
				assert.False(t, auth.IsOwner(ws, zero))
				// A blank OwnerUserID must not be matched by a zero id either --
				// the empty-vs-empty pairing is the whole reason for the type.
				assert.False(t, auth.IsOwner(&store.Workspace{OwnerUserID: ""}, zero),
					"empty owner must not match an empty caller")
			case "WorkspaceCanAccess":
				ok, err := auth.WorkspaceCanAccess(ctx, st, workspaceID, ownerID)
				require.NoError(t, err)
				require.True(t, ok, "control: the owner must be able to read the seeded workspace")

				// Answered by the IsZero() prologue, before the row is ever
				// loaded -- so this pins the prologue, NOT the empty-vs-empty
				// refusal. That refusal is what IsOwner (which has no prologue
				// and is the predicate this delegates the comparison to) asserts
				// directly above.
				ok, err = auth.WorkspaceCanAccess(ctx, st, workspaceID, zero)
				require.NoError(t, err)
				assert.False(t, ok, "a zero caller must not read another user's workspace")
			case "WorkspaceReadableByUsers":
				// The batch form takes a slice, so a zero entry must be dropped
				// rather than short-circuiting the whole call: the real owner in
				// the SAME batch must still resolve. Passing both ids together is
				// what pins that -- a zero-only batch would pass even if the
				// implementation bailed out on the first blank entry.
				got, err := auth.WorkspaceReadableByUsers(
					ctx, st, workspaceID, []userid.UserID{zero, ownerID})
				require.NoError(t, err)
				assert.Equal(t, map[string]bool{owner.ID: true}, got,
					"the zero entry drops out and the real owner still resolves")
			case "WorkspacesReadableByUser":
				got, err := auth.WorkspacesReadableByUser(ctx, st, ownerID, []string{workspaceID})
				require.NoError(t, err)
				require.Equal(t, []string{workspaceID}, got, "control: the owner reads their own workspace")

				got, err = auth.WorkspacesReadableByUser(ctx, st, zero, []string{workspaceID})
				require.NoError(t, err)
				assert.Empty(t, got, "a zero id must not resolve another user's workspace")
			case "CreateSession":
				// Not an access predicate, but it takes the type, so the table
				// requires it to state a contract: a session must never be
				// written for an id that names no user -- that row would then
				// authenticate as blank on every subsequent request.
				_, _, err := auth.CreateSession(ctx, st, zero)
				require.Error(t, err, "a zero UserID must not create a session")
				// Control, per this test's own contract: without it the case
				// passes if CreateSession ever starts erroring for an unrelated
				// reason (a schema change, a newly-required column), proving
				// nothing about the zero id.
				_, _, err = auth.CreateSession(ctx, st, ownerID)
				require.NoError(t, err, "control: a real user's session is created")
			case "WorkerCanUse":
				w, ok, err := auth.WorkerCanUse(ctx, st, worker.ID, ownerID)
				require.NoError(t, err)
				require.True(t, ok, "control: the registrant may use the seeded worker")
				require.NotNil(t, w)

				// The deny comes from the `workerID == "" || userID.IsZero()`
				// prologue, which returns before the row is loaded, so this pins
				// the prologue rather than the empty-vs-empty refusal. The
				// refusal itself is asserted where it is reachable without a
				// row: IsOwner above, and userid_test.go directly.
				w, ok, err = auth.WorkerCanUse(ctx, st, worker.ID, zero)
				require.NoError(t, err)
				assert.False(t, ok, "a zero caller must not reach another user's worker")
				assert.Nil(t, w, "a refused reach must not hand back the worker row either")
			case "RevokeAllUserCredentials":
				// A REVOKE path, so the polarity is inverted: here "false" would
				// mean "do not revoke". A zero id must not report a successful
				// revocation having revoked nothing -- an operator's one
				// containment action would silently no-op.
				api, deleg, err := auth.RevokeAllUserCredentials(ctx, st, zero)
				require.Error(t, err, "a zero UserID must not report a successful revocation")
				assert.Zero(t, api)
				assert.Zero(t, deleg)

				// Control: a real user's revocation succeeds, so the refusal
				// above is about the id and not about revocation never working.
				_, _, err = auth.RevokeAllUserCredentials(ctx, st, ownerID)
				require.NoError(t, err, "control: a real user's credentials revoke")
			case "ResolveDelegationWorkerScope":
				// `user.ID.Matches(minter.RegisteredBy)` decides whether a
				// delegation token gets its minter's OWNER reach (every worker
				// that user has) or minter-only reach. A zero principal must get
				// minter-only, against a minter registered by anyone.
				//
				// The minter used to be a blank-REGISTRANT worker, which made
				// this the sharpest empty-vs-empty pairing in the package. That
				// row is unrepresentable now (see the fixture note above), so the
				// minter is a real stranger's worker and what remains pinned is
				// the reach, not the pairing.
				zeroPrincipal := &auth.UserInfo{
					Credential: auth.DelegationCredential("d-zero", strangerWorker.ID),
				}
				scope, err := auth.ResolveDelegationWorkerScope(ctx, st, zeroPrincipal)
				require.NoError(t, err)
				assert.True(t, scope.Allows(strangerWorker.ID),
					"a token always reaches the worker that minted it")
				assert.False(t, scope.Allows(worker.ID),
					"a zero principal must not inherit the registrant's owner reach")

				// Control: a REAL registrant does widen the scope, so the denial
				// above is about the zero id -- not about the scope never widening.
				ownerWorker2 := storetest.SeedWorker(t, st, owner.ID)
				ownerPrincipal := &auth.UserInfo{
					ID:         ownerID,
					Credential: auth.DelegationCredential("d-owner", worker.ID),
				}
				ownerScope, err := auth.ResolveDelegationWorkerScope(ctx, st, ownerPrincipal)
				require.NoError(t, err)
				assert.True(t, ownerScope.Allows(ownerWorker2.ID),
					"control: the minter's real owner reaches their other workers")
			case "CheckDelegationWorkerScope":
				// The enforcing wrapper around the same comparison: a zero
				// principal must be refused for any worker but the minter.
				zeroPrincipal := &auth.UserInfo{
					Credential: auth.DelegationCredential("d-zero", strangerWorker.ID),
				}
				require.NoError(t,
					auth.CheckDelegationWorkerScope(ctx, st, zeroPrincipal, strangerWorker.ID),
					"control: the minter itself stays reachable")
				assert.ErrorIs(t,
					auth.CheckDelegationWorkerScope(ctx, st, zeroPrincipal, worker.ID),
					auth.ErrDelegationWorkerOutOfScope,
					"a zero principal must not reach beyond the minter")
			case "(*DelegationScopeCache).Resolve":
				// The cache keys on user.ID.String(), so a zero principal must
				// resolve to the same fail-closed scope the uncached path gives
				// -- never share a cache entry with, or inherit the answer of,
				// a real user.
				cache := auth.NewDelegationScopeCache(st)
				zeroPrincipal := &auth.UserInfo{
					Credential: auth.DelegationCredential("d-zero", strangerWorker.ID),
				}
				scope, err := cache.Resolve(ctx, zeroPrincipal)
				require.NoError(t, err)
				assert.True(t, scope.Allows(strangerWorker.ID),
					"a token always reaches the worker that minted it")
				assert.False(t, scope.Allows(worker.ID),
					"a zero principal must not inherit the registrant's owner reach through the cache")

				// And again from the warm cache: a second call must not widen.
				scope, err = cache.Resolve(ctx, zeroPrincipal)
				require.NoError(t, err)
				assert.False(t, scope.Allows(worker.ID),
					"the cached answer must be the same denial, not a widened one")
			default:
				t.Fatalf("zeroUserIDDenyFuncs lists %q but TestZeroUserIDDenies has no case for it", name)
			}
		})
	}
}

// zeroUserIDNonDeciders names exported auth functions that CARRY a caller
// identity but make no authorization decision from it, mapped to why.
//
// The completeness test below requires every identity-carrying exported
// function to sit in exactly one of the two tables, so "this one needs no deny
// case" becomes a recorded judgment rather than an omission. Moving a function
// here is the deliberate act; forgetting it entirely is a failure.
var zeroUserIDNonDeciders = map[string]string{
	"WithUser":                 "stores the principal on a context; the predicates that read it are the deciders",
	"NewInterceptor":           "constructor; it authenticates requests rather than authorizing a principal it was handed",
	"NewInterceptorWithTokens": "constructor; same as NewInterceptor",
	"(*AuthContextRegistry).RegisterAuthenticatedLease": "refuses on a per-user quota keyed on the id, but a zero id could only share the leasesByUser[\"\"] bucket and over-refuse; it never widens reach. Unreachable besides -- every AuthenticateHTTP arm mints through userid.New and refuses a blank",
	"(*AuthContextRegistry).CurrentSyntheticUser":       "returns the configured solo identity; it answers no question about the caller",
	"(*AuthContextRegistry).IsAuthContextCurrent":       "compares cache generations, not identities -- a zero id is stale-vs-current, not allowed-vs-denied",
	"(*AuthContextRegistry).CurrentCredentialExpiry":    "reads a deadline off the credential; identity selects the record, it does not gate it",
}

// TestEveryUserIDCarryingFuncIsClassified is the default-deny companion: every
// exported auth function that carries a caller identity -- as a userid.UserID
// (bare or behind a slice/pointer/map), or inside a *UserInfo -- must appear in
// zeroUserIDDenyFuncs (with a real zero-deny case) or in zeroUserIDNonDeciders
// (with a stated reason).
//
// The *UserInfo half matters most: it is the package's DOMINANT predicate
// shape. ResolveDelegationWorkerScope and CheckDelegationWorkerScope both
// compare user.ID.Matches(...) against a stored registrant, so a future edit
// rewriting either as user.ID.String() == ... would restore empty-vs-empty
// matching on the delegation-scope path. Before this test looked inside
// *UserInfo, that mutation left the whole package green.
func TestEveryUserIDCarryingFuncIsClassified(t *testing.T) {
	dir, err := os.Getwd()
	require.NoError(t, err)

	var found []string
	testutil.ForEachPackageSourceFile(t, dir, func(_ *token.FileSet, file *ast.File) {
		// Resolved from the import PATH, not assumed to be the identifier
		// "userid": a file spelling `uid "…/internal/util/userid"` would
		// otherwise take every predicate it declares outside this net, which is
		// the same bypass internal/audit closed with testutil.ImportedAs.
		useridAlias, _ := testutil.ImportedAs(file, useridPkgPath)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil || !fn.Name.IsExported() || fn.Type.Params == nil {
				continue
			}
			if hasIdentityParam(useridAlias, fn.Type.Params) {
				// Receiver-qualified so two same-named methods on different
				// types cannot share one classification.
				found = append(found, testutil.QualifiedFuncName(fn))
			}
		}
	})

	classified := append([]string(nil), zeroUserIDDenyFuncs...)
	for name := range zeroUserIDNonDeciders {
		classified = append(classified, name)
	}
	assert.ElementsMatch(t, classified, found,
		"every exported auth func carrying a caller identity must appear in zeroUserIDDenyFuncs (with a zero-deny case in TestZeroUserIDDenies) or in zeroUserIDNonDeciders (with a reason)")
}

// hasIdentityParam reports whether params includes a parameter that carries a
// caller identity: a userid.UserID -- directly, or behind a slice / array /
// pointer / variadic / map value -- or a *UserInfo, which holds one.
//
// Both shapes need the same net. The batch predicates take []userid.UserID and
// need the zero-deny contract just as much as the single-id ones (a zero entry
// must be dropped, not matched). And matching only the bare type would leave
// every *UserInfo-taking predicate outside the table -- the delegation-scope
// decisions among them -- which is the blind spot this test exists to close.
func hasIdentityParam(useridAlias string, params *ast.FieldList) bool {
	if params == nil {
		return false
	}
	for _, field := range params.List {
		if carriesUserID(useridAlias, field.Type) || carriesUserInfo(field.Type) {
			return true
		}
	}
	return false
}

// carriesUserID unwraps the type constructors a UserID can hide behind and
// reports whether userid.UserID sits at the bottom.
func carriesUserID(useridAlias string, expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.SelectorExpr:
		pkg, ok := t.X.(*ast.Ident)
		return ok && useridAlias != "" && pkg.Name == useridAlias && t.Sel.Name == "UserID"
	case *ast.ArrayType:
		return carriesUserID(useridAlias, t.Elt)
	case *ast.StarExpr:
		return carriesUserID(useridAlias, t.X)
	case *ast.Ellipsis:
		return carriesUserID(useridAlias, t.Elt)
	case *ast.MapType:
		return carriesUserID(useridAlias, t.Key) || carriesUserID(useridAlias, t.Value)
	default:
		return false
	}
}

// carriesUserInfo mirrors carriesUserID for *UserInfo. The scanned files are
// package auth's own, so the type is written unqualified there.
func carriesUserInfo(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == "UserInfo"
	case *ast.ArrayType:
		return carriesUserInfo(t.Elt)
	case *ast.StarExpr:
		return carriesUserInfo(t.X)
	case *ast.Ellipsis:
		return carriesUserInfo(t.Elt)
	case *ast.MapType:
		return carriesUserInfo(t.Key) || carriesUserInfo(t.Value)
	default:
		return false
	}
}

// TestCarriesUserID_KeysOnTheImportedIdentifier pins the alias resolution that
// decides which declarations this net can even see.
//
// Keying on the literal identifier "userid" meant a file spelling
// `uid "…/internal/util/userid"` took every predicate it declares outside the
// net at once -- and the only thing that would have complained is the table's
// own stale-entry half, which fires solely because those names happen to be
// listed today. A NEW predicate in such a file would have been born
// unclassified and green.
func TestCarriesUserID_KeysOnTheImportedIdentifier(t *testing.T) {
	for name, tc := range map[string]struct {
		alias string
		expr  string
		want  bool
	}{
		"unaliased import": {"userid", "userid.UserID", true},
		"aliased import":   {"uid", "uid.UserID", true},
		"identifier the file uses is not the alias": {"uid", "userid.UserID", false},
		"package not imported at all":               {"", "userid.UserID", false},
		"behind a slice":                            {"userid", "[]userid.UserID", true},
		"behind a pointer":                          {"userid", "*userid.UserID", true},
		"as a map value":                            {"userid", "map[string]userid.UserID", true},
		"some other package's UserID":               {"userid", "other.UserID", false},
		"plain string":                              {"userid", "string", false},
	} {
		t.Run(name, func(t *testing.T) {
			expr, err := parser.ParseExpr(tc.expr)
			require.NoError(t, err)
			assert.Equal(t, tc.want, carriesUserID(tc.alias, expr))
		})
	}
}

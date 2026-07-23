package audit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

// The detection helpers are what every rule in this package rests on, and a
// silent regression in one of them is worse than no rule at all: the sub-tests
// keep passing while covering nothing. TestRepoInvariants guards against a
// TOTAL failure (each rule asserts it matched something), but not against a
// partial one -- dropping a single owner column from ownerBindRe would quietly
// stop covering every worker query while the suite stayed green. These pin the
// behaviour directly, including the false-positive shapes that actually bit
// while the rules were being written and the bypasses a review later found.

func TestWhereClause_IgnoresEverythingBeforeWHERE(t *testing.T) {
	// The shape that bit: an UPDATE whose SET assigns an owner column. It binds
	// a VALUE, and reading it as a predicate flagged two device-authorization
	// queries that decide nothing about ownership.
	where, ok := whereClause("UPDATE device_authorizations\nSET approved = 1, user_id = ?\nWHERE device_code = ? AND consumed_at IS NULL;")
	require.True(t, ok)
	assert.NotContains(t, where, "SET", "the SET clause must be excluded")
	assert.False(t, ownerBindRe.MatchString(where),
		"an owner column assigned in SET is not an ownership predicate")

	// A genuine predicate is still found.
	where, ok = whereClause("SELECT * FROM workspaces WHERE is_deleted = 0 AND owner_user_id = ?;")
	require.True(t, ok)
	assert.True(t, ownerBindRe.MatchString(where))

	// No WHERE at all -- an INSERT -- is out of scope, which is what keeps the
	// rule free of a write-path allowlist.
	_, ok = whereClause("INSERT INTO workspaces (id, owner_user_id) VALUES (?, ?);")
	assert.False(t, ok, "a query with no WHERE binds no predicate")
}

func TestStripSQLComments_PreventsProseFromReadingAsSQL(t *testing.T) {
	// The exact shape that bit, quoted from mysql/db/queries/workers.sql: the
	// file argues at length about why the admin listing is NOT written as an
	// opt-in probe, and spells the rejected predicate out verbatim. Matching
	// that prose classified an unrelated public-key lookup as an ownership
	// query, because a query body runs to the next name marker and so swallows
	// the commentary that follows it.
	body := "SELECT public_key FROM workers WHERE id = ?;\n" +
		"-- be an opt-in `(? IS NULL OR registered_by = ?)` probe: MySQL's\n"
	assert.True(t, ownerBindRe.MatchString(body), "control: the prose does match before stripping")

	stripped := stripSQLComments(body)
	assert.False(t, ownerBindRe.MatchString(stripped),
		"an owner column named inside a comment is not a predicate")
	assert.Contains(t, stripped, "SELECT public_key", "real SQL survives stripping")
}

func TestOwnerBindRe_CoversEveryColumnAndEveryParameterSpelling(t *testing.T) {
	// Each column this repo uses to name a row's owner, in every spelling a
	// bound parameter can take. A column or a spelling silently missing from
	// this regex does not fail anything -- the query is simply never classified
	// as an ownership predicate, so its adapter is never required to refuse an
	// unminted caller. That is the rule's one silent failure mode.
	for _, col := range ownerColumns {
		for _, param := range []string{"?", "$1", "@caller", "sqlc.arg(user_id)", "sqlc.narg(user_id)"} {
			assert.Truef(t, ownerBindRe.MatchString("WHERE "+col+" = "+param),
				"ownerBindRe must match %q = %q", col, param)
		}
		// The same predicate written the other way round, and the set forms.
		assert.Truef(t, ownerBindRe.MatchString("WHERE ? = "+col), "reversed comparison: %q", col)
		assert.Truef(t, ownerBindRe.MatchString("WHERE "+col+" IN (sqlc.slice('ids'))"), "IN form: %q", col)
		assert.Truef(t, ownerBindRe.MatchString("WHERE "+col+" = ANY($1)"), "ANY form: %q", col)
		assert.Truef(t, ownerBindRe.MatchString("WHERE "+col+" <> ?"), "negated comparison: %q", col)
	}
	// user_code is a device-flow field, not an owner reference; matching it on
	// a `user_` prefix would classify the approval queries as ownership gates.
	assert.False(t, ownerBindRe.MatchString("WHERE user_code = ?"),
		"user_code is not an owner column")
	// A column that merely starts with an owner column's name is not one.
	assert.False(t, ownerBindRe.MatchString("WHERE user_identity = ?"),
		"user_identity is not user_id")
}

func TestSqlcQueryBodies_SplitsOnNameMarkers(t *testing.T) {
	got := sqlcQueryBodies("-- name: First :one\nSELECT 1;\n\n-- name: Second :exec\nDELETE FROM t WHERE user_id = ?;\n")
	require.Len(t, got, 2)
	assert.Contains(t, got["First"], "SELECT 1")
	assert.NotContains(t, got["First"], "DELETE",
		"a query body must stop at the next name marker, not swallow it")
	assert.Contains(t, got["Second"], "DELETE FROM t")
}

// TestOwnerFilterRefusalHonoured pins the difference between "this function
// mentions OwnerFilter" and "this function refuses an unminted caller". A
// presence-only check passed `owner, _ := store.OwnerFilter(...)`, which binds
// "" into the query and matches every blank-owner row -- the exact fail-open
// the whole rule exists to stop.
func TestOwnerFilterRefusalHonoured(t *testing.T) {
	cases := map[string]struct {
		body      string
		honoured  bool
		whySubstr string
	}{
		"binds ok and returns on it": {
			body: `owner, ok := store.OwnerFilter(p.UserID)
			if !ok {
				return nil, nil
			}
			_ = owner`,
			honoured: true,
		},
		"discards ok": {
			body: `owner, _ := store.OwnerFilter(p.UserID)
			_ = owner`,
			whySubstr: "discards the ok result",
		},
		"binds ok but never returns on it": {
			body: `owner, ok := store.OwnerFilter(p.UserID)
			_ = owner
			_ = ok`,
			whySubstr: "never returns early on !ok",
		},
		"checks !ok but falls through": {
			body: `owner, ok := store.OwnerFilter(p.UserID)
			if !ok {
				owner = "fallback"
			}
			_ = owner`,
			whySubstr: "never returns early on !ok",
		},
		"single-value use": {
			body:      `_ = store.OwnerFilter(p.UserID)`,
			whySubstr: "does not bind its (value, ok) results",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fn, call := parseFuncWithOwnerFilter(t, tc.body)
			why, ok := ownerFilterRefusalHonoured(fn, call)
			assert.Equal(t, tc.honoured, ok)
			if !tc.honoured {
				assert.Contains(t, why, tc.whySubstr)
			}
		})
	}
}

func TestIsIdentityComparison_KeysOnTheImportedIdentifier(t *testing.T) {
	// CredentialIdentity.Matches answers "same credential", not "same user".
	// Counting it would demand table entries for sites that decide no ownership.
	for src, want := range map[string]bool{
		`package p
func f() { _ = userID.Matches(row.UserID) }`: true,
		`package p
func f() { _ = auth.IsOwner(ws, userID) }`: true,
		`package p
func f() { _ = channelAuth.Credential.Matches(user.Credential) }`: false,
		`package p
func f() { _ = strings.Contains(a, b) }`: false,
		// IsOwner reached through a package this file did not import as "auth"
		// is not auth.IsOwner; the alias below is what makes it one.
		`package p
func f() { _ = other.IsOwner(ws, userID) }`: false,
	} {
		assert.Equal(t, want, hasRef(t, src, func(ref symbolRef) bool {
			return isIdentityComparison("auth", false, ref)
		}), "source: %s", src)
	}

	// An aliased import must still be recognised: keying on the literal
	// identifier "auth" would make `hubauth.IsOwner(...)` invisible to the net.
	assert.True(t, hasRef(t, `package p
func f() { _ = hubauth.IsOwner(ws, userID) }`, func(ref symbolRef) bool {
		return isIdentityComparison("hubauth", false, ref)
	}), "an aliased auth import must still be classified")

	// Inside package auth the predicate is called unqualified, so a
	// selector-only rule would leave the package that owns the comparison as
	// the one place it is not enforced.
	bareIsOwner := `package p
func f() { _ = IsOwner(ws, userID) }`
	assert.True(t, hasRef(t, bareIsOwner, func(ref symbolRef) bool {
		return isIdentityComparison("", true, ref)
	}), "inside package auth a bare IsOwner is the real predicate")
	assert.False(t, hasRef(t, bareIsOwner, func(ref symbolRef) bool {
		return isIdentityComparison("auth", false, ref)
	}), "elsewhere a bare IsOwner names some unrelated helper")
}

func TestIsMustNewCall_KeysOnTheImportedIdentifier(t *testing.T) {
	// The bypass this closes: a file that imports userid under an alias and
	// writes uid.MustNew(row.UserID) used to be invisible to the whole rule,
	// which then reported green while the panicking call shipped.
	assert.True(t, hasRef(t, `package p
func f() { _ = uid.MustNew(x) }`, func(ref symbolRef) bool {
		return isMustNewCall("uid", false, ref)
	}), "an aliased userid import must still be matched")

	assert.True(t, hasRef(t, `package p
func f() { _ = userid.MustNew(x) }`, func(ref symbolRef) bool {
		return isMustNewCall("userid", false, ref)
	}))

	// Some other package's MustNew is not this rule's business.
	assert.False(t, hasRef(t, `package p
func f() { _ = other.MustNew(x) }`, func(ref symbolRef) bool {
		return isMustNewCall("userid", false, ref)
	}))

	assert.False(t, hasRef(t, `package p
func f() { _ = userid.New(x) }`, func(ref symbolRef) bool {
		return isMustNewCall("userid", false, ref)
	}))

	// A bare MustNew is this package's own only inside package userid itself.
	bare := `package p
func f() { _ = MustNew(x) }`
	assert.True(t, hasRef(t, bare, func(ref symbolRef) bool {
		return isMustNewCall("", true, ref)
	}), "inside package userid a bare MustNew is the real thing")
	assert.False(t, hasRef(t, bare, func(ref symbolRef) bool {
		return isMustNewCall("userid", false, ref)
	}), "elsewhere a bare MustNew names some unrelated helper")
}

func TestIsAnyOrgCall_KeysOnTheImportedIdentifier(t *testing.T) {
	// The bypass this closes: a file importing hub/auth under an alias and
	// writing hubauth.AnyOrg() would skip the org check invisibly to the net.
	assert.True(t, hasRef(t, `package p
func f() { _ = hubauth.AnyOrg() }`, func(ref symbolRef) bool {
		return isAnyOrgCall("hubauth", false, ref)
	}), "an aliased auth import must still be matched")

	assert.True(t, hasRef(t, `package p
func f() { _ = auth.AnyOrg() }`, func(ref symbolRef) bool {
		return isAnyOrgCall("auth", false, ref)
	}))

	// The two sibling constructors decide the opposite thing; matching them
	// would demand table entries for sites that skip nothing.
	for _, src := range []string{`package p
func f() { _ = auth.BindOrg(x) }`, `package p
func f() { _ = auth.DenyAllOrg() }`, `package p
func f() { _ = other.AnyOrg() }`} {
		assert.False(t, hasRef(t, src, func(ref symbolRef) bool {
			return isAnyOrgCall("auth", false, ref)
		}), "source: %s", src)
	}

	// Inside package auth the constructor is called unqualified.
	bare := `package p
func f() { _ = AnyOrg() }`
	assert.True(t, hasRef(t, bare, func(ref symbolRef) bool {
		return isAnyOrgCall("", true, ref)
	}), "inside package auth a bare AnyOrg is the real constructor")
	assert.False(t, hasRef(t, bare, func(ref symbolRef) bool {
		return isAnyOrgCall("auth", false, ref)
	}), "elsewhere a bare AnyOrg names some unrelated helper")
}

// TestMustNewArgGuarded pins the difference between "this exemption has a
// recorded reason" and "this exemption's guard is really there". A table of
// prose is what the MustNew rule would otherwise be: an entry can be added for
// any expression, and the guard that made it safe deleted afterwards, with
// nothing anywhere reporting it.
func TestMustNewArgGuarded(t *testing.T) {
	cases := map[string]struct {
		body      string
		guarded   bool
		whySubstr string
	}{
		"refused on the line above": {
			body: `if userID == "" {
				return nil
			}
			_ = userid.MustNew(userID)`,
			guarded: true,
		},
		"refused as one disjunct of a compound flag guard": {
			body: `if userID == "" || name == "" {
				return nil
			}
			_ = userid.MustNew(userID)`,
			guarded: true,
		},
		"reversed comparison": {
			body: `if "" == userID {
				return nil
			}
			_ = userid.MustNew(userID)`,
			guarded: true,
		},
		"no guard at all": {
			body:      `_ = userid.MustNew(userID)`,
			whySubstr: "no `if userID == \"\" { ... return ... }` precedes it",
		},
		"guard runs AFTER the mint": {
			body: `_ = userid.MustNew(userID)
			if userID == "" {
				return nil
			}`,
			whySubstr: "precedes it",
		},
		"guard does not return": {
			body: `if userID == "" {
				userID = "anonymous"
			}
			_ = userid.MustNew(userID)`,
			whySubstr: "precedes it",
		},
		"conjunction refuses only the pair": {
			// `a == "" && b == ""` returns when BOTH are blank, so a blank
			// userID with a non-blank name walks straight into the panic.
			body: `if userID == "" && name == "" {
				return nil
			}
			_ = userid.MustNew(userID)`,
			whySubstr: "precedes it",
		},
		"a different variable is guarded": {
			body: `if name == "" {
				return nil
			}
			_ = userid.MustNew(userID)`,
			whySubstr: "precedes it",
		},
		"a database column is not a local variable": {
			body: `if row.UserID == "" {
				return nil
			}
			_ = userid.MustNew(row.UserID)`,
			whySubstr: "is an expression, not a local variable",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fn, call := parseFuncWithMustNew(t, tc.body)
			why, ok := mustNewArgGuarded(fn, call)
			assert.Equal(t, tc.guarded, ok, "why: %s", why)
			if !tc.guarded {
				assert.Contains(t, why, tc.whySubstr)
			}
		})
	}
}

func TestIsUserIDType_RequiresTheTypeRatherThanBanningString(t *testing.T) {
	// Banning `string` let these three through, which is the same untyped
	// identity wearing a hat.
	for expr, want := range map[string]bool{
		"userid.UserID":  true,
		"string":         false,
		"*string":        false,
		"[]string":       false,
		"rawID":          false,
		"*userid.UserID": false,
	} {
		parsed, err := parser.ParseExpr(expr)
		require.NoError(t, err)
		assert.Equalf(t, want, isUserIDType("userid", parsed), "type %q", expr)
	}
}

func TestCarriesPrincipal_SeesContextExtractionNotJustParameters(t *testing.T) {
	// The reachServerInitiated safety assertion ("this site may not carry a
	// principal") could not fire for the dominant hub/service shape: a handler
	// that takes only a context and calls auth.MustGetUser(ctx) in its body.
	for name, tc := range map[string]struct {
		src  string
		want bool
	}{
		"UserInfo parameter": {`package p
func f(ctx context.Context, user *auth.UserInfo) {}`, true},
		"context extraction": {`package p
func f(ctx context.Context) { user, err := auth.MustGetUser(ctx); _, _ = user, err }`, true},
		"optional context extraction": {`package p
func f(ctx context.Context) { user, _ := auth.GetUser(ctx); _ = user }`, true},
		// A bare userid.UserID is the same fact with the wrapper peeled off.
		// Omitting it let a function take the caller's identity in its most
		// explicit form and still pass the reachServerInitiated assertion.
		"typed identity parameter": {`package p
func f(ctx context.Context, userID userid.UserID, workerID string) {}`, true},
		"no principal at all": {`package p
func f(ctx context.Context, workerID string) { _ = workerID }`, false},
		"unrelated GetUser": {`package p
func f(ctx context.Context) { _ = store.GetUser(ctx) }`, false},
	} {
		t.Run(name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "src.go", tc.src, 0)
			require.NoError(t, err)
			fn := file.Decls[0].(*ast.FuncDecl)
			assert.Equal(t, tc.want, carriesPrincipalWithAlias("auth", "userid", fn))
		})
	}
}

// hasRef reports whether any symbol reference in src satisfies pred.
func hasRef(t *testing.T, src string, pred func(symbolRef) bool) bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "src.go", src, 0)
	require.NoError(t, err)
	found := false
	forEachSymbolRef(file, func(ref symbolRef) {
		if !found && pred(ref) {
			found = true
		}
	})
	return found
}

// parseFuncWithMustNew parses a function whose body is body and returns it with
// the userid.MustNew call expression inside it.
func parseFuncWithMustNew(t *testing.T, body string) (*ast.FuncDecl, *ast.CallExpr) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "src.go", "package p\nfunc f() error {\n"+body+"\nreturn nil\n}\n", 0)
	require.NoError(t, err)
	fn := file.Decls[0].(*ast.FuncDecl)
	var call *ast.CallExpr
	forEachSymbolRef(file, func(ref symbolRef) {
		if call == nil && ref.call != nil && isMustNewCall("userid", false, ref) {
			call = ref.call
		}
	})
	require.NotNil(t, call, "the fixture must contain a userid.MustNew call")
	return fn, call
}

// parseFuncWithOwnerFilter parses a function whose body is body and returns it
// with the store.OwnerFilter call expression inside it.
func parseFuncWithOwnerFilter(t *testing.T, body string) (*ast.FuncDecl, *ast.CallExpr) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "src.go", "package p\nfunc f() (any, error) {\n"+body+"\nreturn nil, nil\n}\n", 0)
	require.NoError(t, err)
	fn := file.Decls[0].(*ast.FuncDecl)
	var call *ast.CallExpr
	ast.Inspect(fn, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok && isSharedOwnerGuardCall(c) {
			call = c
			return false
		}
		return true
	})
	require.NotNil(t, call, "the fixture must contain a store.OwnerFilter call")
	return fn, call
}

// TestForEachSymbolRef_SeesMethodValuesNotJustCalls pins the bypass every rule
// in this package shared: each of them started from
// `call.Fun.(*ast.SelectorExpr)`, so a symbol taken as a VALUE and called one
// line later through a plain identifier was not a shape any of them recognised.
// The registry read still happened.
func TestForEachSymbolRef_SeesMethodValuesNotJustCalls(t *testing.T) {
	src := `package p
func f(m *workermgr.Manager, id string) bool {
	g := m.ConnForTrustedPath
	return g(id) != nil
}`
	file, err := parser.ParseFile(token.NewFileSet(), "src.go", src, 0)
	require.NoError(t, err)

	var refs []symbolRef
	forEachSymbolRef(file, func(ref symbolRef) {
		if ref.name() == "ConnForTrustedPath" {
			refs = append(refs, ref)
		}
	})
	require.Len(t, refs, 1, "the uncalled method value must still be a reference")
	assert.Nil(t, refs[0].call, "a method value heads no call, which is what the rules report")

	// ...and a normal call still arrives with its call attached, so the rules
	// that need to read arguments (MustNew) keep working.
	called, err := parser.ParseFile(token.NewFileSet(), "src.go", `package p
func f(m *workermgr.Manager, id string) { m.ConnForTrustedPath(id) }`, 0)
	require.NoError(t, err)
	var withCall int
	forEachSymbolRef(called, func(ref symbolRef) {
		if ref.name() == "ConnForTrustedPath" && ref.call != nil {
			withCall++
		}
	})
	assert.Equal(t, 1, withCall)
}

// TestIsIdentityComparison_CoversBothComparisonMethods pins the second half of
// userid.UserID's comparison surface. While it was named Equal it was
// unscannable -- `Equal` is Go's most overloaded method name, so no
// syntax-level rule can tell an identity comparison from a timestamp one -- and
// worker/service.requireWorkerOwner, the worker-side ownership gate, decided
// ownership through exactly that call while sitting outside the net.
func TestIsIdentityComparison_CoversBothComparisonMethods(t *testing.T) {
	for src, want := range map[string]bool{
		`package p
func f() { _ = userID.Matches(row.UserID) }`: true,
		`package p
func f() { _ = userID.MatchesUser(svc.RegisteredBy()) }`: true,
		// The name this method used to have must NOT come back as a scanned
		// spelling: matching it would demand a table entry for every time.Time
		// and proto comparison in the repo.
		`package p
func f() { _ = deadline.Equal(other) }`: false,
	} {
		assert.Equal(t, want, hasRef(t, src, func(ref symbolRef) bool {
			return isIdentityComparison("auth", false, ref)
		}), "source: %s", src)
	}
}

// TestIsIdentityFieldName_MatchesBySuffix pins the widening. The closed list of
// four spellings was itself a convention: each name below denotes an identity,
// none of them was in it, and every one would have been born as an untyped
// string inside a package that had explicitly opted into typed identity.
func TestIsIdentityFieldName_MatchesBySuffix(t *testing.T) {
	for name, want := range map[string]bool{
		"UserID":       true,
		"OwnerUserID":  true,
		"TargetUserID": true,
		"ActorUserID":  true,
		"OwnerID":      true,
		"RegisteredBy": true,
		"CreatedBy":    true,
		"WorkspaceID":  false,
		"UserIDs":      false, // a set of ids is not one identity field
		"Username":     false,
	} {
		assert.Equalf(t, want, isIdentityFieldName(name), "field %q", name)
	}
}

// TestReceiverTypeOf_AcceptsBothReceiverForms pins the value-receiver half. The
// registry scan keyed on the "(*Manager)." spelling, so an exported accessor
// declared on the value type was outside the population entirely.
func TestReceiverTypeOf_AcceptsBothReceiverForms(t *testing.T) {
	assert.Equal(t, "Manager", receiverTypeOf("(*Manager).ConnForTrustedPath"))
	assert.Equal(t, "Manager", receiverTypeOf("(Manager).ConnForTrustedPath"))
	assert.Equal(t, "Conn", receiverTypeOf("(*Conn).Send"))
	assert.Empty(t, receiverTypeOf("NewManager"), "a plain function has no receiver")
}

// TestIntraPackageCallees_FindsTheHelpersAFactCanHideBehind pins the transitive
// half of the registry scan. Reading the facts from a method body alone meant a
// delegating accessor -- the most natural way to add one -- touched neither
// registry map, so it was not a registry method at all: absent from
// registryMethodKinds without failing anything, absent from the derived
// accessor set, and every one of its call sites unclassified and green.
func TestIntraPackageCallees_FindsTheHelpersAFactCanHideBehind(t *testing.T) {
	src := `package p
func (m *Manager) PeekConn(workerID string) *Conn {
	logSomething()
	other.Ignored(workerID)
	return m.connLocked(workerID)
}`
	file, err := parser.ParseFile(token.NewFileSet(), "src.go", src, 0)
	require.NoError(t, err)
	fn := file.Decls[0].(*ast.FuncDecl)

	got := intraPackageCallees(testutil.QualifiedFuncName(fn), receiverIdent(fn), fn)
	assert.Contains(t, got, "(*Manager).connLocked", "a call on the method's own receiver is an intra-package edge")
	assert.Contains(t, got, "logSomething", "a bare call names a package-level function")
	assert.NotContains(t, got, "Ignored", "a call on some other value is out of range of a syntax walk")
}

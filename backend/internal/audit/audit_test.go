package audit

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/printer"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

// Import paths the rules recognise calls by. They are matched through
// testutil.ImportedAs rather than by the identifier at the call site, because
// every one of these rules is a net: a file that writes
// `uid "…/internal/util/userid"` and then `uid.MustNew(row.UserID)` must not be
// invisible to the rule that exists to catch exactly that call.
const (
	useridPkg = "github.com/leapmux/leapmux/internal/util/userid"
	authPkg   = "github.com/leapmux/leapmux/internal/hub/auth"
	storePkg  = "github.com/leapmux/leapmux/internal/hub/store"
)

// Directories whose own source defines the machinery a rule scans for, and so
// cannot be held to it. These are directory paths rather than package NAMES:
// internal/hub/service and internal/worker/service are both `package service`,
// so a name-keyed exemption (or a name-keyed classification) silently covers
// the wrong package.
const (
	useridDir    = "internal/util/userid"
	workermgrDir = "internal/hub/workermgr"
	authDir      = "internal/hub/auth"
)

// repoRoot is the backend module root, two levels up from internal/audit.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	return root
}

// TestRepoInvariants runs every mechanically-enforced repo-wide rule over one
// walk of the source. Each sub-test is an independent invariant; a failure names
// the rule, the exact site, and what to do about it.
func TestRepoInvariants(t *testing.T) {
	root := repoRoot(t)

	// One parse, shared by the AST rules. Walking once is the point of merging
	// these: three of them previously walked their own package and were blind
	// to everything outside it.
	type parsed struct {
		fset *token.FileSet
		rel  string
		file *ast.File
	}
	var files []parsed
	scanned := testutil.ForEachRepoSourceFile(t, root, func(fset *token.FileSet, rel string, file *ast.File) {
		files = append(files, parsed{fset, rel, file})
	})
	// A walk that silently stops finding source is the one way a repo-wide net
	// rots while staying green.
	require.Greater(t, scanned, 200, "the repo walk scanned suspiciously few files; the net is broken, not the code")

	t.Run("worker registry reads are classified", func(t *testing.T) {
		// The scanned set is DERIVED from workermgr's own source rather than
		// hand-listed, so a fourth ungated accessor is caught at the method
		// before it is missed at every call site.
		registryConnAccessors := checkRegistryMethodKinds(t, root)

		var unclassified []string
		sites := map[string]*reachSite{}

		for _, f := range files {
			enclosing := testutil.NewEnclosingFuncFinder(f.file)
			dir := packageDir(f.rel)
			// The registry's own package defines these methods; a call
			// inside workermgr is the implementation, not a consumer.
			if dir == workermgrDir {
				continue
			}
			forEachSymbolRef(f.file, func(ref symbolRef) {
				if ref.sel == nil || !registryConnAccessors[ref.name()] {
					return
				}
				where := position(f.fset, ref.pos, f.rel)
				fn, inFunc := enclosing.Find(ref.pos)
				if !inFunc {
					unclassified = append(unclassified, fmt.Sprintf(
						"%s: package-level %s reference belongs to no function, so it can never be classified", where, ref.name()))
					return
				}
				name := siteKey(f.rel, fn)
				site := sites[name]
				if site == nil {
					site = &reachSite{accessors: map[string]bool{}, firstPos: where}
					site.takesPrincipal = carriesPrincipal(f.file, fn)
					_, site.classified = workerReachSites[name]
					sites[name] = site
				}
				site.accessors[ref.name()] = true
				if !site.classified {
					verb := "calls"
					if ref.call == nil {
						verb = "takes a method value of"
					}
					unclassified = append(unclassified, fmt.Sprintf(
						"%s: %s() %s %s -- route user-gated paths through requireOnlineWorker, or add a workerReachSites entry with the matching kind",
						where, name, verb, ref.name()))
				}
			})
		}

		for _, msg := range unclassified {
			assert.Fail(t, "unclassified worker registry read", "%s", msg)
		}
		assert.NotEmpty(t, sites, "the registry-accessor scan matched nothing; the detection is broken, not the code")
		assertNoStaleEntries(t, workerReachSites, sites,
			"workerReachSites lists %q but no registry conn accessor call was found in it -- remove the stale entry or restore the call")

		// The KIND has to mean something, or picking one is a comment with a type.
		for name, site := range sites {
			if !site.classified {
				continue
			}
			switch workerReachSites[name] {
			case reachStoreScoped:
				assert.False(t, site.accessors[connAccessor],
					"%s (%s) is classified reachStoreScoped but calls %s: a store-scoped row justifies disclosing liveness, not a sendable connection",
					name, site.firstPos, connAccessor)
			case reachServerInitiated:
				assert.False(t, site.takesPrincipal,
					"%s (%s) is classified reachServerInitiated but carries a caller principal: a user in the path means the worker id may be user-supplied",
					name, site.firstPos)
			case reachEstablishedChan:
				// No structural claim: the id's provenance (an already-authorized
				// channel record) is not visible in the signature.
			}
		}
	})

	t.Run("identity comparisons are classified", func(t *testing.T) {
		var unclassified []string
		seen := map[string]bool{}

		for _, f := range files {
			enclosing := testutil.NewEnclosingFuncFinder(f.file)
			// Package userid defines Matches; comparing there is the
			// implementation, not a decision about a caller.
			if packageDir(f.rel) == useridDir {
				continue
			}
			authAlias, _ := testutil.ImportedAs(f.file, authPkg)
			inAuthPkg := packageDir(f.rel) == authDir
			forEachSymbolRef(f.file, func(ref symbolRef) {
				if !isIdentityComparison(authAlias, inAuthPkg, ref) {
					return
				}
				where := position(f.fset, ref.pos, f.rel)
				fn, inFunc := enclosing.Find(ref.pos)
				if !inFunc {
					unclassified = append(unclassified, fmt.Sprintf(
						"%s: package-level identity comparison belongs to no function, so it can never be classified", where))
					return
				}
				name := siteKey(f.rel, fn)
				seen[name] = true
				if _, ok := identityComparisonSites[name]; !ok {
					unclassified = append(unclassified, fmt.Sprintf(
						"%s: %s() compares a caller identity against a stored one -- add an identityComparisonSites entry naming the test that pins its zero-id denial",
						where, name))
				}
			})
		}

		for _, msg := range unclassified {
			assert.Fail(t, "unclassified identity comparison", "%s", msg)
		}
		assert.NotEmpty(t, seen, "the identity-comparison scan matched nothing; the detection is broken, not the code")
		assertNoStaleEntries(t, identityComparisonSites, seen,
			"identityComparisonSites lists %q but no identity comparison was found in it -- remove the stale entry or restore the comparison")

		tests := testutil.RepoTestFuncNames(t, root)
		for site, testName := range identityComparisonSites {
			assert.True(t, tests[testName],
				"identityComparisonSites points %q at %q, which no test declares -- the coverage it claims does not exist", site, testName)
		}
	})

	t.Run("org-check skips are classified", func(t *testing.T) {
		var unclassified []string
		seen := map[string]bool{}

		for _, f := range files {
			enclosing := testutil.NewEnclosingFuncFinder(f.file)
			authAlias, _ := testutil.ImportedAs(f.file, authPkg)
			inAuthPkg := packageDir(f.rel) == authDir
			forEachSymbolRef(f.file, func(ref symbolRef) {
				if !isAnyOrgCall(authAlias, inAuthPkg, ref) {
					return
				}
				where := position(f.fset, ref.pos, f.rel)
				fn, inFunc := enclosing.Find(ref.pos)
				if !inFunc {
					unclassified = append(unclassified, fmt.Sprintf(
						"%s: package-level AnyOrg() belongs to no function, so it can never be classified", where))
					return
				}
				name := siteKey(f.rel, fn)
				seen[name] = true
				if _, ok := orgCheckSkipSites[name]; !ok {
					unclassified = append(unclassified, fmt.Sprintf(
						"%s: %s() calls auth.AnyOrg(), skipping the organization check -- bind the caller's org, or add an orgCheckSkipSites entry with the reason",
						where, name))
				}
			})
		}

		for _, msg := range unclassified {
			assert.Fail(t, "unclassified org-check skip", "%s", msg)
		}
		assert.NotEmpty(t, seen, "the AnyOrg scan matched nothing; the detection is broken, not the code")
		assertNoStaleEntries(t, orgCheckSkipSites, seen,
			"orgCheckSkipSites lists %q but no auth.AnyOrg() call was found in it -- remove the stale entry or restore the call")
	})

	t.Run("MustNew is never called on stored data", func(t *testing.T) {
		matched := 0
		for _, f := range files {
			useridAlias, imported := testutil.ImportedAs(f.file, useridPkg)
			inUseridPkg := packageDir(f.rel) == useridDir
			if !imported && !inUseridPkg {
				continue
			}
			enclosing := testutil.NewEnclosingFuncFinder(f.file)
			forEachSymbolRef(f.file, func(ref symbolRef) {
				if !isMustNewCall(useridAlias, inUseridPkg, ref) {
					return
				}
				matched++
				call := ref.call
				// A MustNew taken as a function value has no argument here to
				// judge, and the call it enables happens through an identifier
				// no syntax-level rule can follow. Refuse the shape rather than
				// let it pass as unexamined.
				if call == nil || len(call.Args) != 1 {
					assert.Fail(t, "MustNew referenced without a visible argument",
						"%s: userid.MustNew is taken as a function value, so the data it will panic on is invisible to this rule -- call it directly at the mint site",
						position(f.fset, ref.pos, f.rel))
					return
				}
				if _, isLiteral := call.Args[0].(*ast.BasicLit); isLiteral {
					return // a literal satisfies the contract by inspection
				}
				// The exemption is keyed on the enclosing FUNCTION, not just the
				// file. A file-wide key blesses every other call site in the same
				// file, including ones the recorded reason says nothing about --
				// and this rule exists because the class already recurred twice.
				site := mustNewSiteKey(f.rel, enclosing, call.Pos())
				if _, allowed := mustNewNonLiteralSites[site]; allowed {
					// The exemption's recorded reason is prose; the guard it
					// claims is checkable. Requiring it here is the same move
					// as ownerFilterRefusalHonoured: without it the table
					// reduces to "someone wrote a sentence once", and a guard
					// deleted later leaves the panic exempted and green.
					fn, inFunc := enclosing.Find(call.Pos())
					if !inFunc {
						assert.Fail(t, "exempted MustNew outside any function",
							"%s: %s runs at package level, so nothing can guard it",
							position(f.fset, call.Pos(), f.rel), site)
						return
					}
					if why, guarded := mustNewArgGuarded(fn, call); !guarded {
						assert.Fail(t, "exempted MustNew is not actually guarded",
							"%s: mustNewNonLiteralSites exempts %s, but %s -- the exemption's bar is a local variable this function already refuses when empty",
							position(f.fset, call.Pos(), f.rel), site, why)
					}
					return
				}
				assert.Fail(t, "MustNew reached with unproven data", "%s: userid.MustNew on a non-literal -- if this is a database column it will PANIC on corrupt data, so use userid.New and refuse; if it is genuinely known-non-empty, register %q in mustNewNonLiteralSites with the reason",
					position(f.fset, call.Pos(), f.rel), site)
			})
		}
		assert.NotZero(t, matched, "the MustNew scan matched nothing; the detection is broken, not the code")

		for site := range mustNewNonLiteralSites {
			rel, _, found := splitSiteKey(site)
			require.True(t, found, "mustNewNonLiteralSites key %q is not in \"file#function\" form", site)
			assert.FileExists(t, filepath.Join(root, filepath.FromSlash(rel)),
				"mustNewNonLiteralSites allows %q, whose file no longer exists", site)
		}
	})

	t.Run("opted-in packages carry identity as a type", func(t *testing.T) {
		found := map[string]bool{}
		for _, f := range files {
			dir := packageDir(f.rel)
			if _, opted := typedIdentityPackages[dir]; !opted {
				continue
			}
			found[dir] = true
			useridAlias, _ := testutil.ImportedAs(f.file, useridPkg)
			ast.Inspect(f.file, func(n ast.Node) bool {
				st, ok := n.(*ast.StructType)
				if !ok || st.Fields == nil {
					return true
				}
				for _, field := range st.Fields.List {
					for _, name := range field.Names {
						if !isIdentityFieldName(name.Name) {
							continue
						}
						// Stated as a REQUIREMENT rather than as a ban on
						// `string`. Banning one spelling let *string, []string,
						// and a local `type raw = string` alias through, which
						// is the same untyped identity wearing a hat.
						if isUserIDType(useridAlias, field.Type) {
							continue
						}
						assert.Fail(t, "untyped identity in an opted-in package",
							"%s: %s is %s -- this package opted into typed identity (%s), so use userid.UserID and project to a string at the marshal boundary",
							position(f.fset, name.Pos(), f.rel), name.Name, renderExpr(field.Type), typedIdentityPackages[dir])
					}
				}
				return true
			})
		}
		for dir := range typedIdentityPackages {
			assert.True(t, found[dir], "typedIdentityPackages lists %q, which the walk never reached", dir)
		}
	})

	t.Run("ownership queries refuse an unminted caller", func(t *testing.T) {
		checkOwnerFilterCoverage(t, root)
	})
}

// ---- shared helpers ----

// symbolRef is one reference to a named symbol: `pkg.Name` / `recv.Name` (sel
// set), or a bare `Name` heading a call (ident set). call is the call the
// reference heads, and is nil when the symbol is taken as a VALUE rather than
// called.
//
// Scanning references rather than calls is what closes the method-value bypass.
// Every rule here used to start from `call.Fun.(*ast.SelectorExpr)`, so
//
//	f := m.ConnForTrustedPath
//	return f(workerID)
//
// was invisible: the second line is a call through an identifier no
// syntax-level scan can resolve, and the first is not a call at all. The
// registry read still happened; the net just never saw a shape it recognised.
// A reference counts now, called or not -- which is also why each rule reports
// an uncalled reference rather than trying to follow it.
type symbolRef struct {
	sel   *ast.SelectorExpr
	ident *ast.Ident
	call  *ast.CallExpr
	pos   token.Pos
}

// name is the referenced symbol's identifier, however it is spelled.
func (r symbolRef) name() string {
	if r.sel != nil {
		return r.sel.Sel.Name
	}
	return r.ident.Name
}

// forEachSymbolRef visits every selector expression in file, plus every bare
// identifier that heads a call. Bare identifiers are limited to call position
// because every local variable read is one otherwise; the unqualified shapes
// these rules care about (a package's own IsOwner / MustNew / AnyOrg) are all
// calls.
func forEachSymbolRef(file *ast.File, visit func(symbolRef)) {
	heads := map[ast.Expr]*ast.CallExpr{}
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			heads[call.Fun] = call
		}
		return true
	})
	ast.Inspect(file, func(n ast.Node) bool {
		switch e := n.(type) {
		case *ast.SelectorExpr:
			visit(symbolRef{sel: e, call: heads[e], pos: e.Pos()})
		case *ast.Ident:
			if call, ok := heads[e]; ok {
				visit(symbolRef{ident: e, call: call, pos: e.Pos()})
			}
		}
		return true
	})
}

func position(fset *token.FileSet, pos token.Pos, rel string) string {
	return fmt.Sprintf("%s:%d", rel, fset.Position(pos).Line)
}

// packageDir is the repo-relative directory of a scanned file, which is what
// every rule keys on. The package NAME is not usable as a key: internal/hub/service
// and internal/worker/service are both `package service`, so a name-keyed table
// would let a function in one be born carrying the other's classification.
func packageDir(rel string) string {
	return filepath.ToSlash(filepath.Dir(rel))
}

// siteKey renders one classified function as "dir.(*Recv).Method".
func siteKey(rel string, fn *ast.FuncDecl) string {
	return packageDir(rel) + "." + testutil.QualifiedFuncName(fn)
}

// mustNewSiteKey renders one MustNew call site as "file#EnclosingFunc", or
// "file#<package-level>" for a call outside any function body.
func mustNewSiteKey(rel string, enclosing *testutil.EnclosingFuncFinder, pos token.Pos) string {
	if fn, ok := enclosing.Find(pos); ok {
		return rel + "#" + testutil.QualifiedFuncName(fn)
	}
	return rel + "#<package-level>"
}

func splitSiteKey(site string) (rel, fn string, ok bool) {
	for i := range site {
		if site[i] == '#' {
			return site[:i], site[i+1:], true
		}
	}
	return "", "", false
}

// assertNoStaleEntries fails for every registry key the scan never encountered.
// A stale entry is not harmless: it is a blessing for a site that no longer
// exists, and the next function to acquire that name inherits it.
func assertNoStaleEntries[V any, S any](t *testing.T, registry map[string]V, seen map[string]S, msg string) {
	t.Helper()
	for key := range registry {
		_, found := seen[key]
		assert.Truef(t, found, msg, key)
	}
}

// mustNewArgGuarded reports whether the identifier passed to an exempted
// MustNew call is refused, earlier in the same function, by an
// `if x == "" { ... return ... }`.
//
// why explains the failure for the assertion message. The bar mustNewNonLiteralSites
// documents is exactly this shape, and checking it is what keeps the table from
// degrading into a comment: an entry can otherwise be added for any expression
// at all, and the guard that made it safe can be deleted afterwards without
// anything noticing.
func mustNewArgGuarded(fn *ast.FuncDecl, call *ast.CallExpr) (why string, ok bool) {
	arg, isIdent := call.Args[0].(*ast.Ident)
	if !isIdent {
		return fmt.Sprintf("its argument %s is an expression, not a local variable a guard can refuse", renderExpr(call.Args[0])), false
	}
	if !hasEmptyStringGuardBefore(fn.Body, arg.Name, call.Pos()) {
		return fmt.Sprintf("no `if %s == \"\" { ... return ... }` precedes it in %s", arg.Name, testutil.QualifiedFuncName(fn)), false
	}
	return "", true
}

// hasEmptyStringGuardBefore reports whether body contains, before pos, an
// `if ... name == "" ... { ... return ... }`.
//
// Disjuncts are followed because the real guards refuse several flags at once
// (`if userID == "" || clientName == "" { return ... }`), and a blank name still
// returns from any of them. Conjuncts are NOT followed: `a == "" && b == ""`
// returns only when BOTH are blank, so it does not refuse a blank a.
func hasEmptyStringGuardBefore(body *ast.BlockStmt, name string, pos token.Pos) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok || ifStmt.End() > pos || !comparesToEmptyString(ifStmt.Cond, name) {
			return true
		}
		ast.Inspect(ifStmt.Body, func(inner ast.Node) bool {
			if _, isReturn := inner.(*ast.ReturnStmt); isReturn {
				found = true
				return false
			}
			return true
		})
		return !found
	})
	return found
}

// comparesToEmptyString reports whether cond refuses name when it is empty --
// `name == ""` in either operand order, possibly as one disjunct of an `||`.
func comparesToEmptyString(cond ast.Expr, name string) bool {
	bin, ok := cond.(*ast.BinaryExpr)
	if !ok {
		return false
	}
	if bin.Op == token.LOR {
		return comparesToEmptyString(bin.X, name) || comparesToEmptyString(bin.Y, name)
	}
	if bin.Op != token.EQL {
		return false
	}
	return isIdentNamed(bin.X, name) && isEmptyStringLit(bin.Y) ||
		isIdentNamed(bin.Y, name) && isEmptyStringLit(bin.X)
}

func isIdentNamed(expr ast.Expr, name string) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == name
}

func isEmptyStringLit(expr ast.Expr) bool {
	lit, ok := expr.(*ast.BasicLit)
	return ok && lit.Kind == token.STRING && lit.Value == `""`
}

// reachSite is what the scan learns about one classified function.
type reachSite struct {
	accessors      map[string]bool
	takesPrincipal bool
	firstPos       string
	classified     bool
}

// connAccessor is the one accessor that hands back a sendable connection.
const connAccessor = "ConnForTrustedPath"

// registryStateFields are the live-worker maps. A method that touches one of
// them is answering, or changing, "what is the state of worker X" -- which is
// the cross-tenant liveness oracle. regWaiters is deliberately out of scope: it
// is keyed by an opaque registration token rather than a worker id and holds no
// worker state.
var registryStateFields = map[string]bool{"conns": true, "deregistering": true}

// checkRegistryMethodKinds classifies every exported workermgr.Manager method
// that reaches the live-worker maps and returns the selector names whose call
// sites must appear in workerReachSites.
//
// This is the half of the reach net that used to be a hand-written list of
// three names -- i.e. the net's own INPUT was the convention it replaces. Here
// the population comes from the registry's source: a method missing from
// registryMethodKinds fails, so a fourth accessor cannot be added and then
// silently scanned by nothing.
//
// "Reaches" is transitive, and that word is load-bearing. While the facts were
// read from the method body alone,
//
//	func (m *Manager) PeekConn(workerID string) *Conn { return m.ConnForTrustedPath(workerID) }
//
// mentioned neither conns nor deregistering, so it was not a registry method at
// all: absent from registryMethodKinds without failing anything, absent from the
// derived accessor set, and every one of its call sites unclassified and green.
// A delegating accessor is the most natural way to add one, which made it the
// most likely way to lose the whole rule.
func checkRegistryMethodKinds(t *testing.T, root string) map[string]bool {
	t.Helper()

	scanned := map[string]bool{}
	ungated := map[string]bool{}
	facts := parseRegistryPackage(t, filepath.Join(root, filepath.FromSlash(workermgrDir)))

	for _, key := range sortedKeys(facts.decls) {
		d := facts.decls[key]
		if !d.isManagerMethod || !d.fn.Name.IsExported() {
			continue
		}
		m := facts.reach[key]
		if !m.touchesState {
			continue // regWaiters-only plumbing, or no registry state at all
		}
		name := d.fn.Name.Name
		scanned[name] = true
		kind, classified := registryMethodKinds[name]
		if !classified {
			assert.Fail(t, "unclassified worker registry method",
				"%s: %s reaches the live-worker registry but is missing from registryMethodKinds -- classify it, and if it is registryUngatedByID classify its call sites in workerReachSites too",
				d.where, name)
			continue
		}
		// The kind has to be a claim the source supports, or picking one
		// is a comment with a type.
		switch kind {
		case registryUngatedByID:
			assert.False(t, m.callsAuthorizer,
				"%s: %s is classified registryUngatedByID but runs the ReachAuthorizer -- it is registryGated", d.where, name)
			assert.True(t, m.takesWorkerID,
				"%s: %s is classified registryUngatedByID but takes no worker id", d.where, name)
			assert.False(t, m.takesConn,
				"%s: %s is classified registryUngatedByID but takes a *Conn, so a bare id cannot reach it -- it is registryConnScoped", d.where, name)
			ungated[name] = true
		case registryGated:
			assert.True(t, m.callsAuthorizer,
				"%s: %s is classified registryGated but never runs the ReachAuthorizer -- it is registryUngatedByID", d.where, name)
		case registryConnScoped:
			assert.True(t, m.takesConn,
				"%s: %s is classified registryConnScoped but takes no *Conn, so a bare worker id reaches it -- it is registryUngatedByID", d.where, name)
		case registryBroadcast:
			assert.False(t, m.takesWorkerID,
				"%s: %s is classified registryBroadcast but takes a worker id, so it discloses that worker's state -- it is registryUngatedByID", d.where, name)
		}
	}

	assertNoStaleEntries(t, registryMethodKinds, scanned,
		"registryMethodKinds classifies %q, which workermgr.Manager no longer declares as a method reaching the live-worker maps -- remove the stale entry")
	require.NotEmpty(t, ungated, "no ungated registry accessors found; the registry scan is broken, not the code")
	require.True(t, ungated[connAccessor],
		"%s is not in the derived accessor set, so the reachStoreScoped assertion below checks nothing", connAccessor)
	return ungated
}

// registryDecl is one function or method declared in package workermgr.
type registryDecl struct {
	fn *ast.FuncDecl
	// isManagerMethod is true for a method on Manager or *Manager. Both
	// receiver forms count: keying on the "(*Manager)." spelling alone would
	// have let a value-receiver accessor out of the population entirely.
	isManagerMethod bool
	where           string
}

// registryPackageFacts is what package workermgr's source says about each of
// its own declarations, after propagating the transitive facts.
type registryPackageFacts struct {
	decls map[string]*registryDecl
	reach map[string]registryMethod
}

// parseRegistryPackage parses package workermgr, records the DIRECT facts about
// each declaration, then propagates "reaches the registry state" and "runs the
// authorizer" along intra-package calls to a fixed point.
//
// The whole package is walked, not just Manager's exported methods, because the
// facts a kind claims can sit any number of hops away: an accessor that returns
// m.connLocked(id) touches nothing itself, and its helper is the one holding
// the map.
func parseRegistryPackage(t *testing.T, dir string) registryPackageFacts {
	t.Helper()

	facts := registryPackageFacts{
		decls: map[string]*registryDecl{},
		reach: map[string]registryMethod{},
	}
	callees := map[string][]string{}

	testutil.ForEachPackageSourceFile(t, dir, func(fset *token.FileSet, file *ast.File) {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			key := testutil.QualifiedFuncName(fn)
			pos := fset.Position(fn.Pos())
			facts.decls[key] = &registryDecl{
				fn:              fn,
				isManagerMethod: receiverTypeOf(key) == "Manager",
				where:           fmt.Sprintf("%s/%s:%d", workermgrDir, filepath.Base(pos.Filename), pos.Line),
			}
			recv := receiverIdent(fn)
			facts.reach[key] = inspectRegistryMethod(recv, fn)
			callees[key] = intraPackageCallees(key, recv, fn)
		}
	})

	// Fixed point: a declaration reaches the registry state if it touches it
	// directly or calls something that does.
	for changed := true; changed; {
		changed = false
		for key, targets := range callees {
			m := facts.reach[key]
			for _, target := range targets {
				callee, ok := facts.reach[target]
				if !ok {
					continue
				}
				if callee.touchesState && !m.touchesState {
					m.touchesState, changed = true, true
				}
				if callee.callsAuthorizer && !m.callsAuthorizer {
					m.callsAuthorizer, changed = true, true
				}
			}
			facts.reach[key] = m
		}
	}
	return facts
}

// receiverTypeOf extracts "Manager" from the "(*Manager).Method" and
// "(Manager).Method" spellings QualifiedFuncName produces.
func receiverTypeOf(qualified string) string {
	end := strings.Index(qualified, ")")
	if !strings.HasPrefix(qualified, "(") || end < 0 {
		return ""
	}
	return strings.TrimPrefix(qualified[1:end], "*")
}

// intraPackageCallees returns the keys of the declarations fn calls within its
// own package: a bare `helper(...)` and a `recv.method(...)` on fn's own
// receiver. Anything reached another way (through a field, an interface, or a
// closure) is out of range of a syntax-level walk, which is why the exported
// surface -- not the helper layer -- is what the kinds are asserted against.
func intraPackageCallees(key, recv string, fn *ast.FuncDecl) []string {
	prefix := ""
	if receiverTypeOf(key) != "" {
		prefix = key[:strings.Index(key, ")")+2] // "(*Manager)." / "(Manager)."
	}
	var out []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch f := call.Fun.(type) {
		case *ast.Ident:
			out = append(out, f.Name)
		case *ast.SelectorExpr:
			if ident, ok := f.X.(*ast.Ident); ok && recv != "" && ident.Name == recv && prefix != "" {
				out = append(out, prefix+f.Sel.Name)
			}
		}
		return true
	})
	return out
}

// sortedKeys keeps the scan's failure output stable across runs.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// registryMethod is what the source says about one *Manager method.
type registryMethod struct {
	touchesState    bool
	callsAuthorizer bool
	takesWorkerID   bool
	takesConn       bool
}

// inspectRegistryMethod reads the structural facts a registryMethodKind claims.
// A worker id is a string parameter: every registry method that names one
// worker takes it that way, and the Manager has no other string-shaped input.
func inspectRegistryMethod(recv string, fn *ast.FuncDecl) registryMethod {
	var m registryMethod
	if fn.Type.Params != nil {
		for _, field := range fn.Type.Params.List {
			switch typ := field.Type.(type) {
			case *ast.Ident:
				if typ.Name == "string" {
					m.takesWorkerID = true
				}
			case *ast.StarExpr:
				if ident, ok := typ.X.(*ast.Ident); ok && ident.Name == "Conn" {
					m.takesConn = true
				}
			}
		}
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if ident, ok := sel.X.(*ast.Ident); !ok || ident.Name != recv {
			return true
		}
		switch {
		case registryStateFields[sel.Sel.Name]:
			m.touchesState = true
		case sel.Sel.Name == "reachAuth":
			m.callsAuthorizer = true
		}
		return true
	})
	return m
}

// receiverIdent returns the name the method binds its receiver to, or "" for an
// unnamed receiver (which cannot touch any field).
func receiverIdent(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 || len(fn.Recv.List[0].Names) == 0 {
		return ""
	}
	return fn.Recv.List[0].Names[0].Name
}

// carriesPrincipal reports whether a caller identity is in fn's path -- the
// structural stand-in for "the worker id here may be user-supplied".
//
// Three routes count. A *auth.UserInfo PARAMETER is the obvious one; a bare
// userid.UserID parameter is the same fact with the wrapper peeled off, and
// leaving it out meant a function could take the caller's identity in its most
// explicit form and still be classified reachServerInitiated without the
// assertion firing. The third is the dominant shape in hub/service: a handler
// that takes only a context and calls auth.MustGetUser(ctx) in its body;
// checking parameters alone left this assertion unable to fire for most of the
// package it guards.
func carriesPrincipal(file *ast.File, fn *ast.FuncDecl) bool {
	authAlias, _ := testutil.ImportedAs(file, authPkg)
	useridAlias, _ := testutil.ImportedAs(file, useridPkg)
	return carriesPrincipalWithAlias(authAlias, useridAlias, fn)
}

func carriesPrincipalWithAlias(authAlias, useridAlias string, fn *ast.FuncDecl) bool {
	if fn.Type.Params != nil {
		for _, field := range fn.Type.Params.List {
			if isUserIDType(useridAlias, field.Type) {
				return true
			}
			star, ok := field.Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			if isPkgSelector(authAlias, star.X, "UserInfo") {
				return true
			}
		}
	}
	if fn.Body == nil || authAlias == "" {
		return false
	}
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if (sel.Sel.Name == "MustGetUser" || sel.Sel.Name == "GetUser") && isPkgIdent(authAlias, sel.X) {
			found = true
			return false
		}
		return true
	})
	return found
}

// identityCompareMethods are userid.UserID's two comparison methods -- the only
// ways to ask "is this caller that user" without unwrapping the type.
//
// MatchesUser is in this set because of what it used to be called. As Equal it
// was unscannable: `Equal` is Go's most overloaded method name (time.Time,
// big.Int, netip.Addr, every proto message), so a syntax-level rule could not
// tell an identity comparison from a timestamp one, and the net simply did not
// look at it -- while worker/service.requireWorkerOwner, the worker-side
// ownership gate, decided ownership through exactly that call. Renaming the
// method so both identity comparisons share the Matches prefix is what makes
// this set exact instead of a guess.
var identityCompareMethods = map[string]bool{"Matches": true, "MatchesUser": true}

// isIdentityComparison reports whether ref names a caller-identity comparison:
// `<x>.Matches(...)` / `<x>.MatchesUser(...)` on a userid.UserID, or
// `auth.IsOwner(...)`.
//
// CredentialIdentity also has a Matches method answering a different question
// (is this the same CREDENTIAL), so it is excluded by its receiver's trailing
// selector. That is a heuristic rather than a type check -- resolving receivers
// would need go/types and a full package load -- so the caller also asserts the
// scan matched something, which turns a silently-broken heuristic into a
// failure. The heuristic errs toward over-matching: an unrelated `.Matches`
// costs one reviewed table entry, whereas under-matching is a silent hole.
func isIdentityComparison(authAlias string, inAuthPkg bool, ref symbolRef) bool {
	// Inside package auth itself IsOwner is unqualified; elsewhere it is
	// reached through whatever identifier the file imported auth as.
	if ref.sel == nil {
		return inAuthPkg && ref.ident.Name == "IsOwner"
	}
	sel := ref.sel
	if sel.Sel.Name == "IsOwner" && isPkgIdent(authAlias, sel.X) {
		return true
	}
	if !identityCompareMethods[sel.Sel.Name] {
		return false
	}
	if recv, ok := sel.X.(*ast.SelectorExpr); ok && recv.Sel.Name == "Credential" {
		return false
	}
	return true
}

// isAnyOrgCall reports whether ref names auth.AnyOrg -- spelled through
// whatever identifier the file imported hub/auth as, or bare inside package
// auth itself. AnyOrg is the ONLY constructor that skips the organization
// binding, so this is the whole population of org-check carve-outs.
func isAnyOrgCall(authAlias string, inAuthPkg bool, ref symbolRef) bool {
	if ref.sel == nil {
		// A bare AnyOrg is auth's own only inside package auth; anywhere else
		// it names some unrelated helper.
		return inAuthPkg && ref.ident.Name == "AnyOrg"
	}
	return ref.sel.Sel.Name == "AnyOrg" && isPkgIdent(authAlias, ref.sel.X)
}

// isMustNewCall reports whether ref names userid.MustNew -- spelled through
// whatever identifier the file imported it as, or bare inside package userid
// itself.
func isMustNewCall(useridAlias string, inUseridPkg bool, ref symbolRef) bool {
	if ref.sel == nil {
		// A bare MustNew is this package's own only inside package userid;
		// anywhere else it names some unrelated helper.
		return inUseridPkg && ref.ident.Name == "MustNew"
	}
	return ref.sel.Sel.Name == "MustNew" && isPkgIdent(useridAlias, ref.sel.X)
}

// isPkgIdent reports whether expr is the identifier alias, with an empty alias
// never matching (the file does not import that package under any name).
func isPkgIdent(alias string, expr ast.Expr) bool {
	if alias == "" {
		return false
	}
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == alias
}

// isPkgSelector reports whether expr is `alias.Name`.
func isPkgSelector(alias string, expr ast.Expr, name string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == name && isPkgIdent(alias, sel.X)
}

// isUserIDType reports whether expr names userid.UserID.
func isUserIDType(useridAlias string, expr ast.Expr) bool {
	return isPkgSelector(useridAlias, expr, "UserID")
}

// isIdentityFieldName reports whether a struct field name denotes a user
// identity.
//
// It matches by SUFFIX rather than against a closed list of four spellings.
// The closed list was itself a convention: `TargetUserID`, `ActorUserID`, and
// `OwnerID` all name an identity, none of them appeared in it, and each would
// have been born as an untyped string inside a package that had explicitly
// opted into typed identity -- with the rule reporting green. Over-matching is
// nearly free here: the rule applies only to packages that opted in, and the
// remedy for a false positive is to give the field the type the package
// already promised.
func isIdentityFieldName(name string) bool {
	return strings.HasSuffix(name, "UserID") ||
		strings.HasSuffix(name, "OwnerID") ||
		name == "RegisteredBy" ||
		name == "CreatedBy"
}

// renderExpr prints a type expression for a failure message.
func renderExpr(expr ast.Expr) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, token.NewFileSet(), expr); err != nil {
		return "<unprintable>"
	}
	return buf.String()
}

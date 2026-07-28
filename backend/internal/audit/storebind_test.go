package audit

import (
	"fmt"
	"go/ast"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

// ownerColPattern matches a column that names a row's owner; sqlParamPattern
// matches every way this repo's dialects spell a bound parameter.
//
// Both are deliberately broader than what the queries use TODAY. The rule's
// failure mode is silent: a predicate whose spelling the regex does not know is
// simply never classified as an ownership query, so its adapter is never
// required to refuse an unminted caller and nothing anywhere reports a gap.
// `sqlc.narg` is the concrete case -- it is already used elsewhere in these
// files, and one of them spells out `registered_by = narg(user_id)` while
// arguing why a query is NOT written that way, so the next author who does
// write it would have slipped straight through.
const sqlParamPattern = `(?:\?|\$\d+|@\w+|sqlc\.(?:arg|narg|slice)\b)`

var (
	sqlcNameRe = regexp.MustCompile(`(?m)^--\s*name:\s*(\w+)\s*:`)
	// ownerColPattern is derived from ownerColumns rather than restating it, so
	// the two cannot drift: a column added to that slice and forgotten here
	// would leave its queries silently unclassified.
	ownerColPattern = `(?:` + strings.Join(ownerColumns, `|`) + `)\b`
	ownerBindRe     = regexp.MustCompile(`(?i)` +
		// col = ? / col != $1 / col <> sqlc.arg(...)
		`\b` + ownerColPattern + `\s*(?:=|!=|<>)\s*` + sqlParamPattern +
		// col IN (sqlc.slice(...)) / col = ANY($1)
		`|\b` + ownerColPattern + `\s*(?:=\s*ANY|IN)\s*\(\s*` + sqlParamPattern +
		// ? = col -- the same predicate written the other way round
		`|` + sqlParamPattern + `\s*=\s*` + ownerColPattern +
		// t.user_id = k.user_id -- the owner column joined against an ALIASED
		// owner column instead of a parameter. The bind is still there, one
		// level of indirection away: postgres's bulk tab deletes unnest the
		// bound `sqlc.arg(user_ids)` array into a CTE and join on its user_id
		// column, so the predicate never puts a parameter spelling next to the
		// column and the three branches above all miss it. That is not
		// hypothetical -- removing store.FilterTabIndexKeys from BOTH postgres
		// bulk deletes left this rule green, on the exact fail-open it exists
		// to catch.
		`|\b` + ownerColPattern + `\s*(?:=|!=|<>)\s*\w+\.` + ownerColPattern)
)

// sqlcDir is one sqlc-owning directory: the queries it generates Go from, the
// schema those queries are checked against, and the logical DATABASE it belongs
// to. Every field is READ OUT OF the directory's own sqlc.yaml rather than
// assumed, so a layout change fails loudly instead of shrinking the scan.
//
// database is the PARENT directory, not the directory itself, because the three
// hub dialects (internal/hub/store/{sqlite,postgres,mysql}) are three spellings
// of ONE database and deliberately share every query name. The worker
// (internal/worker) is a different database, and a name shared across that
// boundary would let a call site in one process be judged against the other's
// SQL -- see TestSqlcQueryNames_DoNotCollideAcrossDatabases.
type sqlcDir struct {
	rel        string // repo-relative directory holding sqlc.yaml
	database   string // repo-relative parent: the logical DB its dialects share
	queriesDir string // absolute
	schemaDir  string // absolute
	genPkg     string // import path of the generated Queries package
}

var (
	sqlcQueriesRe = regexp.MustCompile(`(?m)^\s*queries:\s*"([^"]+)"`)
	sqlcSchemaRe  = regexp.MustCompile(`(?m)^\s*schema:\s*"([^"]+)"`)
	sqlcOutRe     = regexp.MustCompile(`(?m)^\s*out:\s*"([^"]+)"`)
)

// modulePath is the backend module, which turns a repo-relative directory into
// the import path a Go file would name it by.
const modulePath = "github.com/leapmux/leapmux"

// sqlcDirs are the directories that ship their own sqlc queries, discovered by
// walking for sqlc.yaml rather than listed.
//
// The three hub dialects were hardcoded twice in this file, and the WORKER
// database -- a fourth sqlc.yaml, with its own owner-keyed tables -- was in
// neither list. Nothing read its .sql for ownership predicates and nothing
// checked its call sites, while the rule reported green over the other three.
// That is the same shape as the reach net's old three-name accessor list, and
// the same shape as the hardcoded dialect list this replaces: deriving the set
// means a new database is covered the moment its sqlc.yaml exists, and a rename
// fails loudly instead of silently shrinking the scan.
func sqlcDirs(t *testing.T, root string) []sqlcDir {
	t.Helper()

	var dirs []sqlcDir
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Generated output holds no hand-written SQL, and neither does a
			// hidden tree -- `.gen-stage-*` is sqlc's own staging copy of a
			// dialect's sqlc.yaml, migrations, and queries, and counting one
			// would register a phantom database whose name set collides with
			// the real one it was copied from. This mirrors the skip list in
			// testutil.walkRepoGoFiles, which is the single definition of
			// "which trees a repo-wide invariant looks at" for Go source.
			if d.Name() == "generated" || (path != root && strings.HasPrefix(d.Name(), ".")) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() != "sqlc.yaml" {
			return nil
		}
		raw, err := os.ReadFile(path)
		require.NoError(t, err, "read %s", path)
		queries := sqlcQueriesRe.FindStringSubmatch(string(raw))
		schema := sqlcSchemaRe.FindStringSubmatch(string(raw))
		out := sqlcOutRe.FindStringSubmatch(string(raw))
		require.NotNil(t, queries, "%s declares no `queries:` directory; the scan cannot find its SQL", path)
		require.NotNil(t, schema, "%s declares no `schema:` directory; the scan cannot find its DDL", path)
		require.NotNil(t, out, "%s declares no `out:` directory; the scan cannot tell which packages call its queries", path)
		dir := filepath.Dir(path)
		rel, err := filepath.Rel(root, dir)
		require.NoError(t, err)
		dirs = append(dirs, sqlcDir{
			rel:        filepath.ToSlash(rel),
			database:   filepath.ToSlash(filepath.Dir(rel)),
			queriesDir: filepath.Join(dir, filepath.FromSlash(queries[1])),
			schemaDir:  filepath.Join(dir, filepath.FromSlash(schema[1])),
			genPkg:     modulePath + "/" + filepath.ToSlash(filepath.Join(rel, out[1])),
		})
		return nil
	})
	require.NoError(t, err, "walk %s", root)
	require.NotEmpty(t, dirs, "no sqlc.yaml found under %s; the scan is broken, not the code", root)
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].rel < dirs[j].rel })
	return dirs
}

// queryCallingPackages are the repo-relative package directories that can run a
// generated sqlc query: the ones importing a generated Queries package.
//
// The scan keys on the query NAME, and every store INTERFACE method is named
// after the query it fronts, so without this filter a service calling
// `st.LifecycleOutbox().ListPendingLifecycleOutbox(...)` reads as a raw query
// call site and is asked to repeat a refusal its adapter already performs. The
// import is the exact discriminator: a package that never imports the generated
// package cannot name its Queries type, so it cannot be running the SQL.
//
// It is keyed on the PACKAGE, not the file, so a call that happens to need no
// generated Params struct -- `svc.Queries.CountWorktreeTabs(ctx, id)` -- is
// still in scope as long as some file in its package imports the package.
func queryCallingPackages(t *testing.T, dirs []sqlcDir, files []parsedFile) map[string]bool {
	t.Helper()

	out := map[string]bool{}
	for _, f := range files {
		for _, dir := range dirs {
			if _, imported := testutil.ImportedAs(f.file, dir.genPkg); imported {
				out[packageDir(f.rel)] = true
				break
			}
		}
	}
	require.NotEmpty(t, out,
		"no package imports a generated sqlc package; the adapter scan is broken, not the code")
	return out
}

// forEachSQLCQuery visits every sqlc query in every sqlc-owning directory, body
// already stripped of `--` comments.
func forEachSQLCQuery(t *testing.T, dirs []sqlcDir, visit func(dir sqlcDir, path, name, body string)) {
	t.Helper()

	for _, dir := range dirs {
		paths, err := filepath.Glob(filepath.Join(dir.queriesDir, "*.sql"))
		require.NoError(t, err)
		require.NotEmpty(t, paths, "no .sql files under %s; the scan is broken, not the code", dir.queriesDir)
		for _, path := range paths {
			raw, err := os.ReadFile(path)
			require.NoError(t, err, "read %s", path)
			for name, body := range sqlcQueryBodies(string(raw)) {
				visit(dir, path, name, stripSQLComments(body))
			}
		}
	}
}

// checkOwnerFilterCoverage is the store-bind rule: every generated query whose
// WHERE clause filters on an owner column must be run by a function that first
// routes the caller id through a shared owner guard (userid.OwnerFilter, or one
// of the helpers that perform the identical refusal internally).
//
// It is derived from the SQL, not from the Go, and that is what makes it
// precise. An INSERT has no WHERE, so the ~37 raw `.String()` unwraps that
// remain -- all column VALUES rather than predicates -- are out of scope
// automatically, with no allowlist needed to say so.
//
// The ADAPTER scan is repo-wide, keyed on the query name, rather than scoped to
// the directories that own the .sql. The hub dialects co-locate db/queries with
// the adapters that run them, so a per-directory scan worked there by accident;
// the worker does not -- its queries live in internal/worker/db and its callers
// in internal/worker/service -- so re-rooting per directory would have collected
// the worker's query names, looked for call sites in a package that contains
// none, and stayed green while checking nothing.
//
// Being derived from the .sql files is also its one structural limit: SQL a Go
// function assembles at runtime is invisible to it. checkHandBuiltOwnerSQL
// below covers that half; the two together are the rule.
func checkOwnerFilterCoverage(t *testing.T, root string, files []parsedFile) {
	t.Helper()

	dirs := sqlcDirs(t, root)
	ownershipQueries := ownershipQueryNames(t, dirs)
	require.NotEmpty(t, ownershipQueries,
		"no ownership queries found; the SQL scan is broken, not the code")
	callers := queryCallingPackages(t, dirs, files)

	checked := 0
	for _, f := range files {
		if !callers[packageDir(f.rel)] {
			continue
		}
		guards := newOwnerGuardScope(packageDir(f.rel), f.file)
		enclosing := testutil.NewEnclosingFuncFinder(f.file)
		// Which functions route the caller id through a shared guard --
		// userid.OwnerFilter / userid.New, or store.GetOwnedWorker, whose whole
		// body is the same refusal followed by the ownership comparison.
		//
		// Calling the guard is not enough: the refusal has to be acted on.
		// `owner, _ := userid.OwnerFilter(p.UserID)` followed by binding
		// owner (== "") reintroduces the exact blank-owner fail-open this
		// rule exists to stop, and a presence-only check passes it.
		guarded := map[string]bool{}
		unhonoured := map[string]string{}
		ast.Inspect(f.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !guards.isSharedGuardCall(call) {
				return true
			}
			fn, in := enclosing.Find(call.Pos())
			if !in {
				return true
			}
			name := testutil.QualifiedFuncName(fn)
			if !guards.isCallerActedGuardCall(call) {
				guarded[name] = true // GetOwnedWorker refuses internally
				return true
			}
			why, honoured := ownerFilterRefusalHonoured(fn, call)
			if honoured {
				guarded[name] = true
				return true
			}
			if _, already := unhonoured[name]; !already {
				unhonoured[name] = why
			}
			// An unhonoured OwnerFilter is reported for its own sake, not only
			// through the coverage assertion below: OwnerFilter's ONLY purpose
			// is to unwrap an id for an ownership predicate, so discarding its
			// refusal inside a query-running package is a defect on its face
			// even in a function this rule does not otherwise classify.
			//
			// userid.New is not reported that way, because it is the general
			// mint: MustNew turns the refusal into a panic, and several callers
			// deliberately keep the zero value. It still has to be honoured to
			// COUNT as a guard below -- a blessing that skipped the refusal
			// check would double as an exemption -- but an unhonoured New in a
			// function that runs no ownership query decides nothing.
			if guards.isBindOnlyGuardCall(call) {
				assert.Fail(t, "shared owner guard result is not acted on",
					"%s: %s() %s -- a zero UserID unwraps to \"\", which MATCHES every blank-owner row instead of none, so the refusal must gate an early return",
					position(f.fset, call.Pos(), f.rel), name, why)
			}
			return true
		})

		ast.Inspect(f.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !ownershipQueries[sel.Sel.Name] {
				return true
			}
			if _, allowed := unguardedOwnerFilterQueries[sel.Sel.Name]; allowed {
				return true
			}
			checked++
			fn, in := enclosing.Find(sel.Pos())
			if !in {
				assert.Fail(t, "ownership query outside any function",
					"%s: %s runs at package level and can never be guarded",
					position(f.fset, sel.Pos(), f.rel), sel.Sel.Name)
				return true
			}
			name := testutil.QualifiedFuncName(fn)
			detail := ""
			if why, called := unhonoured[name]; called {
				detail = fmt.Sprintf(" (it calls one, but %s)", why)
			}
			assert.True(t, guarded[name],
				"%s: %s() runs %s, whose WHERE filters an owner column, without routing the caller id through a shared owner guard%s (userid.OwnerFilter, or userid.New for an id that was never minted) -- a zero UserID unwraps to \"\", which MATCHES every blank-owner row instead of none. Add the guard, or register %q in unguardedOwnerFilterQueries with the reason.",
				position(f.fset, sel.Pos(), f.rel), name, sel.Sel.Name, detail, sel.Sel.Name)
			return true
		})
	}
	assert.NotZero(t, checked, "no ownership query call sites found; the adapter scan is broken, not the code")

	for q, why := range unguardedOwnerFilterQueries {
		assert.True(t, ownershipQueries[q],
			"unguardedOwnerFilterQueries exempts %q, which no longer filters on an owner column -- remove the stale exemption", q)
		// An exemption with no reason is a silent one, which is the thing this
		// table exists to make impossible.
		assert.NotEmpty(t, why, "unguardedOwnerFilterQueries exempts %q with no recorded reason", q)
	}
}

// checkHandBuiltOwnerSQL is the second half of the store-bind rule, covering
// the half checkOwnerFilterCoverage structurally cannot see.
//
// That rule is derived from the sqlc .sql files, so it is blind to SQL a Go
// function assembles at runtime -- and that blind spot is not hypothetical: it
// is exactly how `DELETE FROM <table> WHERE (user_id, tab_id) IN ((?, ?), ...)`
// in sqlutil reached main binding a blank owner. The predicate lived in a Go
// string literal, so no .sql file mentioned it, so no query name was ever
// classified as an ownership predicate, so no adapter was ever required to
// refuse. The rule reported green over the bug it exists to catch.
//
// The shape has to differ from ownerBindRe, which matches `col = ?` inside ONE
// literal. A builder splits the predicate across literals it appends in a loop
// (`" WHERE (user_id, tab_id) IN ("` then `"(?, ?)"`), so no single literal
// carries both halves. This rule therefore drops to the function level: if the
// literals of one function mention WHERE *and* an owner column, that function
// composes an ownership predicate and must route through a shared owner guard.
//
// That is deliberately coarser than ownerBindRe and can over-report -- which is
// the same trade whereClause already makes. An over-report costs one reviewed
// call to the shared guard (or an argued exemption); an under-report is a
// silent fail-open. Today it classifies exactly one function repo-wide, and
// that function passes.
func checkHandBuiltOwnerSQL(t *testing.T, root string) {
	t.Helper()

	storeRoot := filepath.Join(root, filepath.FromSlash(storeDir))
	classified := 0
	for _, dir := range goPackageDirs(t, storeRoot) {
		rel, err := filepath.Rel(root, dir)
		require.NoError(t, err)
		testutil.ForEachPackageSourceFile(t, dir, func(fset *token.FileSet, file *ast.File) {
			guards := newOwnerGuardScope(filepath.ToSlash(rel), file)
			enclosing := testutil.NewEnclosingFuncFinder(file)

			// Which functions compose SQL naming an owner column, and which
			// route through a shared guard. Both are collected per function so
			// the two passes can be compared without depending on which comes
			// first in the source.
			composes := map[string]token.Pos{}
			guarded := map[string]bool{}
			litsByFunc := map[string][]string{}
			ast.Inspect(file, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.BasicLit:
					if node.Kind != token.STRING {
						return true
					}
					if fn, in := enclosing.Find(node.Pos()); in {
						name := testutil.QualifiedFuncName(fn)
						litsByFunc[name] = append(litsByFunc[name], node.Value)
						if _, seen := composes[name]; !seen {
							composes[name] = fn.Pos()
						}
					}
				case *ast.CallExpr:
					if !guards.isSharedGuardCall(node) {
						return true
					}
					if fn, in := enclosing.Find(node.Pos()); in {
						guarded[testutil.QualifiedFuncName(fn)] = true
					}
				}
				return true
			})

			for name, lits := range litsByFunc {
				joined := strings.ToLower(strings.Join(lits, "\n"))
				if !strings.Contains(joined, "where") || !mentionsOwnerColumn(joined) {
					continue
				}
				classified++
				pos := fset.Position(composes[name])
				assert.True(t, guarded[name],
					"%s:%d: %s() composes SQL naming both WHERE and an owner column, but never routes the id through a shared owner guard (userid.OwnerFilter / store.FilterTabIndexKeys) -- a blank owner binds \"\", which MATCHES every blank-owner row instead of none. This is the runtime-composed half of the store-bind rule; the sqlc scan cannot see it.",
					filepath.Base(pos.Filename), pos.Line, name)
			}
		})
	}
	// The rule's own silent failure mode: a walk that reaches no SQL-composing
	// function passes while checking nothing. sqlutil.BulkDeleteTabs is the one
	// site today, so zero means the scan broke, not that the code got safer.
	assert.NotZero(t, classified,
		"no runtime-composed ownership SQL found under %s; the scan is broken, not the code", storeDir)
}

// mentionsOwnerColumn reports whether lowercased text names one of the columns
// that identify a row's owner. It reads ownerColumns for the same reason
// ownerBindRe is built from it: a column added there must widen every rule at
// once, not just the one whose author remembered.
func mentionsOwnerColumn(lowered string) bool {
	for _, col := range ownerColumns {
		if strings.Contains(lowered, col) {
			return true
		}
	}
	return false
}

// goPackageDirs returns root and every directory beneath it that holds
// non-test Go source, skipping sqlc/proto output under `generated`.
//
// Recursion is the point: the sqlc-derived rule walks only the three dialect
// directories, which is precisely why sqlutil -- a sibling package that all
// three delegate their runtime-composed SQL to -- was scanned by nothing.
func goPackageDirs(t *testing.T, root string) []string {
	t.Helper()

	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == "generated" {
			return fs.SkipDir
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, ent := range entries {
			name := ent.Name()
			if !ent.IsDir() && strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
				dirs = append(dirs, path)
				break
			}
		}
		return nil
	})
	require.NoError(t, err, "walk %s", root)
	require.NotEmpty(t, dirs, "no Go packages found under %s", root)
	return dirs
}

// ownershipQueryNames returns the sqlc query names whose body contains a
// WHERE-clause predicate binding an owner column to a parameter.
func ownershipQueryNames(t *testing.T, dirs []sqlcDir) map[string]bool {
	t.Helper()

	names := map[string]bool{}
	forEachSQLCQuery(t, dirs, func(_ sqlcDir, _, name, body string) {
		// Only a WHERE-clause predicate counts. An INSERT's column list and
		// an `UPDATE ... SET user_id = ?` both mention the same identifier
		// while binding a VALUE, and neither decides ownership -- so the
		// match is scoped to the text after WHERE.
		if where, ok := whereClause(body); ok && ownerBindRe.MatchString(where) {
			names[name] = true
		}
	})
	return names
}

// stripSQLComments removes `--` line comments so prose cannot be read as SQL.
// These files carry long explanatory blocks -- one of them spells out
// `registered_by = narg(user_id)` while arguing why a query is NOT written that
// way -- and matching it would classify an unrelated query as an ownership
// predicate.
func stripSQLComments(body string) string {
	var b strings.Builder
	for line := range strings.SplitSeq(body, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// sqlcQueryBodies splits a .sql file into name -> body using sqlc's
// `-- name: Xxx :kind` markers.
func sqlcQueryBodies(src string) map[string]string {
	out := map[string]string{}
	locs := sqlcNameRe.FindAllStringSubmatchIndex(src, -1)
	for i, loc := range locs {
		name := src[loc[2]:loc[3]]
		end := len(src)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		out[name] = src[loc[1]:end]
	}
	return out
}

// ownerGuardScope resolves the shared owner-guard calls visible in ONE file.
//
// It is built from the file's IMPORTS rather than from the identifier at the
// call site, which is the alias-blindness the identity rule already avoids via
// testutil.ImportedAs. Keying on the literal spelling `store` made a file that
// wrote `hubstore "internal/hub/store"` invisible to the whole rule -- the
// guard call would not be recognised, so the adapter would look unguarded, and
// (worse) a file aliasing some UNRELATED package to `store` would have that
// package's GetOwnedWorker blessed as a refusal it never performs.
type ownerGuardScope struct {
	useridAlias string
	storeAlias  string
	inUseridPkg bool
	inStorePkg  bool
}

// dir is the file's repo-relative package directory (packageDir), which is what
// tells the two guard-defining packages apart from every other one: inside them
// the guard is called unqualified, so a selector-only rule would leave the
// packages that OWN the refusal as the only places it is not enforced.
func newOwnerGuardScope(dir string, file *ast.File) ownerGuardScope {
	useridAlias, _ := testutil.ImportedAs(file, useridPkg)
	storeAlias, _ := testutil.ImportedAs(file, storePkg)
	return ownerGuardScope{
		useridAlias: useridAlias,
		storeAlias:  storeAlias,
		inUseridPkg: dir == useridDir,
		inStorePkg:  dir == storeDir,
	}
}

// guardCallName resolves call to (package, function) for the two packages that
// define the shared owner guards, and ok=false for anything else.
//
// The *ast.Ident branch is the unqualified call: inside the package that DEFINES
// a guard it is spelled without a qualifier, so a selector-only rule would leave
// the packages that own the refusal as the only two places it is not enforced.
func (s ownerGuardScope) guardCallName(call *ast.CallExpr) (pkg, name string, ok bool) {
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		ident, isIdent := fun.X.(*ast.Ident)
		if !isIdent {
			return "", "", false
		}
		switch {
		case s.useridAlias != "" && ident.Name == s.useridAlias:
			return useridPkg, fun.Sel.Name, true
		case s.storeAlias != "" && ident.Name == s.storeAlias:
			return storePkg, fun.Sel.Name, true
		}
	case *ast.Ident:
		switch {
		case s.inUseridPkg:
			return useridPkg, fun.Name, true
		case s.inStorePkg:
			return storePkg, fun.Name, true
		}
	}
	return "", "", false
}

// isSharedGuardCall reports whether call routes a caller id through a shared
// owner guard.
//
// userid.OwnerFilter and userid.New are the bind-time refusals, which the
// CALLER must act on (see isCallerActedGuardCall); store.GetOwnedWorker and
// store.FilterTabIndexKeys are the shared helpers that perform the identical
// refusal internally (GetOwnedWorker plus the comparison, FilterTabIndexKeys
// per key across a bulk batch), so a caller delegating to either is guarded
// just as thoroughly.
func (s ownerGuardScope) isSharedGuardCall(call *ast.CallExpr) bool {
	pkg, name, ok := s.guardCallName(call)
	if !ok {
		return false
	}
	switch pkg {
	case useridPkg:
		return isCallerActedGuardName(name)
	case storePkg:
		return name == "GetOwnedWorker" || name == "FilterTabIndexKeys" ||
			name == "FilterTabIndexKeysForTable"
	}
	return false
}

// isCallerActedGuardName reports whether name is a shared guard that returns an
// (owner, ok) pair for the CALLER to act on, as opposed to one that refuses
// internally.
//
// New is in the set alongside OwnerFilter because it IS the same refusal for an
// id that never passed through the type: OwnerFilter is `IsZero -> refuse, else
// unwrap`, and New is `empty -> refuse, else mint` with String() as the unwrap.
// The worker binds owner columns from ids it holds as plain strings (a
// worker_file_tabs row's user_id, a normalized worktree link owner), so without
// New the only guard reachable there would be no guard at all -- and the whole
// point of moving OwnerFilter into package userid was that the worker can call
// it. A name that merely LOOKS generic is not a risk here: the package is
// matched by import path, so some other package's New never qualifies.
//
// It exists as its own predicate because BOTH classifiers must agree on the
// set: isSharedGuardCall (which blesses a caller for delegating) and
// isCallerActedGuardCall (which then demands an honoured `if !ok`). A name
// reaching only the first would bless every caller that calls it while silently
// exempting all of them from ownerFilterRefusalHonoured -- the rule would
// report green with `owner, _ := userid.OwnerFilter(...)` binding "" into the
// predicate, which is the exact fail-open it exists to stop. Routing both
// classifiers through this one function makes that divergence unspellable.
// TestOwnerFilterRefusalHonoured pins that a call this reports true for is
// also held to the refusal check.
func isCallerActedGuardName(name string) bool {
	return name == "OwnerFilter" || name == "New"
}

// isCallerActedGuardCall reports whether call is a guard whose refusal the
// enclosing function must act on, as opposed to one that refuses internally.
func (s ownerGuardScope) isCallerActedGuardCall(call *ast.CallExpr) bool {
	pkg, name, ok := s.guardCallName(call)
	return ok && pkg == useridPkg && isCallerActedGuardName(name)
}

// isBindOnlyGuardCall reports whether call is a guard that exists SOLELY to
// unwrap an id for an ownership predicate, so discarding its refusal is a defect
// wherever it appears -- not only in a function this rule already classifies.
//
// OwnerFilter qualifies and New does not, and that is the whole distinction:
// New is the general mint, so MustNew turns its refusal into a panic and other
// callers deliberately keep the zero value, while OwnerFilter's only caller
// shape is "unwrap this to bind it".
func (s ownerGuardScope) isBindOnlyGuardCall(call *ast.CallExpr) bool {
	pkg, name, ok := s.guardCallName(call)
	return ok && pkg == useridPkg && name == "OwnerFilter"
}

// ownerFilterRefusalHonoured reports whether fn actually acts on the second
// result of an OwnerFilter call: it must bind it to a named variable and use
// that variable to gate an early return.
//
// why explains the failure for the assertion message. Every call site in the
// repo is the same two-line shape (`owner, ok := userid.OwnerFilter(...)`,
// immediately followed by `if !ok { return ... }`), so requiring it costs no
// false positives -- and without it the whole rule reduces to "the function
// mentions OwnerFilter somewhere", which a dropped `if !ok` satisfies.
func ownerFilterRefusalHonoured(fn *ast.FuncDecl, call *ast.CallExpr) (why string, ok bool) {
	assign := assignmentOf(fn.Body, call)
	if assign == nil || len(assign.Lhs) < 2 {
		return "does not bind its (value, ok) results to variables", false
	}
	okIdent, isIdent := assign.Lhs[1].(*ast.Ident)
	if !isIdent || okIdent.Name == "_" {
		return "discards the ok result", false
	}
	valueName := ""
	if v, isIdent := assign.Lhs[0].(*ast.Ident); isIdent {
		valueName = v.Name
	}
	if !hasGuardedReturn(fn.Body, okIdent.Name, valueName, assign.End()) {
		return fmt.Sprintf("never returns early on !%s before using the value it guards", okIdent.Name), false
	}
	return "", true
}

// assignmentOf finds the assignment statement whose right-hand side is call.
func assignmentOf(body *ast.BlockStmt, call *ast.CallExpr) *ast.AssignStmt {
	var found *ast.AssignStmt
	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, rhs := range assign.Rhs {
			if rhs == ast.Expr(call) {
				found = assign
				return false
			}
		}
		return true
	})
	return found
}

// hasGuardedReturn reports whether body refuses on !okName -- an `if` that
// returns early and whose condition includes `!okName` -- positioned after the
// guard call's assignment (bindPos) and before the first use of the value that
// guard produced (valueName).
//
// Both halves of that are load-bearing, and the rule was unsound without them:
//
//   - Position. Searching the whole body accepted a guard placed AFTER the
//     value was already bound into a query, which refuses nothing -- the query
//     has run with a blank owner. It also accepted an unrelated
//     `v, ok := m[k]; if !ok { return }` elsewhere in the same function, purely
//     because the identifier matched. hasEmptyStringGuardBefore has always
//     bounded on position for exactly this reason; this rule now matches it.
//
//   - Disjunction. Matching only a bare `!ok` rejected the natural
//     `if !ok || tabID == "" { return }`, which pushed production code into
//     splitting one refusal into two statements purely to stay visible -- the
//     scanner dictating the shape of the code it audits, which is backwards.
//     Walking `||` operands (as comparesToEmptyString already does) accepts
//     either spelling.
//
// Known limitation, deliberately left: the match is by identifier name, so a
// DIFFERENT `ok` bound between the assignment and the first use would satisfy
// it. Closing that needs type information, not syntax.
func hasGuardedReturn(body *ast.BlockStmt, okName, valueName string, bindPos token.Pos) bool {
	// Candidate guards: refuse on !okName, return early, and sit after the
	// assignment that produced okName.
	var guards []*ast.IfStmt
	ast.Inspect(body, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok || ifStmt.Pos() < bindPos || !negates(ifStmt.Cond, okName) {
			return true
		}
		returns := false
		ast.Inspect(ifStmt.Body, func(inner ast.Node) bool {
			if _, isReturn := inner.(*ast.ReturnStmt); isReturn {
				returns = true
				return false
			}
			return true
		})
		if returns {
			guards = append(guards, ifStmt)
		}
		return true
	})
	if len(guards) == 0 {
		return false
	}
	// The first use of the guarded value outside those guards is the deadline:
	// a refusal that lands after it did not protect that use. A discarded value
	// (`_`) has no use to protect, so any guard suffices.
	firstUse := token.NoPos
	if valueName != "" && valueName != "_" {
		ast.Inspect(body, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if !ok || ident.Name != valueName || ident.Pos() < bindPos {
				return true
			}
			for _, g := range guards {
				if ident.Pos() >= g.Pos() && ident.Pos() < g.End() {
					return true
				}
			}
			if firstUse == token.NoPos || ident.Pos() < firstUse {
				firstUse = ident.Pos()
			}
			return true
		})
	}
	for _, g := range guards {
		if firstUse == token.NoPos || g.End() <= firstUse {
			return true
		}
	}
	return false
}

// negates reports whether cond refuses on !name, either directly or as one
// operand of an `||` chain (`!ok || tabID == ""`). An `&&` is deliberately NOT
// walked: `!ok && x` refuses only when x also holds, which is not a refusal on
// !ok at all.
func negates(cond ast.Expr, name string) bool {
	switch c := cond.(type) {
	case *ast.ParenExpr:
		return negates(c.X, name)
	case *ast.BinaryExpr:
		if c.Op != token.LOR {
			return false
		}
		return negates(c.X, name) || negates(c.Y, name)
	case *ast.UnaryExpr:
		if c.Op != token.NOT {
			return false
		}
		ident, ok := c.X.(*ast.Ident)
		return ok && ident.Name == name
	}
	return false
}

// whereClause returns the text of a query body from its first WHERE keyword
// onward. ok is false when the query has no WHERE at all -- an INSERT, or an
// unfiltered SELECT -- in which case it binds no ownership predicate.
//
// Taking everything after the FIRST WHERE is deliberately conservative: a
// subquery's predicate is included too, so the rule over-reports rather than
// under-reports. An over-report costs one reviewed allowlist entry; an
// under-report is a silent fail-open, which is the thing this exists to stop.
func whereClause(body string) (string, bool) {
	idx := strings.Index(strings.ToUpper(body), "WHERE")
	if idx < 0 {
		return "", false
	}
	return body[idx:], true
}

// userFKRe matches both spellings of a foreign key onto users(id): the inline
// column constraint (`user_id TEXT NOT NULL REFERENCES users(id)`, used by
// sqlite and postgres) and the table-level one (`FOREIGN KEY (user_id)
// REFERENCES users(id)`, used by mysql).
//
// (?m) is load-bearing, not decoration: the inline branch is anchored to the
// start of a LINE, and without it `^` matches only the start of the whole file
// -- so that branch could never fire, and the scan silently covered mysql
// alone while reporting a healthy match count from it. The test asserting a
// non-zero find count does not catch that; only planting an unlisted column in
// a sqlite migration does.
var userFKRe = regexp.MustCompile(`(?im)(?:FOREIGN\s+KEY\s*\(\s*(\w+)\s*\)|^\s*(\w+)\s+[^,(]*?)\s*REFERENCES\s+users\s*\(\s*id\s*\)`)

// TestOwnerColumnsCoversEverySchemaReferenceToUsers checks the half of the
// owner-column rule that the regex cannot check for itself.
//
// ownerBindRe is BUILT from ownerColumns, so asserting that it matches each of
// them proves only that the join worked -- the column half of
// TestOwnerBindRe_CoversEveryColumnAndEveryParameterSpelling stopped being a
// cross-check the moment the two stopped being independent sources. The
// question that still has teeth is whether ownerColumns is COMPLETE, and the
// schema answers it independently: a column declared REFERENCES users(id) is
// by definition a reference to a row's owner.
//
// Without this, a new table with `approver_user_id TEXT REFERENCES users(id)`
// would be filtered in a WHERE clause that no rule ever classified as an
// ownership predicate -- the rule's one silent failure mode, reached by adding
// a table rather than by touching any of this.
func TestOwnerColumnsCoversEverySchemaReferenceToUsers(t *testing.T) {
	root := repoRoot(t)
	known := map[string]bool{}
	for _, col := range ownerColumns {
		known[col] = false // false until the schema is seen to declare it
	}

	found := 0
	for _, dir := range sqlcDirs(t, root) {
		paths, err := filepath.Glob(filepath.Join(dir.schemaDir, "*.sql"))
		require.NoError(t, err)
		require.NotEmpty(t, paths, "no migrations found under %s", dir.schemaDir)
		for _, path := range paths {
			raw, err := os.ReadFile(path)
			require.NoError(t, err, "read %s", path)
			for _, m := range userFKRe.FindAllStringSubmatch(stripSQLComments(string(raw)), -1) {
				col := m[1]
				if col == "" {
					col = m[2]
				}
				col = strings.ToLower(col)
				found++
				seen, listed := known[col]
				assert.Truef(t, listed,
					"%s/%s declares %q as a foreign key onto users(id), so it names a row's owner, but ownerColumns does not list it -- every WHERE clause filtering on it is an ownership predicate no rule currently classifies",
					dir.rel, filepath.Base(path), col)
				if listed && !seen {
					known[col] = true
				}
			}
		}
	}
	require.NotZero(t, found, "no users(id) foreign keys found; the schema scan is broken, not the code")

	// The other direction: an entry that no schema declares is a column that no
	// longer exists, and it silently widens the regex against future tables.
	for col, seen := range known {
		assert.Truef(t, seen, "ownerColumns lists %q, which no migration declares as a foreign key onto users(id) -- remove the stale entry", col)
	}
}

// TestSqlcQueryDirs_DiscoversTheWorkerDatabase pins the derivation that replaced
// a hardcoded three-name dialect list.
//
// The worker's absence WAS the finding: internal/worker ships its own sqlc.yaml,
// its own migrations, and two owner-keyed tables, and every rule in this file
// was rooted at internal/hub/store, so none of them ever read a line of it. The
// three integration-only hub packages (cockroachdb / tidb / yugabytedb) reuse
// another dialect's adapter and ship no queries, so they carry no sqlc.yaml and
// correctly stay out of the set.
func TestSqlcQueryDirs_DiscoversTheWorkerDatabase(t *testing.T) {
	root := repoRoot(t)
	var rels, databases []string
	for _, dir := range sqlcDirs(t, root) {
		rels = append(rels, dir.rel)
		databases = append(databases, dir.database)
		assert.DirExists(t, dir.queriesDir, "%s declares a queries directory that does not exist", dir.rel)
		assert.DirExists(t, dir.schemaDir, "%s declares a schema directory that does not exist", dir.rel)
	}
	assert.Contains(t, rels, "internal/worker",
		"the worker database ships its own sqlc.yaml and its own owner-keyed tables; leaving it out is the hole this test exists to keep closed")
	assert.ElementsMatch(t, []string{
		"internal/hub/store/sqlite",
		"internal/hub/store/postgres",
		"internal/hub/store/mysql",
		"internal/worker",
	}, rels)
	assert.ElementsMatch(t, []string{
		"internal/hub/store", "internal/hub/store", "internal/hub/store", "internal",
	}, databases, "the three hub dialects must group under one database, the worker under its own")
}

// TestSqlcQueryNames_DoNotCollideAcrossDatabases pins the assumption the
// repo-wide adapter scan rests on.
//
// That scan is keyed on the query NAME, because the worker's queries and the
// callers that run them live in different directories. Within one database that
// is exact -- the three hub dialects share every name by design, and a call site
// runs whichever dialect it was built with. ACROSS databases it would not be: a
// name appearing in both the hub and the worker would let a hub adapter be
// judged against the worker's SQL, or (worse) let a worker call site be blessed
// by an exemption argued about a hub query.
func TestSqlcQueryNames_DoNotCollideAcrossDatabases(t *testing.T) {
	byName := map[string]map[string]bool{}
	forEachSQLCQuery(t, sqlcDirs(t, repoRoot(t)), func(dir sqlcDir, _, name, _ string) {
		if byName[name] == nil {
			byName[name] = map[string]bool{}
		}
		byName[name][dir.database] = true
	})
	require.NotEmpty(t, byName, "no sqlc queries found; the scan is broken, not the code")
	for name, databases := range byName {
		assert.Len(t, databases, 1,
			"query %q is declared by more than one database (%v) -- the name-keyed adapter scan cannot tell their call sites apart; rename one", name, databases)
	}
}

// TestOwnerKeyedTables_DerivedFromEveryDialectSchema pins the per-dialect count.
//
// A repo-wide "some tables were found" assertion is exactly what would NOT have
// caught the hazard this scan was written around: a CREATE TABLE terminator of
// `\n\)\s*;` matches sqlite and postgres and matches ZERO mysql tables, because
// mysql closes every table with `) COLLATE=utf8mb4_bin;`. The population would
// have looked healthy while mysql -- and every mysql query downstream of it --
// was covered by nothing. That is the same failure the missing (?m) in userFKRe
// would have caused, and the same reason that comment insists a count assertion
// is not enough. Per dialect, or it is not a pin.
func TestOwnerKeyedTables_DerivedFromEveryDialectSchema(t *testing.T) {
	root := repoRoot(t)
	dirs := sqlcDirs(t, root)
	require.NotEmpty(t, dirs)

	for _, dir := range dirs {
		t.Run(dir.rel, func(t *testing.T) {
			keyed := ownerKeyedTables(t, dir)
			assert.NotEmpty(t, keyed,
				"no table with a composite owner-containing PRIMARY KEY found in %s -- every database here has some, so an empty result means this dialect's DDL spelling defeated the parser, not that its schema is flat",
				dir.schemaDir)
		})
	}

	// The hub dialects are three spellings of ONE schema, so a parser that reads
	// them unequally is a parser that misses tables in the dialect it reads
	// worst. Comparing the derived SETS is what makes that visible; a count
	// comparison would pass on two different tables of the same number.
	byDatabase := map[string][]string{}
	for _, dir := range dirs {
		if dir.database != "internal/hub/store" {
			continue
		}
		var tables []string
		for table := range ownerKeyedTables(t, dir) {
			tables = append(tables, table)
		}
		sort.Strings(tables)
		byDatabase[dir.rel] = tables
	}
	require.Len(t, byDatabase, 3, "the hub database must still have three dialects")
	var reference string
	for rel := range byDatabase {
		if reference == "" || rel < reference {
			reference = rel
		}
	}
	for rel, tables := range byDatabase {
		assert.Equal(t, byDatabase[reference], tables,
			"%s derives a different owner-keyed table set than %s -- the three dialects share one schema, so this is a DDL spelling the parser reads unequally", rel, reference)
	}
}

// TestUnscopedQueryDetection pins the detector against two literal queries
// rather than against whatever the repo's SQL happens to say today, so a change
// that makes every real query compliant cannot quietly retire the rule.
func TestUnscopedQueryDetection(t *testing.T) {
	keyed := map[string]string{"workspace_tab_owned": "user_id"}

	assert.Equal(t, []string{"workspace_tab_owned"},
		unscopedOwnerKeyedTables("SELECT * FROM workspace_tab_owned WHERE workspace_id = ?", keyed),
		"the row identity is (user_id, tab_id); filtering on workspace_id alone matches every user's rows in that workspace")

	assert.Empty(t,
		unscopedOwnerKeyedTables("SELECT * FROM workspace_tab_owned WHERE user_id = ? AND tab_id = ?", keyed),
		"naming the owner column is the whole requirement")

	// No WHERE at all is the same defect with the predicate removed rather than
	// narrowed -- and it is the shape a whole-table list takes.
	assert.Equal(t, []string{"workspace_tab_owned"},
		unscopedOwnerKeyedTables("SELECT * FROM workspace_tab_owned ORDER BY tab_id", keyed),
		"an unfiltered read of an owner-keyed table returns every user's rows")

	// A table outside the population is not this rule's business, however it is
	// filtered: workspaces has a single-column primary key, so no other user
	// holds a row with the same identity.
	assert.Empty(t,
		unscopedOwnerKeyedTables("SELECT * FROM workspaces WHERE id = ?", keyed))

	// INSERT binds values, has no WHERE, and cannot select another user's row.
	assert.Empty(t,
		unscopedOwnerKeyedTables("INSERT INTO workspace_tab_owned (user_id, tab_id) VALUES (?, ?)", keyed))

	// A JOIN reaches the table just as surely as a FROM, and the owner column
	// may be qualified by the alias.
	assert.Equal(t, []string{"workspace_tab_owned"},
		unscopedOwnerKeyedTables("SELECT w.* FROM workspaces w JOIN workspace_tab_owned t ON t.workspace_id = w.id WHERE w.id = ?", keyed))
	assert.Empty(t,
		unscopedOwnerKeyedTables("SELECT w.* FROM workspaces w JOIN workspace_tab_owned t ON t.workspace_id = w.id WHERE t.user_id = ?", keyed))

	// The word boundary, in the direction that matters: owner_user_id is a
	// DIFFERENT column, so a workspace-owner predicate does not scope a
	// user_id-keyed table.
	assert.Equal(t, []string{"workspace_tab_owned"},
		unscopedOwnerKeyedTables("SELECT * FROM workspace_tab_owned t JOIN workspaces w ON w.id = t.workspace_id WHERE w.owner_user_id = ?", keyed),
		"owner_user_id is not user_id -- matching it by substring would bless the predicate that does not scope this table")
}

// TestCreateTableBodies_ReadsEveryDialectsTerminator pins the parser against the
// closing spellings this repo's DDL actually uses. The mysql one is the hazard:
// `) COLLATE=utf8mb4_bin;` is not `\n);`, and a pattern-terminated parser
// silently returns nothing for the whole dialect.
func TestCreateTableBodies_ReadsEveryDialectsTerminator(t *testing.T) {
	for name, schema := range map[string]string{
		"sqlite": "CREATE TABLE t (\n" +
			"    user_id TEXT NOT NULL,\n    tab_id TEXT NOT NULL,\n" +
			"    PRIMARY KEY (user_id, tab_id)\n);",
		"postgres with collate": "CREATE TABLE t (\n" +
			"    user_id TEXT COLLATE \"C\" NOT NULL,\n    tab_id TEXT NOT NULL,\n" +
			"    PRIMARY KEY (user_id, tab_id)\n);",
		"mysql with table options": "CREATE TABLE t (\n" +
			"    user_id VARCHAR(255) NOT NULL,\n    tab_id VARCHAR(255) NOT NULL,\n" +
			"    PRIMARY KEY (user_id, tab_id)\n) COLLATE=utf8mb4_bin;",
		"nested parens in body": "CREATE TABLE t (\n" +
			"    user_id TEXT NOT NULL,\n    tab_id TEXT NOT NULL,\n" +
			"    n INT NOT NULL CHECK (n > 0),\n    PRIMARY KEY (user_id, tab_id)\n);",
		"backquoted table name": "CREATE TABLE `t` (\n" +
			"    user_id VARCHAR(255) NOT NULL,\n    tab_id VARCHAR(255) NOT NULL,\n" +
			"    PRIMARY KEY (user_id, tab_id)\n) COLLATE=utf8mb4_bin;",
		"if not exists": "CREATE TABLE IF NOT EXISTS t (\n" +
			"    user_id TEXT NOT NULL,\n    tab_id TEXT NOT NULL,\n" +
			"    PRIMARY KEY (user_id, tab_id)\n);",
	} {
		t.Run(name, func(t *testing.T) {
			bodies, unterminated := createTableBodies(schema)
			assert.Empty(t, unterminated)
			require.Contains(t, bodies, "t")
			col, ok := compositeOwnerKeyColumn(bodies["t"])
			assert.True(t, ok, "body: %s", bodies["t"])
			assert.Equal(t, "user_id", col)
		})
	}

	// A key that is composite but names no owner, and an owner column that is
	// the WHOLE key, are both outside the population: neither lets one user's
	// predicate reach another user's row through the non-owner half.
	bodies, _ := createTableBodies("CREATE TABLE t (\n    agent_id TEXT NOT NULL,\n    request_id TEXT NOT NULL,\n    PRIMARY KEY (agent_id, request_id)\n);")
	_, ok := compositeOwnerKeyColumn(bodies["t"])
	assert.False(t, ok, "a composite key with no owner column is not owner-keyed")

	bodies, _ = createTableBodies("CREATE TABLE t (\n    user_id TEXT NOT NULL,\n    PRIMARY KEY (user_id)\n);")
	_, ok = compositeOwnerKeyColumn(bodies["t"])
	assert.False(t, ok, "a single-column owner key IS the row identity; there is no other half to filter by")

	bodies, _ = createTableBodies("CREATE TABLE t (\n    id TEXT PRIMARY KEY,\n    user_id TEXT NOT NULL\n);")
	_, ok = compositeOwnerKeyColumn(bodies["t"])
	assert.False(t, ok, "an inline single-column key is not composite")

	// A CREATE TABLE whose body never closes is a broken scan, not an empty
	// table -- ownerKeyedTables refuses rather than silently skipping it.
	_, unterminated := createTableBodies("CREATE TABLE t (\n    user_id TEXT NOT NULL,\n")
	assert.Equal(t, []string{"t"}, unterminated)
}

// ---- owner-keyed table scoping ----

// createTableRe matches the HEAD of a CREATE TABLE statement. The body is
// deliberately not part of the pattern: terminating it on `\n\)\s*;` -- the
// obvious spelling, and the one a first attempt used -- matched ZERO mysql
// tables, because mysql closes every table with `) COLLATE=utf8mb4_bin;`. The
// scan then reported a healthy table count from sqlite+postgres while covering
// neither mysql nor anything downstream of it, which is the identical silent
// failure the missing (?m) in userFKRe would have caused. createTableBodies
// balances parentheses instead, and TestOwnerKeyedTables_DerivedFromEveryDialectSchema
// asserts a non-zero count PER DIALECT rather than repo-wide so the same class
// cannot come back.
var createTableRe = regexp.MustCompile("(?i)CREATE\\s+TABLE\\s+(?:IF\\s+NOT\\s+EXISTS\\s+)?`?(\\w+)`?\\s*\\(")

// tablePrimaryKeyRe matches the TABLE-level `PRIMARY KEY (a, b, ...)`
// constraint. The inline column form (`id TEXT PRIMARY KEY`) has no parenthesis
// after the keyword and so never matches -- correctly, since a single-column key
// cannot be a composite one.
var tablePrimaryKeyRe = regexp.MustCompile(`(?i)PRIMARY\s+KEY\s*\(([^)]*)\)`)

// queryTargetTableRe matches the tables a query reads or writes. INSERT is
// absent on purpose: `INSERT INTO t` binds VALUES, has no WHERE, and so cannot
// select another user's row.
var queryTargetTableRe = regexp.MustCompile("(?i)\\b(?:FROM|JOIN|UPDATE)\\s+`?(\\w+)`?")

// sqlcParamNameRe matches a sqlc parameter wrapper so its ARGUMENT NAME can be
// blanked before the owner column is looked for.
//
// The name inside is a parameter, not a column: `w.owner_user_id =
// sqlc.arg(user_id)` scopes by the WORKSPACE's owner while naming `user_id`
// only as the Go field it generates, so reading the raw text would credit that
// query with a `user_id` predicate it does not have -- and the table whose key
// is (user_id, tab_id) would look scoped when nothing scoped it. Blanking the
// argument keeps the LEFT side (`user_id = sqlc.arg(user_id)`) matching, which
// is the spelling that really is a predicate on the column.
var sqlcParamNameRe = regexp.MustCompile(`(?i)(sqlc\.(?:arg|narg|slice))\s*\([^)]*\)`)

// ownerColumnNamedRe matches one owner column as a whole identifier.
//
// The word boundaries are load-bearing in one direction that matters:
// `\buser_id\b` does NOT match inside `owner_user_id`, because `_` is a word
// character, so a query naming a workspace's owner does not count as having
// scoped a user_id-keyed table. TestUnscopedQueryDetection pins it.
var ownerColumnNamedRe = func() map[string]*regexp.Regexp {
	m := make(map[string]*regexp.Regexp, len(ownerColumns))
	for _, col := range ownerColumns {
		m[col] = regexp.MustCompile(`(?i)\b` + col + `\b`)
	}
	return m
}()

// createTableBodies splits a schema into table name -> column/constraint body.
// unterminated names the tables whose opening parenthesis never balances, which
// is a malformed schema (or a broken scan) rather than a table with no columns.
func createTableBodies(schema string) (bodies map[string]string, unterminated []string) {
	src := stripSQLComments(schema)
	bodies = map[string]string{}
	for _, loc := range createTableRe.FindAllStringSubmatchIndex(src, -1) {
		name := strings.ToLower(src[loc[2]:loc[3]])
		depth, end := 1, -1
		for i := loc[1]; i < len(src) && end < 0; i++ {
			switch src[i] {
			case '(':
				depth++
			case ')':
				if depth--; depth == 0 {
					end = i
				}
			}
		}
		if end < 0 {
			unterminated = append(unterminated, name)
			continue
		}
		bodies[name] = src[loc[1]:end]
	}
	sort.Strings(unterminated)
	return bodies, unterminated
}

// compositeOwnerKeyColumn returns the owner column in a table's PRIMARY KEY,
// and ok=false unless that key is COMPOSITE and contains one.
//
// The composite restriction is what makes the rule a correctness claim rather
// than an authorization one, and what keeps its exemption table reviewable.
// "Every table with an owner column anywhere" measures at ~200 queries
// repo-wide; an exemption table that size is a rubber stamp. A composite key
// containing the owner says the row IDENTITY is per-user: two users may hold
// rows agreeing on every other key column, so a predicate on the non-owner half
// alone can match ANOTHER user's row. That is true regardless of who is allowed
// to see what, which is why the rule needs no judgement about admin paths.
func compositeOwnerKeyColumn(tableBody string) (col string, ok bool) {
	m := tablePrimaryKeyRe.FindStringSubmatch(tableBody)
	if m == nil {
		return "", false
	}
	var cols []string
	for part := range strings.SplitSeq(m[1], ",") {
		cols = append(cols, strings.ToLower(strings.Trim(strings.TrimSpace(part), "`\"")))
	}
	if len(cols) < 2 {
		return "", false
	}
	for _, c := range cols {
		for _, owner := range ownerColumns {
			if c == owner {
				return owner, true
			}
		}
	}
	return "", false
}

// ownerKeyedTables returns dir's tables whose PRIMARY KEY is composite and
// contains an owner column, mapped to that column.
func ownerKeyedTables(t *testing.T, dir sqlcDir) map[string]string {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join(dir.schemaDir, "*.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "no migrations under %s", dir.schemaDir)

	keyed := map[string]string{}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		require.NoError(t, err, "read %s", path)
		bodies, unterminated := createTableBodies(string(raw))
		require.Empty(t, unterminated,
			"%s: CREATE TABLE body never closes for %v; the schema scan is broken, not the code", path, unterminated)
		for table, body := range bodies {
			if col, ok := compositeOwnerKeyColumn(body); ok {
				keyed[table] = col
			}
		}
	}
	return keyed
}

// unscopedOwnerKeyedTables returns the owner-keyed tables body reads or writes
// WITHOUT naming their owner column in its WHERE clause, sorted.
//
// This is the universal half of the store-bind rule, and it exists because the
// other half is a conditional: "IF a query binds an owner column, THEN its
// caller must refuse an unminted id". A query that simply OMITS the owner
// column is outside that rule's domain, so it is never classified, so nothing
// reports it -- it passes green BY CONSTRUCTION. That blind spot is not
// hypothetical: it is how a cross-tenant read of workspace_tab_owned (composite
// key `(user_id, tab_id)`, filtered on workspace_id alone) shipped while both
// SQL-derived rules reported healthy.
func unscopedOwnerKeyedTables(body string, ownerKeyed map[string]string) []string {
	where, hasWhere := whereClause(body)
	where = sqlcParamNameRe.ReplaceAllString(where, "$1()")
	var out []string
	seen := map[string]bool{}
	for _, m := range queryTargetTableRe.FindAllStringSubmatch(body, -1) {
		table := strings.ToLower(m[1])
		col, keyed := ownerKeyed[table]
		if !keyed || seen[table] {
			continue
		}
		seen[table] = true
		if hasWhere && ownerColumnNamedRe[col].MatchString(where) {
			continue
		}
		out = append(out, table)
	}
	sort.Strings(out)
	return out
}

// checkOwnerScopedQueries is the universal half of the store-bind rule: a query
// against a table whose PRIMARY KEY is composite AND contains an owner column
// must name that column in its WHERE clause.
//
// See unscopedOwnerKeyedTables for why the conditional half cannot catch this,
// and compositeOwnerKeyColumn for why the population is narrowed to composite
// keys rather than to every table carrying an owner FK.
func checkOwnerScopedQueries(t *testing.T, root string) {
	t.Helper()

	dirs := sqlcDirs(t, root)
	keyedByDir := make(map[string]map[string]string, len(dirs))
	population := 0
	for _, dir := range dirs {
		keyed := ownerKeyedTables(t, dir)
		require.NotEmpty(t, keyed,
			"no owner-keyed table found in %s; the schema scan is broken, not the code", dir.schemaDir)
		keyedByDir[dir.rel] = keyed
		population += len(keyed)
	}
	require.NotZero(t, population)

	classified, flagged := 0, map[string]bool{}
	forEachSQLCQuery(t, dirs, func(dir sqlcDir, path, name, body string) {
		unscoped := unscopedOwnerKeyedTables(body, keyedByDir[dir.rel])
		if len(unscoped) == 0 {
			return
		}
		classified++
		flagged[name] = true
		if _, allowed := unscopedOwnerKeyedQueries[name]; allowed {
			return
		}
		for _, table := range unscoped {
			assert.Fail(t, "owner-keyed row reached by the non-owner half of its key",
				"%s/%s: %s targets %s without naming %q in its WHERE clause -- that table's PRIMARY KEY is composite AND contains %q, so two users may hold rows agreeing on every other key column and this predicate can match ANOTHER user's row. Bind the owner column, or register %q in unscopedOwnerKeyedQueries with the reason.",
				dir.rel, filepath.Base(path), name, table, keyedByDir[dir.rel][table], keyedByDir[dir.rel][table], name)
		}
	})
	// The rule's own silent failure mode. Every dialect has owner-keyed tables
	// and every dialect has at least one deliberately owner-blind query against
	// them (the worktree ref-count sweeps), so zero means the query scan stopped
	// reaching the SQL, not that the code got safer.
	assert.NotZero(t, classified,
		"no query against an owner-keyed table was classified; the scan is broken, not the code")

	for name, why := range unscopedOwnerKeyedQueries {
		assert.True(t, flagged[name],
			"unscopedOwnerKeyedQueries exempts %q, which no longer touches an owner-keyed table unscoped -- remove the stale exemption", name)
		assert.NotEmpty(t, why, "unscopedOwnerKeyedQueries exempts %q with no recorded reason", name)
	}
}

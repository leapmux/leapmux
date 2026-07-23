package audit

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
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
		`|` + sqlParamPattern + `\s*=\s*` + ownerColPattern)
)

// storeDialects are the store adapter packages that ship their own sqlc
// queries, discovered rather than listed.
//
// The three names were hardcoded twice in this file. That is the same shape as
// the reach net's old three-name accessor list: a fourth dialect directory
// would have been scanned by nothing -- neither its SQL read for ownership
// predicates nor its adapters checked for the guard -- while the rule reported
// green over the other three. Deriving the set means a new dialect is covered
// the moment its queries exist, and a rename fails loudly instead of silently
// shrinking the scan.
func storeDialects(t *testing.T, root string) []string {
	t.Helper()

	storeDir := filepath.Join(root, "internal", "hub", "store")
	entries, err := os.ReadDir(storeDir)
	require.NoError(t, err, "read %s", storeDir)

	var dialects []string
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(storeDir, ent.Name(), "db", "queries")); err == nil {
			dialects = append(dialects, ent.Name())
		}
	}
	require.NotEmpty(t, dialects, "no store dialect with db/queries found under %s; the scan is broken, not the code", storeDir)
	return dialects
}

// checkOwnerFilterCoverage is the store-bind rule: every generated query whose
// WHERE clause filters on an owner column must be run by an adapter method that
// first routes the caller id through store.OwnerFilter.
//
// It is derived from the SQL, not from the Go, and that is what makes it
// precise. An INSERT has no WHERE, so the ~37 raw `.String()` unwraps that
// remain -- all column VALUES rather than predicates -- are out of scope
// automatically, with no allowlist needed to say so.
func checkOwnerFilterCoverage(t *testing.T, root string) {
	t.Helper()

	dialects := storeDialects(t, root)
	ownershipQueries := ownershipQueryNames(t, root, dialects)
	require.NotEmpty(t, ownershipQueries,
		"no ownership queries found; the SQL scan is broken, not the code")

	checked := 0
	for _, dialect := range dialects {
		dir := filepath.Join(root, "internal", "hub", "store", dialect)
		testutil.ForEachPackageSourceFile(t, dir, func(fset *token.FileSet, file *ast.File) {
			enclosing := testutil.NewEnclosingFuncFinder(file)
			// Which functions route the caller id through a shared guard --
			// store.OwnerFilter, or store.GetOwnedWorker, whose whole body is
			// the same refusal followed by the ownership comparison.
			//
			// Calling OwnerFilter is not enough: the refusal has to be acted on.
			// `owner, _ := store.OwnerFilter(p.UserID)` followed by binding
			// owner (== "") reintroduces the exact blank-owner fail-open this
			// rule exists to stop, and a presence-only check passes it.
			guarded := map[string]bool{}
			type unhonouredCall struct{ where, why string }
			var unhonoured []unhonouredCall
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isSharedOwnerGuardCall(call) {
					return true
				}
				fn, in := enclosing.Find(call.Pos())
				if !in {
					return true
				}
				name := testutil.QualifiedFuncName(fn)
				if !isOwnerFilterCall(call) {
					guarded[name] = true // GetOwnedWorker refuses internally
					return true
				}
				if why, honoured := ownerFilterRefusalHonoured(fn, call); honoured {
					guarded[name] = true
				} else {
					pos := fset.Position(call.Pos())
					unhonoured = append(unhonoured, unhonouredCall{
						where: fmt.Sprintf("%s/%s:%d", dialect, filepath.Base(pos.Filename), pos.Line),
						why:   fmt.Sprintf("%s() %s", name, why),
					})
				}
				return true
			})
			for _, u := range unhonoured {
				assert.Fail(t, "store.OwnerFilter result is not acted on",
					"%s: %s -- a zero UserID unwraps to \"\", which MATCHES every blank-owner row instead of none, so the refusal must gate an early return",
					u.where, u.why)
			}

			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !ownershipQueries[sel.Sel.Name] {
					return true
				}
				if why, allowed := unguardedOwnerFilterQueries[sel.Sel.Name]; allowed {
					_ = why
					return true
				}
				checked++
				fn, in := enclosing.Find(sel.Pos())
				if !in {
					assert.Fail(t, "ownership query outside any function",
						"%s/%s:%d: %s runs at package level and can never be guarded",
						dialect, filepath.Base(fset.Position(sel.Pos()).Filename),
						fset.Position(sel.Pos()).Line, sel.Sel.Name)
					return true
				}
				name := testutil.QualifiedFuncName(fn)
				assert.True(t, guarded[name],
					"%s/%s:%d: %s() runs %s, whose WHERE filters an owner column, without routing the caller id through store.OwnerFilter -- a zero UserID unwraps to \"\", which MATCHES every blank-owner row instead of none. Add the OwnerFilter guard, or register %q in unguardedOwnerFilterQueries with the reason.",
					dialect, filepath.Base(fset.Position(sel.Pos()).Filename),
					fset.Position(sel.Pos()).Line, name, sel.Sel.Name, sel.Sel.Name)
				return true
			})
		})
	}
	assert.NotZero(t, checked, "no ownership query call sites found; the adapter scan is broken, not the code")

	for q := range unguardedOwnerFilterQueries {
		assert.True(t, ownershipQueries[q],
			"unguardedOwnerFilterQueries exempts %q, which no longer filters on an owner column -- remove the stale exemption", q)
	}
}

// ownershipQueryNames returns the sqlc query names whose body contains a
// WHERE-clause predicate binding an owner column to a parameter.
func ownershipQueryNames(t *testing.T, root string, dialects []string) map[string]bool {
	t.Helper()

	names := map[string]bool{}
	for _, dialect := range dialects {
		glob := filepath.Join(root, "internal", "hub", "store", dialect, "db", "queries", "*.sql")
		paths, err := filepath.Glob(glob)
		require.NoError(t, err)
		for _, path := range paths {
			raw, err := os.ReadFile(path)
			require.NoError(t, err, "read %s", path)
			for name, body := range sqlcQueryBodies(string(raw)) {
				body = stripSQLComments(body)
				// Only a WHERE-clause predicate counts. An INSERT's column
				// list and an `UPDATE ... SET user_id = ?` both mention the
				// same identifier while binding a VALUE, and neither decides
				// ownership -- so the match is scoped to the text after WHERE.
				if where, ok := whereClause(body); ok && ownerBindRe.MatchString(where) {
					names[name] = true
				}
			}
		}
	}
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

func isSharedOwnerGuardCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "store" {
		return false
	}
	// OwnerFilter is the bind-time refusal; GetOwnedWorker is the shared
	// helper that performs the identical refusal plus the comparison, so an
	// adapter delegating to it is guarded just as thoroughly.
	return sel.Sel.Name == "OwnerFilter" || sel.Sel.Name == "GetOwnedWorker"
}

func isOwnerFilterCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "OwnerFilter"
}

// ownerFilterRefusalHonoured reports whether fn actually acts on the second
// result of an OwnerFilter call: it must bind it to a named variable and use
// that variable to gate an early return.
//
// why explains the failure for the assertion message. All 105 call sites in the
// repo are the same two-line shape (`owner, ok := store.OwnerFilter(...)`
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
	if !hasGuardedReturn(fn.Body, okIdent.Name) {
		return fmt.Sprintf("never returns early on !%s", okIdent.Name), false
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

// hasGuardedReturn reports whether body contains `if !name { ... return ... }`.
func hasGuardedReturn(body *ast.BlockStmt, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		unary, ok := ifStmt.Cond.(*ast.UnaryExpr)
		if !ok || unary.Op != token.NOT {
			return true
		}
		if ident, ok := unary.X.(*ast.Ident); !ok || ident.Name != name {
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
	for _, dialect := range storeDialects(t, root) {
		paths, err := filepath.Glob(filepath.Join(root, "internal", "hub", "store", dialect, "db", "migrations", "*.sql"))
		require.NoError(t, err)
		require.NotEmpty(t, paths, "no migrations found for dialect %q", dialect)
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
					dialect, filepath.Base(path), col)
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

// TestStoreDialects_DiscoversQueryOwningPackages pins the derivation that
// replaced a hardcoded three-name list. The three integration-only packages
// (cockroachdb / tidb / yugabytedb) reuse another dialect's adapter and ship no
// queries of their own, so including them would make the adapter scan look for
// Go files that do not exist.
func TestStoreDialects_DiscoversQueryOwningPackages(t *testing.T) {
	got := storeDialects(t, repoRoot(t))
	assert.ElementsMatch(t, []string{"sqlite", "postgres", "mysql"}, got)
}

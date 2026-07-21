package store

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

// Import paths of the four sqlc-generated packages (the three hub dialects
// plus the worker) whose param/result structs the guards below inspect.
const (
	sqliteGenPkg   = "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	mysqlGenPkg    = "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	postgresGenPkg = "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	workerGenPkg   = "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// TestGeneratedInterfaceParamsAreAllowlisted type-checks every sqlc-generated
// package (the three hub dialects plus the worker) via go/packages and fails
// if a generated struct carries an interface{} field that is not on the
// explicit allowlist below.
//
// Why: the sqltime/pgtime db_type overrides retype every DATETIME/timestamptz
// param and result column mechanically, so a raw time.Time bind is a compile
// error -- EXCEPT where sqlc emits interface{} (narg-in-OR-chain keyset
// params, decltype-losing expressions). Those fields fall outside the
// compile-time guarantee and are safe only because their fill sites bind
// valuer types by convention. This scan pins the set of such holes: a new
// query whose param or result lands as interface{} (a new cursor OR-chain, an
// aggregate or CASE expression that lost its decltype) fails here and forces
// a conscious decision -- type it (db_type/column override, CAST, or a typed
// builder like the dialects' withCursor closures) or extend the allowlist
// with a comment saying why it cannot be typed.
func TestGeneratedInterfaceParamsAreAllowlisted(t *testing.T) {
	// Field names that are interface{} today, per generated package, each with
	// a reason it cannot be typed by the overrides:
	allowlist := map[string]map[string]string{
		sqliteGenPkg: {
			"CursorTime": "keyset narg IS NULL OR-chain; fed sqltime.SQLiteNullTime by the typed withCursor closure",
			"Now":        "registration-key expiry narg OR-chain; fed sqltime.SQLiteNullTime by the typed builder",
			"Query":      "search LIKE narg OR-chain; fed a *string-derived pattern",
			"ClientType": "narg OR-chain over a text column",
			"TabType":    "narg OR-chain over a text column",
		},
		mysqlGenPkg: {
			"LeaseMillis": "arithmetic expression param; not a timestamp",
		},
		postgresGenPkg: {},
		workerGenPkg:   {},
	}

	patterns := make([]string, 0, len(allowlist))
	for pkgPath := range allowlist {
		patterns = append(patterns, pkgPath)
	}
	sort.Strings(patterns)

	// Types only: the scan reads package-scope struct fields, not syntax.
	pkgs := loadPackages(t, &packages.Config{Mode: packages.NeedName | packages.NeedTypes}, patterns...)
	require.Len(t, pkgs, len(allowlist),
		"expected one loaded package per generated import path %v -- run `task generate-sqlc` if a generated dir is missing", patterns)

	for _, pkg := range pkgs {
		allowed, ok := allowlist[pkg.PkgPath]
		require.True(t, ok, "packages.Load returned unexpected package %s", pkg.PkgPath)
		seen := map[string]bool{}
		scope := pkg.Types.Scope()
		for _, typeName := range scope.Names() {
			tn, ok := scope.Lookup(typeName).(*types.TypeName)
			if !ok {
				continue
			}
			st, ok := types.Unalias(tn.Type()).Underlying().(*types.Struct)
			if !ok {
				continue
			}
			for i := 0; i < st.NumFields(); i++ {
				field := st.Field(i)
				if !isEmptyInterface(field.Type()) {
					continue
				}
				seen[field.Name()] = true
				if _, ok := allowed[field.Name()]; !ok {
					assert.Fail(t, "untyped generated field",
						"%s: field %s.%s is interface{} -- it escapes the sqltime/pgtime compile-time guarantee; type it (db_type/column override, CAST, typed builder) or allowlist it with a reason (see %s)",
						pkg.PkgPath, typeName, field.Name(), pkg.Fset.Position(field.Pos()))
				}
			}
		}
		// Keep the allowlist honest: a fixed hole that got typed should be
		// removed here so the list never overstates the exceptions.
		for name := range allowed {
			assert.True(t, seen[name],
				"%s: allowlisted field %s no longer exists as interface{} -- remove the stale entry", pkg.PkgPath, name)
		}
	}
}

// TestNoRawTimeBindsIntoInterfaceParams type-checks all non-generated store
// and worker code and fails on any raw time.Time, *time.Time, or
// database/sql.NullTime value (matched by static type) headed into an untyped
// bind: a struct composite literal filling an empty-interface field, an
// assignment whose target is an empty interface (a generated interface{}
// param field, a map[...]any entry), a `var x any = ...` declaration, an
// append into a []any slice, or any argument of an
// Exec/Query/QueryRow(Context) call (the raw-SQL write paths, e.g.
// sqlutil.BulkUpsertTabs, that bypass sqlc-generated params entirely).
//
// Why: the interface{} params pinned by
// TestGeneratedInterfaceParamsAreAllowlisted (the keyset CursorTime, the
// registration-key Now) and the hand-assembled ExecContext arg slices sit
// outside the sqltime/pgtime compile-time guarantee: `CursorTime: ct.Time`
// or `args = append(args, r.SomeAt)` compiles fine but hands the driver a raw
// time.Time, which binds the driver's default layout instead of the canonical
// storage form and silently corrupts the raw-string keyset compares.
// sql.NullTime is equally wrong -- its Value() emits the inner raw time.Time.
// Only driver-valuer types (sqltime.SQLiteNullTime, pgtime.*, etc.; see
// internal/util/sqltime) may cross these binds.
//
// Coverage is stated precisely: the scan matches by STATIC type at the sites
// go/types can see. A raw time already laundered through an intermediate
// empty interface (a helper returning any, a multi-value assignment from a
// function call) is invisible to any static scan -- at that point the
// expression's type is interface{}, indistinguishable from a legitimate
// value. The allowlist test above pins WHICH generated fields are untyped;
// this scan closes every syntactic path a statically-typed raw time can take
// into an untyped bind.
//
// Test files are scanned too (Tests: true): a fixture binding a raw time.Time
// into an interface{} param would mask the same corruption in whatever the
// test asserts. Keyed and positional literals are both covered; the scan is
// deliberately not limited to generated param structs, since binding a raw
// time into ANY empty-interface sink in these trees is the same layout
// hazard.
func TestNoRawTimeBindsIntoInterfaceParams(t *testing.T) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedSyntax |
			packages.NeedTypesInfo | packages.NeedDeps,
		Tests: true,
	}
	pkgs := loadPackages(t, cfg,
		"github.com/leapmux/leapmux/internal/hub/store/...",
		"github.com/leapmux/leapmux/internal/worker/...",
	)

	// With Tests: true a source file is type-checked twice when its package
	// has internal tests (once as the plain package, once as the test
	// variant), so violations are keyed by position to dedupe the reports.
	violations := map[string]string{}
	for _, pkg := range pkgs {
		// Generated packages only DECLARE the interface{} fields (pinned by the
		// allowlist test above); the fill sites live in hand-written code. The
		// synthesized ".test" main packages contain no hand-written binds.
		if strings.Contains(pkg.PkgPath, "/generated/") || strings.HasSuffix(pkg.PkgPath, ".test") {
			continue
		}
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.CompositeLit:
					st := structUnder(pkg.TypesInfo.TypeOf(node))
					if st == nil {
						return true
					}
					for i, elt := range node.Elts {
						field, value := literalField(pkg.TypesInfo, st, i, elt)
						if field == nil || !isEmptyInterface(field.Type()) {
							continue
						}
						flagRawTime(pkg, violations, value, "interface{} field "+field.Name())
					}
				case *ast.AssignStmt:
					// Pairwise x = y only. A multi-value assignment from a
					// function call carries the callee's typed results; a raw
					// time can only reach an interface{} target there via a
					// helper that already returns any, which no static scan
					// can see (documented limitation above).
					if len(node.Lhs) != len(node.Rhs) {
						return true
					}
					for i, lhs := range node.Lhs {
						if t := pkg.TypesInfo.TypeOf(lhs); t == nil || !isEmptyInterface(t) {
							continue
						}
						flagRawTime(pkg, violations, node.Rhs[i], "interface{} assignment target")
					}
				case *ast.ValueSpec:
					// var x any = rawTime -- the laundering declaration itself.
					// An untyped `var x = rawTime` keeps the value's own type
					// and needs no check here.
					if node.Type == nil {
						return true
					}
					if t := pkg.TypesInfo.TypeOf(node.Type); t == nil || !isEmptyInterface(t) {
						return true
					}
					for _, v := range node.Values {
						flagRawTime(pkg, violations, v, "interface{} variable declaration")
					}
				case *ast.CallExpr:
					switch fun := node.Fun.(type) {
					case *ast.Ident:
						// Builtin append into a []any (the hand-assembled
						// ExecContext arg slices). append(a, b...) is safe:
						// the spread arg is a slice, never a raw time.
						if obj, ok := pkg.TypesInfo.Uses[fun].(*types.Builtin); !ok || obj.Name() != "append" || len(node.Args) < 2 {
							return true
						}
						t := pkg.TypesInfo.TypeOf(node.Args[0])
						if t == nil {
							return true
						}
						sl, ok := types.Unalias(t).Underlying().(*types.Slice)
						if !ok || !isEmptyInterface(sl.Elem()) {
							return true
						}
						for _, arg := range node.Args[1:] {
							flagRawTime(pkg, violations, arg, "append into []any")
						}
					case *ast.SelectorExpr:
						// Variadic driver binds passed directly to a query
						// method. Matching by method name is deliberately
						// broad: a raw time argument to ANY method with these
						// names in the store/worker trees is the same layout
						// hazard, and false positives are impossible for
						// legitimate code (a raw time has no valid use there).
						if !rawBindMethods[fun.Sel.Name] {
							return true
						}
						for _, arg := range node.Args {
							flagRawTime(pkg, violations, arg, fun.Sel.Name+" argument")
						}
					}
				}
				return true
			})
		}
	}

	keys := make([]string, 0, len(violations))
	for key := range violations {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		assert.Fail(t, "raw time bind into interface{} param", "%s", violations[key])
	}
}

// rawBindMethods are the database/sql-shaped query methods whose variadic
// args reach a driver bind directly, bypassing sqlc-generated params (the
// sqlutil.BulkUpsertTabs / sessions Touch raw-SQL paths).
var rawBindMethods = map[string]bool{
	"Exec": true, "ExecContext": true,
	"Query": true, "QueryContext": true,
	"QueryRow": true, "QueryRowContext": true,
}

// flagRawTime records a violation when the expression's static type is one of
// the raw-time shapes (see rawTimeTypeName); sink names where the value was
// headed, and the position key dedupes the Tests:true double type-check.
func flagRawTime(pkg *packages.Package, violations map[string]string, value ast.Expr, sink string) {
	badType, ok := rawTimeTypeName(pkg.TypesInfo.TypeOf(value))
	if !ok {
		return
	}
	pos := pkg.Fset.Position(value.Pos())
	violations[fmt.Sprintf("%s:%d:%d %s", pos.Filename, pos.Line, pos.Column, sink)] = fmt.Sprintf(
		"%s: %s bound into %s -- this sink requires a driver-valuer type (sqltime.SQLiteNullTime etc.; see internal/util/sqltime), never a raw time value: a raw bind uses the driver's default layout and silently breaks the canonical-storage keyset compares",
		pos, badType, sink)
}

// loadPackages runs packages.Load and fails the test loudly on any load,
// parse, or type error anywhere in the returned graph: a broken load must
// never let a guard pass vacuously.
func loadPackages(t *testing.T, cfg *packages.Config, patterns ...string) []*packages.Package {
	t.Helper()
	pkgs, err := packages.Load(cfg, patterns...)
	require.NoError(t, err, "packages.Load %v", patterns)
	var loadErrs []string
	packages.Visit(pkgs, nil, func(pkg *packages.Package) {
		for _, e := range pkg.Errors {
			loadErrs = append(loadErrs, fmt.Sprintf("%s: %v", pkg.PkgPath, e))
		}
	})
	require.Empty(t, loadErrs,
		"packages.Load %v reported errors (if a generated package is missing, run `task generate-sqlc` first)", patterns)
	return pkgs
}

// structUnder resolves a composite literal's type down to its struct shape,
// unwrapping aliases, one level of pointer (elided &T-element literals), and
// named types. Returns nil when the literal is not a struct (slice, map,
// array literals).
func structUnder(t types.Type) *types.Struct {
	if t == nil {
		return nil
	}
	t = types.Unalias(t)
	if ptr, ok := t.(*types.Pointer); ok {
		t = types.Unalias(ptr.Elem())
	}
	st, ok := t.Underlying().(*types.Struct)
	if !ok {
		return nil
	}
	return st
}

// literalField resolves which struct field the i-th composite-literal element
// fills -- keyed literals via the type-checker's field object for the key,
// positional literals via field order -- and returns it with the value
// expression being bound. Returns nils for elements that resolve to no field
// (which the type checker rejects anyway).
func literalField(info *types.Info, st *types.Struct, i int, elt ast.Expr) (*types.Var, ast.Expr) {
	if kv, ok := elt.(*ast.KeyValueExpr); ok {
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			return nil, nil
		}
		if field, ok := info.Uses[key].(*types.Var); ok {
			return field, kv.Value
		}
		// Fallback if the type checker recorded no object for the key:
		// match the field by name.
		for j := 0; j < st.NumFields(); j++ {
			if st.Field(j).Name() == key.Name {
				return st.Field(j), kv.Value
			}
		}
		return nil, nil
	}
	if i < st.NumFields() {
		return st.Field(i), elt
	}
	return nil, nil
}

// rawTimeTypeName reports whether t is one of the raw-time shapes that must
// never cross an interface{} bind -- time.Time, *time.Time, or
// database/sql.NullTime (pointer or value; its Value() emits the inner raw
// time.Time). Matching is by defining package path + type name, never by
// string suffix, so a local type that happens to be named Time cannot be
// confused with time.Time.
func rawTimeTypeName(t types.Type) (string, bool) {
	if t == nil {
		return "", false
	}
	t = types.Unalias(t)
	prefix := ""
	if ptr, ok := t.(*types.Pointer); ok {
		t = types.Unalias(ptr.Elem())
		prefix = "*"
	}
	named, ok := t.(*types.Named)
	if !ok {
		return "", false
	}
	obj := named.Obj()
	if obj.Pkg() == nil {
		return "", false
	}
	switch path, name := obj.Pkg().Path(), obj.Name(); {
	case path == "time" && name == "Time":
		return prefix + "time.Time", true
	case path == "database/sql" && name == "NullTime":
		return prefix + "sql.NullTime", true
	}
	return "", false
}

// isEmptyInterface reports whether t is the empty interface (interface{} or
// its alias any, or a defined type whose underlying is the empty interface)
// -- the shape sqlc emits when it cannot resolve a param or column type.
func isEmptyInterface(t types.Type) bool {
	iface, ok := types.Unalias(t).Underlying().(*types.Interface)
	return ok && iface.Empty()
}

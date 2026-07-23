package testutil

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ForEachPackageSourceFile parses every non-test .go file directly in dir and
// invokes fn once per file with a shared FileSet (so positions from different
// files are comparable and printable).
//
// It backs the "completeness" tests that turn an omission into a failure --
// hub/auth's zero-UserID deny table and hub/service's worker-reach
// classification table -- both of which walk their own package's source to find
// declarations or call sites an author forgot to register. The scan-and-parse
// prologue was identical in both; keeping one copy means a third such test
// inherits the same file filter instead of hand-rolling it (and quietly
// disagreeing about, say, whether to descend into subdirectories).
//
// Only files at the top level of dir are parsed: subdirectories are separate
// packages, and a per-package table cannot classify them.
func ForEachPackageSourceFile(t *testing.T, dir string, fn func(fset *token.FileSet, file *ast.File)) {
	t.Helper()

	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err, "read package dir %s", dir)

	parsed := 0
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		require.NoError(t, err, "parse %s", name)
		fn(fset, file)
		parsed++
	}
	// A silently empty scan is the one way a completeness test can pass while
	// checking nothing at all -- e.g. after a move that leaves the test behind
	// in a directory with no source.
	require.NotZero(t, parsed, "no non-test Go source found in %s; the scan would vacuously pass", dir)
}

// EnclosingFuncFinder maps a position back to the function declaration that
// contains it, for tests that classify call sites by their enclosing function.
//
// It exists so a call that sits in NO function declaration -- a package-level
// `var h = func() { ... }` -- is reported rather than walked past. A scan that
// only visits *ast.FuncDecl nodes misses that shape entirely, which is a hole
// in exactly the tests whose job is to have no holes.
type EnclosingFuncFinder struct {
	decls []*ast.FuncDecl
}

// NewEnclosingFuncFinder indexes the function declarations in file.
func NewEnclosingFuncFinder(file *ast.File) *EnclosingFuncFinder {
	f := &EnclosingFuncFinder{}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
			f.decls = append(f.decls, fn)
		}
	}
	return f
}

// Find returns the declaration whose body contains pos. ok is false when pos
// lies outside every function body (a package-level declaration).
func (f *EnclosingFuncFinder) Find(pos token.Pos) (*ast.FuncDecl, bool) {
	for _, fn := range f.decls {
		if fn.Body.Pos() <= pos && pos <= fn.Body.End() {
			return fn, true
		}
	}
	return nil, false
}

// QualifiedFuncName renders a declaration as "(*Recv).Name" for a method and
// "Name" for a plain function.
//
// The receiver is part of the key on purpose: bare names collide, and a second
// type defining a same-named method would otherwise inherit the first one's
// classification and be born blessed -- the exact hole these tables exist to
// prevent. Generic receivers (`func (p *Pool[T]) reach()`) are rendered by
// their base type name for the same reason: falling back to the bare function
// name would reintroduce the collision.
func QualifiedFuncName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	recv := receiverName(fn.Recv.List[0].Type)
	if recv == "" {
		return fn.Name.Name
	}
	return "(" + recv + ")." + fn.Name.Name
}

// receiverName renders a receiver type expression, unwrapping the pointer and
// any generic type parameters.
func receiverName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		if inner := receiverName(t.X); inner != "" {
			return "*" + inner
		}
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr: // Recv[T]
		return receiverName(t.X)
	case *ast.IndexListExpr: // Recv[T, U]
		return receiverName(t.X)
	}
	return ""
}

// walkRepoGoFiles parses every .go file under root that accept admits, and
// invokes visit with the file's repo-relative slash-separated path.
//
// It is the single definition of "which trees a repo-wide invariant looks at".
// Generated, vendored, and hidden trees are skipped -- none is hand-written, so
// an invariant about what an author may write does not apply to them -- and so
// is testdata, whose files are fixtures that may be deliberately malformed and
// would otherwise fail the parse.
//
// The skip list lives here rather than at each caller because it used to be
// copied: the source walk skipped testdata and the test-name walk did not, so a
// TestXxx under testdata would have satisfied a coverage assertion that no real
// test backed. One copy cannot disagree with itself.
func walkRepoGoFiles(
	t *testing.T,
	root string,
	accept func(rel, name string) bool,
	visit func(fset *token.FileSet, path, rel string, file *ast.File),
) {
	t.Helper()

	fset := token.NewFileSet()
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch name := d.Name(); {
			case name == "generated", name == "vendor", name == "node_modules", name == "testdata":
				return fs.SkipDir
			case name != "." && strings.HasPrefix(name, "."):
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		require.NoError(t, relErr)
		rel = filepath.ToSlash(rel)
		if !accept(rel, name) {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		require.NoError(t, parseErr, "parse %s", rel)
		visit(fset, path, rel, file)
		return nil
	}))
}

// ForEachRepoSourceFile parses every non-test, non-generated .go file under
// root and invokes fn with the file's repo-relative path and a shared FileSet.
// It returns the number of files scanned, which callers should assert is
// plausible: a walk that silently stops finding source is the one way a
// repo-wide invariant test can rot while staying green.
func ForEachRepoSourceFile(t *testing.T, root string, fn func(fset *token.FileSet, relPath string, file *ast.File)) int {
	t.Helper()

	scanned := 0
	walkRepoGoFiles(t, root,
		func(rel, name string) bool {
			if strings.HasSuffix(name, "_test.go") {
				return false
			}
			// storetest is a test-support package: its seeds are known-good
			// fixtures, which several of these invariants deliberately exempt.
			return !strings.Contains(rel, "/storetest/")
		},
		func(fset *token.FileSet, _, rel string, file *ast.File) {
			scanned++
			fn(fset, rel, file)
		})
	return scanned
}

// RepoTestFuncNames collects every TestXxx function declared anywhere under
// root, so an invariant table can point at the coverage that proves it and fail
// when that coverage is deleted.
func RepoTestFuncNames(t *testing.T, root string) map[string]bool {
	t.Helper()

	names := map[string]bool{}
	walkRepoGoFiles(t, root,
		func(_, name string) bool { return strings.HasSuffix(name, "_test.go") },
		func(_ *token.FileSet, _, _ string, file *ast.File) {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
					names[fn.Name.Name] = true
				}
			}
		})
	return names
}

// ImportedAs returns the identifier under which file imports importPath, so a
// rule that recognises a call by its package qualifier can key on the PATH
// rather than on the spelling at the call site.
//
// This matters because every such rule is a security net: a file that writes
// `uid "…/internal/util/userid"` and then `uid.MustNew(row.UserID)` is invisible
// to a check hardcoding the identifier `userid`, and the net reports green while
// the exact class it exists to catch ships.
//
// ok is false when the file does not import the path at all, and also for a
// blank (`_`) or dot (`.`) import: neither binds an identifier that a selector
// expression could name, so no call site can be attributed through one.
//
// The unaliased case assumes the conventional "package name == last path
// segment". Every package these rules target satisfies it, and each rule
// additionally asserts that its scan matched something, so a package that broke
// the convention would surface as a failing net rather than a silent one.
func ImportedAs(file *ast.File, importPath string) (string, bool) {
	quoted := strconv.Quote(importPath)
	for _, imp := range file.Imports {
		if imp.Path.Value != quoted {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name == "_" || imp.Name.Name == "." {
				return "", false
			}
			return imp.Name.Name, true
		}
		return path.Base(importPath), true
	}
	return "", false
}

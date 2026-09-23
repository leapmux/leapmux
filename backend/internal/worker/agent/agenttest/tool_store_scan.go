package agenttest

import (
	"go/ast"
	"go/token"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
)

// enclosingFuncName reports the function that a position sits in, and "" for a
// position outside every one.
//
// It spans the whole declaration rather than the body alone, because a helper states
// its store in the SIGNATURE (`func newCursorToolStoreForTest(t *testing.T)
// *cursorToolStore`). testutil.EnclosingFuncFinder maps a CALL SITE back to its
// function and tests the body for that reason, so it reports the return type of the
// one helper this rule exists to bless as belonging to no function at all.
//
// The empty answer is the one that matters most: a package-level
// `var store = cursorToolStore{}` holds its handle for the whole test binary, and no
// cleanup in any test can reach it.
func enclosingFuncName(file *ast.File, pos token.Pos) string {
	for _, decl := range file.Decls {
		fn, isFunc := decl.(*ast.FuncDecl)
		if isFunc && fn.Pos() <= pos && pos <= fn.End() {
			return testutil.QualifiedFuncName(fn)
		}
	}
	return ""
}

// callsAny reports whether body calls one of the functions in names. A name is a
// plain function of the package, or "alias.Name" for a function of the package
// that the file imports as alias.
func callsAny(body *ast.BlockStmt, names []string) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		call, isCall := node.(*ast.CallExpr)
		if !isCall {
			return true
		}
		spelled := ""
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			spelled = fun.Name
		case *ast.SelectorExpr:
			// "alias.Name" matches only a name that the caller qualified with the
			// file's import alias. A selector on anything but an identifier cannot
			// be a package function.
			if x, isIdent := fun.X.(*ast.Ident); isIdent {
				spelled = x.Name + "." + fun.Sel.Name
			}
		}
		for _, name := range names {
			if spelled != "" && spelled == name {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// ToolStoreRule states what RequireToolStoreHandlesClosed checks in one provider
// package.
type ToolStoreRule struct {
	// Stores maps each provider tool store type to the ONE test helper that may
	// mention it. That helper registers the cleanup which closes the handle.
	Stores map[string]string
	// Constructors are the transcript constructors that open a provider store of
	// their own. A test that calls one must release that handle.
	Constructors []string
	// Release is the function that closes a transcript's handle when a test ends.
	// ReleasePackage is its import path, or "" for a function of the package under
	// scan.
	Release, ReleasePackage string
}

// RequireToolStoreHandlesClosed fails when a test in dir opens a provider
// database handle that nothing closes before the test ends.
//
// Three Cursor tests did this. Each built a store with `var store cursorToolStore`,
// read a fixture database out of t.TempDir, and left the handle open. Unix unlinks an
// open file without complaint, so the suite was green on macOS and Linux for as long
// as those tests existed. Windows refuses to remove a file that any handle holds open
// -- SQLite asks for FILE_SHARE_READ|FILE_SHARE_WRITE and never FILE_SHARE_DELETE --
// so t.TempDir's own RemoveAll failed the test there, and reported a file that the test
// no longer used.
//
// A leak of this shape fails on ONE platform and passes everywhere the author looks,
// which is why it is worth a scan rather than a review habit. Two rules cover the two
// ways to open a handle:
//
//   - Mention a store type only inside its `*ForTest` helper, which registers the
//     close. The rule covers the TYPE rather than one construction, because
//     `var store cursorToolStore` builds one with no composite literal at all --
//     and that is the spelling the three tests used.
//   - Call a transcript constructor only beside the Release function, which
//     closes the handle the constructor opened. The constructor releases its own
//     handle from context.AfterFunc, on a goroutine that races the RemoveAll.
func RequireToolStoreHandlesClosed(t *testing.T, dir string, rule ToolStoreRule) {
	t.Helper()

	stores, transcripts := 0, 0
	testutil.ForEachPackageTestFile(t, dir, func(fset *token.FileSet, file *ast.File) {
		ast.Inspect(file, func(node ast.Node) bool {
			ident, isIdent := node.(*ast.Ident)
			if !isIdent {
				return true
			}
			helper, isStore := rule.Stores[ident.Name]
			if !isStore {
				return true
			}
			stores++
			assert.Equal(t, helper, enclosingFuncName(file, ident.Pos()),
				"%s: a test mentions %s outside %s, so nothing closes the database handle it opens; "+
					"Windows then fails the RemoveAll of t.TempDir",
				fset.Position(ident.Pos()), ident.Name, helper)
			return true
		})

		release := rule.Release
		if rule.ReleasePackage != "" {
			alias, imported := testutil.ImportedAs(file, rule.ReleasePackage)
			if !imported {
				release = ""
			} else {
				release = alias + "." + rule.Release
			}
		}
		for _, decl := range file.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			if !isFunc || fn.Body == nil || !callsAny(fn.Body, rule.Constructors) {
				continue
			}
			transcripts++
			assert.True(t, release != "" && callsAny(fn.Body, []string{release}),
				"%s: %s opens a provider store through a transcript constructor and never calls "+
					"%s, so the handle is closed from context.AfterFunc -- "+
					"on a goroutine that races the RemoveAll of t.TempDir",
				fset.Position(fn.Pos()), fn.Name.Name, rule.Release)
		}
	})

	// Both halves must match something. A rename that moved every call site past this
	// scan would otherwise leave it green while it checks nothing.
	assert.NotZero(t, stores, "no provider tool store type found; the store half of this scan is vacuous")
	assert.NotZero(t, transcripts, "no transcript constructor call found; the transcript half of this scan is vacuous")
}

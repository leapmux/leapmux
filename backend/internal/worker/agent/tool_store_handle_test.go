package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// toolStoreConstructors maps each provider tool store to the ONE test helper that may
// mention it. That helper registers the cleanup which closes the handle.
var toolStoreConstructors = map[string]string{
	"cursorToolStore": "newCursorToolStoreForTest",
	"zcodeToolStore":  "newZCodeToolStoreForTest",
}

// toolTranscriptConstructors are the per-agent transcript constructors that open a
// provider store of their own. A test that calls one must release that handle.
var toolTranscriptConstructors = []string{
	"newCursorToolTranscript",
	"newZCodeToolTranscript",
}

// TestEveryTestClosesTheProviderStoreHandleItOpens fails when a test in this package
// opens a provider database handle that nothing closes before the test ends.
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
//   - Call a transcript constructor only beside releaseToolStoreAtTestEnd, which
//     closes the handle the constructor opened. The constructor releases its own
//     handle from context.AfterFunc, on a goroutine that races the RemoveAll.
func TestEveryTestClosesTheProviderStoreHandleItOpens(t *testing.T) {
	t.Parallel()

	stores, transcripts := 0, 0
	testutil.ForEachPackageTestFile(t, ".", func(fset *token.FileSet, file *ast.File) {
		ast.Inspect(file, func(node ast.Node) bool {
			ident, isIdent := node.(*ast.Ident)
			if !isIdent {
				return true
			}
			helper, isStore := toolStoreConstructors[ident.Name]
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

		for _, decl := range file.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			if !isFunc || fn.Body == nil || !callsAny(fn.Body, toolTranscriptConstructors) {
				continue
			}
			transcripts++
			assert.True(t, callsAny(fn.Body, []string{"releaseToolStoreAtTestEnd"}),
				"%s: %s opens a provider store through a transcript constructor and never calls "+
					"releaseToolStoreAtTestEnd, so the handle is closed from context.AfterFunc -- "+
					"on a goroutine that races the RemoveAll of t.TempDir",
				fset.Position(fn.Pos()), fn.Name.Name)
		}
	})

	// Both halves must match something. A rename that moved every call site past this
	// scan would otherwise leave it green while it checks nothing.
	assert.NotZero(t, stores, "no provider tool store type found; the store half of this scan is vacuous")
	assert.NotZero(t, transcripts, "no transcript constructor call found; the transcript half of this scan is vacuous")
}

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

// callsAny reports whether body calls one of the plain functions in names.
func callsAny(body *ast.BlockStmt, names []string) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		call, isCall := node.(*ast.CallExpr)
		if !isCall {
			return true
		}
		// A plain identifier only. A method or a package-qualified call is a
		// selector, and neither spelling can reach these package functions.
		ident, isIdent := call.Fun.(*ast.Ident)
		if !isIdent {
			return true
		}
		for _, name := range names {
			if ident.Name == name {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// parseGuardFixture parses one in-memory test file for the two scans below.
func parseGuardFixture(t *testing.T, src string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "fixture_test.go", src, 0)
	require.NoError(t, err)
	return file
}

// enclosingFuncName must span the whole declaration, and must report the package
// level as belonging to no function.
//
// The blessed helper states its store in the SIGNATURE, so a finder that reads the
// body alone reports that helper's return type as an orphan and fails the one
// declaration the rule exists to allow. testutil.EnclosingFuncFinder reads the body,
// correctly, because it maps a CALL SITE back to its function -- which is why this
// rule carries its own finder rather than reuse that one.
//
// The package-level answer matters for the opposite reason: a
// `var store = cursorToolStore{}` holds its handle for the whole test binary, and no
// cleanup in any test can reach it.
func TestEnclosingFuncNameSpansTheSignatureAndReportsThePackageLevel(t *testing.T) {
	t.Parallel()

	file := parseGuardFixture(t, `package p

var packageStore = cursorToolStore{}

func newCursorToolStoreForTest(t *testing.T) *cursorToolStore {
	return &cursorToolStore{}
}
`)
	var owners []string
	ast.Inspect(file, func(node ast.Node) bool {
		if ident, isIdent := node.(*ast.Ident); isIdent && ident.Name == "cursorToolStore" {
			owners = append(owners, enclosingFuncName(file, ident.Pos()))
		}
		return true
	})

	assert.Equal(t, []string{"", "newCursorToolStoreForTest", "newCursorToolStoreForTest"}, owners,
		"the package-level mention belongs to no function, and the return type and the body both belong to the helper")
}

// callsAny must see a call inside a nested closure, and must pass over a call through
// a selector.
//
// Both shapes are at the real call sites. Cursor's transcript tests build a transcript
// inside a t.Run closure, so a scan that read the top level of a body alone would pass
// over every one of them and report green. A selector cannot reach an unexported
// package function at all, so counting one would bless a test that calls something
// else with the same final name.
func TestCallsAnyReadsANestedCallAndPassesOverASelector(t *testing.T) {
	t.Parallel()

	file := parseGuardFixture(t, `package p

func inAClosure() {
	run(func() {
		releaseToolStoreAtTestEnd(t, transcript)
	})
}

func throughASelector() {
	helper.releaseToolStoreAtTestEnd(t, transcript)
}

func notAtAll() {
	run(nil)
}
`)
	bodies := map[string]*ast.BlockStmt{}
	for _, decl := range file.Decls {
		if fn, isFunc := decl.(*ast.FuncDecl); isFunc {
			bodies[fn.Name.Name] = fn.Body
		}
	}
	wanted := []string{"releaseToolStoreAtTestEnd"}

	assert.True(t, callsAny(bodies["inAClosure"], wanted), "a call inside a closure still counts")
	assert.False(t, callsAny(bodies["throughASelector"], wanted), "a selector cannot reach a package function")
	assert.False(t, callsAny(bodies["notAtAll"], wanted))
}

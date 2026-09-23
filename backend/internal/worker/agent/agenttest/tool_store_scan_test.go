package agenttest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

// callsAny must match a function of another package by the alias that the file
// imports it as, and only by that alias. After the provider split, a provider's
// tests release the handle through a test-support package, so the call is
// qualified.
func TestCallsAnyMatchesAQualifiedCallByItsAlias(t *testing.T) {
	t.Parallel()

	file := parseGuardFixture(t, `package p

func qualified() {
	support.Release(t, transcript)
}

func plain() {
	Release(t, transcript)
}
`)
	bodies := map[string]*ast.BlockStmt{}
	for _, decl := range file.Decls {
		if fn, isFunc := decl.(*ast.FuncDecl); isFunc {
			bodies[fn.Name.Name] = fn.Body
		}
	}

	assert.True(t, callsAny(bodies["qualified"], []string{"support.Release"}), "the alias and the name match")
	assert.False(t, callsAny(bodies["qualified"], []string{"other.Release"}), "another alias is another package")
	assert.False(t, callsAny(bodies["qualified"], []string{"Release"}), "a plain name is a function of this package")
	assert.False(t, callsAny(bodies["plain"], []string{"support.Release"}), "a plain call is not the qualified one")
}

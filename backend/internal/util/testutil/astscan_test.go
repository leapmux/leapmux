package testutil

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// QualifiedFuncName is the KEY every classification table is written against,
// so a shape it renders wrongly is a shape that can never be classified --
// or, worse, one that silently inherits another function's entry.
func TestQualifiedFuncName(t *testing.T) {
	src := `package p
func Plain() {}
func (s Value) OnValue() {}
func (s *Ptr) OnPointer() {}
func (p *Pool[T]) OnGenericPointer() {}
func (p Pool2[T, U]) OnGenericValue() {}
`
	file, err := parser.ParseFile(token.NewFileSet(), "src.go", src, 0)
	require.NoError(t, err)

	got := map[string]bool{}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			got[QualifiedFuncName(fn)] = true
		}
	}

	for _, want := range []string{
		"Plain",
		"(Value).OnValue",
		"(*Ptr).OnPointer",
		// Generic receivers render by their base type. Falling back to the bare
		// method name here would let two same-named methods on different
		// generic types share one table entry -- the collision the receiver
		// qualification exists to prevent.
		"(*Pool).OnGenericPointer",
		"(Pool2).OnGenericValue",
	} {
		assert.Truef(t, got[want], "expected a rendered name %q, got %v", want, keys(got))
	}
}

// EnclosingFuncFinder must report "no enclosing function" for a call that sits
// in a package-level declaration. A scan that only visits *ast.FuncDecl nodes
// walks straight past `var h = func() { ... }`, which is a hole in exactly the
// tests whose job is to have none.
func TestEnclosingFuncFinder_PackageLevelLiteralHasNoEnclosingFunc(t *testing.T) {
	src := `package p

var handler = func() { reach() }

func Named() { reach() }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "src.go", src, 0)
	require.NoError(t, err)
	finder := NewEnclosingFuncFinder(file)

	var inFunc, orphan int
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); !ok || ident.Name != "reach" {
			return true
		}
		if fn, found := finder.Find(call.Pos()); found {
			assert.Equal(t, "Named", QualifiedFuncName(fn))
			inFunc++
		} else {
			orphan++
		}
		return true
	})

	assert.Equal(t, 1, inFunc, "the call inside a named function is attributed to it")
	assert.Equal(t, 1, orphan, "the package-level literal's call belongs to no function and must be reported")
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The two package scanners must PARTITION a directory: every top-level .go file
// belongs to exactly one of them.
//
// A filter that leaked would be invisible at the caller. `worker/agent` scans its
// test files for a type that its non-test files DECLARE, so a test-file scan that
// also read source would report the declaration itself as an offender; and a
// source-file scan that also read tests would report every helper. Both halves
// must stay disjoint, and neither may be empty.
func TestPackageScannersPartitionTheDirectory(t *testing.T) {
	t.Parallel()

	source := map[string]bool{}
	ForEachPackageSourceFile(t, ".", func(fset *token.FileSet, file *ast.File) {
		source[filepath.Base(fset.Position(file.Pos()).Filename)] = true
	})
	tests := map[string]bool{}
	ForEachPackageTestFile(t, ".", func(fset *token.FileSet, file *ast.File) {
		tests[filepath.Base(fset.Position(file.Pos()).Filename)] = true
	})

	assert.Contains(t, source, "astscan.go")
	assert.Contains(t, tests, "astscan_test.go")
	for name := range tests {
		assert.True(t, strings.HasSuffix(name, "_test.go"), "%s is not a test file", name)
		assert.NotContains(t, source, name, "%s reached both scanners", name)
	}

	// Together they must cover the whole directory. A third filter state -- a file
	// neither scanner reads -- would hide that file from every rule built on these.
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		assert.True(t, source[name] || tests[name], "%s reached neither scanner", name)
	}
}

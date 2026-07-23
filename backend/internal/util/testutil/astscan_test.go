package testutil

import (
	"go/ast"
	"go/parser"
	"go/token"
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

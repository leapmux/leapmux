package control

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

// TestPackageNeverWritesStdout keeps the JSON contract structural.
//
// The client runs under every leapmux control command, and stdout carries
// that command's result. A line of prose there -- the browser step-up that an
// interceptor opens in the middle of an ordinary verb, say -- stops
// `leapmux ... --json | jq` from parsing. So the package writes prose only to
// an injected error writer (defaulting to os.Stderr), and this test fails the
// first reference to stdout or to a function that prints to it.
func TestPackageNeverWritesStdout(t *testing.T) {
	forbidden := map[string]bool{
		"os.Stdout":   true,
		"fmt.Print":   true,
		"fmt.Printf":  true,
		"fmt.Println": true,
	}
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	scanned := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err, "parse %s", name)
		scanned++
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && forbidden[pkg.Name+"."+sel.Sel.Name] {
				assert.Failf(t, "the package writes to stdout",
					"%s uses %s.%s; write to errOut or os.Stderr instead", fset.Position(sel.Pos()), pkg.Name, sel.Sel.Name)
			}
			return true
		})
	}
	require.NotZero(t, scanned, "the scan must find the package's sources")
	_, err = os.Stat("client.go")
	require.NoError(t, err, "the scan runs in the package directory")
}

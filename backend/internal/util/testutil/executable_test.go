package testutil

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writesExecutable reports whether call is os.WriteFile or os.OpenFile with a
// literal mode that sets an execute bit. osName is the identifier that the file
// imports the os package under.
func writesExecutable(call *ast.CallExpr, osName string) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || len(call.Args) != 3 {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok || pkg.Name != osName || (selector.Sel.Name != "WriteFile" && selector.Sel.Name != "OpenFile") {
		return false
	}
	literal, ok := call.Args[2].(*ast.BasicLit)
	if !ok || literal.Kind != token.INT {
		return false
	}
	mode, err := strconv.ParseUint(literal.Value, 0, 32)
	return err == nil && mode&0o111 != 0
}

// A program that a test writes with a plain os.WriteFile fails to start at
// random while a parallel test forks. See WriteExecutable.
//
// The helper's own write is the one site. Requiring it also proves that the scan
// finds such a call, so the test cannot pass by finding nothing.
func TestEveryExecutableGoesThroughWriteExecutable(t *testing.T) {
	t.Parallel()
	var sites []string
	walkRepoGoFiles(t, RepoPath(t, "backend"),
		func(string, string) bool { return true },
		func(_ *token.FileSet, _, rel string, file *ast.File) {
			osName, ok := ImportedAs(file, "os")
			if !ok {
				return
			}
			ast.Inspect(file, func(node ast.Node) bool {
				if call, isCall := node.(*ast.CallExpr); isCall && writesExecutable(call, osName) {
					sites = append(sites, rel)
				}
				return true
			})
		})
	assert.Equal(t, []string{"internal/util/testutil/executable.go"}, sites,
		"write a program through testutil.WriteExecutable, which keeps forks out while the file is open")
}

func TestWritesExecutableRecognizesAnExecutableMode(t *testing.T) {
	t.Parallel()
	for source, want := range map[string]bool{
		`os.WriteFile(path, data, 0o755)`:        true,
		`os.WriteFile(path, data, 0o700)`:        true,
		`os.WriteFile(path, data, 0755)`:         true,
		`os.OpenFile(path, flags, 0o711)`:        true,
		`os.WriteFile(path, data, 0o644)`:        false,
		`os.WriteFile(path, data, 0o600)`:        false,
		`os.WriteFile(path, data, mode)`:         false,
		`os.Chtimes(path, atime, 0o755)`:         false,
		`other.WriteFile(path, data, 0o755)`:     false,
		`writeFile(path, data, 0o755)`:           false,
		`os.WriteFile(path, data)`:               false,
		`os.WriteFile(path, data, 0o610)`:        true,
		`os.WriteFile(path, data, 0x1ed)`:        true,
		`os.WriteFile(path, data, "0o755")`:      false,
		`os.WriteFile(path, data, 0xFFFFFFFFFF)`: false,
		`a.os.WriteFile(path, data, 0o755)`:      false,
	} {
		assert.Equal(t, want, writesExecutable(parseCall(t, source), "os"), source)
	}
}

// A file can import the os package under another identifier, and the guard must
// follow it. Otherwise an alias hides a plain write from the guard.
func TestWritesExecutableFollowsTheImportName(t *testing.T) {
	t.Parallel()
	call := parseCall(t, `stdos.WriteFile(path, data, 0o755)`)
	assert.True(t, writesExecutable(call, "stdos"))
	assert.False(t, writesExecutable(call, "os"))
}

// parseCall parses a call expression.
func parseCall(t *testing.T, source string) *ast.CallExpr {
	t.Helper()
	expression, err := parser.ParseExpr(source)
	require.NoError(t, err, source)
	call, ok := expression.(*ast.CallExpr)
	require.True(t, ok, source)
	return call
}

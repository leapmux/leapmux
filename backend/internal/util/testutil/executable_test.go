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

// fileWritesExecutable reports whether file contains any os.WriteFile or
// os.OpenFile call with a literal mode that sets an execute bit.
func fileWritesExecutable(file *ast.File) bool {
	osName, ok := ImportedAs(file, "os")
	if !ok {
		return false
	}
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		if call, isCall := node.(*ast.CallExpr); isCall && writesExecutable(call, osName) {
			found = true
			return false
		}
		return true
	})
	return found
}

// A program that a test writes with a plain os.WriteFile fails to start at
// random while a parallel test forks. See package execfile, which every writer
// of a program goes through: WriteExecutable (execfile.Overwrite) in a test,
// and the Worker's copy of its own executable (execfile.CreateExclusive) in
// production.
//
// package execfile takes the mode as a parameter, so the scan finds no site at
// all. It must still prove that it read the tree, or it would pass by finding
// nothing: it requires that the walk visited execfile.go.
func TestEveryExecutableGoesThroughWriteExecutable(t *testing.T) {
	t.Parallel()
	var sites []string
	visitedWriter := false
	walkRepoGoFiles(t, RepoPath(t, "backend"),
		func(string, string) bool { return true },
		func(_ *token.FileSet, _, rel string, file *ast.File) {
			if rel == "internal/util/execfile/execfile.go" {
				visitedWriter = true
			}
			if fileWritesExecutable(file) {
				sites = append(sites, rel)
			}
		})
	require.True(t, visitedWriter, "the scan reads the tree")
	assert.Empty(t, sites,
		"write a program through testutil.WriteExecutable in a test, or execfile.CreateExclusive/Overwrite elsewhere, which keep forks out while the file is open")
}

// TestFileWritesExecutableDetectsRawWritesEndToEnd proves that ImportedAs and
// the AST walk together catch both direct and aliased executable writes, so
// TestEveryExecutableGoesThroughWriteExecutable cannot pass vacuously if the
// matcher breaks.
func TestFileWritesExecutableDetectsRawWritesEndToEnd(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "plain os.WriteFile with execute bit",
			src:  "package p\nimport \"os\"\nfunc f() { _ = os.WriteFile(\"x\", nil, 0o755) }\n",
			want: true,
		},
		{
			name: "aliased os.OpenFile with execute bit",
			src:  "package p\nimport stdos \"os\"\nfunc f() { _, _ = stdos.OpenFile(\"x\", 0, 0o700) }\n",
			want: true,
		},
		{
			name: "plain os.WriteFile without execute bit",
			src:  "package p\nimport \"os\"\nfunc f() { _ = os.WriteFile(\"x\", nil, 0o600) }\n",
			want: false,
		},
		{
			name: "unrelated package WriteFile",
			src:  "package p\nimport \"bytes\"\nvar _ = bytes.Buffer{}\nfunc f() { _ = other.WriteFile(\"x\", nil, 0o755) }\n",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "probe.go", tc.src, 0)
			require.NoError(t, err)
			assert.Equal(t, tc.want, fileWritesExecutable(file))
		})
	}
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

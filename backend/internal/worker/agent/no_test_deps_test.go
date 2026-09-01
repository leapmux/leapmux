package agent

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testOnlyImports are the packages that belong to a test binary and nowhere
// else. `testing` registers flags and pulls in the whole framework; testify
// carries its own YAML parser and reflection helpers.
var testOnlyImports = []string{
	"testing",
	"github.com/stretchr/testify",
}

// TestProductionSourcesDoNotImportTesting fails when a NON-test file in this
// package imports the test framework.
//
// This package is in `cmd/leapmux`'s import graph, so such an import links
// `testing`, testify and testify's own copy of `gopkg.in/yaml.v3` into the
// SHIPPED binary. A test-only dependency then becomes part of the deployed
// surface, and nothing reports it: the code compiles, every test passes, and
// the cost is visible only from a whole-graph query nobody runs twice.
//
// `acp_test_helpers.go` did exactly that until it was renamed to
// `_test.go`. The rename is the whole fix -- the helpers it declares are
// unexported and every consumer is already a test file in this package -- which
// is why the guard is worth more than the defect was.
//
// Other packages in this repo hold test helpers in NON-test files on purpose
// (`internal/util/testutil`, `internal/hub/store/storetest`, ...). That is fine:
// nothing in the production graph imports them. The rule is scoped to this
// package because this package is what the binary needs.
func TestProductionSourcesDoNotImportTesting(t *testing.T) {
	t.Parallel()

	testutil.ForEachPackageSourceFile(t, ".", func(fset *token.FileSet, file *ast.File) {
		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			require.NoError(t, err, "unquote import path")
			for _, banned := range testOnlyImports {
				assert.False(t, path == banned || strings.HasPrefix(path, banned+"/"),
					"%s: a non-test file imports %q, which links the test framework "+
						"into the shipped binary; move the file to _test.go",
					fset.Position(imp.Pos()), path)
			}
		}
	})
}

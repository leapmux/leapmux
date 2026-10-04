package providers

import (
	"go/ast"
	"go/types"
	"slices"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

// jsonRPCPublication identifies the methods that publish a JSON-RPC control
// request with no session check: (*providerkit.JSONRPCProcess).PublishControlRequest
// and (*providerkit.JSONRPCProcess).PublishControlRequestInSession. The first
// calls the second, so a provider can skip the check through either one.
var jsonRPCPublication = struct {
	pkgPath, receiver string
	names             []string
}{
	pkgPath:  modulePath + providerTree + "/internal/providerkit",
	receiver: "JSONRPCProcess",
	names:    []string{"PublishControlRequest", "PublishControlRequestInSession"},
}

// isJSONRPCPublication reports whether fn is an unguarded publication method.
// It compares the package path and the names, because each loaded package can
// hold its own copy of an imported package's type objects.
func isJSONRPCPublication(fn *types.Func) bool {
	if !slices.Contains(jsonRPCPublication.names, fn.Name()) || fn.Pkg() == nil || fn.Pkg().Path() != jsonRPCPublication.pkgPath {
		return false
	}
	recv := fn.Signature().Recv()
	if recv == nil {
		return false
	}
	receiver := recv.Type()
	if pointer, ok := receiver.(*types.Pointer); ok {
		receiver = pointer.Elem()
	}
	named, ok := receiver.(*types.Named)
	return ok && named.Obj().Name() == jsonRPCPublication.receiver
}

// acpFamily lists the provider packages that build on the ACP base, read from
// allowedProviderImports so that a new ACP provider joins the guard with its
// import row.
func acpFamily() []string {
	var family []string
	for key, deps := range allowedProviderImports {
		if slices.Contains(deps, "acp") {
			family = append(family, key)
		}
	}
	sort.Strings(family)
	return family
}

// TestACPProvidersPublishControlRequestsThroughTheSessionGuard fails when a
// package of the ACP family publishes a JSON-RPC control request through one
// of the jsonRPCPublication methods. acp.Base embeds both methods, so a
// provider can call one by mistake. The call publishes the card of
// any session, although an agent that serves one session must refuse the
// dialogs of every other one: a card of a retired session lets the reader
// approve work that no transcript shows. Each ACP provider publishes through
// acp.Base.PublishSessionControlRequest, which checks the session first.
//
// The scan type-checks the packages, so it finds the call whatever its
// spelling: the promoted method, the embedded field, or a method value.
func TestACPProvidersPublishControlRequestsThroughTheSessionGuard(t *testing.T) {
	t.Parallel()

	family := acpFamily()
	require.Contains(t, family, "kiro", "the family must hold the ACP providers, or the guard checks nothing")
	patterns := []string{modulePath + providerTree + "/acp"}
	for _, key := range family {
		patterns = append(patterns, modulePath+providerTree+"/"+key)
	}
	loaded, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
	}, patterns...)
	require.NoError(t, err)
	require.Len(t, loaded, len(patterns))

	calls := map[string][]string{}
	for _, pkg := range loaded {
		require.Empty(t, pkg.Errors, "%s must type-check", pkg.PkgPath)
		for _, file := range pkg.Syntax {
			var enclosing string
			ast.Inspect(file, func(node ast.Node) bool {
				if decl, ok := node.(*ast.FuncDecl); ok {
					enclosing = decl.Name.Name
				}
				selector, ok := node.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				fn, ok := pkg.TypesInfo.Uses[selector.Sel].(*types.Func)
				if ok && isJSONRPCPublication(fn) {
					position := pkg.Fset.Position(selector.Pos())
					calls[pkg.PkgPath] = append(calls[pkg.PkgPath], enclosing+" at "+position.String())
				}
				return true
			})
		}
	}

	// The base itself publishes through the method, inside the guard. Finding
	// that call proves that the scan recognizes the method.
	base := calls[modulePath+providerTree+"/acp"]
	require.Len(t, base, 1, "the ACP base must publish in one place, inside the session guard: %v", base)
	assert.Regexp(t, `^PublishSessionControlRequest at `, base[0])
	for _, key := range family {
		assert.Emptyf(t, calls[modulePath+providerTree+"/"+key],
			"%s publishes a control request with no session check; call acp.Base.PublishSessionControlRequest", key)
	}
}

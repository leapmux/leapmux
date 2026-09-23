package main

import (
	"path"
	"sort"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

// shippedBinary is the import path of the binary that LeapMux ships.
const shippedBinary = "github.com/leapmux/leapmux/cmd/leapmux"

// thisModule prefixes each package path of this module.
const thisModule = "github.com/leapmux/leapmux/"

// TestShippedBinaryLinksNoTestSupport fails when the shipped binary links the
// test framework or one of this module's test-support packages.
//
// A production file that imports `testing`, testify, or a test-support package
// links it into the SHIPPED binary. A test-only dependency then becomes part of
// the deployed surface, and nothing reports it: the code compiles, every test
// passes, and the cost is visible only from a whole-graph query that nobody runs
// twice. testify alone carries its own YAML parser and reflection helpers, and
// `testing` registers its flags on the command line of the binary.
//
// The query here is that whole-graph query. It walks every package that the
// binary links, rather than a rule for each package, so a test helper in a
// non-test file of ANY package in the graph fails it. Test-support packages
// (agenttest, testutil, storetest, and the rest) are fine where they are:
// nothing in the production graph may import them, and this test is what holds
// that.
//
// The name rule applies to this module only. The standard library links
// internal/synctest, which is part of the runtime and not a test dependency.
func TestShippedBinaryLinksNoTestSupport(t *testing.T) {
	t.Parallel()

	roots, err := packages.Load(&packages.Config{Mode: packages.NeedName | packages.NeedImports | packages.NeedDeps}, shippedBinary)
	require.NoError(t, err)
	require.Len(t, roots, 1)
	require.Empty(t, roots[0].Errors, "the binary must load")

	linked := map[string]bool{}
	packages.Visit(roots, nil, func(p *packages.Package) {
		linked[p.PkgPath] = true
	})
	require.Contains(t, linked, thisModule+"internal/worker/agent/providers/claude",
		"the walk must reach the worker, or it proves nothing about the providers")

	var banned []string
	for pkgPath := range linked {
		if isTestSupport(pkgPath) {
			banned = append(banned, pkgPath)
		}
	}
	sort.Strings(banned)
	assert.Empty(t, banned, "the shipped binary links test support; move the importing code to a _test.go file")
}

// isTestSupport reports whether a package belongs to a test binary and nowhere
// else.
func isTestSupport(pkgPath string) bool {
	switch {
	case pkgPath == "testing" || strings.HasPrefix(pkgPath, "testing/"):
		return true
	case pkgPath == "github.com/stretchr/testify" || strings.HasPrefix(pkgPath, "github.com/stretchr/testify/"):
		return true
	case !strings.HasPrefix(pkgPath, thisModule):
		return false
	}
	return testutil.IsTestSupportPackage(path.Base(pkgPath))
}

// TestIsTestSupport pins the rule, so a change to it cannot ban nothing.
func TestIsTestSupport(t *testing.T) {
	t.Parallel()

	for pkgPath, want := range map[string]bool{
		"testing":                                             true,
		"testing/fstest":                                      true,
		"github.com/stretchr/testify/assert":                  true,
		thisModule + "internal/util/testutil":                 true,
		thisModule + "internal/worker/agent/agenttest":        true,
		thisModule + "internal/hub/store/storetest":           true,
		thisModule + "internal/worker/agent":                  false,
		thisModule + "internal/worker/agent/providers/claude": false,
		"internal/synctest":                                   false,
		"github.com/other/pkgtest":                            false,
		"testingx":                                            false,
	} {
		assert.Equalf(t, want, isTestSupport(pkgPath), "%s", pkgPath)
	}
}

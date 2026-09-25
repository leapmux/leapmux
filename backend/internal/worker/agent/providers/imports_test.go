package providers

import (
	"go/ast"
	"go/token"
	"path"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// agentTree is the repo-relative directory of package agent and every package
	// under it.
	agentTree = "internal/worker/agent"
	// providerTree is the directory of the composition root and every provider.
	providerTree = agentTree + "/providers"
	// modulePath prefixes each import path of this module.
	modulePath = "github.com/leapmux/leapmux/"
)

// allowedProviderImports states, for each package under providerTree, the
// provider packages that its production code may import. A provider package is
// any package under providerTree except those under providers/internal, which
// every provider may import. The keys and the values are relative to
// providerTree, and "." is the composition root.
//
// Only the ACP family shares code between providers. The ACP providers import
// the base in acp, Kilo builds on the OpenCode family base, and OpenCode, Kilo,
// ZCode and MiMo Code read the OpenCode-family session store. Every other edge
// between providers is a provider-specific shape that leaked into another
// provider.
var allowedProviderImports = map[string][]string{
	".": {"amp", "claude", "cline", "codewhale", "codex", "copilot", "cursor", "goose", "grok", "kilo", "kimi", "kiro", "mimo", "ohmypi", "opencode", "pi", "qwen", "reasonix", "zcode"},

	"acp":                    nil,
	"amp":                    nil,
	"acp/acptest":            nil,
	"claude":                 nil,
	"cline":                  nil,
	"claude/claudetest":      {"claude"},
	"codewhale":              nil,
	"codex":                  nil,
	"copilot":                nil,
	"cursor":                 {"acp"},
	"goose":                  {"acp"},
	"grok":                   {"acp"},
	"kilo":                   {"acp", "opencode", "opencode/opencodestore"},
	"kimi":                   nil,
	"kiro":                   {"acp"},
	"mimo":                   {"opencode/opencodestore"},
	"ohmypi":                 nil,
	"opencode":               {"acp", "opencode/opencodestore"},
	"opencode/opencodestore": nil,
	"opencode/opencodestore/opencodestoretest": nil,
	"opencode/opencodetest":                    nil,
	"pi":                                       nil,
	"qwen":                                     {"acp"},
	"reasonix":                                 {"acp"},
	"zcode":                                    {"opencode/opencodestore"},

	"internal/providerkit":                       nil,
	"internal/providerkit/providerkittest":       nil,
	"internal/sessionstore":                      nil,
	"internal/tooltranscript":                    nil,
	"internal/tooltranscript/tooltranscripttest": nil,
}

// allowedAgentTreeImports states, for each package in the agent tree outside
// providerTree, the packages of the agent tree that it may import. Package agent
// and its test support know no provider: a provider imports them, so an import
// the other way is a cycle or a shape that belongs in the provider.
var allowedAgentTreeImports = map[string][]string{
	// Registration.AgentDir is an agentdir.Spec, and Options.AgentDirs the
	// directories of the worker.
	agentTree:                        {agentTree + "/internal/agentdir", agentTree + "/internal/launch"},
	agentTree + "/internal/agentdir": nil,
	// agentdirtest builds directories for the tests of the providers.
	agentTree + "/internal/agentdir/agentdirtest": {agentTree + "/internal/agentdir"},
	agentTree + "/internal/launch":                nil,
	// Registration.Locator is a launch.Locator, so a test registration needs it.
	agentTree + "/agenttest": {agentTree, agentTree + "/internal/launch"},
}

// TestProviderImportGraph checks the production imports of each package under
// providerTree against allowedProviderImports.
func TestProviderImportGraph(t *testing.T) {
	t.Parallel()

	imports := productionImports(t)
	found := 0
	for dir, deps := range imports {
		if dir != providerTree && !strings.HasPrefix(dir, providerTree+"/") {
			continue
		}
		found++
		key := strings.TrimPrefix(strings.TrimPrefix(dir, providerTree), "/")
		if key == "" {
			key = "."
		}
		allowed, ok := allowedProviderImports[key]
		if !assert.Truef(t, ok, "%s is a package with no row in allowedProviderImports; state the providers it may import", key) {
			continue
		}
		for _, dep := range deps {
			if !strings.HasPrefix(dep, providerTree+"/") {
				continue
			}
			rel := strings.TrimPrefix(dep, providerTree+"/")
			if strings.HasPrefix(rel, "internal/") {
				continue
			}
			assert.Containsf(t, allowed, rel, "%s must not import the provider package %s", key, rel)
		}
	}
	assert.Equalf(t, len(allowedProviderImports), found,
		"allowedProviderImports and the packages under %s must list the same packages", providerTree)
}

// TestOnlyTheCompositionRootWiresProviders checks that no production package
// outside providerTree imports a provider package, bootstrap excepted, and that
// bootstrap imports the composition root alone. The worker service and the rest
// of the agent tree reach a provider only through the Registry.
func TestOnlyTheCompositionRootWiresProviders(t *testing.T) {
	t.Parallel()

	imports := productionImports(t)
	require.Contains(t, imports, "internal/worker/bootstrap", "the scan must see the package that wires the registry")
	assert.Contains(t, imports["internal/worker/bootstrap"], providerTree,
		"bootstrap builds the one registry from the composition root")
	for dir, deps := range imports {
		if dir == providerTree || strings.HasPrefix(dir, providerTree+"/") {
			continue
		}
		for _, dep := range deps {
			if dep != providerTree && !strings.HasPrefix(dep, providerTree+"/") {
				continue
			}
			if dir == "internal/worker/bootstrap" && dep == providerTree {
				continue
			}
			t.Errorf("%s imports %s; only bootstrap may import a provider package, and only the composition root", dir, dep)
		}
	}
}

// TestNeutralAgentPackagesImportNoProvider checks the agent-tree imports of each
// package outside providerTree against allowedAgentTreeImports.
func TestNeutralAgentPackagesImportNoProvider(t *testing.T) {
	t.Parallel()

	imports := productionImports(t)
	found := 0
	for dir, deps := range imports {
		if dir == providerTree || strings.HasPrefix(dir, providerTree+"/") {
			continue
		}
		if dir != agentTree && !strings.HasPrefix(dir, agentTree+"/") {
			continue
		}
		found++
		allowed, ok := allowedAgentTreeImports[dir]
		if !assert.Truef(t, ok, "%s has no row in allowedAgentTreeImports", dir) {
			continue
		}
		for _, dep := range deps {
			if dep != agentTree && !strings.HasPrefix(dep, agentTree+"/") {
				continue
			}
			assert.Containsf(t, allowed, dep, "%s must not import %s", dir, dep)
		}
	}
	assert.Equalf(t, len(allowedAgentTreeImports), found,
		"allowedAgentTreeImports and the packages of %s outside %s must list the same packages", agentTree, providerTree)
}

// TestAgentTestImportsOnlyTheNeutralAPI checks both source and test files.
// Registration fixtures may import launch for Locator, but no provider package.
func TestAgentTestImportsOnlyTheNeutralAPI(t *testing.T) {
	t.Parallel()

	allowed := map[string]bool{
		agentTree:                      true,
		agentTree + "/internal/launch": true,
	}
	check := func(_ *token.FileSet, file *ast.File) {
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			require.NoError(t, err)
			dep, isModuleImport := strings.CutPrefix(importPath, modulePath)
			if !isModuleImport || (dep != agentTree && !strings.HasPrefix(dep, agentTree+"/")) {
				continue
			}
			assert.Truef(t, allowed[dep], "agenttest must not import %s", dep)
		}
	}
	productionFiles, testFiles := 0, 0
	testutil.ForEachPackageSourceFile(t, "../agenttest", func(fset *token.FileSet, file *ast.File) {
		productionFiles++
		check(fset, file)
	})
	testutil.ForEachPackageTestFile(t, "../agenttest", func(fset *token.FileSet, file *ast.File) {
		testFiles++
		check(fset, file)
	})
	require.NotZero(t, productionFiles, "the guard must scan the agenttest source")
	require.NotZero(t, testFiles, "the guard must scan the agenttest tests")
}

// productionImports maps each package directory of the backend module, repo
// relative, to the module packages that its production files import, sorted.
func productionImports(t *testing.T) map[string][]string {
	t.Helper()
	sets := map[string]map[string]bool{}
	scanned := testutil.ForEachRepoSourceFile(t, testutil.RepoPath(t, "backend"), func(_ *token.FileSet, rel string, file *ast.File) {
		dir := path.Dir(rel)
		if sets[dir] == nil {
			sets[dir] = map[string]bool{}
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			require.NoError(t, err)
			if dep, ok := strings.CutPrefix(importPath, modulePath); ok {
				sets[dir][dep] = true
			}
		}
	})
	require.Greater(t, scanned, 500, "the scan must see the whole backend module")
	imports := make(map[string][]string, len(sets))
	for dir, set := range sets {
		deps := make([]string, 0, len(set))
		for dep := range set {
			deps = append(deps, dep)
		}
		sort.Strings(deps)
		imports[dir] = deps
	}
	return imports
}

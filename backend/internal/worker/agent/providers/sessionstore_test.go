package providers

import (
	"context"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/amp/amptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/cline/clinetest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/muse/musetest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/zcode/zcodetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const nativeSessionStorePeerPattern = "^TestNativeSessionStorePeer$"

type sessionRuntimeConstructor func(*testing.T, string, string, string) agenttest.SessionRuntimeFixture

// Each provider's test package owns the native peer protocol.
var nativeSessionRuntimeFixtures = map[leapmuxv1.AgentProvider]sessionRuntimeConstructor{
	leapmuxv1.AgentProvider_AGENT_PROVIDER_MUSE_CODE: musetest.NewSessionRuntimeFixture,
	leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP:       amptest.NewSessionRuntimeFixture,
	leapmuxv1.AgentProvider_AGENT_PROVIDER_CLINE:     clinetest.NewSessionRuntimeFixture,
	leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE:     zcodetest.NewSessionRuntimeFixture,
}

func TestNativeSessionStorePeer(t *testing.T) {
	for _, run := range []func() bool{amptest.RunSessionStorePeer, clinetest.RunSessionStorePeer, zcodetest.RunSessionStorePeer, musetest.RunSessionStorePeer} {
		if run() {
			return
		}
	}
}

func TestEveryRegisteredProviderTreatsAnAbsentStoreAsEmpty(t *testing.T) {
	t.Parallel()
	registry := Registry()
	for _, id := range registry.Providers() {
		t.Run(id.String(), func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			workingDir := filepath.Join(home, "workspace", "project")
			require.NoError(t, os.MkdirAll(workingDir, 0o700))
			query := agent.StoredSessionQuery{
				WorkingDir: workingDir,
				HomeDir:    home,
				EnvEntries: []string{"HOME=" + home, "USERPROFILE=" + home},
			}
			denied := launch.Custom(func(context.Context, string, bool) (launch.Spec, launch.Resolution) {
				t.Errorf("%v attempted an uncontrolled native session launch", id)
				return launch.Spec{}, launch.Missing
			})
			query.RuntimeLocator = &denied
			var fixture *agenttest.SessionRuntimeFixture
			if constructor := nativeSessionRuntimeFixtures[id]; constructor != nil {
				controlled := constructor(t, home, workingDir, nativeSessionStorePeerPattern)
				fixture = &controlled
				query.RuntimeLocator = &controlled.Locator
				query.EnvEntries = append(query.EnvEntries, controlled.EnvEntries...)
			}
			got, err := registry.Plugin(id).ListStoredSessions(t.Context(), query)
			assert.NoErrorf(t, err, "%v: an absent store is the normal state, not a failure", id)
			assert.Emptyf(t, got, "%v", id)
			if fixture != nil {
				require.NotNil(t, fixture.AssertInvoked)
				fixture.AssertInvoked(t)
			}
		})
	}
}

// Follow local calls from the public reader so an indirect process launch cannot escape the guard.
func TestEveryNativeSessionCommandReaderHasAControlledRuntimeFixture(t *testing.T) {
	t.Parallel()
	found := make(map[leapmuxv1.AgentProvider]bool)
	for id, dir := range providerDirs {
		calls := make(map[string][]string)
		launches := make(map[string]bool)
		testutil.ForEachPackageSourceFile(t, dir, func(_ *token.FileSet, file *ast.File) {
			aliases := make(map[string]string)
			for _, pkg := range []string{"providerkit", "launch"} {
				location := "github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
				if pkg == "providerkit" {
					location = "github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
				}
				if alias, ok := testutil.ImportedAs(file, location); ok {
					aliases[alias] = pkg
				}
			}
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Body == nil {
					continue
				}
				name := function.Name.Name
				ast.Inspect(function.Body, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					switch target := call.Fun.(type) {
					case *ast.Ident:
						calls[name] = append(calls[name], target.Name)
					case *ast.SelectorExpr:
						if object, ok := target.X.(*ast.Ident); ok && aliases[object.Name] != "" {
							pkg := aliases[object.Name]
							if pkg == "providerkit" && target.Sel.Name == "RunCLI" || pkg == "launch" && target.Sel.Name == "Wrap" {
								launches[name] = true
							}
						} else {
							calls[name] = append(calls[name], target.Sel.Name)
						}
					}
					return true
				})
			}
		})
		visited := make(map[string]bool)
		var reachesLaunch func(string) bool
		reachesLaunch = func(name string) bool {
			if visited[name] {
				return false
			}
			visited[name] = true
			if launches[name] {
				return true
			}
			for _, callee := range calls[name] {
				if reachesLaunch(callee) {
					return true
				}
			}
			return false
		}
		if reachesLaunch("ListStoredSessions") {
			found[id] = true
		}
	}
	require.NotEmpty(t, found, "the guard must find native session command readers")
	for id := range found {
		assert.NotNilf(t, nativeSessionRuntimeFixtures[id], "%v needs a provider-owned native session fixture", id)
	}
	for id := range nativeSessionRuntimeFixtures {
		assert.Truef(t, found[id], "%v has a fixture but no native session command reader", id)
	}
}

package providers

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/amp/amptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/cline/clinetest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/zcode/zcodetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const nativeSessionStorePeerPattern = "^TestNativeSessionStorePeer$"

type sessionRuntimeConstructor func(*testing.T, string, string, string) agenttest.SessionRuntimeFixture

// Each provider's test package owns the native peer protocol.
var nativeSessionRuntimeFixtures = map[leapmuxv1.AgentProvider]sessionRuntimeConstructor{
	leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP:   amptest.NewSessionRuntimeFixture,
	leapmuxv1.AgentProvider_AGENT_PROVIDER_CLINE: clinetest.NewSessionRuntimeFixture,
	leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE: zcodetest.NewSessionRuntimeFixture,
}

func TestNativeSessionStorePeer(t *testing.T) {
	for _, run := range []func() bool{amptest.RunSessionStorePeer, clinetest.RunSessionStorePeer, zcodetest.RunSessionStorePeer} {
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

// The session modules identify native command readers through their actual launch calls.
func TestEveryNativeSessionCommandReaderHasAControlledRuntimeFixture(t *testing.T) {
	t.Parallel()
	found := make(map[leapmuxv1.AgentProvider]bool)
	for id, dir := range providerDirs {
		testutil.ForEachPackageSourceFile(t, dir, func(fset *token.FileSet, file *ast.File) {
			filename := filepath.Base(fset.Position(file.Pos()).Filename)
			if !strings.HasPrefix(filename, "session") {
				return
			}
			launcherAliases := make(map[string]string)
			for _, pkg := range []string{"providerkit", "launch"} {
				base := "github.com/leapmux/leapmux/internal/worker/agent/"
				location := base + "internal/launch"
				if pkg == "providerkit" {
					location = base + "providers/internal/providerkit"
				}
				if alias, ok := testutil.ImportedAs(file, location); ok {
					launcherAliases[alias] = pkg
				}
			}
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				name, ok := selector.X.(*ast.Ident)
				if !ok {
					return true
				}
				pkg := launcherAliases[name.Name]
				if pkg == "providerkit" && selector.Sel.Name == "RunCLI" || pkg == "launch" && selector.Sel.Name == "Wrap" {
					found[id] = true
				}
				return true
			})
		})
	}
	require.NotEmpty(t, found, "the guard must find the native session command readers")
	for id := range found {
		assert.NotNilf(t, nativeSessionRuntimeFixtures[id], "%v needs a provider-owned native session fixture", id)
	}
	for id := range nativeSessionRuntimeFixtures {
		assert.Truef(t, found[id], "%v has a fixture but no native session command reader", id)
	}
}

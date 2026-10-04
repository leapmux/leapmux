//go:build unix

package cursor

import (
	"context"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startCursorNativeLifecycleTestAgent(t *testing.T, sink *agenttest.Sink) *Agent {
	t.Helper()
	installFakeCursorCLI(t, "new")
	provider, err := Start(context.Background(), agent.Options{
		AgentID: "cursor-new", WorkingDir: t.TempDir(), Shell: testutil.TestShell(), LoginShell: false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
	}, agent.NewProviderServices(sink))
	require.NoError(t, err)
	a := provider.(*Agent)
	t.Cleanup(func() { a.Stop(); _ = a.Wait() })
	return a
}

func TestStartCursorCLI_AdvertisesNativeSubagents(t *testing.T) {
	a := startCursorNativeLifecycleTestAgent(t, &agenttest.Sink{})
	assert.Equal(t, true, a.HooksForTest().ClientCapabilityMeta["subagents"])
	require.NotNil(t, a.HooksForTest().SessionMetadataHandler)
}

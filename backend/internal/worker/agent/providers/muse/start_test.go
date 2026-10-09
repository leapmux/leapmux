package muse

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLaunchArgsUsesNativeTrustByDefault(t *testing.T) {
	t.Parallel()
	for _, trust := range []string{"", contracts.MuseWorkspaceTrustNative} {
		args, err := launchArgs(agent.Options{Options: optionmap.Map{contracts.MuseOptionIDWorkspaceTrust: trust}})
		require.NoError(t, err)
		assert.Equal(t, []string{"serve"}, args)
	}
}

func TestLaunchArgsRequiresAnExplicitTrustChoice(t *testing.T) {
	t.Parallel()
	args, err := launchArgs(agent.Options{Options: optionmap.Map{contracts.MuseOptionIDWorkspaceTrust: contracts.MuseWorkspaceTrustAgent}})
	require.NoError(t, err)
	assert.Equal(t, []string{"serve", "--trust-workspace"}, args)
	_, err = launchArgs(agent.Options{Options: optionmap.Map{contracts.MuseOptionIDWorkspaceTrust: "invalid"}})
	require.Error(t, err)
}

func TestOpenSessionAcceptsAnUnselectedNativeModel(t *testing.T) {
	a, _ := testAgent(t)
	peer := &museTestPeer{agent: a, reply: func(method string, params json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodSessionStart, method)
		return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"unselected","modelId":null,"activeTurnId":null,"approvalMode":{"mode":"onRequest"}},"viewCursor":"v:unselected:0"}`)}
	}}
	a.SetStdinForTest(peer)
	require.NoError(t, a.openSession("", time.Second))
	assert.Equal(t, "unselected", a.sessionID)
	assert.NotContains(t, a.SettingsSnapshot().SurfacedOptions, agent.OptionIDModel)
}

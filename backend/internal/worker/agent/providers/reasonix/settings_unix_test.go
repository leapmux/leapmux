//go:build unix

package reasonix

import (
	"encoding/json"
	"path/filepath"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReasonixModeRejectsInvalidAcknowledgments(t *testing.T) {
	t.Parallel()
	for _, response := range []string{`null`, `[]`, `"invalid"`} {
		t.Run(response, func(t *testing.T) {
			t.Parallel()
			a, requests := acptest.NewAgentForRPCWithRequestResponder(t,
				func() *Agent { return &Agent{} }, func(a *Agent) *acp.Base { return &a.Base },
				func(agenttest.RecordedRequest) agenttest.RPCReply {
					return agenttest.RPCReply{Result: json.RawMessage(response)}
				},
			)
			sink := &agenttest.Sink{}
			a.SetSinkForTest(agent.NewProviderServices(sink))
			acknowledged := false
			err := a.setReasonixMode("normal", func(string) { acknowledged = true })
			require.ErrorContains(t, err, "must be an object")
			assert.False(t, acknowledged)
			assert.Zero(t, sink.GoalClears())
			require.Len(t, requests(), 1)
		})
	}
}

func TestReasonixModeValidatesBeforeSending(t *testing.T) {
	t.Parallel()
	a, requests := newReasonixAgentForRPC(t)
	a.SetAvailableModesForTest([]*leapmuxv1.AvailableOption{{Id: "normal"}, {Id: "plan"}, {Id: "goal"}})
	err := a.setReasonixMode("unknown", func(string) { t.Error("an unknown mode cannot be acknowledged") })
	require.ErrorContains(t, err, "unknown Reasonix mode")
	err = a.setReasonixMode("normal", nil)
	require.ErrorContains(t, err, "acknowledgment callback")
	a.SetSessionIDForTest("")
	err = a.setReasonixMode("normal", func(string) { t.Error("an absent session cannot be acknowledged") })
	require.ErrorContains(t, err, "session is unavailable")
	assert.Empty(t, requests())
}

func TestReasonixModeSettingsInvalidateGoalConfirmations(t *testing.T) {
	for _, mode := range []string{"normal", "plan"} {
		t.Run(mode, func(t *testing.T) {
			installFakeReasonixCLI(t, filepath.Join(t.TempDir(), "args.txt"))
			sink := &agenttest.Sink{}
			provider, err := Start(t.Context(), agent.Options{
				AgentID: "reasonix-mode", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
				Options: map[string]string{agent.OptionIDPermissionMode: "goal"}, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
			}, agent.NewProviderServices(sink))
			require.NoError(t, err)
			a := provider.(*Agent)
			t.Cleanup(func() { a.Stop(); _ = a.Wait() })
			a.goalStatusMu.Lock()
			generation := a.goalOperationGeneration
			a.goalStatusMu.Unlock()
			result := a.UpdateSettings(map[string]string{agent.OptionIDPermissionMode: mode})
			require.True(t, result.AppliedLive)
			assert.Equal(t, 1, sink.GoalClears(), "leaving Goal mode clears its native objective")
			a.goalStatusMu.Lock()
			current := a.goalOperationGeneration
			a.goalStatusMu.Unlock()
			require.Greater(t, current, generation)
			require.ErrorContains(t, a.confirmReasonixGoal(a.CurrentSessionID(), generation), "goal changed")
		})
	}
}

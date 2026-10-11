//go:build unix

package copilot

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/require"
)

func TestCopilotManualCompactionUsesNativeSessionRPC(t *testing.T) {
	requestsPath := filepath.Join(t.TempDir(), "requests.jsonl")
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary: "copilot", HelperRun: "TestHelperCopilotNativeConnection",
		WantEnv: "LEAPMUX_TEST_COPILOT_NATIVE",
		Env:     []string{"LEAPMUX_TEST_COPILOT_SESSION_REQUESTS=" + requestsPath},
	})
	provider, err := startNativeCopilot(t.Context(), agent.Options{
		AgentID: "native-compaction", WorkingDir: t.TempDir(), Shell: testutil.TestShell(), APITimeout: time.Second,
	}, agent.NewProviderServices(agenttest.Nop()))
	require.NoError(t, err)
	t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })
	compactor, supported := provider.(agent.ContextCompactor)
	require.True(t, supported, "Copilot must dispatch manual compaction through its native RPC")
	require.NoError(t, compactor.CompactContext())
	// The peer answers this request after it records the compaction request.
	_, err = provider.(*Agent).SendRequest("probe.echo", json.RawMessage(`{}`), time.Second)
	require.NoError(t, err)

	raw, err := os.ReadFile(requestsPath)
	require.NoError(t, err)
	var compactCount, sendCount int
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte{'\n'}) {
		var request struct {
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		require.NoError(t, json.Unmarshal(line, &request))
		switch request.Method {
		case "session.history.compact":
			compactCount++
			require.JSONEq(t, `"manual"`, string(request.Params["trigger"]))
			require.JSONEq(t, `"`+provider.(*Agent).currentNativeSessionID()+`"`, string(request.Params["sessionId"]))
		case "session.send":
			sendCount++
		}
	}
	require.Equal(t, 1, compactCount)
	require.Zero(t, sendCount)
}

func TestCopilotManualCompactionRefusesBusyAndUnavailableSessions(t *testing.T) {
	a, _ := newNativeCopilotForEvents(t)
	a.stateMu.Lock()
	a.active = true
	a.stateMu.Unlock()
	require.ErrorIs(t, a.CompactContext(), agent.ErrAgentBusy)
	a.stateMu.Lock()
	a.active = false
	a.sessionID = ""
	a.stateMu.Unlock()
	require.ErrorContains(t, a.CompactContext(), "session is unavailable")
}

func TestCopilotManualCompactionDoesNotOfferSteering(t *testing.T) {
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary: "copilot", HelperRun: "TestHelperCopilotNativeConnection",
		WantEnv: "LEAPMUX_TEST_COPILOT_NATIVE",
		Env:     []string{"LEAPMUX_TEST_COPILOT_COMPACTION_OUTCOME=pending"},
	})
	sink := &agenttest.Sink{}
	provider, err := startNativeCopilot(t.Context(), agent.Options{
		AgentID: "native-compaction-steer", WorkingDir: t.TempDir(), Shell: testutil.TestShell(), APITimeout: time.Second,
	}, agent.NewProviderServices(sink))
	require.NoError(t, err)
	t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })
	a := provider.(*Agent)
	require.NoError(t, a.CompactContext())
	require.False(t, a.SupportsSteering(), "a summary RPC does not accept an immediate user message")
	require.ErrorIs(t, a.SteerInput("unexpected steer", nil), agent.ErrAgentBusy)
	require.ErrorIs(t, a.SendInput("unexpected turn", nil), agent.ErrAgentBusy)
	active, published := sink.LastTurnActive()
	require.True(t, published)
	require.True(t, active)
	require.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_UNSPECIFIED, sink.TurnKinds()[len(sink.TurnKinds())-1])
	a.forgetNativeSessionState("")
	require.True(t, a.SupportsSteering(), "a replacement releases the old summary RPC")
	active, published = sink.LastTurnActive()
	require.True(t, published)
	require.False(t, active)
}

func TestCopilotManualCompactionReportsNativeFailure(t *testing.T) {
	for _, outcome := range []string{"refused", "invalid-result", "rpc-error"} {
		t.Run(outcome, func(t *testing.T) {
			agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
				Binary: "copilot", HelperRun: "TestHelperCopilotNativeConnection",
				WantEnv: "LEAPMUX_TEST_COPILOT_NATIVE",
				Env:     []string{"LEAPMUX_TEST_COPILOT_COMPACTION_OUTCOME=" + outcome},
			})
			sink := &agenttest.Sink{}
			provider, err := startNativeCopilot(t.Context(), agent.Options{
				AgentID: "native-compaction-failure", WorkingDir: t.TempDir(), Shell: testutil.TestShell(), APITimeout: time.Second,
			}, agent.NewProviderServices(sink))
			require.NoError(t, err)
			t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })
			a := provider.(*Agent)
			require.NoError(t, a.CompactContext())
			require.Eventually(t, func() bool { return len(sink.LeapMuxNotifications()) == 1 }, 30*time.Second, time.Millisecond)
			notification := sink.LeapMuxNotifications()[0]
			require.Equal(t, contracts.NotificationTypeAgentError, notification[contracts.NotificationFieldType])
			require.Contains(t, notification[contracts.NotificationFieldError], "could not compact")
			require.Eventually(t, func() bool {
				a.stateMu.Lock()
				defer a.stateMu.Unlock()
				return !a.active
			}, 30*time.Second, time.Millisecond)
		})
	}
}

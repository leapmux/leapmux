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
			a.HandleOutput(nativeCopilotSessionEvent(t, a.currentNativeSessionID(), "", contracts.CopilotEventSessionCompactionStart, map[string]any{"trigger": "manual"}))
			require.NoError(t, a.CompactContext())
			require.Eventually(t, func() bool { return len(sink.LeapMuxNotifications()) == 1 }, 30*time.Second, time.Millisecond)
			infos := sink.SessionInfos()
			require.NotEmpty(t, infos)
			require.Contains(t, infos[len(infos)-1], "compaction")
			require.Nil(t, infos[len(infos)-1]["compaction"])
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

func TestAutomaticCompactionThatOutlivesTheTurnIsLiveState(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)
	// Trimmed from .tmp/verify-c29/copilot/round2/auto/frames.jsonl.
	a.HandleOutput([]byte(`{"method":"session.event","params":{"sessionId":"session-1","event":{"type":"assistant.turn_start","data":{"turnId":"0"},"ephemeral":true}}}`))
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventSessionCompactionStart, map[string]any{"trigger": "auto"}))
	require.Empty(t, sink.Messages())
	require.Equal(t, []map[string]any{{"compaction": map[string]any{"trigger": "auto"}}}, sink.SessionInfos())
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventSessionIdle, map[string]any{}))
	require.Len(t, sink.SessionInfos(), 1, "the turn end must keep the live compaction")
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventSessionCompactionComplete, map[string]any{"success": true, "tokensRemoved": 40}))
	infos := sink.SessionInfos()
	require.Contains(t, infos[len(infos)-1], "compaction")
	require.Nil(t, infos[len(infos)-1]["compaction"])
	count := 0
	for _, row := range sink.Messages() {
		if _, ok := copilotEventOfType(row.Content, contracts.CopilotEventSessionCompactionComplete); ok {
			count++
		}
	}
	require.Equal(t, 1, count)
}

func TestCompactionLiveStateUsesTheChildSink(t *testing.T) {
	t.Parallel()
	a, root := newNativeCopilotForEvents(t)
	child := &agenttest.Sink{}
	a.children = map[string]*copilotNativeChild{"child": {nativeAgentID: "child", sink: agent.NewProviderServices(child)}}
	a.HandleOutput(nativeCopilotEvent(t, "child", contracts.CopilotEventSessionCompactionStart, map[string]any{}))
	require.Empty(t, child.Messages())
	require.Empty(t, root.SessionInfos())
	require.Equal(t, []map[string]any{{"compaction": map[string]any{}}}, child.SessionInfos())
	a.HandleOutput(nativeCopilotEvent(t, "child", contracts.CopilotEventSessionCompactionComplete, map[string]any{"success": false}))
	require.Len(t, child.Messages(), 1)
	require.Nil(t, child.SessionInfos()[1]["compaction"])
}

func TestCompactionEndsWhenTheSessionIsReplaced(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventSessionCompactionStart, map[string]any{}))
	a.forgetNativeSessionState("new-session")
	infos := sink.SessionInfos()
	require.Len(t, infos, 2)
	require.Contains(t, infos[1], "compaction")
	require.Nil(t, infos[1]["compaction"])
}

type compactionResetSink struct {
	agent.ProviderServices
	entered chan struct{}
	resume  chan struct{}
}

func (s *compactionResetSink) SetTurnState(state agent.TurnState, seq uint64) {
	s.ProviderServices.SetTurnState(state, seq)
	close(s.entered)
	<-s.resume
}

func TestCompactionResetWaitsForTheOutputLock(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)
	observer := &compactionResetSink{ProviderServices: a.sink, entered: make(chan struct{}), resume: make(chan struct{})}
	a.sink = agent.NewProviderServices(observer)
	a.stateMu.Lock()
	a.active = true
	a.stateMu.Unlock()
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventSessionCompactionStart, map[string]any{}))
	a.outputMu.Lock()
	finished := make(chan struct{})
	go func() { a.forgetNativeSessionState("replacement"); close(finished) }()
	<-observer.entered
	if len(sink.SessionInfos()) != 1 {
		t.Error("the live compaction changed before the session reset held the output lock")
	}
	close(observer.resume)
	a.outputMu.Unlock()
	<-finished
	require.Len(t, sink.SessionInfos(), 2)
	require.Nil(t, sink.SessionInfos()[1]["compaction"])
}

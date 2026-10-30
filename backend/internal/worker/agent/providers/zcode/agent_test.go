package zcode

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZCodeProviderSetupFallsBackToAccountConfig(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.catalog = zcodeTestCatalog(t, `{
      "provider": {
        "builtin:zai-coding-plan": {
          "kind": "anthropic", "enabled": true,
          "options": {"apiKey": "plan-key"},
          "models": {"GLM-5.3": {}}
        }
      }
    }`)
	a.registryRevision = "leapmux-test"
	a.builtinProviderConfigPath = filepath.Join(t.TempDir(), "zcode-builtin.json")
	require.NoError(t, os.WriteFile(a.builtinProviderConfigPath, []byte(zcodeBuiltinZAIAccountConfig), 0o600))

	refuseZCodeRequest(t, a, stdin, MethodUpdateProviderRegistry,
		ErrMethodNotFound, "Method not found")
	answerZCodeRequest(t, a, stdin, MethodUpdateAccountConfig,
		`{"receivedRevision":"leapmux-test","providerCount":1,"status":"received"}`)
	answerZCodeRequest(t, a, stdin, MethodProviderRefreshRegistry, `{"refreshed":true}`)

	require.NoError(t, a.pushProviderRegistry(zcodeTestRPCTimeout))
	a.Mu.Lock()
	accountConfig := a.account != nil
	a.Mu.Unlock()
	assert.True(t, accountConfig)
	requests := stdin.Requests(t)
	require.Len(t, requests, 3)
	assert.Equal(t, MethodUpdateProviderRegistry, requests[0].Method)
	assert.Equal(t, MethodUpdateAccountConfig, requests[1].Method)
	assert.Equal(t, MethodProviderRefreshRegistry, requests[2].Method)
	var params zcodeAccountProviderPayload
	require.NoError(t, json.Unmarshal(requests[1].Params, &params))
	assert.Contains(t, params.Providers, "account:zai-individual-coding-plan")
	a.Mu.Lock()
	accountID, bridged := a.catalog.accountProviderID("builtin:zai-coding-plan")
	a.Mu.Unlock()
	assert.True(t, bridged, "an accepted snapshot records the bridge")
	assert.Equal(t, "account:zai-individual-coding-plan", accountID)
}

func TestZCodeProviderSetupRejectsAMismatchedAccountRevision(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.catalog = zcodeTestCatalog(t, `{
      "provider": {
        "builtin:zai-coding-plan": {
          "kind": "anthropic", "enabled": true,
          "options": {"apiKey": "plan-key"},
          "models": {"GLM-5.3": {}}
        }
      }
    }`)
	a.registryRevision = "leapmux-test"
	a.builtinProviderConfigPath = filepath.Join(t.TempDir(), "zcode-builtin.json")
	require.NoError(t, os.WriteFile(a.builtinProviderConfigPath, []byte(zcodeBuiltinZAIAccountConfig), 0o600))

	refuseZCodeRequest(t, a, stdin, MethodUpdateProviderRegistry,
		ErrMethodNotFound, "Method not found")
	answerZCodeRequest(t, a, stdin, MethodUpdateAccountConfig,
		`{"receivedRevision":"another-revision","providerCount":1,"status":"received"}`)

	err := a.pushProviderRegistry(zcodeTestRPCTimeout)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `returned revision "another-revision"`)
	a.Mu.Lock()
	accountConfig := a.account != nil
	a.Mu.Unlock()
	assert.False(t, accountConfig)
	a.Mu.Lock()
	_, bridged := a.catalog.accountProviderID("builtin:zai-coding-plan")
	a.Mu.Unlock()
	assert.False(t, bridged, "a refused snapshot records no bridge")
}

// zcodeStartPlanTestAgent returns an agent whose configuration holds a Z.ai Coding Plan
// and a Z.ai Start Plan, which config.json does not enable. The Start Plan's base URL
// points at baseURL, and the agent checks it through client.
func zcodeStartPlanTestAgent(t *testing.T, stdin *zcodeRecordedStdin, baseURL string, client *http.Client) *Agent {
	t.Helper()
	return zcodeStartPlanTestAgentWithClock(t, stdin, baseURL, client, nil)
}

// zcodeStartPlanTestAgentWithClock is zcodeStartPlanTestAgent with the agent's timers on
// clock.
func zcodeStartPlanTestAgentWithClock(t *testing.T, stdin *zcodeRecordedStdin, baseURL string, client *http.Client, clock quartz.Clock) *Agent {
	t.Helper()
	a := newZCodeTestAgentWithClock(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin, clock)
	a.catalog = zcodeTestCatalog(t, fmt.Sprintf(`{
      "provider": {
        "builtin:zai-coding-plan": {"kind": "anthropic", "enabled": true, "options": {"apiKey": "plan-key"}, "models": {"GLM-5.3": {}}},
        "builtin:zai-start-plan": {"kind": "anthropic", "enabled": false, "options": {"apiKey": "start-key", "baseURL": %q}, "models": {"GLM-5.3-Flash": {}}}
      }
    }`, baseURL+"/api/v1/zcode-plan/anthropic"))
	a.catalog.accountSelection = zcodeAccountSelection{domain: "zai", planKinds: map[string]string{"zai": "individual-coding-plan"}}
	a.registryRevision = "leapmux-test"
	a.identity = zcodeClientIdentity{appVersion: "3.16.3", deviceMid: "11111111-2222-4333-8444-555555555555"}
	a.startPlanHTTP = client
	a.builtinProviderConfigPath = filepath.Join(t.TempDir(), "zcode-builtin.json")
	require.NoError(t, os.WriteFile(a.builtinProviderConfigPath, []byte(zcodeBuiltinZAIAccountConfig), 0o600))
	return a
}

// A Start Plan that config.json does not enable is still a plan the account holds. The
// account snapshot reports the models that the billing API grants it.
func TestZCodeProviderSetupReportsTheCheckedStartPlan(t *testing.T) {
	t.Parallel()

	server, client := zcodeTestStartPlanServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer start-key", r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, zcodeLiveBalanceReply)
	})
	stdin := &zcodeRecordedStdin{}
	a := zcodeStartPlanTestAgent(t, stdin, server.URL, client)
	refuseZCodeRequest(t, a, stdin, MethodUpdateProviderRegistry, ErrMethodNotFound, "Method not found")
	answerZCodeRequest(t, a, stdin, MethodUpdateAccountConfig,
		`{"receivedRevision":"leapmux-test","providerCount":2,"status":"received"}`)
	answerZCodeRequest(t, a, stdin, MethodProviderRefreshRegistry, `{"refreshed":true}`)

	require.NoError(t, a.pushProviderRegistry(zcodeTestRPCTimeout))
	requests := stdin.Requests(t)
	require.Len(t, requests, 3)
	var params zcodeAccountProviderPayload
	require.NoError(t, json.Unmarshal(requests[1].Params, &params))
	start := params.Providers["account:zai-start-plan"]
	assert.True(t, start.Access.Entitled)
	assert.Equal(t, []string{"GLM-5.3-Flash", "GLM-5.3"}, start.BuiltinModelIDs,
		"the snapshot carries the models that the plan grants, in the release's spelling")
	assert.Equal(t, zcodeAccountProviderState{Availability: zcodeAvailabilityAvailable, Entitled: true, Current: true},
		params.States["account:zai-start-plan"])
	assert.Equal(t, zcodeAccountProviderState{Availability: zcodeAvailabilityAvailable, Entitled: true, Current: true},
		params.States["account:zai-individual-coding-plan"])
}

// A refused credential means that a model request with the same key fails too, so the
// snapshot offers none of the plan's models.
func TestZCodeProviderSetupReportsARefusedStartPlanCredential(t *testing.T) {
	t.Parallel()

	server, client := zcodeTestStartPlanServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	stdin := &zcodeRecordedStdin{}
	a := zcodeStartPlanTestAgent(t, stdin, server.URL, client)
	refuseZCodeRequest(t, a, stdin, MethodUpdateProviderRegistry, ErrMethodNotFound, "Method not found")
	answerZCodeRequest(t, a, stdin, MethodUpdateAccountConfig,
		`{"receivedRevision":"leapmux-test","providerCount":2,"status":"received"}`)
	answerZCodeRequest(t, a, stdin, MethodProviderRefreshRegistry, `{"refreshed":true}`)

	require.NoError(t, a.pushProviderRegistry(zcodeTestRPCTimeout))
	var params zcodeAccountProviderPayload
	require.NoError(t, json.Unmarshal(stdin.Requests(t)[1].Params, &params))
	assert.False(t, params.Providers["account:zai-start-plan"].Access.Entitled)
	assert.Nil(t, params.Providers["account:zai-start-plan"].BuiltinModelIDs)
	assert.Equal(t, zcodeAccountProviderState{
		Availability: zcodeAvailabilityUnavailable, Current: true, UnavailableReason: zcodeUnavailableCredentialFailed,
	}, params.States["account:zai-start-plan"])
}

// A legacy build takes the workspace registry and never reads the account snapshot, so
// the Start Plan check that runs beside the legacy request must end.
func TestZCodeProviderSetupLegacyBuildStopsTheStartPlanCheck(t *testing.T) {
	t.Parallel()

	reached := make(chan struct{}, 1)
	ended := make(chan struct{}, 1)
	server, client := zcodeTestStartPlanServer(t, func(_ http.ResponseWriter, r *http.Request) {
		reached <- struct{}{}
		<-r.Context().Done()
		ended <- struct{}{}
	})
	stdin := &zcodeRecordedStdin{}
	a := zcodeStartPlanTestAgent(t, stdin, server.URL, client)
	// The legacy reply waits until the check's request is in flight, so the test
	// observes the stop end that request rather than a request that never started.
	go func() {
		<-reached
		req := waitZCodeRequest(t, stdin, MethodUpdateProviderRegistry)
		a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, req), json.RawMessage(`{"status":"applied","providerCount":2}`)))
	}()

	require.NoError(t, a.pushProviderRegistry(zcodeTestRPCTimeout))
	select {
	case <-ended:
	case <-time.After(30 * time.Second):
		t.Fatal("the Start Plan check kept running after the legacy registry was applied")
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.False(t, a.account != nil)
}

func TestZCodeProviderSetupOverlapsCurrentAccountCheckWithProtocolDetection(t *testing.T) {
	t.Parallel()
	reached := make(chan struct{}, 1)
	server, client := zcodeTestStartPlanServer(t, func(w http.ResponseWriter, _ *http.Request) {
		reached <- struct{}{}
		_, _ = io.WriteString(w, zcodeLiveBalanceReply)
	})
	stdin := &zcodeRecordedStdin{}
	a := zcodeStartPlanTestAgent(t, stdin, server.URL, client)
	go func() {
		select {
		case <-reached:
			req := waitZCodeRequest(t, stdin, MethodUpdateProviderRegistry)
			a.HandleOutput(zcodeErrorReplyLine(t, zcodeSentRequestID(t, req), ErrMethodNotFound, "Method not found"))
		case <-a.Context().Done():
		}
	}()
	answerZCodeRequest(t, a, stdin, MethodUpdateAccountConfig, `{"receivedRevision":"leapmux-test","status":"received"}`)
	answerZCodeRequest(t, a, stdin, MethodProviderRefreshRegistry, `{"refreshed":true}`)
	require.NoError(t, a.pushProviderRegistry(zcodeTestRPCTimeout))
	var payload zcodeAccountProviderPayload
	require.NoError(t, json.Unmarshal(zcodeNthRequest(t, stdin, MethodUpdateAccountConfig, 1).Params, &payload))
	assert.True(t, payload.Providers["account:zai-start-plan"].Access.Entitled)
	assert.Equal(t, []string{"GLM-5.3-Flash", "GLM-5.3"}, payload.Providers["account:zai-start-plan"].BuiltinModelIDs)
}

// An installed release that LeapMux cannot read fails the account snapshot only, and
// LeapMux sends no credential over the network for a start that must fail.
func TestZCodeProviderSetupAnUnreadableReleaseFailsOnlyTheAccountPath(t *testing.T) {
	t.Parallel()

	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%v", legacy), func(t *testing.T) {
			t.Parallel()

			stdin := &zcodeRecordedStdin{}
			a := zcodeStartPlanTestAgent(t, stdin, "https://zcode.invalid", newZCodeStartPlanHTTPClient(zcodeOfflineTransport{t: t}))
			a.builtinProviderConfigPath = filepath.Join(t.TempDir(), "missing.json")
			if legacy {
				answerZCodeRequest(t, a, stdin, MethodUpdateProviderRegistry, `{"status":"applied","providerCount":2}`)
				require.NoError(t, a.pushProviderRegistry(zcodeTestRPCTimeout))
				return
			}
			refuseZCodeRequest(t, a, stdin, MethodUpdateProviderRegistry, ErrMethodNotFound, "Method not found")
			err := a.pushProviderRegistry(zcodeTestRPCTimeout)
			require.ErrorContains(t, err, "read ZCode built-in provider configuration")
			assert.Len(t, stdin.Requests(t), 1, "no account snapshot is sent")
		})
	}
}

func TestZCodeLaunchEnvValueTakesTheFinalDuplicate(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "/new", zcodeLaunchEnvValue([]string{
		"PATH=/bin",
		zcodeBuiltinProviderConfigEnv + "=/old",
		zcodeBuiltinProviderConfigEnv + "=/new",
	}, zcodeBuiltinProviderConfigEnv))
}

func TestZCodeInterrupt_IdleIsANoop(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)

	require.NoError(t, a.Interrupt(agent.StopContext{}))
	assert.Empty(t, stdin.Frames(), "Interrupt with no active turn must not write")
}

func TestZCodeInterrupt_SendsSessionStop(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	a.Mu.Lock()
	a.turnActive = true
	a.Mu.Unlock()
	// Nothing answers this stdin, so the call returns a context error. The FRAME
	// is what this test is about.
	a.CancelForTest()

	_ = a.Interrupt(agent.StopContext{})

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	assert.Equal(t, MethodSessionStop, requests[0].Method)
	var params map[string]any
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	assert.Equal(t, "sess-1", params["sessionId"])
}

// The app-server accepts session/stop and then announces nothing: no turn.completed,
// no turn.failed. A live interrupt therefore left the turn active for the rest of the
// session -- the thinking indicator ran without end, the input queue could never
// drain, and the unfinished model output was never stored.
//
// The silence is what ends it now. An accepted stop alone does not, because the
// app-server accepts one for a turn that goes on working as well (RL-016).
func TestZCodeInterrupt_EndsTheTurnTheAppServerNeverAnnounces(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	// The silent seed keeps the progress list empty, so the model reset the
	// settlement emits is the only one the assertion can see.
	zcodeSeedTestTurn(t, a, false)
	a.Mu.Lock()
	a.turns[a.foregroundTurnID].contentByScope["silent-stream"] = &zcodeScopeContent{observed: []byte("partial")}
	a.turnActive = true
	a.Mu.Unlock()

	// The app-server's own answer to a stop: an empty object, and no event after it.
	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.NotNil(t, timer.fire, "the stop arms the silence window")
	timer.fire()

	a.Mu.Lock()
	turnActive := a.turnActive
	a.Mu.Unlock()
	assert.False(t, turnActive, "an app-server that says nothing more ends the turn")

	reset := false
	for _, update := range sink.ProgressUpdates() {
		if update.Operation == agent.ProgressModelReset {
			reset = true
		}
	}
	assert.True(t, reset, "the thinking indicator stops with the turn")
}

// The abort reaches the model stream, not a command the runtime already launched: an
// interrupted `sleep 90` ran its full ninety seconds and then reported success. A row
// that claimed the call was interrupted would state an outcome the runtime contradicts.
func TestZCodeInterrupt_LeavesARunningToolCallOpen(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	a.Mu.Lock()
	a.turnActive = true
	a.toolInvocations["call-1"] = &zcodeToolInvocation{spanID: "call-1", nativeCallID: "call-1", name: "bash", lastFrame: []byte(`{"toolCallId":"call-1"}`)}
	zcodeOwnTestToolLocked(t, a, "call-1")
	a.Mu.Unlock()

	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt(agent.StopContext{}))

	assert.Empty(t, sink.Messages(), "the call keeps its running card until its own update arrives")
	a.Mu.Lock()
	_, stillOpen := a.toolInvocations["call-1"]
	a.Mu.Unlock()
	assert.True(t, stillOpen, "the call is still the runtime's to finish")
}

// A stop the app-server REFUSES leaves the turn alone: the turn is still running, and
// reporting it finished would hide a live agent behind an idle chat.
func TestZCodeInterrupt_ARefusedStopLeavesTheTurnRunning(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	a.Mu.Lock()
	a.turnActive = true
	a.Mu.Unlock()

	refuseZCodeRequest(t, a, stdin, MethodSessionStop, -32000, "the session is busy")
	require.Error(t, a.Interrupt(agent.StopContext{}))

	a.Mu.Lock()
	turnActive := a.turnActive
	a.Mu.Unlock()
	assert.True(t, turnActive, "a refused stop stopped nothing")
}

func TestZCodeInterrupt_StoppedAgentReturnsError(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	a.SetStoppedForTest(true)
	a.Mu.Lock()
	a.turnActive = true
	a.Mu.Unlock()

	err := a.Interrupt(agent.StopContext{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopped")
	assert.Empty(t, stdin.Frames())
}

func TestZCodeInterrupt_NoSessionIsANoop(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	a.Mu.Lock()
	a.turnActive = true
	a.sessionID = ""
	a.Mu.Unlock()

	require.NoError(t, a.Interrupt(agent.StopContext{}))
	assert.Empty(t, stdin.Frames())
}

// "unknown" is the app-server's placeholder before a session exists. Adopting it
// would make later RPCs send a sessionId the app-server rejects.
func TestSettleZCodeSettingsSnapshot_RejectsTheUnknownSessionPlaceholder(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	_, _, err := a.settleZCodeSettingsSnapshot(zcodeSettingsRequestIdentityForTest(a), nil, json.RawMessage(`{"session":{"sessionId":"unknown"},"runtime":{"eventSeq":3}}`))
	require.ErrorIs(t, err, errNoZCodeSession)

	a.Mu.Lock()
	sessionID, seq := a.sessionID, a.lastSeq
	a.Mu.Unlock()
	assert.Equal(t, "sess-1", sessionID)
	assert.Zero(t, seq, "an incremental snapshot cannot advance the event cursor")
}

func TestSettleZCodeSettingsSnapshot_EmptyIsANoop(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	_, _, err := a.settleZCodeSettingsSnapshot(zcodeSettingsRequestIdentityForTest(a), nil, nil)
	require.NoError(t, err)
	_, _, err = a.settleZCodeSettingsSnapshot(zcodeSettingsRequestIdentityForTest(a), nil, json.RawMessage(``))
	require.NoError(t, err)
	a.Mu.Lock()
	sessionID := a.sessionID
	a.Mu.Unlock()
	assert.Equal(t, "sess-1", sessionID)
}

// --- opening a session: the resume handle, and what happens when it does not hold ---

func TestZCodeOpenSession_ResumeAdoptsTheSessionAndSkipsCreate(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	// The real startup state: nothing is open yet, so a session id left over from the
	// helper would hide a resume that adopted nothing.
	a.Mu.Lock()
	a.sessionID = ""
	a.Mu.Unlock()

	answerZCodeRequest(t, a, stdin, MethodSessionResume,
		`{"session":{"sessionId":"sess-old"},"runtime":{"eventSeq":42},"projection":{"contextWindow":200000}}`)

	require.NoError(t, a.openSession("sess-old", zcodeTestRPCTimeout))

	requests := stdin.Requests(t)
	require.Len(t, requests, 1, "a resume that holds must not also create a session")
	assert.Equal(t, MethodSessionResume, requests[0].Method)
	var params struct {
		SessionID    string         `json:"sessionId"`
		Workspace    zcodeWorkspace `json:"workspace"`
		ToolDenylist []string       `json:"toolDenylist"`
	}
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	assert.Equal(t, "sess-old", params.SessionID)
	assert.Equal(t, a.workspace, params.Workspace, "the app-server indexes a session by workspace")
	zcodeAssertToolDenylist(t, params.ToolDenylist)

	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, "sess-old", a.sessionID)
	assert.Equal(t, int64(42), a.lastSeq)
	assert.Equal(t, int64(200000), a.contextWindow)
}

// A resumed session must not replay its history: LeapMux persisted that transcript
// already, and every replayed row would be written a second time.
func TestZCodeOpenSession_ResumedSessionSubscribesAfterTheSnapshotSequence(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.Mu.Lock()
	a.sessionID = ""
	a.Mu.Unlock()

	answerZCodeRequest(t, a, stdin, MethodSessionResume,
		`{"session":{"sessionId":"sess-old"},"runtime":{"eventSeq":42}}`)
	answerZCodeRequest(t, a, stdin, MethodSessionSubscribe, `{"sessionId":"sess-old","eventSeq":42,"events":[]}`)

	require.NoError(t, a.openSession("sess-old", zcodeTestRPCTimeout))
	require.NoError(t, a.subscribe(zcodeTestRPCTimeout))

	var params struct {
		SessionID       string `json:"sessionId"`
		DeliveryKind    string `json:"deliveryKind"`
		IncludeSnapshot bool   `json:"includeSnapshot"`
		AfterSeq        int64  `json:"afterSeq"`
	}
	sub := waitZCodeRequest(t, stdin, MethodSessionSubscribe)
	require.NoError(t, json.Unmarshal(sub.Params, &params))
	assert.Equal(t, "sess-old", params.SessionID)
	assert.Equal(t, DeliveryContinuous, params.DeliveryKind)
	assert.False(t, params.IncludeSnapshot, "a snapshot is O(messages) and is the main cause of a subscribe timeout")
	assert.Equal(t, int64(42), params.AfterSeq, "the resumed transcript must not be replayed")
}

// A resume the app-server refuses fails the whole start.
//
// Creating a fresh session instead discards the conversation that the user asked
// to continue: the tab comes up with no history, and the only record is a log
// line on the worker that nobody reads. The stored handle also survives a
// visible failure, so a resume that fails on a condition that passes succeeds on
// the next start.
func TestZCodeOpenSession_RefusedResumeFailsTheStart(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.Mu.Lock()
	a.sessionID = ""
	a.Mu.Unlock()

	refuseZCodeRequest(t, a, stdin, MethodSessionResume, ErrSessionNotActive, "no such session")

	err := a.openSession("sess-gone", zcodeTestRPCTimeout)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sess-gone", "the handle that failed is what the user has to replace")
	assert.Contains(t, err.Error(), "/clear", "the failure must state the command that recovers the tab")

	requests := stdin.Requests(t)
	require.Len(t, requests, 1, "a refused resume must not open a session behind the user's back")
	assert.Equal(t, MethodSessionResume, requests[0].Method)

	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Empty(t, a.sessionID, "no session was opened, so the agent adopts none")
}

// A resume that answers with no usable session id fails too, and abandons that
// document WHOLE.
//
// "unknown" is the app-server's placeholder for "no session exists", so this reply
// describes no session -- but it still carries a sequence, a mode and a context
// window. None of them may reach the agent: the app-server numbers protocol events
// per session from one, so a carried sequence becomes a watermark that makes
// dispatchZCodeEvent drop later events as duplicates, and `yolo` is a mode that
// approves every tool call by itself.
func TestZCodeOpenSession_ResumeWithNoUsableSessionIDFailsTheStart(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.Mu.Lock()
	a.sessionID = ""
	a.mode = contracts.ZCodeDefaultMode
	a.Mu.Unlock()

	answerZCodeRequest(t, a, stdin, MethodSessionResume,
		`{"session":{"sessionId":"unknown"},"runtime":{"eventSeq":9},`+
			`"settings":{"mode":{"current":"yolo"}},"projection":{"contextWindow":123456}}`)

	err := a.openSession("sess-gone", zcodeTestRPCTimeout)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sess-gone")
	assert.Contains(t, err.Error(), "/clear")

	requests := stdin.Requests(t)
	require.Len(t, requests, 1, "a resume that adopts nothing must not open a session instead")

	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Empty(t, a.sessionID)
	assert.Zero(t, a.lastSeq, "the abandoned sequence must not become a watermark")
	assert.Equal(t, contracts.ZCodeDefaultMode, a.mode, "the abandoned session's mode must not survive")
	assert.Zero(t, a.contextWindow, "the abandoned session's context window must not label this agent's usage")
}

func TestZCodeOpenSession_WithoutAHandleGoesStraightToCreate(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)
	a.Mu.Lock()
	a.sessionID = ""
	a.model = "builtin:zai/GLM-5.3"
	a.mode = contracts.ZCodeModePlan
	a.Mu.Unlock()

	answerZCodeRequest(t, a, stdin, MethodSessionCreate,
		`{"session":{"sessionId":"sess-new"},"runtime":{"eventSeq":0}}`)

	require.NoError(t, a.openSession("", zcodeTestRPCTimeout))

	requests := stdin.Requests(t)
	require.Len(t, requests, 1, "an empty handle must not reach session/resume")
	assert.Equal(t, MethodSessionCreate, requests[0].Method)
	var params struct {
		Workspace    zcodeWorkspace `json:"workspace"`
		Mode         string         `json:"mode"`
		Model        zcodeModelRef  `json:"model"`
		ToolDenylist []string       `json:"toolDenylist"`
	}
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	assert.Equal(t, a.workspace, params.Workspace)
	assert.Equal(t, contracts.ZCodeModePlan, params.Mode)
	assert.Equal(t, zcodeModelRef{ProviderID: "builtin:zai", ModelID: "GLM-5.3"}, params.Model)
	zcodeAssertToolDenylist(t, params.ToolDenylist)

	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, "sess-new", a.sessionID)
}

func TestZCodeOpenSession_AccountConfigEnablesDynamicWorkflows(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.catalog = zcodeTestCatalog(t, `{
      "provider": {
        "builtin:zai-coding-plan": {
          "kind": "anthropic", "enabled": true,
          "options": {"apiKey": "plan-key"},
          "models": {"GLM-5.3": {}}
        }
      }
    }`)
	a.Mu.Lock()
	a.account = &zcodeAccountState{}
	a.sessionID = ""
	a.model = "builtin:zai-coding-plan/GLM-5.3"
	a.Mu.Unlock()

	answerZCodeRequest(t, a, stdin, MethodSessionCreate,
		`{"session":{"sessionId":"sess-new"},"runtime":{"eventSeq":0}}`)
	require.NoError(t, a.openSession("", zcodeTestRPCTimeout))

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	var params map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	var dynamicWorkflowEnabled bool
	require.NoError(t, json.Unmarshal(params["dynamicWorkflowEnabled"], &dynamicWorkflowEnabled))
	assert.True(t, dynamicWorkflowEnabled)
	assert.NotContains(t, params, "model", "the live account snapshot decides whether the provider is an individual or a team plan")
}

func TestZCodeResumeSession_AccountConfigEnablesDynamicWorkflows(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.Mu.Lock()
	a.account = &zcodeAccountState{}
	a.Mu.Unlock()

	answerZCodeRequest(t, a, stdin, MethodSessionResume,
		`{"session":{"sessionId":"sess-existing"},"runtime":{"eventSeq":4}}`)
	require.NoError(t, a.openSession("sess-existing", zcodeTestRPCTimeout))

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	var params map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	var enabled bool
	require.NoError(t, json.Unmarshal(params["dynamicWorkflowEnabled"], &enabled))
	assert.True(t, enabled)
}

// A create that produces no session is fatal, unlike a resume that fails: Start
// tears the process down on it, because every later RPC carries the session id.
func TestZCodeOpenSession_ReportsACreateThatProducesNoSession(t *testing.T) {
	t.Parallel()

	t.Run("the app-server refuses", func(t *testing.T) {
		t.Parallel()
		stdin := &zcodeRecordedStdin{}
		a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
		a.Mu.Lock()
		a.sessionID = ""
		a.Mu.Unlock()

		refuseZCodeRequest(t, a, stdin, MethodSessionCreate, ErrInternal, "the store is unreadable")

		err := a.openSession("", zcodeTestRPCTimeout)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "the store is unreadable", "the app-server's reason is the only diagnostic there is")
	})

	t.Run("the reply carries no session id", func(t *testing.T) {
		t.Parallel()
		stdin := &zcodeRecordedStdin{}
		a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
		a.Mu.Lock()
		a.sessionID = ""
		a.Mu.Unlock()

		answerZCodeRequest(t, a, stdin, MethodSessionCreate, `{"runtime":{"eventSeq":0}}`)

		err := a.openSession("", zcodeTestRPCTimeout)
		require.Error(t, err)
		assert.Contains(t, err.Error(), MethodSessionCreate)
	})
}

func TestZCodeClearContext_StopsThenClosesTheRetiredSession(t *testing.T) {
	t.Parallel()
	for _, subscribeFails := range []bool{false, true} {
		stdin := &zcodeRecordedStdin{}
		a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
		result := make(chan error, 1)
		go func() { _, err := a.ClearContext(); result <- err }()
		create := waitZCodeRequest(t, stdin, MethodSessionCreate)
		a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, create), json.RawMessage(`{"session":{"sessionId":"new"},"settings":{"mode":{"current":"build"}}}`)))
		subscribe := waitZCodeRequest(t, stdin, MethodSessionSubscribe)
		if subscribeFails {
			a.HandleOutput(zcodeErrorReplyLine(t, zcodeSentRequestID(t, subscribe), ErrInternal, "subscribe failed"))
		} else {
			a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, subscribe), json.RawMessage(`{"sessionId":"new","eventSeq":0,"events":[]}`)))
		}
		if subscribeFails {
			assert.Error(t, <-result)
		} else {
			require.NoError(t, <-result)
		}
		stop := waitZCodeRequest(t, stdin, MethodSessionStop)
		assert.JSONEq(t, `{"sessionId":"sess-1"}`, string(stop.Params))
		assert.Len(t, stdin.Requests(t), 3, "close must wait for the stop reply")
		a.HandleOutput(zcodeErrorReplyLine(t, zcodeSentRequestID(t, stop), ErrSessionNotActive, "session is gone"))
		closeRequest := waitZCodeRequest(t, stdin, "session/close")
		assert.JSONEq(t, `{"sessionId":"sess-1"}`, string(closeRequest.Params))
		a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, closeRequest), json.RawMessage(`{}`)))
	}
}

func TestZCodeClearContext_EndsALiveCompaction(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated,
		`{"operationId":"c1","status":"started","trigger":"auto"}`))
	answerZCodeRequest(t, a, stdin, MethodSessionCreate, `{"session":{"sessionId":"new"},"settings":{"mode":{"current":"build"}}}`)
	answerZCodeRequest(t, a, stdin, MethodSessionSubscribe, `{"sessionId":"new","eventSeq":0,"events":[]}`)
	_, err := a.ClearContext()
	require.NoError(t, err)
	assert.Equal(t, []any{map[string]any{"trigger": "auto"}, nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
}

func TestZCodeClearContext_OpensAFreshSessionAndDropsPerSessionState(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	a.Mu.Lock()
	a.turnActive = true
	a.compactionSessionID = "sess-1"
	a.lastSeq = 99
	a.toolInvocations["call-1"] = &zcodeToolInvocation{spanID: "call-1", nativeCallID: "call-1", name: "Bash", input: json.RawMessage(`{"command":"ls"}`), final: true}
	a.pendingControls["req-1"] = newPendingControl(json.RawMessage(`{"type":"control_request"}`))
	a.latestContextUsage = map[string]any{"context_tokens": int64(12)}
	a.lastGoalUpdate = agent.GoalUpdate{NativeID: "g1", Objective: "Ship it", Status: agent.GoalStatusActive}
	a.Mu.Unlock()
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	zcodeStartTestTurn(t, a, `{"turnNumber":2,"inputSource":"background_task"}`)
	a.children.rememberChild("spawn-1", "sub-1", "child-1")
	a.children.rememberTitle("spawn-2", "a title")

	go func() {
		create := waitZCodeRequest(t, stdin, MethodSessionCreate)
		// Every state document carries the settings, and session/create honors the mode
		// it was given -- so the fresh session already runs in the mode the cleared one
		// did, and no session/setMode follows.
		a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, create), json.RawMessage(
			`{"session":{"sessionId":"sess-fresh"},"runtime":{"eventSeq":0},`+
				`"settings":{"mode":{"current":"build"}}}`)))
		sub := waitZCodeRequest(t, stdin, MethodSessionSubscribe)
		a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, sub), json.RawMessage(`{"sessionId":"sess-fresh","eventSeq":0,"events":[]}`)))
	}()

	sessionID, clearErr := a.ClearContext()
	require.NoError(t, clearErr)
	assert.Equal(t, "sess-fresh", sessionID)
	for _, req := range stdin.Requests(t) {
		assert.NotEqual(t, MethodSetMode, req.Method, "session/create already opened the session in that mode")
	}

	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.False(t, a.turnActive)
	assert.Empty(t, a.compactionSessionID)
	assert.Empty(t, a.turns, "the replaced session's turn owners go with it")
	assert.Empty(t, a.foregroundTurnID)
	assert.Empty(t, a.toolInvocations, "one record per call, so a session replace drops every fact about it")
	assert.Empty(t, a.pendingControls)
	assert.Nil(t, a.latestContextUsage)
	// The goal goes with the session. A goal check of the replaced session must
	// not mark the cleared goal on the card of the fresh one.
	assert.Zero(t, a.lastGoalUpdate)
	// The subagent index goes with the session too. A tool-call id that the replaced
	// session was still running never reaches a result, so an entry left here would
	// route the next session's row into a transcript that belongs to a conversation
	// the user cleared.
	_, hasChild := a.children.child("spawn-1")
	assert.False(t, hasChild)
	_, hasTool := a.children.toolChild("sub-1")
	assert.False(t, hasTool)
	assert.Empty(t, a.children.title("spawn-2"))
}

// A context clear opens the fresh session in the mode the user runs in. ZCode keeps
// plan mode in a flag beside the native mode, so a session in plan mode reads `build`
// in settings.mode.current. The create request must still ask for plan, or the clear
// drops the user out of plan mode.
func TestZCodeClearContext_KeepsPlanMode(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	// ZCode's EnterPlanMode tool turned the plan flag on and kept the native mode.
	a.HandleOutput(zcodeModeChangedLine(t, 1, zcodePlanEntered))

	go func() {
		create := waitZCodeRequest(t, stdin, MethodSessionCreate)
		// session/create folds `mode:"plan"` into the flag, so its reply reads build.
		a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, create), json.RawMessage(
			`{"session":{"sessionId":"sess-fresh"},"runtime":{"eventSeq":0},`+
				`"settings":{"mode":{"current":"build"}}}`)))
		sub := waitZCodeRequest(t, stdin, MethodSessionSubscribe)
		a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, sub), json.RawMessage(`{"sessionId":"sess-fresh","eventSeq":0,"events":[]}`)))
	}()

	_, clearErr := a.ClearContext()
	require.NoError(t, clearErr)

	creates := zcodeRequestsFor(t, stdin, MethodSessionCreate)
	require.Len(t, creates, 1)
	var params struct {
		Mode string `json:"mode"`
	}
	require.NoError(t, json.Unmarshal(creates[0].Params, &params))
	assert.Equal(t, contracts.ZCodeModePlan, params.Mode)
	assert.Empty(t, zcodeRequestsFor(t, stdin, MethodSetMode),
		"session/create already opened the fresh session in plan mode")
	assert.Equal(t, contracts.ZCodeModePlan, zcodePermissionMode(t, a))
}

func TestZCodeClearContextKeepsTheCurrentSessionWhenCreationFails(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.Mu.Lock()
	a.lastSeq = 42
	a.stateRevision = 17
	a.modeObserved = true
	a.Mu.Unlock()
	go func() {
		request := waitZCodeRequest(t, stdin, MethodSessionCreate)
		a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{}`)))
	}()
	_, clearErr := a.ClearContext()
	assert.Error(t, clearErr)
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, "sess-1", a.sessionID)
	assert.Equal(t, int64(42), a.lastSeq)
	assert.Equal(t, int64(17), a.stateRevision)
	assert.True(t, a.modeObserved)
}

// A subscription reply must be the full {sessionId, eventSeq, events} document
// for THIS session. A reply that does not prove which session and sequence it
// covers must not dispatch anything or move the cursor: a malformed receipt
// used to log and succeed.
func TestZCodeSubscribe_RequiresAFullSubscribeReceipt(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, result string }{
		{"a result that is not an object", `"subscribed"`},
		{"a null result", `null`},
		{"an empty object", `{}`},
		{"a missing session id", `{"eventSeq":0,"events":[]}`},
		{"a missing sequence", `{"sessionId":"sess-1","events":[]}`},
		{"a missing event list", `{"sessionId":"sess-1","eventSeq":0}`},
		{"a null event list", `{"sessionId":"sess-1","eventSeq":0,"events":null}`},
		{"an event list that is not an array", `{"sessionId":"sess-1","eventSeq":0,"events":{}}`},
		{"an event of the wrong type", `{"sessionId":"sess-1","eventSeq":0,"events":["x"]}`},
		{"a negative sequence", `{"sessionId":"sess-1","eventSeq":-1,"events":[]}`},
		{"a sequence of the wrong type", `{"sessionId":"sess-1","eventSeq":"0","events":[]}`},
		{"a session id of the wrong type", `{"sessionId":5,"eventSeq":0,"events":[]}`},
		{"the unknown session placeholder", `{"sessionId":"unknown","eventSeq":0,"events":[]}`},
		{"another session's receipt", `{"sessionId":"sess-other","eventSeq":5,"events":[` +
			`{"eventId":"e1","sessionId":"sess-other","seq":1,"type":"session.titleUpdated","payload":{}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stdin := &zcodeRecordedStdin{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
			answerZCodeRequest(t, a, stdin, MethodSessionSubscribe, tc.result)
			err := a.subscribe(zcodeTestRPCTimeout)
			require.Error(t, err)
			a.Mu.Lock()
			defer a.Mu.Unlock()
			assert.Zero(t, a.lastSeq, "a rejected receipt must advance no cursor")
		})
	}
}

// The required receipt shape is accepted with or without the optional snapshot.
func TestZCodeSubscribe_AcceptsAValidReceipt(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, result string }{
		{"a full receipt", `{"sessionId":"sess-1","eventSeq":0,"events":[]}`},
		{"a receipt with an optional snapshot", `{"sessionId":"sess-1","eventSeq":0,"events":[],` +
			`"snapshot":{"session":{"sessionId":"sess-1"},"runtime":{"eventSeq":0}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stdin := &zcodeRecordedStdin{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
			answerZCodeRequest(t, a, stdin, MethodSessionSubscribe, tc.result)
			require.NoError(t, a.subscribe(zcodeTestRPCTimeout))
		})
	}
}

// A context clear that cannot open its subscription fails the call: the user
// must see that the fresh session is not streaming events yet.
func TestZCodeClearContext_ReportsAnInvalidSubscribeReceipt(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	answerZCodeRequest(t, a, stdin, MethodSessionCreate, `{"session":{"sessionId":"sess-fresh"},"runtime":{"eventSeq":0},"settings":{"mode":{"current":"build"}}}`)
	answerZCodeRequest(t, a, stdin, MethodSessionSubscribe, `{"eventSeq":0}`)
	_, err := a.ClearContext()
	require.Error(t, err)
	assert.Contains(t, err.Error(), MethodSessionSubscribe)
}

func TestZCodeClearContext_PreservesOutputFromTheReplacementDuringSettings(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.thoughtLevel = "high"
	zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
		switch request.Method {
		case MethodSessionCreate:
			return json.RawMessage(`{"session":{"sessionId":"replacement"},"settings":{"mode":{"current":"build"}}}`)
		case MethodSetThoughtLevel:
			a.HandleOutput(zcodeTurnEventLineForSession(t, "replacement", 1, "new-turn", contracts.ZCodeEventTurnStarted,
				`{"turnNumber":1}`))
			a.HandleOutput(zcodeTurnEventLineForSession(t, "replacement", 2, "new-turn", contracts.ZCodeEventModelStreaming,
				`{"kind":"text_delta","assistantMessageId":"new-message","delta":"replacement output"}`))
			return json.RawMessage(`{"settings":{"thoughtLevel":{"enabled":true,"current":"high","available":[{"value":"high"}]}}}`)
		case MethodSessionSubscribe:
			return json.RawMessage(`{"sessionId":"replacement","eventSeq":2,"events":[]}`)
		case MethodSessionStop, MethodSessionClose:
			return json.RawMessage(`{}`)
		default:
			return nil
		}
	})
	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	assert.Equal(t, "replacement", sessionID)
	assert.True(t, a.PublishTurnActive().Active)
	assert.Equal(t, len("replacement output"), a.generationBuffer.RetainedTextLenForTest(zcodeTextScope("new-message")))
	assert.Zero(t, sink.MessageCount(), "context replacement cannot flush the new turn as interrupted")
}

func TestZCodeClearContext_RetainsCapturedOutputAfterAStorageFailure(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{PersistErr: errors.New("storage unavailable")}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.sink.UpdateSessionID("sess-1")
	a.HandleOutput(zcodeTurnEventLine(t, 1, "old", contracts.ZCodeEventTurnStarted, `{"turnNumber":1}`))
	a.HandleOutput(zcodeTurnEventLine(t, 2, "old", contracts.ZCodeEventModelStreaming,
		`{"kind":"text_delta","assistantMessageId":"old-message","delta":"retained old output"}`))
	zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
		switch request.Method {
		case MethodSessionCreate:
			return json.RawMessage(`{"session":{"sessionId":"replacement"},"settings":{"mode":{"current":"build"}}}`)
		case MethodSessionSubscribe:
			return json.RawMessage(`{"sessionId":"replacement","eventSeq":0,"events":[]}`)
		case MethodSessionStop, MethodSessionClose:
			return json.RawMessage(`{}`)
		default:
			return nil
		}
	})
	_, err := a.ClearContext()
	require.NoError(t, err)
	assert.Equal(t, len("retained old output"), a.generationBuffer.RetainedTextLenForTest(zcodeTextScope("old-message")))
	attempts := sink.MessageCount()
	sink.PersistErr = nil
	require.NoError(t, a.sink.OutputQueue().Flush())
	require.Greater(t, sink.MessageCount(), attempts, "restored storage must retry the retained row")
	row := sink.Messages()[sink.MessageCount()-1]
	_, text, completion, ok := agenttest.DecodeAssembledMessage(row.Content)
	require.True(t, ok)
	assert.Equal(t, "retained old output", text)
	assert.Equal(t, agent.MessageCompletionInterrupted, completion)
	assert.Equal(t, "sess-1", row.AgentSessionID)
}

// --- startup selection and the reasoning-level ladder ---

// The model ladder recovers on a level the model runs. Startup must not reapply
// the level it just rejected through the separate thought-level setter: the
// recovered level is what the session runs.
func TestApplyStartupSettings_TheModelLadderNeverReappliesTheRejectedLevel(t *testing.T) {
	t.Parallel()

	a, stdin := zcodeAgentOnTheWideModel(t, "max")
	runZCodeSetModelScript(a, stdin,
		zcodeSetModelRejectLevel("account:p/narrow"),
		zcodeSetModelRejectMissingLevel("account:p/narrow"),
		zcodeSetModelAccept(`{"settings":{
      "model":{"current":{"providerId":"account:p","modelId":"narrow"}},
      "thoughtLevel":{"enabled":true,"current":"high","available":[{"value":"low"},{"value":"high"}]}}}`),
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.applyStartupSettings(zcodeSettingsRequest{Model: "account:p/narrow", ThoughtLevel: "low"}, zcodeTestRPCTimeout)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("applyStartupSettings did not return")
	}

	requireNoZCodeThoughtLevelRequest(t, stdin, "startup never reapplies the level the ladder just rejected")
	attempts := zcodeSetModelAttempts(t, stdin)
	require.Len(t, attempts, 3)
	assert.Equal(t, "low", attempts[0].ReasoningLevel)
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, "account:p/narrow", a.model)
	assert.Equal(t, "high", a.thoughtLevel, "the recovered level is what the session runs")
}

// An exhausted ladder rejects the requested level in every selection it tries.
// Startup must not hand that level to the separate thought-level setter.
func TestApplyStartupSettings_AnExhaustedLadderDoesNotResendTheRejectedLevel(t *testing.T) {
	t.Parallel()

	a, stdin := zcodeAgentOnTheWideModel(t, "max")
	runZCodeSetModelScript(a, stdin,
		zcodeSetModelRejectLevel("account:p/narrow"),
		zcodeSetModelRejectMissingLevel("account:p/narrow"),
		zcodeSetModelRejectLevel("account:p/narrow"),
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.applyStartupSettings(zcodeSettingsRequest{Model: "account:p/narrow", ThoughtLevel: "low"}, zcodeTestRPCTimeout)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("applyStartupSettings did not return")
	}

	requireNoZCodeThoughtLevelRequest(t, stdin, "the level the ladder rejected is never resent")
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, "account:p/wide", a.model, "the observed model stands")
	assert.Equal(t, "max", a.thoughtLevel, "the observed level stands")
}

// A model switch that settles on the requested level needs no second request for
// that level.
func TestApplyStartupSettings_ASettledLevelNeedsNoSecondRequest(t *testing.T) {
	t.Parallel()

	a, stdin := zcodeAgentOnTheWideModel(t, "high")
	runZCodeSetModelScript(a, stdin,
		zcodeSetModelAccept(`{"settings":{
      "model":{"current":{"providerId":"account:p","modelId":"narrow"}},
      "thoughtLevel":{"enabled":true,"current":"high","available":[{"value":"low"},{"value":"high"}]}}}`),
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.applyStartupSettings(zcodeSettingsRequest{Model: "account:p/narrow"}, zcodeTestRPCTimeout)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("applyStartupSettings did not return")
	}

	requireNoZCodeThoughtLevelRequest(t, stdin, "the model request carries the level the session already runs")
	assert.Len(t, zcodeSetModelAttempts(t, stdin), 1)
}

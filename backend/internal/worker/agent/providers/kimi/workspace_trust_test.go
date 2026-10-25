package kimi

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kimiTestWorkspaceID is the workspace that the fake states for a session.
const kimiTestWorkspaceID = "wd_project_0123456789ab"

// kimiTrustRoute is the trust route of that workspace.
const kimiTrustRoute = kimiRouteWorkspaces + "/" + kimiTestWorkspaceID + "/trust"

// newKimiTrustRig opens a session whose workspace the fake reports untrusted
// unless trusted is true.
func newKimiTrustRig(t *testing.T, trusted bool) *kimiTestRig {
	t.Helper()
	rig := newKimiTestRig(t, agent.Options{})
	rig.fake.reply("GET "+kimiSessionPath(rig.sessionID(), ""), fakeKapReply{Data: map[string]any{"id": rig.sessionID(), "workspace_id": kimiTestWorkspaceID}})
	rig.fake.reply("GET "+kimiTrustRoute, fakeKapReply{Data: map[string]any{"trusted": trusted}})
	rig.fake.reply("POST "+kimiTrustRoute, fakeKapReply{Data: map[string]any{"trusted": true}})
	return rig
}

// answerKimiWorkspaceTrust sends the saved answer to the question, as the worker
// forwards it.
func answerKimiWorkspaceTrust(t *testing.T, a *Agent, requestID, decision string) error {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"type": "control_response", "response": map[string]any{
		"subtype": "success", "request_id": requestID,
		"response": map[string]string{contracts.WorkspaceTrustFieldDecision: decision},
	}})
	require.NoError(t, err)
	return a.SendRawInput(raw, agent.StopContext{})
}

// A workspace that Kimi Code does not trust gets one question, which names the
// folder and what the trust controls.
func TestKimiWorkspaceTrustAsksAboutAnUntrustedWorkspace(t *testing.T) {
	t.Parallel()
	rig := newKimiTrustRig(t, false)
	rig.agent.checkWorkspaceTrust()
	assert.Len(t, rig.fake.requestsTo("GET "+kimiTrustRoute), 1)
	require.Equal(t, 1, rig.sink.PublishedControlCount())
	published := rig.sink.LastPublishedControl()
	assert.Equal(t, providerkit.WorkspaceTrustRequestID(rig.sessionID()), published.RequestID)
	assert.JSONEq(t, `{"type":"leapmux_workspace_trust","path":"/work/project","configuration":["MCP servers","skills","instructions"]}`, string(published.Payload))
}

// Clearing the context retires the old session's unanswered question. The real
// asynchronous check of the replacement session publishes its own question.
func TestKimiClearContextReplacesAnUnansweredWorkspaceTrustQuestion(t *testing.T) {
	t.Parallel()
	rig := newKimiTrustRig(t, false)
	rig.agent.checkWorkspaceTrust()
	oldRequestID := providerkit.WorkspaceTrustRequestID("session_1")
	require.Equal(t, oldRequestID, rig.sink.LastPublishedControl().RequestID)
	rig.fake.reply("GET "+kimiSessionPath("session_2", ""), fakeKapReply{Data: map[string]any{
		"id": "session_2", "workspace_id": kimiTestWorkspaceID,
	}})

	sessionID, err := rig.agent.ClearContext()
	require.NoError(t, err)
	require.Equal(t, "session_2", sessionID)
	assert.Contains(t, rig.sink.CanceledControls(), oldRequestID)
	waitFor(t, func() bool { return rig.sink.PublishedControlCount() == 2 }, "the replacement session publishes its workspace-trust question")
	published := rig.sink.LastPublishedControl()
	assert.Equal(t, "workspace-trust:session_2", published.RequestID)
	assert.Equal(t, "session_2", published.AgentSessionID)
}

// A trust read of the old session can finish after ClearContext retired its
// generation. The stale result publishes nothing, while the replacement session
// completes its own read and publishes its own question.
func TestKimiClearContextRetiresABlockedOldSessionWorkspaceTrustCheck(t *testing.T) {
	t.Parallel()
	rig := newKimiTrustRig(t, false)
	readEntered := make(chan struct{})
	readRelease := make(chan struct{})
	release := sync.OnceFunc(func() { close(readRelease) })
	defer release()
	untrusted := fakeKapReply{Data: map[string]any{"trusted": false}}
	rig.fake.reply("GET "+kimiTrustRoute, fakeKapReply{
		Data: map[string]any{"trusted": false},
		BeforeReply: func() {
			close(readEntered)
			<-readRelease
		},
	}, untrusted)
	rig.fake.reply("GET "+kimiSessionPath("session_2", ""), fakeKapReply{Data: map[string]any{
		"id": "session_2", "workspace_id": kimiTestWorkspaceID,
	}})

	oldCheckDone := make(chan struct{})
	go func() {
		rig.agent.checkWorkspaceTrust()
		close(oldCheckDone)
	}()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-readEntered:
	case <-ctx.Done():
		t.Fatal("the old session's workspace-trust read did not start")
	}

	sessionID, err := rig.agent.ClearContext()
	require.NoError(t, err)
	require.Equal(t, "session_2", sessionID)
	waitFor(t, func() bool {
		return rig.sink.PublishedControlCount() == 1 && rig.sink.LastPublishedControl().RequestID == "workspace-trust:session_2"
	}, "the replacement session publishes while the old session's read is blocked")
	release()
	select {
	case <-oldCheckDone:
	case <-ctx.Done():
		t.Fatal("the old session's workspace-trust check did not finish")
	}

	require.Equal(t, 1, rig.sink.PublishedControlCount(), "the stale old-session check must not publish")
	assert.Equal(t, "workspace-trust:session_2", rig.sink.LastPublishedControl().RequestID)
	assert.Len(t, rig.fake.requestsTo("GET "+kimiTrustRoute), 2, "both sessions completed the real HTTP read")
}

// A trusted workspace, a workspace the session does not state, and an answer
// without a verdict ask nothing.
func TestKimiWorkspaceTrustAsksNothingWithoutAnUntrustedVerdict(t *testing.T) {
	t.Parallel()
	for name, setup := range map[string]func(rig *kimiTestRig){
		"a trusted workspace": func(rig *kimiTestRig) {
			rig.fake.reply("GET "+kimiTrustRoute, fakeKapReply{Data: map[string]any{"trusted": true}})
		},
		"an answer without a verdict": func(rig *kimiTestRig) {
			rig.fake.reply("GET "+kimiTrustRoute, fakeKapReply{Data: map[string]any{}})
		},
		"a refused trust read": func(rig *kimiTestRig) {
			rig.fake.reply("GET "+kimiTrustRoute, fakeKapReply{Code: 40401, Msg: "workspace not found"})
		},
		"a session without a workspace": func(rig *kimiTestRig) {
			rig.fake.reply("GET "+kimiSessionPath(rig.sessionID(), ""), fakeKapReply{Data: map[string]any{"id": rig.sessionID()}})
		},
		"a workspace id that leaves its route": func(rig *kimiTestRig) {
			rig.fake.reply("GET "+kimiSessionPath(rig.sessionID(), ""), fakeKapReply{Data: map[string]any{"id": rig.sessionID(), "workspace_id": "../config"}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiTrustRig(t, false)
			setup(rig)
			rig.agent.checkWorkspaceTrust()
			assert.Zero(t, rig.sink.PublishedControlCount())
		})
	}
}

// Trust grants the workspace, and asks the Worker for a new process with the
// same session, because the session that runs does not load the project.
func TestKimiWorkspaceTrustTrustAsksForANewProcess(t *testing.T) {
	t.Parallel()
	rig := newKimiTrustRig(t, false)
	rig.agent.checkWorkspaceTrust()
	requestID := providerkit.WorkspaceTrustRequestID(rig.sessionID())
	require.False(t, rig.agent.NativeTurnRestartRequired())
	published := len(rig.sink.TurnStates())
	require.NoError(t, answerKimiWorkspaceTrust(t, rig.agent, requestID, contracts.WorkspaceTrustDecisionTrust))
	assert.Len(t, rig.fake.requestsTo("POST "+kimiTrustRoute), 1)
	assert.True(t, rig.agent.NativeTurnRestartRequired())
	require.Greater(t, len(rig.sink.TurnStates()), published, "the turn state is republished, so the Worker reads the restart")
	assert.False(t, rig.sink.TurnStates()[len(rig.sink.TurnStates())-1].Active)
	assert.Equal(t, []string{requestID}, rig.sink.CanceledControls())
}

// A grant that Kimi Code refuses keeps the question answerable and asks for no
// new process.
func TestKimiWorkspaceTrustKeepsTheQuestionAfterARefusedGrant(t *testing.T) {
	t.Parallel()
	for name, reply := range map[string]fakeKapReply{
		"an untrusted answer": {Data: map[string]any{"trusted": false}},
		"a refused grant":     {Code: 50001, Msg: "the trust store is unavailable"},
		"no verdict":          {Data: map[string]any{}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiTrustRig(t, false)
			rig.fake.reply("POST "+kimiTrustRoute, reply)
			rig.agent.checkWorkspaceTrust()
			requestID := providerkit.WorkspaceTrustRequestID(rig.sessionID())
			require.Error(t, answerKimiWorkspaceTrust(t, rig.agent, requestID, contracts.WorkspaceTrustDecisionTrust))
			assert.False(t, rig.agent.NativeTurnRestartRequired())
			assert.Empty(t, rig.sink.CanceledControls())

			rig.fake.reply("POST "+kimiTrustRoute, fakeKapReply{Data: map[string]any{"trusted": true}})
			require.NoError(t, answerKimiWorkspaceTrust(t, rig.agent, requestID, contracts.WorkspaceTrustDecisionTrust))
			assert.True(t, rig.agent.NativeTurnRestartRequired())
		})
	}
}

// Reject sends nothing to Kimi Code, asks for no new process, and the agent asks
// no more.
func TestKimiWorkspaceTrustRejectAsksNoMore(t *testing.T) {
	t.Parallel()
	rig := newKimiTrustRig(t, false)
	rig.agent.checkWorkspaceTrust()
	requestID := providerkit.WorkspaceTrustRequestID(rig.sessionID())
	require.NoError(t, answerKimiWorkspaceTrust(t, rig.agent, requestID, contracts.WorkspaceTrustDecisionReject))
	assert.Empty(t, rig.fake.requestsTo("POST "+kimiTrustRoute))
	assert.False(t, rig.agent.NativeTurnRestartRequired())
	assert.Equal(t, []string{requestID}, rig.sink.CanceledControls())
	rig.agent.checkWorkspaceTrust()
	assert.Len(t, rig.fake.requestsTo("GET "+kimiTrustRoute), 1, "a declined workspace is not checked again")
}

// A session that the agent stops driving retires its question with its other
// controls.
func TestKimiWorkspaceTrustRetiresWithTheSessionControls(t *testing.T) {
	t.Parallel()
	rig := newKimiTrustRig(t, false)
	rig.agent.checkWorkspaceTrust()
	rig.agent.withdrawAllControls()
	requestID := providerkit.WorkspaceTrustRequestID(rig.sessionID())
	assert.Contains(t, rig.sink.CanceledControls(), requestID)
	require.ErrorIs(t, answerKimiWorkspaceTrust(t, rig.agent, requestID, contracts.WorkspaceTrustDecisionTrust), providerkit.ErrWorkspaceTrustClosed)
	assert.Empty(t, rig.fake.requestsTo("POST "+kimiTrustRoute))
}

// The plugin turns the browser's neutral decision into the saved answer that the
// agent reads.
func TestKimiWorkspaceTrustResolvesTheNeutralDecision(t *testing.T) {
	t.Parallel()
	rig := newKimiTrustRig(t, false)
	rig.agent.checkWorkspaceTrust()
	request := rig.sink.LastPublishedControl()
	for behavior, want := range map[string]string{
		agent.ControlBehaviorAllow: contracts.WorkspaceTrustDecisionTrust,
		agent.ControlBehaviorDeny:  contracts.WorkspaceTrustDecisionReject,
	} {
		content, err := json.Marshal(map[string]any{"response": map[string]any{
			"request_id": request.RequestID, "response": map[string]any{"behavior": behavior},
		}})
		require.NoError(t, err)
		resolution := kimiProvider{}.ResolveControlResponse(agent.ControlResponseContext{
			RequestID: request.RequestID, RequestPayload: request.Payload, ResponseContent: content,
		})
		require.False(t, resolution.Withhold, behavior)
		assert.Contains(t, string(resolution.Content), `"decision":"`+want+`"`, behavior)
	}
}

package copilot

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// copilotTrustedFolder is the working directory of the folder-trust tests.
const copilotTrustedFolder = "/work/project"

// newCopilotFolderTrustHarness starts a fake runtime that answers the
// folder-trust requests, and an agent that records its control requests.
// answers overrides the default answer of a method.
func newCopilotFolderTrustHarness(t *testing.T, answers map[string]string) (copilotRuntimeHarness, *agenttest.ControlSink) {
	t.Helper()
	table := map[string]string{
		"session.create":                             copilotRuntimeEchoSessionID,
		"session.eventLog.registerInterest":          `{"handle":"interest-1"}`,
		"session.permissions.folderTrust.isTrusted":  `{"trusted":false}`,
		"session.permissions.folderTrust.addTrusted": `{"success":true}`,
		"session.mcp.reload":                         `{}`,
		"sessions.loadDeferredRepoHooks":             `{"startupPrompts":[],"hookCount":0}`,
	}
	for method, answer := range answers {
		table[method] = answer
	}
	h := newCopilotRuntimeHarness(t, table, nil)
	sink := &agenttest.ControlSink{}
	h.agent.sink = agent.NewProviderServices(sink)
	h.agent.opts.WorkingDir = copilotTrustedFolder
	return h, sink
}

// answerCopilotFolderTrust sends the saved answer to the folder-trust question,
// as the worker forwards it.
func answerCopilotFolderTrust(t *testing.T, a *Agent, requestID, decision string) error {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"type": "control_response", "response": map[string]any{
		"subtype": "success", "request_id": requestID,
		"response": map[string]string{contracts.WorkspaceTrustFieldDecision: decision},
	}})
	require.NoError(t, err)
	return a.SendRawInput(raw, agent.StopContext{})
}

// copilotFolderTrustRequestID identifies the question of the session `session-1`.
var copilotFolderTrustRequestID = providerkit.WorkspaceTrustRequestID("session-1")

// Copilot is asked about the working directory, and an untrusted folder gets one
// question that names the folder and Copilot's MCP servers.
func TestFolderTrustAsksCopilotAboutTheWorkingDirectory(t *testing.T) {
	t.Parallel()
	h, sink := newCopilotFolderTrustHarness(t, nil)
	h.agent.checkFolderTrust()
	assert.Equal(t, []string{"session.permissions.folderTrust.isTrusted"}, h.methods())
	assert.Equal(t, map[string]any{"sessionId": "session-1", "path": copilotTrustedFolder}, h.lastParams(t))
	require.Equal(t, 1, sink.PublishedControlCount())
	published := sink.LastPublishedControl()
	assert.Equal(t, copilotFolderTrustRequestID, published.RequestID)
	assert.JSONEq(t, `{"type":"leapmux_workspace_trust","path":"/work/project","configuration":["MCP servers"]}`, string(published.Payload))
}

// A trusted folder, a refused check and an answer without a verdict ask nothing.
func TestFolderTrustAsksNothingWithoutAnUntrustedVerdict(t *testing.T) {
	t.Parallel()
	for name, answer := range map[string]string{
		"a trusted folder":     `{"trusted":true}`,
		"a refused check":      `{"error":{"code":-32603,"message":"no trust store"}}`,
		"an answer without it": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h, sink := newCopilotFolderTrustHarness(t, map[string]string{"session.permissions.folderTrust.isTrusted": answer})
			h.agent.checkFolderTrust()
			assert.Zero(t, sink.PublishedControlCount())
		})
	}
}

// Trust adds the folder to Copilot's list, loads the project's MCP servers in the
// running session and the deferred repository hooks, and retires the question.
func TestFolderTrustTrustAddsTheFolderAndLoadsTheProject(t *testing.T) {
	t.Parallel()
	h, sink := newCopilotFolderTrustHarness(t, nil)
	h.agent.checkFolderTrust()
	require.NoError(t, answerCopilotFolderTrust(t, h.agent, copilotFolderTrustRequestID, contracts.WorkspaceTrustDecisionTrust))
	assert.Equal(t, []string{
		"session.permissions.folderTrust.isTrusted",
		"session.permissions.folderTrust.addTrusted",
		"session.mcp.reload",
		"sessions.loadDeferredRepoHooks",
	}, h.methods())
	var addTrusted map[string]any
	require.NoError(t, json.Unmarshal((*h.requests)[1].Params, &addTrusted))
	assert.Equal(t, map[string]any{"sessionId": "session-1", "path": copilotTrustedFolder}, addTrusted)
	assert.Equal(t, map[string]any{"sessionId": "session-1"}, h.lastParams(t))
	assert.Equal(t, []string{copilotFolderTrustRequestID}, sink.CanceledControls())
}

// The folder is trusted once Copilot adds it, so a failed reload or hook load is
// logged and the question still retires: the next session in the folder loads
// the project.
func TestFolderTrustRetiresTheQuestionAfterAFailedReload(t *testing.T) {
	t.Parallel()
	h, sink := newCopilotFolderTrustHarness(t, map[string]string{
		"session.mcp.reload":             `{"error":{"code":-32603,"message":"reload failed"}}`,
		"sessions.loadDeferredRepoHooks": `{"error":{"code":-32603,"message":"hooks failed"}}`,
	})
	h.agent.checkFolderTrust()
	require.NoError(t, answerCopilotFolderTrust(t, h.agent, copilotFolderTrustRequestID, contracts.WorkspaceTrustDecisionTrust))
	assert.Equal(t, []string{copilotFolderTrustRequestID}, sink.CanceledControls())
}

// A trust that Copilot refuses keeps the question answerable.
func TestFolderTrustKeepsTheQuestionAfterARefusedTrust(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		answer   string
		wantText string
	}{
		"a refused trust": {`{"success":false}`, "did not add the folder /work/project"},
		"a failed trust":  {`{"error":{"code":-32603,"message":"write failed"}}`, "write failed"},
		"no receipt":      {`{}`, "did not add the folder"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h, sink := newCopilotFolderTrustHarness(t, map[string]string{"session.permissions.folderTrust.addTrusted": test.answer})
			h.agent.checkFolderTrust()
			err := answerCopilotFolderTrust(t, h.agent, copilotFolderTrustRequestID, contracts.WorkspaceTrustDecisionTrust)
			require.ErrorContains(t, err, test.wantText)
			assert.Empty(t, sink.CanceledControls())
			assert.NotContains(t, h.methods(), "session.mcp.reload", "nothing loads before Copilot trusts the folder")
		})
	}
}

// Reject sends nothing to Copilot, and the agent asks no more, not even after its
// session changes.
func TestFolderTrustRejectAsksNoMore(t *testing.T) {
	t.Parallel()
	h, sink := newCopilotFolderTrustHarness(t, nil)
	h.agent.checkFolderTrust()
	require.NoError(t, answerCopilotFolderTrust(t, h.agent, copilotFolderTrustRequestID, contracts.WorkspaceTrustDecisionReject))
	assert.Equal(t, []string{"session.permissions.folderTrust.isTrusted"}, h.methods())
	assert.Equal(t, []string{copilotFolderTrustRequestID}, sink.CanceledControls())

	h.agent.stateMu.Lock()
	h.agent.sessionID = "session-2"
	h.agent.stateMu.Unlock()
	h.agent.checkFolderTrust()
	assert.Len(t, *h.requests, 1, "a declined folder is not checked again")
}

// A session replacement withdraws the open question with the other controls of
// the session, and the next session asks again.
func TestFolderTrustAsksAgainInAReplacedSession(t *testing.T) {
	t.Parallel()
	h, sink := newCopilotFolderTrustHarness(t, nil)
	h.agent.checkFolderTrust()
	require.Equal(t, 1, sink.PublishedControlCount(), "the old session has an unanswered question")
	oldID := h.agent.currentNativeSessionID()
	newID, err := h.agent.ClearContext()
	require.NoError(t, err)
	require.NotEqual(t, oldID, newID)
	assert.Equal(t, []string{copilotFolderTrustRequestID}, sink.CanceledControls())
	require.Eventually(t, h.agent.backgroundWork.Idle, offReaderWait, time.Millisecond)
	require.Equal(t, 2, sink.PublishedControlCount())
	assert.Equal(t, providerkit.WorkspaceTrustRequestID(newID), sink.LastPublishedControl().RequestID)
	err = answerCopilotFolderTrust(t, h.agent, copilotFolderTrustRequestID, contracts.WorkspaceTrustDecisionTrust)
	require.ErrorIs(t, err, providerkit.ErrWorkspaceTrustClosed, "the question of the previous session takes no answer")
}

// Allow All approves tool calls, not the trust of a folder.
func TestFolderTrustIsNotABypassedPermission(t *testing.T) {
	t.Parallel()
	h, sink := newCopilotFolderTrustHarness(t, nil)
	h.agent.options[agent.OptionIDPermissionMode] = contracts.CopilotPermissionModeAllowAll
	h.agent.checkFolderTrust()
	h.agent.approvePendingPermissions()
	assert.Equal(t, []string{"session.permissions.folderTrust.isTrusted"}, h.methods())
	assert.Empty(t, sink.CanceledControls())
}

// The plugin turns the browser's neutral decision into the saved answer that the
// agent reads.
func TestFolderTrustResolvesTheNeutralDecision(t *testing.T) {
	t.Parallel()
	h, sink := newCopilotFolderTrustHarness(t, nil)
	h.agent.checkFolderTrust()
	request := sink.LastPublishedControl()
	for behavior, want := range map[string]string{
		agent.ControlBehaviorAllow: contracts.WorkspaceTrustDecisionTrust,
		agent.ControlBehaviorDeny:  contracts.WorkspaceTrustDecisionReject,
	} {
		content, err := json.Marshal(map[string]any{"response": map[string]any{
			"request_id": request.RequestID, "response": map[string]any{"behavior": behavior},
		}})
		require.NoError(t, err)
		resolution := Registration().Plugin.ResolveControlResponse(agent.ControlResponseContext{
			RequestID: request.RequestID, RequestPayload: request.Payload, ResponseContent: content,
		})
		require.False(t, resolution.Withhold, behavior)
		var stored struct {
			Response struct {
				RequestID string `json:"request_id"`
				Response  struct {
					Decision string `json:"decision"`
				} `json:"response"`
			} `json:"response"`
		}
		require.NoError(t, json.Unmarshal(resolution.Content, &stored), behavior)
		assert.Equal(t, request.RequestID, stored.Response.RequestID, behavior)
		assert.Equal(t, want, stored.Response.Response.Decision, behavior)
	}
}

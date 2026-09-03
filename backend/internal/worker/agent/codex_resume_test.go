package agent

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startOrResumeThread must never answer a failed thread/resume with
// thread/start. Codex opens an EMPTY thread for a start, so the fallback
// reported success and left the user with a tab that has no history, no memory
// of the work, and no report of why -- only a warning in the worker log.
func TestCodexStartOrResumeThread(t *testing.T) {
	t.Parallel()

	const timeout = 5 * time.Second

	resume := func(t *testing.T, body string) (codexThreadResult, func() []codexRecordedRequest, error) {
		t.Helper()
		a, _, requests := newCodexAgentForRPC(t, func(string) json.RawMessage {
			return json.RawMessage(body)
		})
		thread, err := a.startOrResumeThread(map[string]interface{}{}, "thread-old", timeout)
		return thread, requests, err
	}

	t.Run("adopts the resumed thread and sends no thread/start", func(t *testing.T) {
		t.Parallel()
		thread, requests, err := resume(t, `{"thread":{"id":"thread-old"},"model":"gpt-5.6-sol"}`)
		require.NoError(t, err)
		assert.Equal(t, "thread-old", thread.ID)
		require.NotNil(t, thread.settings[OptionIDModel])
		assert.Equal(t, "gpt-5.6-sol", *thread.settings[OptionIDModel])
		sent := requests()
		require.Len(t, sent, 1, "a resume that holds must not also start a thread")
		assert.Equal(t, "thread/resume", sent[0].Method)
	})

	// codexThreadParams sends a concrete model and omits the account default, and
	// applyThreadResult then adopts whatever model the response reports. The two
	// halves are asserted separately: a row whose stored model equals its response
	// model proves nothing about the second half, so every row here either omits the
	// model on the wire or reports a DIFFERENT model back.
	t.Run("pins concrete models and adopts the reported one", func(t *testing.T) {
		t.Parallel()

		for _, test := range []struct {
			name string
			// storedModel is a.model at launch, and what codexThreadParams reads.
			storedModel string
			// responseModel is the effective model Codex reports back.
			responseModel string
			// wantModelOmitted states the wire shape explicitly, so a row that omits
			// wantModel cannot assert omission by accident. "model" absent and "model"
			// present but empty are different requests, and only the first is correct.
			wantModelOmitted bool
			// resumeID is empty for the thread/start path.
			resumeID   string
			wantMethod string
		}{
			{name: "user selection is pinned and kept", storedModel: "gpt-5.6-luna", responseModel: "gpt-5.6-luna", resumeID: "thread-old", wantMethod: "thread/resume"},
			{name: "retired selection is pinned and clamped by codex", storedModel: "gpt-5.2", responseModel: "gpt-5.6-sol", resumeID: "thread-old", wantMethod: "thread/resume"},
			{name: "interrupted startup default resolves on resume", storedModel: DefaultModelSentinel, responseModel: "gpt-5.6-sol", wantModelOmitted: true, resumeID: "thread-old", wantMethod: "thread/resume"},
			{name: "new session omits the account default", storedModel: DefaultModelSentinel, responseModel: "gpt-5.6-sol", wantModelOmitted: true, wantMethod: "thread/start"},
			{name: "new session pins a concrete model", storedModel: "gpt-5.6-terra", responseModel: "gpt-5.6-terra", wantMethod: "thread/start"},
		} {
			t.Run(test.name, func(t *testing.T) {
				t.Parallel()

				a, _, requests := newCodexAgentForRPC(t, func(string) json.RawMessage {
					return json.RawMessage(`{"thread":{"id":"thread-1"},"model":"` + test.responseModel + `"}`)
				})
				a.model = test.storedModel
				params := codexThreadParams(test.storedModel, "/work", CodexDefaultApprovalPolicy, CodexDefaultSandboxPolicy, CodexDefaultServiceTier)

				thread, err := a.startOrResumeThread(params, test.resumeID, timeout)
				require.NoError(t, err)
				a.applyThreadResult(thread)

				sent := requests()
				require.Len(t, sent, 1)
				assert.Equal(t, test.wantMethod, sent[0].Method)
				if test.wantModelOmitted {
					assert.NotContains(t, sent[0].Params, "model", "the account default lets Codex resolve the model")
				} else {
					assert.Equal(t, test.storedModel, sent[0].Params["model"], "a concrete model is pinned on the wire")
				}
				assert.Equal(t, test.responseModel, a.model, "the agent adopts the model the response reports")
			})
		}
	})

	// An RPC error arrives as a delivered body rather than as a transport error,
	// so the agent's own message is only in the response. The failure must carry
	// it: "carried no thread ID" identifies the symptom and hides the reason.
	t.Run("fails with the agent's own reason when the resume is refused", func(t *testing.T) {
		t.Parallel()
		_, requests, err := resume(t, `{"code":-32000,"message":"thread not found"}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "thread-old")
		assert.Contains(t, err.Error(), "thread not found")
		assert.Contains(t, err.Error(), "/clear")
		require.Len(t, requests(), 1, "a refused resume must not start a thread behind the user's back")
	})

	t.Run("fails when the response does not parse", func(t *testing.T) {
		t.Parallel()
		_, requests, err := resume(t, `not json`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "thread-old")
		assert.Contains(t, err.Error(), "/clear")
		require.Len(t, requests(), 1)
	})

	t.Run("fails when the response carries no thread ID", func(t *testing.T) {
		t.Parallel()
		_, requests, err := resume(t, `{"thread":{}}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "thread-old")
		assert.Contains(t, err.Error(), "/clear")
		require.Len(t, requests(), 1)
	})

	// A launch with no stored thread goes straight to thread/start, which is the
	// only path that mints a new thread now.
	t.Run("starts a thread when there is nothing to resume", func(t *testing.T) {
		t.Parallel()
		a, _, requests := newCodexAgentForRPC(t, func(string) json.RawMessage {
			return json.RawMessage(`{"thread":{"id":"thread-new"},"model":"gpt-5.4"}`)
		})
		thread, err := a.startOrResumeThread(map[string]interface{}{}, "", timeout)
		require.NoError(t, err)
		assert.Equal(t, "thread-new", thread.ID)
		sent := requests()
		require.Len(t, sent, 1)
		assert.Equal(t, "thread/start", sent[0].Method)
	})

	t.Run("keeps the server reason when thread start fails", func(t *testing.T) {
		t.Parallel()
		a, _, requests := newCodexAgentForRPC(t, func(string) json.RawMessage {
			return json.RawMessage(`{"code":-32000,"message":"model unavailable"}`)
		})

		_, err := a.startOrResumeThread(map[string]interface{}{}, "", timeout)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "model unavailable")
		require.Len(t, requests(), 1)
	})
}

func TestCodexClearContextStartsFreshThread(t *testing.T) {
	t.Parallel()

	a, sink, requests := newCodexAgentForRPC(t, func(method string) json.RawMessage {
		require.Equal(t, "thread/start", method)
		return json.RawMessage(`{
			"thread":{"id":"thread-new"},
			"model":"gpt-5.6-sol",
			"reasoningEffort":"medium",
			"approvalPolicy":"never",
			"sandbox":{"type":"dangerFullAccess"}
		}`)
	})
	a.threadID = "thread-old"
	a.turnID = "turn-old"
	a.turnSawPlan = true
	a.turnPlanText = "old plan"
	a.turnAssistantText = "old answer"
	a.streamingPlan = true
	a.model = "gpt-5.6-sol"
	a.approvalPolicy = "never"
	a.sandboxPolicy = CodexSandboxDangerFullAccess
	a.serviceTier = CodexServiceTierFast

	sessionID, ok := a.ClearContext()

	require.True(t, ok)
	assert.Equal(t, "thread-new", sessionID)
	assert.Equal(t, "thread-new", a.threadID)
	assert.Empty(t, a.turnID)
	assert.False(t, a.turnSawPlan)
	assert.Empty(t, a.turnPlanText)
	assert.Empty(t, a.turnAssistantText)
	assert.False(t, a.streamingPlan)
	assert.Equal(t, CodexSandboxDangerFullAccess, a.sandboxPolicy)
	assert.Equal(t, CodexNetworkEnabled, a.networkAccess)
	assert.Equal(t, "thread-new", sink.LastSessionID())

	sent := requests()
	require.Len(t, sent, 1)
	assert.Equal(t, "thread/start", sent[0].Method)
	assert.NotContains(t, sent[0].Params, "threadId", "a clear must not resume the old thread")
	assert.Equal(t, "clear", sent[0].Params["sessionStartSource"])
	assert.Equal(t, "gpt-5.6-sol", sent[0].Params["model"])
	assert.Equal(t, "never", sent[0].Params["approvalPolicy"])
	assert.Equal(t, CodexSandboxDangerFullAccess, sent[0].Params["sandbox"])
	assert.Equal(t, CodexServiceTierFast, sent[0].Params["serviceTier"])
}

func testCodexThreadResponse(id, model string, effort, serviceTier, approvalPolicy, sandbox json.RawMessage) codexThreadResponse {
	response := codexThreadResponse{
		Model:          model,
		Effort:         effort,
		ServiceTier:    serviceTier,
		ApprovalPolicy: approvalPolicy,
		Sandbox:        sandbox,
	}
	response.Thread.ID = id
	return response
}

func TestCodexThreadResultAppliesConfirmedSettings(t *testing.T) {
	t.Parallel()

	result, err := newCodexThreadResult(testCodexThreadResponse(
		"thread-1",
		"gpt-5.6-sol",
		json.RawMessage(`"medium"`),
		json.RawMessage(`"fast"`),
		json.RawMessage(`"on-request"`),
		json.RawMessage(`"workspace-write"`),
	))
	require.NoError(t, err)

	a := &CodexAgent{
		model:          "gpt-5.6-luna",
		effort:         EffortAuto,
		serviceTier:    CodexDefaultServiceTier,
		approvalPolicy: "never",
		sandboxPolicy:  CodexSandboxReadOnly,
	}
	a.applyThreadResult(result)

	assert.Equal(t, "gpt-5.6-sol", a.model)
	assert.Equal(t, "medium", a.effort)
	assert.Equal(t, CodexServiceTierFast, a.serviceTier)
	assert.Equal(t, CodexDefaultApprovalPolicy, a.approvalPolicy)
	assert.Equal(t, CodexSandboxWorkspaceWrite, a.sandboxPolicy)
}

func TestCodexThreadResultAppliesCurrentSandboxObject(t *testing.T) {
	t.Parallel()

	result, err := newCodexThreadResult(testCodexThreadResponse(
		"thread-1",
		"gpt-5.6-sol",
		nil,
		nil,
		nil,
		json.RawMessage(`{"type":"workspaceWrite","networkAccess":true}`),
	))
	require.NoError(t, err)

	a := &CodexAgent{
		sandboxPolicy: CodexSandboxReadOnly,
		networkAccess: CodexNetworkRestricted,
	}
	a.applyThreadResult(result)

	assert.Equal(t, CodexSandboxWorkspaceWrite, a.sandboxPolicy)
	assert.Equal(t, CodexNetworkRestricted, a.networkAccess)

	fullAccess, err := newCodexThreadResult(testCodexThreadResponse(
		"thread-2",
		"gpt-5.6-sol",
		nil,
		nil,
		nil,
		json.RawMessage(`{"type":"dangerFullAccess"}`),
	))
	require.NoError(t, err)
	a.sandboxPolicy = CodexSandboxReadOnly
	a.networkAccess = CodexNetworkRestricted
	a.applyThreadResult(fullAccess)
	assert.Equal(t, CodexSandboxDangerFullAccess, a.sandboxPolicy)
	assert.Equal(t, CodexNetworkEnabled, a.networkAccess)
}

func TestCodexThreadResultPreservesTurnOnlySettings(t *testing.T) {
	t.Parallel()

	result, err := newCodexThreadResult(testCodexThreadResponse(
		"thread-1",
		"gpt-5.6-sol",
		json.RawMessage(`"high"`),
		nil,
		nil,
		json.RawMessage(`{"type":"workspaceWrite","networkAccess":false}`),
	))
	require.NoError(t, err)

	a := &CodexAgent{
		effort:        "low",
		networkAccess: CodexNetworkEnabled,
	}
	a.applyThreadResult(result)

	assert.Equal(t, "low", a.effort,
		"thread/start does not accept effort, so its response must not replace an explicit turn setting")
	assert.Equal(t, CodexNetworkEnabled, a.networkAccess,
		"thread/start accepts only the sandbox mode, so its response must not replace turn network access")
}

func TestCodexThreadResultNullSettingsUseProviderDefaults(t *testing.T) {
	t.Parallel()

	result, err := newCodexThreadResult(testCodexThreadResponse(
		"thread-1",
		"gpt-5.6-sol",
		json.RawMessage(`null`),
		json.RawMessage(`null`),
		nil,
		nil,
	))
	require.NoError(t, err)

	a := &CodexAgent{
		effort:      EffortHigh,
		serviceTier: CodexServiceTierFast,
	}
	a.applyThreadResult(result)

	assert.Equal(t, EffortHigh, a.effort)
	assert.Equal(t, CodexDefaultServiceTier, a.serviceTier)
	assert.Equal(t, "", a.approvalPolicy)
	assert.Equal(t, "", a.sandboxPolicy)
}

func TestCodexThreadResultPreservesGranularApprovalPolicy(t *testing.T) {
	t.Parallel()

	result, err := newCodexThreadResult(testCodexThreadResponse(
		"thread-1",
		"gpt-5.6-sol",
		nil,
		nil,
		json.RawMessage(`{"granular":{"sandboxApproval":true}}`),
		nil,
	))
	require.NoError(t, err)

	a := &CodexAgent{approvalPolicy: CodexDefaultApprovalPolicy}
	a.applyThreadResult(result)

	assert.Equal(t, CodexDefaultApprovalPolicy, a.approvalPolicy)
}

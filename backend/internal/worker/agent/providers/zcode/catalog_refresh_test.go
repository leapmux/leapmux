package zcode

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func zcodeApplyTestSetting(a *Agent, method string) error {
	switch method {
	case MethodSetModel:
		return a.applyZCodeModel("personal:p/A", "", zcodeTestRPCTimeout)
	case MethodSetThoughtLevel:
		return a.applyZCodeThoughtLevel("high", zcodeTestRPCTimeout)
	case MethodSetMode:
		return a.applyZCodeMode("plan", zcodeTestRPCTimeout)
	default:
		panic("unsupported test settings method: " + method)
	}
}

func TestZCodeOpenSession_ReplacesTheCompleteAccountCatalog(t *testing.T) {
	t.Parallel()
	for _, available := range []string{`[{"ref":{"providerId":"personal:p","modelId":"B"}}]`, `[]`} {
		t.Run(available, func(t *testing.T) {
			stdin := &zcodeRecordedStdin{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
			a.account = &zcodeAccountState{}
			a.catalog = zcodeTestCatalog(t, `{"provider":{"personal:p":{"kind":"anthropic","options":{"apiKey":"k"},"models":{"A":{},"B":{}}}}}`)
			zcodeApplySettings(t, a, `{"model":{"current":{"providerId":"personal:p","modelId":"B"},"available":[{"ref":{"providerId":"personal:p","modelId":"A"}},{"ref":{"providerId":"personal:p","modelId":"B"}}]}}`)
			done := make(chan error, 1)
			go func() { done <- a.openSession("sess-1", zcodeTestRPCTimeout) }()
			request := zcodeNthRequest(t, stdin, MethodSessionResume, 1)
			a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{"session":{"sessionId":"sess-1"},"settings":{"model":{"current":{"providerId":"personal:p","modelId":"B"},"available":`+available+`}}}`)))
			require.NoError(t, <-done)
			models, _, _ := a.zcodeModelsForUI()
			if available == `[]` {
				assert.Empty(t, models)
			} else {
				require.Len(t, models, 1)
				assert.Equal(t, "personal:p/B", models[0].Id)
			}
			a.Mu.Lock()
			_, found := a.resolveZCodeModelIDLocked("personal:p/A")
			a.Mu.Unlock()
			assert.False(t, found, "a removed native model must not fall back to the legacy registry")
			// A delayed setter reply must not restore a removed model.
			zcodeApplySettings(t, a, `{"model":{"available":[{"ref":{"providerId":"personal:p","modelId":"A"}}]}}`)
			later, _, _ := a.zcodeModelsForUI()
			assert.Equal(t, models, later)
		})
	}
}

func TestZCodeSettingsSnapshot_CannotAdoptARetiredSession(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	_, applied := a.applyStateSnapshot(json.RawMessage(`{"session":{"sessionId":"retired"},"runtime":{"eventSeq":999},"settings":{"model":{"current":{"providerId":"old","modelId":"A"}}}}`))
	assert.False(t, applied)
	assert.Equal(t, "sess-1", a.sessionID)
	assert.Zero(t, a.lastSeq)
	assert.Empty(t, a.model)
}

func TestZCodeSetterReply_CannotChangeAReplacementSession(t *testing.T) {
	t.Parallel()
	for _, method := range []string{MethodSetModel, MethodSetThoughtLevel, MethodSetMode} {
		t.Run(method, func(t *testing.T) {
			stdin := &zcodeRecordedStdin{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
			a.catalog = zcodeTestCatalog(t, `{"provider":{"personal:p":{"kind":"anthropic","options":{"apiKey":"k"},"models":{"A":{}}}}}`)
			done := make(chan error, 1)
			go func() { done <- zcodeApplyTestSetting(a, method) }()
			request := zcodeNthRequest(t, stdin, method, 1)
			a.Mu.Lock()
			a.sessionID, a.model, a.thoughtLevel = "replacement", "new/B", "low"
			a.Mu.Unlock()
			// Even a reply that omits its session identity belongs to the old request.
			a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{}`)))
			require.ErrorIs(t, <-done, errNoZCodeSession)
			assert.Equal(t, "new/B", a.model)
			assert.Equal(t, "low", a.thoughtLevel)
			assert.False(t, a.planEnabled)
			assert.Empty(t, a.unresolvedSettings)
		})
	}
}

func TestZCodeSetterSnapshot_DoesNotDiscardUndeliveredEvents(t *testing.T) {
	t.Parallel()
	for _, method := range []string{MethodSetModel, MethodSetThoughtLevel, MethodSetMode} {
		t.Run(method, func(t *testing.T) {
			stdin := &zcodeRecordedStdin{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
			a.catalog = zcodeTestCatalog(t, `{"provider":{"personal:p":{"kind":"anthropic","options":{"apiKey":"k"},"models":{"A":{}}}}}`)
			a.lastSeq = 7
			done := make(chan error, 1)
			go func() { done <- zcodeApplyTestSetting(a, method) }()
			request := zcodeNthRequest(t, stdin, method, 1)
			a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{"session":{"sessionId":"sess-1"},"runtime":{"eventSeq":55,"stateRevision":9}}`)))
			require.NoError(t, <-done)
			assert.Equal(t, int64(9), a.stateRevision)
			assert.Equal(t, int64(7), a.lastSeq)
			a.HandleOutput(zcodeEventLine(t, 8, contracts.ZCodeEventTurnStarted, `{"turnId":"later"}`))
			assert.Equal(t, int64(8), a.lastSeq)
			assert.True(t, a.turnActive)
		})
	}
}

func TestZCodeAccountCatalog_RetriesWithoutResendingAnAcceptedSnapshot(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	a.account = &zcodeAccountState{generation: 1, pushedGeneration: 1}
	a.model, a.thoughtLevel, a.lastSeq = "personal:p/B", "high", 7
	state := a.account
	done := make(chan bool, 1)
	go func() { done <- a.refreshAccountSnapshot(state) }()
	first := zcodeNthRequest(t, stdin, MethodSessionResume, 1)
	assert.False(t, a.sessionLifecycleMu.TryLock(), "the active ID stays protected until the read completes")
	a.HandleOutput(zcodeErrorReplyLine(t, zcodeSentRequestID(t, first), -32603, "temporary failure"))
	require.True(t, <-done, "a catalog failure must not postpone billing checks")
	assert.Zero(t, state.catalogGeneration)
	go func() { done <- a.refreshAccountSnapshot(state) }()
	second := zcodeNthRequest(t, stdin, MethodSessionResume, 2)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, second), json.RawMessage(`{"session":{"sessionId":"sess-1","target":null},"runtime":{"eventSeq":999},"settings":{"model":{"current":{"providerId":"personal:p","modelId":"A"},"available":[{"ref":{"providerId":"personal:p","modelId":"A"}},{"ref":{"providerId":"personal:p","modelId":"B"}}]},"thoughtLevel":{"enabled":true,"current":"low"}}}`)))
	read := zcodeNthRequest(t, stdin, MethodSessionRead, 1)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, read), json.RawMessage(`{}`)))
	require.True(t, <-done)
	assert.Equal(t, 1, state.catalogGeneration)
	assert.Equal(t, int64(7), a.lastSeq)
	assert.Equal(t, "personal:p/B", a.model)
	assert.Equal(t, "high", a.thoughtLevel)
	models, _, _ := a.zcodeModelsForUI()
	require.Len(t, models, 2)
	assert.False(t, models[0].IsDefault)
	assert.True(t, models[1].IsDefault)
	for _, request := range stdin.Requests(t) {
		assert.NotEqual(t, MethodUpdateAccountConfig, request.Method)
	}
}

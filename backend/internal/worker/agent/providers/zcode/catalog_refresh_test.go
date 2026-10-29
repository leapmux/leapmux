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
		_, err := a.applyZCodeModel("personal:p/A", "", zcodeTestRPCTimeout)
		return err
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
	zcodeAnswerRegistryRefresh(t, a, stdin, 1)
	first := zcodeNthRequest(t, stdin, MethodSessionResume, 1)
	var resumeParams struct {
		Workspace    zcodeWorkspace `json:"workspace"`
		ToolDenylist []string       `json:"toolDenylist"`
	}
	require.NoError(t, json.Unmarshal(first.Params, &resumeParams))
	assert.Equal(t, a.workspace, resumeParams.Workspace, "the catalog resume states the same workspace as every resume")
	zcodeAssertToolDenylist(t, resumeParams.ToolDenylist)
	assert.False(t, a.sessionLifecycleMu.TryLock(), "the active ID stays protected until the read completes")
	a.HandleOutput(zcodeErrorReplyLine(t, zcodeSentRequestID(t, first), -32603, "temporary failure"))
	require.True(t, <-done, "a catalog failure must not postpone billing checks")
	assert.Zero(t, state.catalogGeneration)
	go func() { done <- a.refreshAccountSnapshot(state) }()
	zcodeAnswerRegistryRefresh(t, a, stdin, 2)
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

// zcodeAnswerRegistryRefresh answers the n-th provider/refreshRegistry request
// with the acknowledged result.
func zcodeAnswerRegistryRefresh(t *testing.T, a *Agent, stdin *zcodeRecordedStdin, n int) {
	t.Helper()
	request := zcodeNthRequest(t, stdin, MethodProviderRefreshRegistry, n)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{"refreshed":true}`)))
}

// zcodeCatalogRefreshActive reports whether a refresh pass runs or is requested.
func zcodeCatalogRefreshActive(a *Agent) bool {
	a.catalogRefresh.mu.Lock()
	defer a.catalogRefresh.mu.Unlock()
	return a.catalogRefresh.running || a.catalogRefresh.pending
}

// zcodeAnswerCatalogResume answers the n-th catalog resume of one model.
func zcodeAnswerCatalogResume(t *testing.T, a *Agent, stdin *zcodeRecordedStdin, n int, available string) {
	t.Helper()
	request := zcodeNthRequest(t, stdin, MethodSessionResume, n)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{"session":{"sessionId":"sess-1"},"settings":{"model":{"available":`+available+`}}}`)))
}

// The registry refresh requires the exact refreshed acknowledgement. Only
// method-not-found marks the capability absent; anything else fails the call.
func TestZCodeCatalogRegistry_RequiresARefreshedAcknowledgement(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		reply   string
		errCode int
		ok      bool
	}{
		{name: "a refreshed acknowledgement", reply: `{"refreshed":true}`, ok: true},
		{name: "an empty acknowledgement", reply: `{}`},
		{name: "a refreshed false", reply: `{"refreshed":false}`},
		{name: "a malformed acknowledgement", reply: `"refreshed"`},
		{name: "a missing method", errCode: ErrMethodNotFound, ok: true},
		{name: "another refusal", errCode: -32019},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stdin := &zcodeRecordedStdin{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
			done := make(chan error, 1)
			go func() { done <- a.refreshZCodeProviderRegistry(zcodeTestRPCTimeout) }()
			request := zcodeNthRequest(t, stdin, MethodProviderRefreshRegistry, 1)
			var params struct {
				Reason string `json:"reason"`
			}
			require.NoError(t, json.Unmarshal(request.Params, &params))
			assert.Equal(t, "session_resume", params.Reason)
			if tc.errCode != 0 {
				a.HandleOutput(zcodeErrorReplyLine(t, zcodeSentRequestID(t, request), tc.errCode, "refused"))
			} else {
				a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(tc.reply)))
			}
			err := <-done
			if tc.ok {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

// The refresh pass runs the registry refresh BEFORE the catalog read, and the
// catalog read resumes through the shared resume policy of every resume.
func TestZCodeCatalogRefresh_RefreshesTheRegistryBeforeTheCatalogRead(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	a.account = &zcodeAccountState{}
	a.Mu.Lock()
	a.model = "personal:p/A"
	a.Mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- a.awaitZCodeCatalogRefresh() }()
	registry := zcodeNthRequest(t, stdin, MethodProviderRefreshRegistry, 1)
	assert.Empty(t, stdin.RequestsFor(MethodSessionResume),
		"the catalog read starts only after the registry refresh")
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, registry), json.RawMessage(`{"refreshed":true}`)))
	resume := zcodeNthRequest(t, stdin, MethodSessionResume, 1)
	var resumeParams struct {
		Workspace    zcodeWorkspace `json:"workspace"`
		ToolDenylist []string       `json:"toolDenylist"`
	}
	require.NoError(t, json.Unmarshal(resume.Params, &resumeParams))
	assert.Equal(t, a.workspace, resumeParams.Workspace, "the refresh resume states the same workspace as every resume")
	zcodeAssertToolDenylist(t, resumeParams.ToolDenylist)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, resume), json.RawMessage(`{"session":{"sessionId":"sess-1"},"settings":{"model":{"current":{"providerId":"personal:p","modelId":"A"},"available":[{"ref":{"providerId":"personal:p","modelId":"A"}}]}}}`)))
	assert.NoError(t, <-done)
	models, current, _ := a.zcodeModelsForUI()
	require.Len(t, models, 1)
	assert.Equal(t, "personal:p/A", models[0].Id)
	assert.True(t, models[0].IsDefault)
	assert.Equal(t, "personal:p/A", current, "the refresh keeps the model the agent observes")
}

// A registry acknowledgement that is not `{refreshed:true}` fails the pass
// before the catalog read, so no stale registry serves a fresh catalog.
func TestZCodeCatalogRefresh_StopsThePassOnAMalformedRegistryAcknowledgement(t *testing.T) {
	t.Parallel()
	for _, reply := range []string{`{}`, `{"refreshed":false}`, `"refreshed"`} {
		t.Run(reply, func(t *testing.T) {
			t.Parallel()
			stdin := &zcodeRecordedStdin{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
			done := make(chan error, 1)
			go func() { done <- a.awaitZCodeCatalogRefresh() }()
			registry := zcodeNthRequest(t, stdin, MethodProviderRefreshRegistry, 1)
			a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, registry), json.RawMessage(reply)))
			assert.Error(t, <-done)
			assert.Empty(t, stdin.RequestsFor(MethodSessionResume),
				"a refused registry refresh stops the pass before the catalog read")
		})
	}
}

// A build without provider/refreshRegistry has no such capability: the pass
// keeps going and reads the catalog.
func TestZCodeCatalogRefresh_TreatsAMissingMethodAsAnAbsentCapability(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	done := make(chan error, 1)
	go func() { done <- a.awaitZCodeCatalogRefresh() }()
	registry := zcodeNthRequest(t, stdin, MethodProviderRefreshRegistry, 1)
	a.HandleOutput(zcodeErrorReplyLine(t, zcodeSentRequestID(t, registry), ErrMethodNotFound, "Method not found"))
	zcodeAnswerCatalogResume(t, a, stdin, 1, `[]`)
	assert.NoError(t, <-done)
}

// Account and workspace triggers coalesce through one owner: a burst during a
// pass requests exactly one following pass, and the waiters of one pass share
// its result.
func TestZCodeCatalogRefresh_CoalescesAccountAndWorkspaceTriggers(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	first := make(chan error, 1)
	go func() { first <- a.awaitZCodeCatalogRefresh() }()
	registry := zcodeNthRequest(t, stdin, MethodProviderRefreshRegistry, 1)
	// The burst: two account waiters and one workspace trigger, all during the pass.
	waiters := make([]chan error, 2)
	for i := range waiters {
		waiters[i] = make(chan error, 1)
		go func(ch chan error) { ch <- a.awaitZCodeCatalogRefresh() }(waiters[i])
	}
	a.scheduleZCodeCatalogRefresh()
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, registry), json.RawMessage(`{"refreshed":true}`)))
	zcodeAnswerCatalogResume(t, a, stdin, 1, `[]`)
	assert.NoError(t, <-first)
	// One following pass runs for the whole burst, and its result reaches every
	// waiter of it.
	registry = zcodeNthRequest(t, stdin, MethodProviderRefreshRegistry, 2)
	a.HandleOutput(zcodeErrorReplyLine(t, zcodeSentRequestID(t, registry), -32603, "shared failure"))
	var messages []string
	for _, ch := range waiters {
		err := <-ch
		require.Error(t, err)
		messages = append(messages, err.Error())
	}
	assert.Contains(t, messages[0], "shared failure")
	assert.Equal(t, messages[0], messages[1], "the waiters of one pass share its result")
	assert.Len(t, stdin.RequestsFor(MethodProviderRefreshRegistry), 2,
		"one following pass, not one pass per trigger")
	assert.Len(t, stdin.RequestsFor(MethodSessionResume), 1,
		"the failed following pass never reaches the catalog read")
}

// The catalog refresh folds membership and metadata only: a resume reply's mode,
// sequence and revision never move the session's own state.
func TestZCodeCatalogRefresh_FoldsMembershipWithoutTouchingModeOrSequence(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	a.Mu.Lock()
	a.model, a.thoughtLevel, a.mode, a.modeObserved, a.lastSeq = "personal:p/A", "high", "edit", true, int64(7)
	a.Mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- a.awaitZCodeCatalogRefresh() }()
	zcodeAnswerRegistryRefresh(t, a, stdin, 1)
	request := zcodeNthRequest(t, stdin, MethodSessionResume, 1)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{"session":{"sessionId":"sess-1"},"runtime":{"eventSeq":999,"stateRevision":42},"settings":{"mode":{"current":"yolo"},"model":{"current":{"providerId":"personal:p","modelId":"B"},"available":[{"ref":{"providerId":"personal:p","modelId":"A"}},{"ref":{"providerId":"personal:p","modelId":"B"}}]},"thoughtLevel":{"enabled":true,"current":"low","available":[{"value":"low"}]}}}`)))
	assert.NoError(t, <-done)
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, "edit", a.mode, "the refresh reads no mode")
	assert.Equal(t, "high", a.thoughtLevel, "the refresh reads no thought level")
	assert.Equal(t, "personal:p/A", a.model, "the refresh folds no current model")
	assert.Equal(t, int64(7), a.lastSeq, "the refresh moves no event watermark")
	assert.Zero(t, a.stateRevision, "the refresh folds no state revision")
}

// Membership and reasoning metadata are the refresh's own fold: a removed model
// leaves, an added model joins, and a changed reasoning default replaces the
// stale one.
func TestZCodeCatalogRefresh_FoldsAddedAndRemovedModelsAndReasoningDefaults(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	a.Mu.Lock()
	a.model = "personal:p/A"
	a.Mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- a.awaitZCodeCatalogRefresh() }()
	zcodeAnswerRegistryRefresh(t, a, stdin, 1)
	request := zcodeNthRequest(t, stdin, MethodSessionResume, 1)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{"session":{"sessionId":"sess-1"},"settings":{"model":{"available":[{"ref":{"providerId":"personal:p","modelId":"A"},"reasoning":{"levels":[{"value":"low"},{"value":"high"}],"defaultLevel":"low"}},{"ref":{"providerId":"personal:p","modelId":"B"},"reasoning":{"levels":[{"value":"low"},{"value":"high"}],"defaultLevel":"low"}}]}}}`)))
	assert.NoError(t, <-done)
	models, _, _ := a.zcodeModelsForUI()
	require.Len(t, models, 2)
	assert.Equal(t, "low", models[0].DefaultEffort)

	go func() { done <- a.awaitZCodeCatalogRefresh() }()
	zcodeAnswerRegistryRefresh(t, a, stdin, 2)
	request = zcodeNthRequest(t, stdin, MethodSessionResume, 2)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{"session":{"sessionId":"sess-1"},"settings":{"model":{"available":[{"ref":{"providerId":"personal:p","modelId":"B"},"reasoning":{"levels":[{"value":"low"},{"value":"high"}],"defaultLevel":"high"}},{"ref":{"providerId":"personal:p","modelId":"C"}}]}}}`)))
	assert.NoError(t, <-done)
	models, _, _ = a.zcodeModelsForUI()
	require.Len(t, models, 2)
	assert.Equal(t, "personal:p/B", models[0].Id)
	assert.Equal(t, "high", models[0].DefaultEffort, "the changed reasoning default replaces the stale one")
	assert.Equal(t, "personal:p/C", models[1].Id)
	a.Mu.Lock()
	_, removed := a.resolveZCodeModelIDLocked("personal:p/A")
	_, added := a.resolveZCodeModelIDLocked("personal:p/C")
	a.Mu.Unlock()
	assert.False(t, removed, "a removed personal model leaves the live catalog")
	assert.True(t, added, "an added personal model joins the live catalog")
}

// A catalog read whose reply arrives after a session replacement belongs to the
// replaced session: it folds nothing.
func TestZCodeCatalogRefresh_LateReadAfterASessionReplacementIsDiscarded(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	done := make(chan error, 1)
	go func() { done <- a.awaitZCodeCatalogRefresh() }()
	zcodeAnswerRegistryRefresh(t, a, stdin, 1)
	request := zcodeNthRequest(t, stdin, MethodSessionResume, 1)
	a.Mu.Lock()
	a.sessionID = "sess-2"
	a.Mu.Unlock()
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{"session":{"sessionId":"sess-1"},"settings":{"model":{"available":[{"ref":{"providerId":"personal:p","modelId":"A"}}]}}}`)))
	require.ErrorIs(t, <-done, errNoZCodeSession)
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Empty(t, a.liveModels, "a late read of a replaced session folds nothing")
}

// An explicitly empty catalog is the authoritative answer: it replaces the live
// models and manufactures none from the legacy files.
func TestZCodeCatalogRefresh_ExplicitlyEmptyCatalogStaysEmpty(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	a.account = &zcodeAccountState{}
	a.catalog = zcodeTestCatalog(t, `{"provider":{"legacy:p":{"kind":"anthropic","options":{"apiKey":"k"},"models":{"Old":{}}}}}`)
	a.Mu.Lock()
	a.model = "personal:p/A"
	a.Mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- a.awaitZCodeCatalogRefresh() }()
	zcodeAnswerRegistryRefresh(t, a, stdin, 1)
	zcodeAnswerCatalogResume(t, a, stdin, 1, `[{"ref":{"providerId":"personal:p","modelId":"A"}}]`)
	assert.NoError(t, <-done)

	go func() { done <- a.awaitZCodeCatalogRefresh() }()
	zcodeAnswerRegistryRefresh(t, a, stdin, 2)
	zcodeAnswerCatalogResume(t, a, stdin, 2, `[]`)
	assert.NoError(t, <-done)
	models, _, _ := a.zcodeModelsForUI()
	assert.Empty(t, models)
	a.Mu.Lock()
	_, found := a.resolveZCodeModelIDLocked("legacy:p/Old")
	a.Mu.Unlock()
	assert.False(t, found, "an explicitly empty catalog manufactures no model from stale legacy files")
}

// An account generation becomes catalog-refreshed only after its registry and
// catalog reads succeed.
func TestZCodeAccountCatalog_MarksTheGenerationOnlyAfterBothReadsSucceed(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	a.account = &zcodeAccountState{generation: 1, pushedGeneration: 1}
	state := a.account
	done := make(chan bool, 1)
	go func() { done <- a.refreshAccountSnapshot(state) }()
	registry := zcodeNthRequest(t, stdin, MethodProviderRefreshRegistry, 1)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, registry), json.RawMessage(`{}`)))
	require.True(t, <-done, "a registry failure must not postpone billing checks")
	assert.Zero(t, state.catalogGeneration, "a failed registry read accepts no catalog generation")

	go func() { done <- a.refreshAccountSnapshot(state) }()
	zcodeAnswerRegistryRefresh(t, a, stdin, 2)
	zcodeAnswerCatalogResume(t, a, stdin, 1, `[]`)
	read := zcodeNthRequest(t, stdin, MethodSessionRead, 1)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, read), json.RawMessage(`{}`)))
	require.True(t, <-done)
	assert.Equal(t, 1, state.catalogGeneration)
}

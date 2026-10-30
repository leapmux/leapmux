package zcode

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The `stop` acknowledgement goes through the shared v4 decoder. That decoder
// is strict on purpose: an acknowledgement for another command must never be
// taken as the stop's own verdict, and a payload with no execution id must
// never reach the wire as an unguarded stop.

func TestSendZCodeStopCommand_RejectsAnAcknowledgementForAnotherCommand(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)

	done := make(chan error, 1)
	go func() {
		_, err := a.sendZCodeStopCommand("sess-1", "exec-u")
		done <- err
	}()
	command := waitZCodeStopCommand(t, stdin)
	a.HandleOutput(zcodeReplyLine(t, command.ID, json.RawMessage(
		`{"commandId":"another","status":"accepted","revisionAtDecision":9}`)))

	err := <-done
	require.Error(t, err)
	assert.ErrorContains(t, err, "another")
}

func TestSendZCodeStopCommand_RejectsANonObjectAcknowledgement(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)

	done := make(chan error, 1)
	go func() {
		_, err := a.sendZCodeStopCommand("sess-1", "exec-u")
		done <- err
	}()
	command := waitZCodeStopCommand(t, stdin)
	a.HandleOutput(zcodeReplyLine(t, command.ID, json.RawMessage(`"accepted"`)))

	require.Error(t, <-done, "a non-object acknowledgement states no verdict of the command")
}

func TestSendZCodeStopCommand_RefusesToSendAnUnguardedStop(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)

	_, err := a.sendZCodeStopCommand("sess-1", "")
	require.Error(t, err)
	assert.Empty(t, stdin.Frames(), "an unguarded v4 stop must never reach the wire")
}

// routeZCodeRPC returns the runtime-headers handler instead of running it. The
// answer is registered before the handler exists, so a cancellation that lands
// in between suppresses the reply.
func TestRouteZCodeRPC_TheRuntimeHeaderHandlerRunsAfterItsRegistration(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)
	const logical = "sess-1:provider-runtime-headers:91919191-9292-4393-8494-959595959595"

	handler, consumed := a.routeZCodeRPC(providerkit.ParseLine(zcodeRuntimeHeadersLine(t,
		"server-1", logical, "sess-1", zcodeTestRuntimeWorkspace, "builtin:zai")))
	require.True(t, consumed, "a server request is consumed by its handler")
	require.NotNil(t, handler)
	assert.Empty(t, stdin.Frames(), "routeZCodeRPC returns the handler without running it")

	// The registration already exists: this cancellation suppresses the reply
	// even though the handler has not run yet.
	a.HandleOutput(zcodeRuntimeHeadersCancelLine(t, logical, "sess-1", zcodeTestRuntimeWorkspace))
	handler()
	assert.Empty(t, stdin.Frames(), "the app-server withdrew the request before the handler ran")

	// Without a cancellation the handler answers.
	handler2, _ := a.routeZCodeRPC(providerkit.ParseLine(zcodeRuntimeHeadersLine(t,
		"server-2", logical+"-2", "sess-1", zcodeTestRuntimeWorkspace, "builtin:zai")))
	handler2()
	replies := stdin.Requests(t)
	require.Len(t, replies, 1)
	assert.Equal(t, `"server-2"`, string(replies[0].ID))
}

// The registration of a runtime-headers answer exists BEFORE its handler runs,
// keyed by the logical request id and addressed by the envelope id.
func TestRouteZCodeRPC_RegistersTheRuntimeHeaderAnswerBeforeTheHandlerRuns(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)
	const logical = "sess-1:provider-runtime-headers:bbbb2222-3333-4444-8555-666666666666"

	handler, _ := a.routeZCodeRPC(providerkit.ParseLine(zcodeRuntimeHeadersLine(t,
		"server-1", logical, "sess-1", zcodeTestRuntimeWorkspace, "builtin:zai")))
	require.NotNil(t, handler)

	a.runtimeHeaderMu.Lock()
	answer := a.runtimeHeaderAnswers[logical]
	a.runtimeHeaderMu.Unlock()
	require.NotNil(t, answer, "the answer is registered before the handler runs")
	assert.Equal(t, zcodeRuntimeHeaderPending, answer.state)
	assert.Equal(t, `"server-1"`, string(answer.envelopeID), "the envelope id is the reply address")
	assert.Empty(t, stdin.Frames())

	handler()
	_ = zcodeAwaitReplies(t, stdin, 1)
	a.runtimeHeaderMu.Lock()
	remaining := len(a.runtimeHeaderAnswers)
	a.runtimeHeaderMu.Unlock()
	assert.Zero(t, remaining, "the answer removes its registration after its reply")
}

type zcodeReplacingParams struct{ replace func() }

func (params zcodeReplacingParams) MarshalJSON() ([]byte, error) {
	params.replace()
	return []byte(`{"sessionId":"sess-1"}`), nil
}

func TestZCodeSessionRequest_ValidatesItsLifetimeAfterEncoding(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	a.Mu.Lock()
	identity := a.captureZCodeObservationIdentityLocked()
	a.Mu.Unlock()
	sent := 0
	zcodeRespondToRequestsForTest(t, a, func(zcodeSentRequest) json.RawMessage { sent++; return json.RawMessage(`{}`) })
	params := zcodeReplacingParams{replace: func() { a.Mu.Lock(); a.sessionEpoch++; a.Mu.Unlock() }}
	_, err := a.sendZCodeSessionRequest(a.Context(), identity, MethodSessionSend, params, zcodeTestRPCTimeout, nil)
	require.ErrorIs(t, err, agent.ErrInputSessionChanged)
	assert.Zero(t, sent)
}

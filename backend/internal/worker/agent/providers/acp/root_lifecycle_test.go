package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFollowUpPromptWaitsForANativeTurnAcceptedDuringSelection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		out := &agenttest.Stdin{}
		b, sink := newACPTurnBase(t, out)
		ctx, cancel := context.WithCancel(t.Context())
		b.SetContextForTest(ctx)
		defer func() { b.SimulateExitForTest(); b.Process.Stop(); cancel() }()
		const secondEnd = `{"jsonrpc":"2.0","method":"_vendor/turn_ended","params":{"sessionId":"session-1","reason":"end_turn","turn":"second"}}`
		hookCalls := 0
		b.hooks.FollowUpPrompt = func(sessionID string, stopped bool) (string, []*leapmuxv1.Attachment, bool) {
			assert.Equal(t, "session-1", sessionID)
			assert.False(t, stopped)
			hookCalls++
			if hookCalls != 1 {
				return "", nil, false
			}
			require.True(t, b.BeginAgentTurn())
			b.HandleOutput(acptest.Chunk("session-1", "agent_message_chunk", "The native turn runs before the retained input."))
			return "Retain this exact input for after the native turn.", nil, true
		}
		require.True(t, b.BeginAgentTurn())
		b.EndAgentTurn(json.RawMessage(agentTurnEndFrame))
		synctest.Wait()
		assert.Empty(t, out.String(), "selected input must wait for accepted native work")
		assert.True(t, b.AgentTurnActive())
		assert.True(t, b.PromptActive())
		b.EndAgentTurn(json.RawMessage(secondEnd))
		synctest.Wait()
		assert.Equal(t, 1, strings.Count(out.String(), `"method":"session/prompt"`))
		assert.Contains(t, out.String(), "Retain this exact input for after the native turn.")
		assert.True(t, b.PromptActive())
		b.HandleJSONRPCResponseForTest(promptResponse(1))
		synctest.Wait()
		ends := turnEndContents(sink)
		require.Len(t, ends, 3)
		assert.JSONEq(t, agentTurnEndFrame, ends[0])
		assert.JSONEq(t, secondEnd, ends[1])
		assert.JSONEq(t, `{"stopReason":"end_turn"}`, ends[2])
		assert.False(t, b.PromptActive())
	})
}

func TestFailedFollowUpPreparationCannotClearTheNativeTurnItAccepted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		out := &agenttest.Stdin{}
		b, sink := newACPTurnBase(t, out)
		ctx, cancel := context.WithCancel(t.Context())
		b.SetContextForTest(ctx)
		defer func() { b.SimulateExitForTest(); b.Process.Stop(); cancel() }()
		hookCalls := 0
		b.hooks.FollowUpPrompt = func(string, bool) (string, []*leapmuxv1.Attachment, bool) {
			hookCalls++
			return "the selected input", nil, hookCalls == 1
		}
		b.hooks.PreparePrompt = func(string, []agent.ClassifiedAttachment, []map[string]any) error {
			assert.True(t, b.BeginAgentTurn())
			b.HandleOutput(acptest.Chunk("session-1", "agent_message_chunk", "The accepted native turn keeps its output."))
			return assert.AnError
		}
		require.True(t, b.BeginAgentTurn())
		b.EndAgentTurn(json.RawMessage(agentTurnEndFrame))
		synctest.Wait()
		assert.Empty(t, out.String())
		assert.True(t, b.AgentTurnActive())
		assert.True(t, b.PromptActive())
		b.EndAgentTurn(json.RawMessage(agentTurnEndFrame))
		synctest.Wait()
		assert.Equal(t, []string{"text:The accepted native turn keeps its output."}, assembledTexts(t, sink.Messages()))
		assert.False(t, b.PromptActive())
	})
}

func TestRetainedFollowUpWaitsForNativeWorkAcceptedBeforeAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		out := &agenttest.Stdin{}
		b, _ := newACPTurnBase(t, out)
		ctx, cancel := context.WithCancel(t.Context())
		b.SetContextForTest(ctx)
		defer func() { b.SimulateExitForTest(); b.Process.Stop(); cancel() }()
		batch := newACPFollowUpPrompt("session-1", "Retain this batch until native work ends.", nil)
		b.pendingFollowUp, b.promptActive, b.promptOutputDrained = batch, true, true
		b.sessionMu.Lock()
		require.True(t, b.schedulePendingFollowUp("session-1"))
		require.True(t, b.BeginAgentTurn())
		b.HandleOutput(acptest.Chunk("session-1", "agent_message_chunk", "Native work accepted before continuation admission."))
		b.sessionMu.Unlock()
		synctest.Wait()
		assert.Empty(t, out.String())
		assert.True(t, b.AgentTurnActive())
		assert.True(t, b.PromptActive())
		b.Mu.Lock()
		retained, scheduled := b.pendingFollowUp, batch.scheduled
		b.Mu.Unlock()
		assert.Same(t, batch, retained)
		assert.False(t, scheduled)
		b.EndAgentTurn(json.RawMessage(agentTurnEndFrame))
		synctest.Wait()
		assert.Equal(t, 1, strings.Count(out.String(), `"method":"session/prompt"`))
		assert.Contains(t, out.String(), "Retain this batch until native work ends.")
		b.HandleJSONRPCResponseForTest(promptResponse(1))
		synctest.Wait()
		assert.False(t, b.PromptActive())
	})
}

func TestRetiredFollowUpRunnerCannotClaimAReplacementBatch(t *testing.T) {
	for _, replacementSession := range []string{"session-1", "session-2"} {
		t.Run(replacementSession, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				out := &agenttest.Stdin{}
				b, sink := newACPTurnBase(t, out)
				ctx, cancel := context.WithCancel(t.Context())
				b.SetContextForTest(ctx)
				defer func() { b.SimulateExitForTest(); b.Process.Stop(); cancel() }()
				old := newACPFollowUpPrompt("session-1", "Retired input must never be sent.", nil)
				b.pendingFollowUp, b.promptActive, b.promptOutputDrained = old, true, true
				b.sessionMu.Lock()
				require.True(t, b.schedulePendingFollowUp("session-1"))
				replacement := newACPFollowUpPrompt(replacementSession, "Send only the replacement batch.", nil)
				b.Mu.Lock()
				b.sessionID = replacementSession
				b.resetTurnStateLocked()
				b.pendingFollowUp, b.promptActive, b.promptOutputDrained = replacement, true, true
				b.Mu.Unlock()
				sink.UpdateSessionID(replacementSession)
				require.True(t, b.schedulePendingFollowUp(replacementSession))
				b.sessionMu.Unlock()
				synctest.Wait()
				assert.Equal(t, 1, strings.Count(out.String(), `"method":"session/prompt"`))
				assert.NotContains(t, out.String(), "Retired input must never be sent.")
				assert.Contains(t, out.String(), "Send only the replacement batch.")
				assert.Empty(t, sink.LeapMuxNotifications())
				assert.True(t, b.PromptActive())
				b.HandleJSONRPCResponseForTest(promptResponse(1))
				synctest.Wait()
				assert.False(t, b.PromptActive())
			})
		})
	}
}

type followUpFailureGateSink struct {
	agent.ProviderServices
	gate *acpLifecycleGate
}

func (sink *followUpFailureGateSink) PersistLeapMuxNotification(content map[string]interface{}) {
	sink.gate.pause()
	sink.ProviderServices.PersistLeapMuxNotification(content)
}

func TestFollowUpAdmissionIncludesItsFailurePublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, sink := newACPTurnBase(t, &agenttest.Stdin{})
		ctx, cancel := context.WithCancel(t.Context())
		b.SetContextForTest(ctx)
		defer func() { b.SimulateExitForTest(); b.Process.Stop(); cancel() }()
		gate := newACPLifecycleGate(t)
		b.sink = &followUpFailureGateSink{ProviderServices: agent.NewProviderServices(sink), gate: gate}
		b.hooks.PreparePrompt = func(string, []agent.ClassifiedAttachment, []map[string]any) error { return assert.AnError }
		b.pendingFollowUp = newACPFollowUpPrompt("session-1", "Fail before the native write.", nil)
		b.promptActive, b.promptOutputDrained = true, true
		require.True(t, b.schedulePendingFollowUp("session-1"))
		<-gate.entered
		b.rootCallbacks.closeAdmission()
		joined := make(chan struct{})
		go func() { b.rootCallbacks.wait(); close(joined) }()
		synctest.Wait()
		select {
		case <-joined:
			t.Error("shutdown joined before the admitted failure publication finished")
		default:
		}
		gate.open()
		<-joined
		synctest.Wait()
		require.Len(t, sink.LeapMuxNotifications(), 1)
	})
}

type acpLifecycleGate struct {
	entered, release         chan struct{}
	enteredOnce, releaseOnce sync.Once
}

func newACPLifecycleGate(t *testing.T) *acpLifecycleGate {
	t.Helper()
	gate := &acpLifecycleGate{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(gate.open)
	return gate
}
func (gate *acpLifecycleGate) pause() {
	gate.enteredOnce.Do(func() { close(gate.entered); <-gate.release })
}
func (gate *acpLifecycleGate) open() { gate.releaseOnce.Do(func() { close(gate.release) }) }
func waitACPLifecycleSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal(description)
	}
}

type acpLifecycleGateSink struct {
	agent.ProviderServices
	resetGate, endGate, messageGate *acpLifecycleGate
	needle                          []byte
}

func (sink *acpLifecycleGateSink) ResetSpans() {
	if sink.resetGate != nil {
		sink.resetGate.pause()
	}
	sink.ProviderServices.ResetSpans()
}
func (sink *acpLifecycleGateSink) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	err := sink.ProviderServices.PersistTurnEnd(content, span)
	if err == nil && sink.endGate != nil {
		sink.endGate.pause()
	}
	return err
}
func (sink *acpLifecycleGateSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	err := sink.ProviderServices.PersistMessage(source, content, span)
	if err == nil && sink.messageGate != nil && bytes.Contains(content.Original, sink.needle) {
		sink.messageGate.pause()
	}
	return err
}

func TestPromptSpanResetCannotEraseANativeTurnThatStartsConcurrently(t *testing.T) {
	b, sink := newACPTurnBase(t, &agenttest.Stdin{})
	gate := newACPLifecycleGate(t)
	b.sink = &acpLifecycleGateSink{ProviderServices: agent.NewProviderServices(sink), resetGate: gate}
	b.promptActive = true
	ended := make(chan struct{})
	go func() { defer close(ended); b.persistFinishedTurn(json.RawMessage(`{"stopReason":"end_turn"}`)) }()
	waitACPLifecycleSignal(t, gate.entered, "the old divider did not reach its span reset")
	startNative := func() bool {
		ok := b.BeginAgentTurn()
		if ok {
			b.HandleOutput(toolCallFrame(`{"sessionUpdate":"tool_call","toolCallId":"native-new-tool","title":"Read","kind":"read","status":"pending"}`))
		}
		return ok
	}
	if b.turnMu.TryLock() {
		// The old implementation permits a complete new opener inside its reset.
		b.turnMu.Unlock()
		require.True(t, startNative())
		gate.open()
	} else {
		started := make(chan bool, 1)
		go func() { started <- startNative() }()
		gate.open()
		require.True(t, <-started)
	}
	waitACPLifecycleSignal(t, ended, "the old divider did not finish")
	assert.Equal(t, "read", sink.GetSpanType("native-new-tool"))
}

func TestNativeTurnStartingAfterDetachmentKeepsItsOwnOutput(t *testing.T) {
	for _, withDivider := range []bool{false, true} {
		t.Run(map[bool]string{false: "without divider", true: "with divider"}[withDivider], func(t *testing.T) {
			b, sink := newACPTurnBase(t, &agenttest.Stdin{})
			gate := newACPLifecycleGate(t)
			b.sink = &acpLifecycleGateSink{ProviderServices: agent.NewProviderServices(sink), messageGate: gate, needle: []byte("OLD-NATIVE-TEXT")}
			require.True(t, b.BeginAgentTurn())
			b.HandleOutput(acptest.Chunk("session-1", "agent_message_chunk", "OLD-NATIVE-TEXT"))
			ended := make(chan struct{})
			go func() {
				defer close(ended)
				if withDivider {
					b.EndAgentTurn(json.RawMessage(agentTurnEndFrame))
				} else {
					b.EndAgentTurnWithoutRow()
				}
			}()
			waitACPLifecycleSignal(t, gate.entered, "the old native end did not detach its output")
			require.True(t, b.BeginAgentTurn())
			b.HandleOutput(acptest.Chunk("session-1", "agent_message_chunk", "NEW-NATIVE-TEXT"))
			gate.open()
			waitACPLifecycleSignal(t, ended, "the old native end did not finish")
			require.True(t, b.AgentTurnActive())
			b.EndAgentTurn(json.RawMessage(agentTurnEndFrame))
			assert.Equal(t, []string{"text:OLD-NATIVE-TEXT", "text:NEW-NATIVE-TEXT"}, assembledTexts(t, sink.Messages()))
			b.turnMu.Lock()
			remaining := b.turnAssistantText.String()
			b.turnMu.Unlock()
			assert.Empty(t, remaining)
		})
	}
}

func TestNativeEndCannotClearTheReplacementSessionsTurn(t *testing.T) {
	a, sink, _ := newRetiringTestAgent(t)
	gate := newACPLifecycleGate(t)
	a.sink = &acpLifecycleGateSink{ProviderServices: agent.NewProviderServices(sink), endGate: gate}
	require.True(t, a.BeginAgentTurn())
	ended := make(chan struct{})
	go func() { defer close(ended); a.EndAgentTurn(json.RawMessage(agentTurnEndFrame)) }()
	waitACPLifecycleSignal(t, gate.entered, "the old native end did not persist")
	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	require.Equal(t, "session-2", sessionID)
	require.True(t, a.BeginAgentTurn())
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-2","update":{"sessionUpdate":"tool_call","toolCallId":"replacement-tool","title":"Read","kind":"read","status":"pending"}}}`))
	gate.open()
	waitACPLifecycleSignal(t, ended, "the old native end did not finish")
	assert.Equal(t, "session-2", a.CurrentSessionID())
	assert.True(t, a.PromptActive())
	assert.True(t, a.AgentTurnActive())
	assert.Equal(t, "read", sink.GetSpanType("replacement-tool"))
}

func TestNativeEndCapturesCleanupBeforeTheSessionCanChange(t *testing.T) {
	a, sink, _ := newRetiringTestAgent(t)
	gate := newACPLifecycleGate(t)
	a.sink = &acpLifecycleGateSink{ProviderServices: agent.NewProviderServices(sink), endGate: gate}
	var mu sync.Mutex
	var captured []string
	a.hooks.CaptureTurnOutputCleanup = func() func() {
		sessionID := a.sessionID
		mu.Lock()
		captured = append(captured, sessionID)
		mu.Unlock()
		return func() {}
	}
	require.True(t, a.BeginAgentTurn())
	ended := make(chan struct{})
	go func() { defer close(ended); a.EndAgentTurn(json.RawMessage(agentTurnEndFrame)) }()
	waitACPLifecycleSignal(t, gate.entered, "the native end did not reach persistence")
	_, err := a.ClearContext()
	require.NoError(t, err)
	require.True(t, a.BeginAgentTurn())
	gate.open()
	waitACPLifecycleSignal(t, ended, "the old native end did not finish")
	mu.Lock()
	actual := append([]string(nil), captured...)
	mu.Unlock()
	assert.NotEmpty(t, actual)
	for _, sessionID := range actual {
		assert.Equal(t, "session-1", sessionID)
	}
}

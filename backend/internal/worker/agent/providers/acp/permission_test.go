package acp

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func TestBypassCannotApproveARequestFromARetiredSession(t *testing.T) {
	a, sink, requests := newRetiringTestAgent(t)
	_, err := a.ClearContext()
	require.NoError(t, err)
	checks := 0
	a.hooks.WithBypassPermission = func(claim func()) { checks++; claim() }
	a.hooks.PermissionAllowsBypass = func(*providerkit.ParsedLine) bool { return true }
	frame := []byte(`{"jsonrpc":"2.0","id":73,"method":"session/request_permission","params":{"sessionId":"session-1","options":[{"optionId":"allow","kind":"allow_once"}]}}`)
	a.PublishSessionControlRequest(providerkit.ParseLine(frame), acpPermissionCancelAnswer())
	syncTestPeer(t, a)
	assert.Zero(t, checks, "session ownership precedes every standing-policy check")
	assert.Empty(t, sink.PublishedControls())
	assert.JSONEq(t, `{"outcome":{"outcome":"cancelled"}}`, agenttest.JSONRPCResultsByID(t, rawLinesOf(requests()))["73"])
}

func TestPermissionApprovalAnswerRequiresOneUnambiguousOfferedOption(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`not JSON`, `null`, `{}`, `{"options":null}`, `{"options":"allow"}`,
		`{"options":[{"optionId":"yes","kind":"allow_always"}]}`,
		`{"options":[{"optionId":"","kind":"allow_once"}]}`,
		`{"options":[{"optionId":"yes","kind":"allow_once"},{"optionId":"yes","kind":"reject_once"}]}`,
		`{"options":[{"optionId":"yes","kind":"allow_once"},{"optionId":"other","kind":"allow_once"}]}`,
	} {
		assert.Nil(t, permissionApprovalAnswer(json.RawMessage(raw)), raw)
	}
	answer := permissionApprovalAnswer(json.RawMessage(`{"options":[{"optionId":"no","kind":"reject_once"},{"optionId":"native:once","kind":"allow_once"}]}`))
	encoded, err := json.Marshal(answer)
	require.NoError(t, err)
	assert.JSONEq(t, `{"outcome":{"outcome":"selected","optionId":"native:once"}}`, string(encoded))
}

func TestWithOptionValueClaimsUnderTheSettingsLock(t *testing.T) {
	t.Parallel()
	b, _ := newACPTurnBase(t, &agenttest.Stdin{})
	b.permissionMode = "test-bypass"
	b.options.values = map[string]string{"approval": "test-bypass"}
	count := 0
	claim := func() {
		count++
		unlocked := b.Mu.TryLock()
		if unlocked {
			b.Mu.Unlock()
		}
		assert.False(t, unlocked, "a settings change cannot pass the authorization check before the claim")
	}
	b.WithOptionValue(agent.OptionIDPermissionMode, "test-bypass", claim)
	b.WithOptionValue("approval", "test-bypass", claim)
	b.WithOptionValue("approval", "", claim)
	b.WithOptionValue("missing", "test-bypass", claim)
	b.WithOptionValue(agent.OptionIDPermissionMode, "ask", claim)
	assert.Equal(t, 2, count)
}

// controlAnswerRecorder records each ControlRequestAnswered report.
type controlAnswerRecorder struct {
	mu      sync.Mutex
	reports []bool
}

func (r *controlAnswerRecorder) record(_ *providerkit.ParsedLine, queued bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reports = append(r.reports, queued)
}

func (r *controlAnswerRecorder) all() []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bool(nil), r.reports...)
}

func rejectOnceAnswer() any {
	return map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": "reject_once"}}
}

func TestAnswerControlRequest_AQueuedAnswerIsNotPublished(t *testing.T) {
	t.Parallel()
	a, sink, requests := newRetiringTestAgent(t)
	recorder := &controlAnswerRecorder{}
	a.hooks.AnswerControlRequest = func(*providerkit.ParsedLine) (any, bool) { return rejectOnceAnswer(), true }
	a.hooks.ControlRequestAnswered = recorder.record

	a.PublishSessionControlRequest(providerkit.ParseLine(permissionRequest(t, 80, "session-1")), acpPermissionCancelAnswer())
	syncTestPeer(t, a)

	assert.Empty(t, sink.PublishedControls(), "the provider's own answer needs no card")
	assert.Equal(t, []bool{true}, recorder.all())
	assert.JSONEq(t, `{"outcome":{"outcome":"selected","optionId":"reject_once"}}`,
		agenttest.JSONRPCResultsByID(t, rawLinesOf(requests()))["80"])
}

func TestAnswerControlRequest_ADeclinedRequestIsPublishedForTheReader(t *testing.T) {
	t.Parallel()
	a, sink, requests := newRetiringTestAgent(t)
	recorder := &controlAnswerRecorder{}
	a.hooks.AnswerControlRequest = func(*providerkit.ParsedLine) (any, bool) { return nil, false }
	a.hooks.ControlRequestAnswered = recorder.record

	a.PublishSessionControlRequest(providerkit.ParseLine(permissionRequest(t, 81, "session-1")), acpPermissionCancelAnswer())
	syncTestPeer(t, a)

	require.Len(t, sink.PublishedControls(), 1)
	assert.Empty(t, recorder.all(), "a declined request reports no answer")
	assert.Equal(t, -1, indexOfAnswer(t, requests(), 81), "the reader answers it")
}

// An id that the base cannot register is the one way this rig makes the
// queueing fail before any write: the shutdown and full-queue refusals live in
// the process internals of providerkit, which its own tests cover.
func TestAnswerControlRequest_AnUnqueuedAnswerReportsQueuedFalseAndWritesNothing(t *testing.T) {
	t.Parallel()
	a, _, requests := newRetiringTestAgent(t)
	recorder := &controlAnswerRecorder{}
	a.hooks.AnswerControlRequest = func(*providerkit.ParsedLine) (any, bool) { return rejectOnceAnswer(), true }
	a.hooks.ControlRequestAnswered = recorder.record

	frame := []byte(`{"jsonrpc":"2.0","id":{"not":"an id"},"method":"session/request_permission","params":{"sessionId":"session-1"}}`)
	a.PublishSessionControlRequest(providerkit.ParseLine(frame), acpPermissionCancelAnswer())
	syncTestPeer(t, a)

	assert.Equal(t, []bool{false}, recorder.all())
	for _, request := range requests() {
		assert.NotContains(t, request.Raw, "reject_once", "no answer reaches the agent")
	}
}

// After a definite delivery failure the base publishes the original request,
// so the reader can still answer it. The provider's answer then stays the
// approval answer of the card, which the bypass sweep may send later.
func TestAnswerControlRequest_ADeliveryFailurePublishesTheRequest(t *testing.T) {
	t.Parallel()
	a, sink, _ := newRetiringTestAgent(t)
	recorder := &controlAnswerRecorder{}
	a.hooks.AnswerControlRequest = func(*providerkit.ParsedLine) (any, bool) {
		return map[string]any{"unencodable": make(chan int)}, true
	}
	a.hooks.ControlRequestAnswered = recorder.record

	a.PublishSessionControlRequest(providerkit.ParseLine(permissionRequest(t, 82, "session-1")), acpPermissionCancelAnswer())

	assert.Equal(t, []bool{true}, recorder.all(), "the answer was queued")
	testutil.RequireEventually(t, func() bool { return len(sink.PublishedControls()) == 1 })
}

func TestAnswerControlRequest_AForeignSessionIsRefusedBeforeTheHook(t *testing.T) {
	t.Parallel()
	a, sink, requests := newRetiringTestAgent(t)
	_, err := a.ClearContext()
	require.NoError(t, err)
	var asked atomic.Bool
	a.hooks.AnswerControlRequest = func(*providerkit.ParsedLine) (any, bool) {
		asked.Store(true)
		return rejectOnceAnswer(), true
	}

	a.PublishSessionControlRequest(providerkit.ParseLine(permissionRequest(t, 83, "session-1")), acpPermissionCancelAnswer())
	syncTestPeer(t, a)

	assert.False(t, asked.Load(), "session ownership precedes the provider's answer")
	assert.Empty(t, sink.PublishedControls())
	assert.JSONEq(t, `{"outcome":{"outcome":"cancelled"}}`, agenttest.JSONRPCResultsByID(t, rawLinesOf(requests()))["83"])
}

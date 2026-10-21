package zcode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ZCode steers through the session mailbox and through nothing else. The
// app-server protocol offers no steering input: session/send is the only input
// method, its schema is strict (no delivery, no intent), and its handler
// refuses with -32010 while a prompt runs, before any runtime admission.
// Verified against ZCode 3.16.2 (CLI 0.17.0) by disassembly of
// Resources/glm/zcode.cjs and by a live probe that held a turn inside a Bash
// call: a plain mid-turn session/send returned -32010, and sends carrying
// intent or delivery fields returned -32602 unrecognized keys with no steer
// events and no queued input left behind. The mailbox path the same probe
// proved (turn.steerQueued, turn.steerDrained, the message on every follow-up
// model request) is what steering.go drives; the tests below pin its contract.

// zcodeSteerSessionID satisfies the drain's `^sess_[A-Za-z0-9._-]+$` directory
// rule, which the harness default "sess-1" does not.
const zcodeSteerSessionID = "sess_steering-1"

// newZCodeSteerTestAgent is a test agent whose mailbox drain has already
// proved itself and whose user turn is running: the state SteerInput needs.
func newZCodeSteerTestAgent(t *testing.T, sink agent.ProviderServices) *Agent {
	t.Helper()
	a := newZCodeTestAgent(t, sink)
	a.mailboxRoot = t.TempDir()
	a.Mu.Lock()
	a.sessionID = zcodeSteerSessionID
	a.mailboxActive = true
	a.turnActive = true
	a.Mu.Unlock()
	return a
}

// zcodeUnreadEnvelopes lists the envelope files the next drain would read, in
// the drain's own order, and fails on any leftover that is not one.
func zcodeUnreadEnvelopes(t *testing.T, a *Agent) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(a.mailboxRoot, zcodeSteerSessionID, zcodeMailboxUnreadDir))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var names []string
	for _, entry := range entries {
		require.True(t, strings.HasSuffix(entry.Name(), ".json"), "a temporary name must never be left behind: %s", entry.Name())
		names = append(names, entry.Name())
	}
	return names
}

func zcodeReadEnvelope(t *testing.T, a *Agent, name string) zcodeMailboxEnvelope {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(a.mailboxRoot, zcodeSteerSessionID, zcodeMailboxUnreadDir, name))
	require.NoError(t, err)
	var envelope zcodeMailboxEnvelope
	require.NoError(t, json.Unmarshal(raw, &envelope))
	return envelope
}

// zcodeSteerEventPayload builds the steerQueued or steerDrained payload the
// runtime sends for a drained envelope, wrapped exactly as its drain wraps it.
func zcodeSteerEventPayload(t *testing.T, eventType string, envelope zcodeMailboxEnvelope) string {
	t.Helper()
	wrapped := zcodeMailboxWrappedInput(envelope)
	var payload any
	switch eventType {
	case contracts.ZCodeEventTurnSteerQueued:
		payload = map[string]any{"pendingInputId": "pending_1", "input": wrapped, "delivery": "guide", "queueLength": 1}
	case contracts.ZCodeEventTurnSteerDrained:
		payload = map[string]any{
			"injectedMessageIds": []string{"msg_1"},
			"drainedInputs":      []map[string]any{{"pendingInputId": "pending_1", "messageId": "msg_1", "text": wrapped, "delivery": "guide"}},
		}
	default:
		t.Fatalf("not a steer event: %s", eventType)
	}
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	return string(encoded)
}

func zcodeSteerEventLine(t *testing.T, seq int64, eventType, payload string) []byte {
	t.Helper()
	line := zcodeEventLine(t, seq, eventType, payload)
	// The harness stamps "sess-1"; the steering session is the one the agent runs.
	return []byte(strings.Replace(string(line), `"sessionId":"sess-1"`, `"sessionId":"`+zcodeSteerSessionID+`"`, 1))
}

func TestZCodeSteerInputWritesTheEnvelopeTheDrainReads(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeSteerTestAgent(t, agent.NewProviderServices(sink))

	require.NoError(t, a.SteerInput("Stop and summarize first.", nil))

	names := zcodeUnreadEnvelopes(t, a)
	require.Len(t, names, 1)
	envelope := zcodeReadEnvelope(t, a, names[0])
	assert.Equal(t, 1, envelope.Version)
	assert.Equal(t, zcodeSteerSessionID, envelope.ToSessionID)
	assert.True(t, strings.HasPrefix(envelope.MessageID, zcodeMailboxMessageIDPrefix), envelope.MessageID)
	assert.Equal(t, "sess_leapmux_test-agent", envelope.FromSessionID)
	assert.Regexp(t, `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`, envelope.CreatedAt)
	// The drain presents the content as reference material from another
	// session; the content itself must say it is the user's steer.
	assert.Contains(t, envelope.Content, "The user sent a new message while you were working:\n\nStop and summarize first.")
	assert.Contains(t, envelope.Content, "Address the message above as you continue this turn.")
	assert.Contains(t, envelope.Content, `the "For reference only" line above does not apply`)

	a.Mu.Lock()
	tracked := a.mailboxSteers[envelope.MessageID]
	a.Mu.Unlock()
	require.NotNil(t, tracked)
	assert.Equal(t, names[0], tracked.file)
	assert.Empty(t, tracked.pendingInputID, "the runtime has not admitted it yet")
	assert.Empty(t, sink.SteerEchoReports(), "nothing is reported before the runtime states an outcome")
}

func TestZCodeSteerInputNamesEnvelopesInMintOrder(t *testing.T) {
	t.Parallel()
	a := newZCodeSteerTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))

	require.NoError(t, a.SteerInput("first", nil))
	require.NoError(t, a.SteerInput("second", nil))

	names := zcodeUnreadEnvelopes(t, a)
	require.Len(t, names, 2)
	// The drain reads names in sort order, so the names must sort as minted
	// even when both land in the same millisecond.
	assert.Less(t, names[0], names[1])
	assert.Contains(t, zcodeReadEnvelope(t, a, names[0]).Content, "first")
	assert.Contains(t, zcodeReadEnvelope(t, a, names[1]).Content, "second")
}

func TestZCodeSteerInputRefusesAttachmentsTheMailboxCannotCarry(t *testing.T) {
	t.Parallel()
	a := newZCodeSteerTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	// No active turn: the refusal must depend on the input alone, so the
	// queue learns "next turn" rather than "turn ended" for this input.
	a.Mu.Lock()
	a.turnActive = false
	a.Mu.Unlock()

	err := a.SteerInput("look at this", []*leapmuxv1.Attachment{{Filename: "shot.png", MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}}})
	require.ErrorIs(t, err, agent.ErrSteeringUnsupported)
	assert.Contains(t, err.Error(), `"shot.png" goes in the next turn`)
	assert.Empty(t, zcodeUnreadEnvelopes(t, a))

	err = a.SteerInput("read this", []*leapmuxv1.Attachment{{Filename: "paper.pdf", MimeType: "application/pdf", Data: []byte("%PDF-1.4")}})
	require.ErrorIs(t, err, agent.ErrSteeringUnsupported)
	assert.Empty(t, zcodeUnreadEnvelopes(t, a))
}

func TestZCodeSteerInputInlinesATextAttachment(t *testing.T) {
	t.Parallel()
	a := newZCodeSteerTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))

	require.NoError(t, a.SteerInput("use these notes", []*leapmuxv1.Attachment{{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("line one\nline two")}}))

	names := zcodeUnreadEnvelopes(t, a)
	require.Len(t, names, 1)
	content := zcodeReadEnvelope(t, a, names[0]).Content
	assert.Contains(t, content, "use these notes")
	assert.Contains(t, content, "notes.txt")
	assert.Contains(t, content, "line one\nline two")
}

func TestZCodeSteerInputNeedsAnActiveTurn(t *testing.T) {
	t.Parallel()
	a := newZCodeSteerTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	a.Mu.Lock()
	a.turnActive = false
	a.Mu.Unlock()
	require.ErrorIs(t, a.SteerInput("now", nil), agent.ErrNoActiveTurn)

	// A compaction alone keeps the queue busy, and is still not a turn to steer.
	a.Mu.Lock()
	a.compactionSessionID = zcodeSteerSessionID
	a.Mu.Unlock()
	require.ErrorIs(t, a.SteerInput("now", nil), agent.ErrNoActiveTurn)
	assert.Empty(t, zcodeUnreadEnvelopes(t, a))
}

func TestZCodeSteerInputNeedsTheDiscoveredMailbox(t *testing.T) {
	t.Parallel()
	a := newZCodeSteerTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	a.Mu.Lock()
	a.mailboxActive = false
	a.Mu.Unlock()

	assert.False(t, a.SupportsSteering())
	require.ErrorIs(t, a.SteerInput("now", nil), agent.ErrSteeringUnsupported)
	assert.Empty(t, zcodeUnreadEnvelopes(t, a))
}

func TestZCodeSteerInputRefusesASessionTheDrainCannotResolve(t *testing.T) {
	t.Parallel()
	a := newZCodeSteerTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	a.Mu.Lock()
	a.sessionID = "sess-1"
	a.Mu.Unlock()

	err := a.SteerInput("now", nil)
	require.ErrorIs(t, err, agent.ErrSteeringUnsupported)
	_, statErr := os.Stat(filepath.Join(a.mailboxRoot, "sess-1"))
	assert.True(t, os.IsNotExist(statErr), "no envelope for a session the runtime's drain would throw on")
}

func TestZCodeSteerInputRefusesAnInputTheRuntimeWouldReject(t *testing.T) {
	t.Parallel()
	a := newZCodeSteerTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))

	err := a.SteerInput(strings.Repeat("x", zcodeSteerInputMaxBytes), nil)
	require.Error(t, err)
	assert.NotErrorIs(t, err, agent.ErrSteeringUnsupported, "an oversized input is not a next-turn candidate either")
	assert.Contains(t, err.Error(), "200000")
	assert.Empty(t, zcodeUnreadEnvelopes(t, a))
}

// The size admission and the event correlation both read the wrapped input,
// so the rendering must match the runtime's formatter byte for byte. The
// expectation is the `input` field of the turn.steerQueued event probe run10
// recorded, including the escape the runtime applies to each attribute.
func TestZCodeMailboxWrappedInputMatchesTheRuntimeFormatter(t *testing.T) {
	t.Parallel()
	wrapped := zcodeMailboxWrappedInput(zcodeMailboxEnvelope{
		MessageID:     "leapmux-1792573924078",
		FromSessionID: "sess_leapmux-external-steer",
		CreatedAt:     "2026-10-21T09:12:04.078Z",
		Content:       "MAILBOXSTEER The user says: stop what you are doing and reply with the single word STEERED.",
	})
	assert.Equal(t, "<session-message source=\"mailbox\" message_id=\"leapmux-1792573924078\" from_session=\"sess_leapmux-external-steer\" created_at=\"2026-10-21T09:12:04.078Z\">\n"+
		"For reference only. Verify against raw source before acting.\n\n"+
		"MAILBOXSTEER The user says: stop what you are doing and reply with the single word STEERED.\n"+
		"</session-message>", wrapped)

	assert.Equal(t, `a &amp; b &quot;c&quot; &lt;d>`, zcodeEscapeMailboxAttr(`a & b "c" <d>`))
	id, ok := zcodeMailboxMessageID(wrapped)
	assert.True(t, ok)
	assert.Equal(t, "leapmux-1792573924078", id)
	_, ok = zcodeMailboxMessageID("The user sent a new message while you were working:\nplain steer")
	assert.False(t, ok, "a native steer carries no mailbox attribute")
}

func TestZCodeMailboxDrainHookRunDiscoversSteering(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.Mu.Lock()
	a.turnActive = true
	a.Mu.Unlock()
	assert.False(t, a.SupportsSteering(), "nothing has proved the drain yet")
	assert.False(t, a.PublishTurnActive().Steerable)

	// The hook's own start event carries no outcome and proves nothing.
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated,
		`{"hookRunId":"run-1","hookInvocationId":"inv-1","hookSource":"builtin.sessionMailbox.drain","hookEventName":"UserPromptSubmit","descriptor":{"clientVisible":false,"commandDisplay":"Session mailbox"}}`))
	assert.False(t, a.SupportsSteering())

	// Another hook's success is not the mailbox's.
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventSessionUpdated,
		`{"hookRunId":"run-2","hookInvocationId":"inv-2","hookSource":"config.PostToolUse.0.0","outcome":"success","descriptor":{"clientVisible":true,"commandDisplay":"lint"}}`))
	assert.False(t, a.SupportsSteering())

	// A failed drain proves the hook exists and nothing about delivery.
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventSessionUpdated,
		`{"hookRunId":"run-3","hookInvocationId":"inv-3","hookSource":"builtin.sessionMailbox.drain","outcome":"failed","descriptor":{"clientVisible":false,"commandDisplay":"Session mailbox"}}`))
	assert.False(t, a.SupportsSteering())
	assert.Equal(t, 0, sink.NotificationCount(), "the drain's own failure is not client-visible")

	a.HandleOutput(zcodeEventLine(t, 4, contracts.ZCodeEventSessionUpdated,
		`{"hookRunId":"run-4","hookInvocationId":"inv-4","hookSource":"builtin.sessionMailbox.drain","outcome":"success","durationMs":1,"descriptor":{"clientVisible":false,"commandDisplay":"Session mailbox"}}`))
	assert.True(t, a.SupportsSteering())
	assert.True(t, a.PublishTurnActive().Steerable, "a running prompt turn is steerable once the drain is proven")
	// The flip must reach the browser too: the Steer control reads the agent
	// status change, which was last sampled at startup, before any prompt could
	// prove the drain. The re-broadcast happens exactly once, on the first
	// proof; the run above and any later success add nothing.
	assert.Equal(t, []string{"sess-1"}, sink.StatusActives())
	a.HandleOutput(zcodeEventLine(t, 5, contracts.ZCodeEventSessionUpdated,
		`{"hookRunId":"run-5","hookInvocationId":"inv-5","hookSource":"builtin.sessionMailbox.drain","outcome":"success","durationMs":1,"descriptor":{"clientVisible":false,"commandDisplay":"Session mailbox"}}`))
	assert.Equal(t, []string{"sess-1"}, sink.StatusActives())

	a.Mu.Lock()
	a.turnActive = false
	a.compactionSessionID = "sess-1"
	a.Mu.Unlock()
	state := a.PublishTurnActive()
	assert.True(t, state.Active)
	assert.False(t, state.Steerable, "a compaction is never steerable")
}

func TestZCodeMailboxSteerEventsResolveTheLedgerEntry(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeSteerTestAgent(t, agent.NewProviderServices(sink))
	require.True(t, a.SteerRowAtEcho())
	require.NoError(t, a.SteerInput("change course", nil))
	envelope := zcodeReadEnvelope(t, a, zcodeUnreadEnvelopes(t, a)[0])

	a.HandleOutput(zcodeSteerEventLine(t, 1, contracts.ZCodeEventTurnSteerQueued, zcodeSteerEventPayload(t, contracts.ZCodeEventTurnSteerQueued, envelope)))
	assert.Empty(t, sink.SteerEchoReports(), "queued is admission, not delivery")
	assert.Equal(t, 0, sink.NotificationCount(), "the row waits for the drain, per SteerRowAtEcho")
	a.Mu.Lock()
	pendingInput := a.mailboxSteers[envelope.MessageID].pendingInputID
	a.Mu.Unlock()
	assert.Equal(t, "pending_1", pendingInput)

	a.HandleOutput(zcodeSteerEventLine(t, 2, contracts.ZCodeEventTurnSteerDrained, zcodeSteerEventPayload(t, contracts.ZCodeEventTurnSteerDrained, envelope)))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
	assert.Equal(t, 0, sink.NotificationCount())
	a.Mu.Lock()
	tracked := len(a.mailboxSteers)
	a.Mu.Unlock()
	assert.Equal(t, 0, tracked, "the drain delivered it")
}

func TestZCodeMailboxSteerDiscardEvents(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reason  string
		outcome agent.SteerOutcome
	}{
		{zcodeSteerDiscardReasonPromoted, agent.SteerOutcomeEchoed},
		{"user_removed", agent.SteerOutcomeDropped},
		{"session_resumed", agent.SteerOutcomeDropped},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newZCodeSteerTestAgent(t, agent.NewProviderServices(sink))
			require.NoError(t, a.SteerInput("will be dropped", nil))
			envelope := zcodeReadEnvelope(t, a, zcodeUnreadEnvelopes(t, a)[0])
			a.HandleOutput(zcodeSteerEventLine(t, 1, contracts.ZCodeEventTurnSteerQueued, zcodeSteerEventPayload(t, contracts.ZCodeEventTurnSteerQueued, envelope)))

			a.HandleOutput(zcodeSteerEventLine(t, 2, contracts.ZCodeEventSessionUpdated,
				fmt.Sprintf(`{"pendingInputIds":["pending_1"],"reason":%q,"targetTurnId":"turn_1"}`, tc.reason)))

			assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: tc.outcome, Count: 1}}, sink.SteerEchoReports())
			assert.Equal(t, 0, sink.NotificationCount(), "the requeue or the promoted turn is the visible outcome")
			a.Mu.Lock()
			tracked := len(a.mailboxSteers)
			a.Mu.Unlock()
			assert.Equal(t, 0, tracked)
		})
	}
}

func TestZCodeMailboxSteerEventsKeepTheRowForTheRuntimesOwnSteers(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeSteerTestAgent(t, agent.NewProviderServices(sink))

	// A background task notification the runtime steered into the turn.
	a.HandleOutput(zcodeSteerEventLine(t, 1, contracts.ZCodeEventTurnSteerQueued,
		`{"pendingInputId":"pending_7","input":"[SYSTEM NOTIFICATION - NOT USER INPUT]\nBackground task finished.","delivery":"guide","queueLength":1}`))
	assert.Equal(t, 1, sink.NotificationCount())

	// A mailbox message LeapMux did not write (an id it never tracked).
	a.HandleOutput(zcodeSteerEventLine(t, 2, contracts.ZCodeEventTurnSteerDrained, zcodeSteerEventPayload(t, contracts.ZCodeEventTurnSteerDrained,
		zcodeMailboxEnvelope{MessageID: "leapmux-0-000000", FromSessionID: "sess_other", CreatedAt: "2026-10-21T00:00:00.000Z", Content: "hello"})))
	assert.Equal(t, 2, sink.NotificationCount())
}

func TestZCodeActiveTurnPublishKeepsAWaitingSteer(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeSteerTestAgent(t, agent.NewProviderServices(sink))
	require.NoError(t, a.SteerInput("still working on it", nil))
	names := zcodeUnreadEnvelopes(t, a)
	require.Len(t, names, 1)

	// The turn is running, so the envelope is deliverable at the next boundary:
	// a publish in that state must not sweep it, or every progress republish
	// would eat a steer still in flight.
	assert.True(t, a.PublishTurnActive().Active)
	assert.Equal(t, names, zcodeUnreadEnvelopes(t, a))
	assert.Empty(t, sink.SteerEchoReports())
}

func TestZCodeOneDrainEventResolvesEverySteerItTook(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeSteerTestAgent(t, agent.NewProviderServices(sink))
	require.NoError(t, a.SteerInput("first nudge", nil))
	require.NoError(t, a.SteerInput("second nudge", nil))
	first := zcodeReadEnvelope(t, a, zcodeUnreadEnvelopes(t, a)[0])
	second := zcodeReadEnvelope(t, a, zcodeUnreadEnvelopes(t, a)[1])

	wrappedFirst := zcodeMailboxWrappedInput(first)
	wrappedSecond := zcodeMailboxWrappedInput(second)
	payload, err := json.Marshal(map[string]any{
		"injectedMessageIds": []string{"msg_1", "msg_2"},
		"drainedInputs": []map[string]any{
			{"pendingInputId": "pending_1", "text": wrappedFirst, "delivery": "guide"},
			{"pendingInputId": "pending_2", "text": wrappedSecond, "delivery": "guide"},
		},
	})
	require.NoError(t, err)
	a.HandleOutput(zcodeSteerEventLine(t, 1, contracts.ZCodeEventTurnSteerDrained, string(payload)))

	// The ledger settles the resolved run in send order, so one drain of two
	// steers is ONE echoed report of two inputs.
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 2}}, sink.SteerEchoReports())
	assert.Equal(t, 0, sink.NotificationCount())
}

func TestZCodeTurnEndWithdrawsUndeliveredMailboxSteers(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeSteerTestAgent(t, agent.NewProviderServices(sink))
	require.NoError(t, a.SteerInput("too late", nil))
	require.Len(t, zcodeUnreadEnvelopes(t, a), 1)

	// The turn ends without a drain: the next prompt's UserPromptSubmit drain
	// would fold the envelope in as reference material, beside the input the
	// queue requeues for that same prompt.
	a.HandleOutput(zcodeSteerEventLine(t, 1, contracts.ZCodeEventTurnCompleted, `{"resultType":"success","toolCallCount":0}`))

	assert.Empty(t, zcodeUnreadEnvelopes(t, a))
	_, err := os.Stat(filepath.Join(a.mailboxRoot, zcodeSteerSessionID))
	assert.True(t, os.IsNotExist(err), "the session's whole mailbox goes, read copies included")
	a.Mu.Lock()
	tracked := len(a.mailboxSteers)
	a.Mu.Unlock()
	assert.Equal(t, 0, tracked)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeDropped, Count: 1}}, sink.SteerEchoReports(),
		"the queue learns the steer never reached the model and requeues it")
	assert.False(t, a.PublishTurnActive().Active)

	// A later steer, in a later turn, starts clean.
	a.Mu.Lock()
	a.turnActive = true
	a.Mu.Unlock()
	require.NoError(t, a.SteerInput("next turn", nil))
	assert.Len(t, zcodeUnreadEnvelopes(t, a), 1)
}

func TestZCodeStopDrainSuccessResolvesTheSteerItTook(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeSteerTestAgent(t, agent.NewProviderServices(sink))
	require.NoError(t, a.SteerInput("steer the final answer", nil))
	envelope := zcodeReadEnvelope(t, a, zcodeUnreadEnvelopes(t, a)[0])

	// The Stop drain renames the envelope out of unread and answers
	// continue:true; the runtime injects it into the continuation request and
	// states no steer event of its own. A second Stop success with the next
	// steer's envelope still in unread resolves nothing for it.
	sessionDir := filepath.Join(a.mailboxRoot, zcodeSteerSessionID)
	a.Mu.Lock()
	envelopeName := a.mailboxSteers[envelope.MessageID].file
	a.Mu.Unlock()
	require.NoError(t, os.MkdirAll(filepath.Join(sessionDir, zcodeMailboxReadDir), 0o700))
	require.NoError(t, os.Rename(
		filepath.Join(sessionDir, zcodeMailboxUnreadDir, envelopeName),
		filepath.Join(sessionDir, zcodeMailboxReadDir, "drained.json")))

	a.HandleOutput(zcodeSteerEventLine(t, 1, contracts.ZCodeEventSessionUpdated,
		`{"hookRunId":"run-9","hookInvocationId":"inv-9","hookSource":"builtin.sessionMailbox.drain","hookEventName":"Stop","outcome":"success","durationMs":1,"descriptor":{"clientVisible":false,"commandDisplay":"Session mailbox"}}`))

	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
	a.Mu.Lock()
	tracked := len(a.mailboxSteers)
	a.Mu.Unlock()
	assert.Equal(t, 0, tracked)

	// An entry whose envelope is still in unread keeps waiting: the drain took
	// nothing of its own to report about it yet.
	require.NoError(t, a.SteerInput("a second steer", nil))
	a.HandleOutput(zcodeSteerEventLine(t, 2, contracts.ZCodeEventSessionUpdated,
		`{"hookRunId":"run-10","hookInvocationId":"inv-10","hookSource":"builtin.sessionMailbox.drain","hookEventName":"Stop","outcome":"success","durationMs":1,"descriptor":{"clientVisible":false,"commandDisplay":"Session mailbox"}}`))
	assert.Len(t, sink.SteerEchoReports(), 1)
}

func TestZCodeTurnEndReportsAnEnvelopeThatLeftUnreadAsUncertain(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeSteerTestAgent(t, agent.NewProviderServices(sink))
	require.NoError(t, a.SteerInput("ambiguous", nil))
	// The envelope left unread with no resolution the settled paths recognize:
	// the sweep must neither claim delivery nor auto-replay the input.
	sessionDir := filepath.Join(a.mailboxRoot, zcodeSteerSessionID)
	require.NoError(t, os.Remove(filepath.Join(sessionDir, zcodeMailboxUnreadDir, zcodeUnreadEnvelopes(t, a)[0])))

	a.HandleOutput(zcodeSteerEventLine(t, 1, contracts.ZCodeEventTurnCompleted, `{"resultType":"success","toolCallCount":0}`))

	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeUncertain, Count: 1}}, sink.SteerEchoReports())
}

func TestZCodeTurnEndWithNoPendingSteerLeavesTheMailboxAlone(t *testing.T) {
	t.Parallel()
	a := newZCodeSteerTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, a.SteerInput("delivered", nil))
	envelope := zcodeReadEnvelope(t, a, zcodeUnreadEnvelopes(t, a)[0])
	a.HandleOutput(zcodeSteerEventLine(t, 1, contracts.ZCodeEventTurnSteerDrained, zcodeSteerEventPayload(t, contracts.ZCodeEventTurnSteerDrained, envelope)))
	// The runtime renamed it; mimic the read copy it leaves.
	sessionDir := filepath.Join(a.mailboxRoot, zcodeSteerSessionID)
	require.NoError(t, os.MkdirAll(filepath.Join(sessionDir, zcodeMailboxReadDir), 0o700))
	require.NoError(t, os.Rename(
		filepath.Join(sessionDir, zcodeMailboxUnreadDir, zcodeUnreadEnvelopes(t, a)[0]),
		filepath.Join(sessionDir, zcodeMailboxReadDir, "delivered.json")))

	a.HandleOutput(zcodeSteerEventLine(t, 2, contracts.ZCodeEventTurnCompleted, `{"resultType":"success","toolCallCount":1}`))

	// Nothing was pending, so the publish paid no sweep; the read copy waits
	// for a sweep that has something to withdraw, or for teardown.
	_, err := os.Stat(filepath.Join(sessionDir, zcodeMailboxReadDir, "delivered.json"))
	assert.NoError(t, err)
}

func TestZCodeClearContextRemovesTheReplacedSessionsMailbox(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	a.mailboxRoot = t.TempDir()
	stale := filepath.Join(a.mailboxRoot, "sess-1", zcodeMailboxUnreadDir)
	require.NoError(t, os.MkdirAll(stale, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(stale, "0000000000001-000001.json"), []byte(`{}`), 0o600))
	answerZCodeRequest(t, a, stdin, MethodSessionCreate, `{"session":{"sessionId":"sess_new"},"settings":{"mode":{"current":"build"}}}`)
	answerZCodeRequest(t, a, stdin, MethodSessionSubscribe, `{}`)

	_, err := a.ClearContext()
	require.NoError(t, err)

	_, statErr := os.Stat(filepath.Join(a.mailboxRoot, "sess-1"))
	assert.True(t, os.IsNotExist(statErr), "the retired session's envelopes can never drain again")
}

func TestZCodeStopRemovesTheMailboxRoot(t *testing.T) {
	t.Parallel()
	a := newZCodeSteerTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, a.SteerInput("pending at stop", nil))
	root := a.mailboxRoot

	a.cleanupZCodeMailbox()
	a.cleanupZCodeMailbox()

	_, err := os.Stat(root)
	assert.True(t, os.IsNotExist(err))
}

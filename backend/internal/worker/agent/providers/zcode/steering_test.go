package zcode

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ZCode steers through the command inbox: `v4/command` with a `sendText` of
// the `guide` delivery, and `deleteQueueItem` to take a queued steer back.
// Probed live against ZCode 3.16.3, whose shapes the fixtures below repeat.

// zcodeTestCommand is one `v4/command` request that the agent sent.
type zcodeTestCommand struct {
	ID           int64
	CommandID    string          `json:"commandId"`
	ClientID     string          `json:"clientId"`
	SessionID    string          `json:"sessionId"`
	BaseRevision *int64          `json:"baseRevision"`
	Type         string          `json:"type"`
	Payload      json.RawMessage `json:"payload"`
	IssuedAt     int64           `json:"issuedAt"`
}

// zcodeSentCommands decodes the commands that the agent sent, in send order.
func zcodeSentCommands(t *testing.T, stdin *zcodeRecordedStdin) []zcodeTestCommand {
	t.Helper()
	var commands []zcodeTestCommand
	for _, req := range stdin.Requests(t) {
		if req.Method != MethodCommand {
			continue
		}
		var command zcodeTestCommand
		require.NoError(t, json.Unmarshal(req.Params, &command))
		require.NoError(t, json.Unmarshal(req.ID, &command.ID))
		commands = append(commands, command)
	}
	return commands
}

// zcodeSentCommandsOf is the commands of one type, in send order.
func zcodeSentCommandsOf(t *testing.T, stdin *zcodeRecordedStdin, commandType string) []zcodeTestCommand {
	t.Helper()
	var commands []zcodeTestCommand
	for _, command := range zcodeSentCommands(t, stdin) {
		if command.Type == commandType {
			commands = append(commands, command)
		}
	}
	return commands
}

// answerZCodeCommand answers the nth command of commandType, counted from 1,
// with the acknowledgement that reply builds from it. The command blocks its
// caller, so the answer goes out from its own goroutine.
func answerZCodeCommand(t *testing.T, a *Agent, stdin *zcodeRecordedStdin, commandType string, nth int, reply func(zcodeTestCommand) string) {
	t.Helper()
	go func() {
		deadline := time.Now().Add(zcodeTestRPCTimeout)
		for time.Now().Before(deadline) {
			if commands := zcodeSentCommandsOf(t, stdin, commandType); len(commands) >= nth {
				command := commands[nth-1]
				a.HandleOutput(zcodeReplyLine(t, command.ID, json.RawMessage(reply(command))))
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		t.Errorf("timed out waiting for %s command %d", commandType, nth)
	}()
}

// zcodeAcceptedSteer acknowledges a steer that the inbox admitted with delivery.
func zcodeAcceptedSteer(delivery string) func(zcodeTestCommand) string {
	return func(command zcodeTestCommand) string {
		return fmt.Sprintf(`{"commandId":%q,"status":"accepted","revisionAtDecision":8,"result":{"type":"inputAccepted","delivery":%q,"inputId":%q},"memoryEnabled":false}`,
			command.CommandID, delivery, command.CommandID)
	}
}

// zcodeCommandAnswer acknowledges a command with status and revision.
func zcodeCommandAnswer(status string, revision int64) func(zcodeTestCommand) string {
	return func(command zcodeTestCommand) string {
		return fmt.Sprintf(`{"commandId":%q,"status":%q,"revisionAtDecision":%d}`, command.CommandID, status, revision)
	}
}

// zcodePendingInputID is the queued-input id that ZCode states for a command.
func zcodePendingInputID(commandID string) string { return "queue_" + commandID }

func zcodeSteerQueuedLine(t *testing.T, seq int64, commandID string) []byte {
	t.Helper()
	return zcodeEventLine(t, seq, contracts.ZCodeEventTurnSteerQueued, fmt.Sprintf(
		`{"inputId":%q,"queryId":%q,"pendingInputId":%q,"input":"steered","inputPresentation":"user_steer","delivery":"guide","targetTurnId":"turn_1","queueLength":1}`,
		commandID, commandID, zcodePendingInputID(commandID)))
}

func zcodeSteerDrainedLine(t *testing.T, seq int64, queryIDs, pendingInputIDs []string) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"injectedMessageIds": []string{"msg_1"},
		"pendingInputIds":    pendingInputIDs,
		"queryIds":           queryIDs,
		"drainedInputs":      []map[string]any{{"messageId": "msg_1", "text": "steered", "delivery": "guide"}},
		"targetTurnId":       "turn_1",
	})
	require.NoError(t, err)
	return zcodeEventLine(t, seq, contracts.ZCodeEventTurnSteerDrained, string(payload))
}

func zcodeSteerDiscardLine(t *testing.T, seq int64, reason string, pendingInputIDs ...string) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"pendingInputIds": pendingInputIDs, "reason": reason, "targetTurnId": "turn_1"})
	require.NoError(t, err)
	return zcodeEventLine(t, seq, contracts.ZCodeEventSessionUpdated, string(payload))
}

// newZCodeSteerAgent is a test agent whose user turn runs: the state
// SteerInput needs.
func newZCodeSteerAgent(t *testing.T) (*Agent, *agenttest.Sink, *zcodeRecordedStdin) {
	t.Helper()
	sink := &agenttest.Sink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	a.HandleOutput(zcodeTurnEventLine(t, 0, "turn_1", contracts.ZCodeEventTurnStarted, `{"turnNumber":1,"input":"hi","inputId":"lead"}`))
	return a, sink, stdin
}

// steerZCode sends one steer that the inbox queues for the running turn, and
// feeds the turn.steerQueued event that ZCode sends with it. It returns the
// command id.
func steerZCode(t *testing.T, a *Agent, stdin *zcodeRecordedStdin, nth int, seq int64) string {
	t.Helper()
	answerZCodeCommand(t, a, stdin, zcodeCommandSendText, nth, zcodeAcceptedSteer("queue"))
	require.NoError(t, agenttest.SteerInput(a, zcodeTestSteerRequest(nth, "Identical steer")))
	commands := zcodeSentCommandsOf(t, stdin, zcodeCommandSendText)
	require.Len(t, commands, nth)
	commandID := commands[nth-1].CommandID
	a.HandleOutput(zcodeSteerQueuedLine(t, seq, commandID))
	return commandID
}

func zcodeTestSteerRequest(n int, content string) agent.SteerRequest {
	return agent.SteerRequest{InputID: fmt.Sprintf("input-%d", n), AttemptID: fmt.Sprintf("attempt-%d", n), Content: content}
}

func zcodeTestSteerResolution(n int, outcome agent.SteerOutcome, receipt agent.NativeInputReceipt) agent.SteerResolution {
	return agent.SteerResolution{InputID: fmt.Sprintf("input-%d", n), AttemptID: fmt.Sprintf("attempt-%d", n), Outcome: outcome, Receipt: receipt}
}

func zcodeTestSteerEcho(n int, commandID, turnID string) agent.SteerResolution {
	return zcodeTestSteerResolution(n, agent.SteerOutcomeEchoed, agent.NativeInputReceipt{SessionID: "sess-1", InputID: commandID, TurnID: turnID})
}

func zcodePendingSteerCount(a *Agent) int {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return len(a.steers)
}

func TestZCodeSteerInputSendsAGuideCommand(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newZCodeSteerAgent(t)
	answerZCodeCommand(t, a, stdin, zcodeCommandSendText, 1, zcodeAcceptedSteer("queue"))
	require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{Content: "Also run the linter."}))

	commands := zcodeSentCommands(t, stdin)
	require.Len(t, commands, 1)
	command := commands[0]
	assert.True(t, strings.HasPrefix(command.CommandID, zcodeSteerCommandPrefix), command.CommandID)
	assert.Equal(t, zcodeCommandClientID, command.ClientID)
	assert.Equal(t, "sess-1", command.SessionID)
	assert.Equal(t, zcodeCommandSendText, command.Type)
	assert.JSONEq(t, `{"text":"Also run the linter.","requestedDelivery":"guide"}`, string(command.Payload))
	assert.Positive(t, command.IssuedAt)
	assert.Nil(t, command.BaseRevision, "a sendText is no compare-and-set command")

	assert.Equal(t, 1, zcodePendingSteerCount(a), "the steer waits for the model to read it")
	assert.Empty(t, sink.SteerResolutions())
	a.Mu.Lock()
	revision := a.commandRevision
	a.Mu.Unlock()
	assert.Equal(t, int64(8), revision, "the acknowledgement states the session revision")
}

// ZCode can report the queued input before it acknowledges v4/command. The
// steer must already be registered then, so its native event persists no row
// and its pendingInputId can address the queue immediately.
func TestZCodeSteerQueuedBeforeCommandAcknowledgement(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		resolve func(t *testing.T, a *Agent, sink *agenttest.Sink, stdin *zcodeRecordedStdin, commandID, pendingInputID string)
	}{
		{"drainage", func(t *testing.T, a *Agent, sink *agenttest.Sink, _ *zcodeRecordedStdin, commandID, pendingInputID string) {
			a.HandleOutput(zcodeSteerDrainedLine(t, 2, nil, []string{pendingInputID}))
			assert.Equal(t, []agent.SteerResolution{zcodeTestSteerEcho(1, commandID, "turn_1")}, sink.SteerResolutions())
			assert.Zero(t, zcodePendingSteerCount(a))
		}},
		{"deleteQueueItem", func(t *testing.T, a *Agent, _ *agenttest.Sink, stdin *zcodeRecordedStdin, _, pendingInputID string) {
			answerZCodeCommand(t, a, stdin, zcodeCommandDeleteQueueItem, 1, zcodeCommandAnswer(zcodeCommandAccepted, 9))
			assert.True(t, a.deleteZCodeQueuedInput("sess-1", pendingInputID))
			deletes := zcodeSentCommandsOf(t, stdin, zcodeCommandDeleteQueueItem)
			require.Len(t, deletes, 1)
			assert.JSONEq(t, fmt.Sprintf(`{"queueItemId":%q}`, pendingInputID), string(deletes[0].Payload))
			a.HandleOutput(zcodeSteerDiscardLine(t, 2, "user_removed", pendingInputID))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, stdin := newZCodeSteerAgent(t)
			done := make(chan error, 1)
			go func() { done <- agenttest.SteerInput(a, zcodeTestSteerRequest(1, "early queued input")) }()
			waitZCodeRequestNumber(t, stdin, MethodCommand, 0)
			commands := zcodeSentCommandsOf(t, stdin, zcodeCommandSendText)
			require.Len(t, commands, 1)
			commandID := commands[0].CommandID

			a.HandleOutput(zcodeSteerQueuedLine(t, 1, commandID))
			assert.Zero(t, sink.NotificationCount(), "LeapMux's own queued steer persists no native notification")
			a.Mu.Lock()
			steer := a.steers[commandID]
			require.NotNil(t, steer)
			pendingInputID := steer.pendingInputID
			a.Mu.Unlock()
			assert.Equal(t, zcodePendingInputID(commandID), pendingInputID)

			a.HandleOutput(zcodeReplyLine(t, commands[0].ID, json.RawMessage(zcodeAcceptedSteer("queue")(commands[0]))))
			require.NoError(t, <-done)
			tc.resolve(t, a, sink, stdin, commandID, pendingInputID)
			assert.Zero(t, sink.NotificationCount())
		})
	}
}

// The model read the steer when ZCode drained it into the turn, which ZCode
// states by the command id, or by the queued input alone.
func TestZCodeSteerDrainEchoesTheSteer(t *testing.T) {
	t.Parallel()
	for name, line := range map[string]func(t *testing.T, commandID string) []byte{
		"by command": func(t *testing.T, commandID string) []byte {
			return zcodeSteerDrainedLine(t, 2, []string{commandID}, nil)
		},
		"by queued input": func(t *testing.T, commandID string) []byte {
			return zcodeSteerDrainedLine(t, 2, nil, []string{zcodePendingInputID(commandID)})
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, stdin := newZCodeSteerAgent(t)
			commandID := steerZCode(t, a, stdin, 1, 1)
			a.HandleOutput(line(t, commandID))
			assert.Equal(t, []agent.SteerResolution{zcodeTestSteerEcho(1, commandID, "turn_1")}, sink.SteerResolutions())
			assert.Zero(t, zcodePendingSteerCount(a))
			assert.Zero(t, sink.NotificationCount(), "the queue's row is the visible outcome")
		})
	}
}

// A turn end between the check and the admission makes the steer a turn of its
// own, whose prompt the model reads.
func TestZCodeSteerThatStartsATurnEchoesAtItsNativeStart(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newZCodeSteerAgent(t)
	answerZCodeCommand(t, a, stdin, zcodeCommandSendText, 1, zcodeAcceptedSteer("startNow"))
	require.NoError(t, agenttest.SteerInput(a, zcodeTestSteerRequest(1, "late")))
	require.Empty(t, sink.SteerResolutions(), "the acknowledgment carries no turn ID")
	commandID := zcodeSentCommandsOf(t, stdin, zcodeCommandSendText)[0].CommandID
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn_2", contracts.ZCodeEventTurnStarted, fmt.Sprintf(`{"inputId":%q}`, commandID)))
	assert.Equal(t, []agent.SteerResolution{zcodeTestSteerEcho(1, commandID, "turn_2")}, sink.SteerResolutions())
	assert.Zero(t, zcodePendingSteerCount(a))
}

func TestZCodeTurnEndKeepsASteerAdmittedAsTheNextTurn(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newZCodeSteerAgent(t)
	answerZCodeCommand(t, a, stdin, zcodeCommandSendText, 1, zcodeAcceptedSteer("startNow"))
	require.NoError(t, agenttest.SteerInput(a, zcodeTestSteerRequest(1, "Run this next")))
	a.Mu.Lock()
	a.turns["turn_1"].settled = true
	a.refreshTurnActiveLocked()
	a.Mu.Unlock()
	a.withdrawZCodeSteersAfterTurnEnd()
	require.Empty(t, sink.SteerResolutions(), "the steer is already admitted to start a turn")
	commandID := zcodeSentCommandsOf(t, stdin, zcodeCommandSendText)[0].CommandID
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn_2", contracts.ZCodeEventTurnStarted, fmt.Sprintf(`{"inputId":%q}`, commandID)))
	assert.Equal(t, []agent.SteerResolution{zcodeTestSteerEcho(1, commandID, "turn_2")}, sink.SteerResolutions())
}

// A steer that the inbox did not admit fails, so the queue keeps the input, and
// the ledger holds nothing for it.
func TestZCodeSteerInputThatTheInboxRefuses(t *testing.T) {
	t.Parallel()
	for name, reply := range map[string]func(zcodeTestCommand) string{
		"a rejection":           zcodeCommandAnswer("rejected", 3),
		"a failure":             zcodeCommandAnswer("failed", 3),
		"an accept of no input": zcodeCommandAnswer(zcodeCommandAccepted, 3),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, stdin := newZCodeSteerAgent(t)
			answerZCodeCommand(t, a, stdin, zcodeCommandSendText, 1, reply)
			err := agenttest.SteerInput(a, agent.SteerRequest{Content: "refused"})
			require.Error(t, err)
			assert.NotErrorIs(t, err, agent.ErrSteeringUnsupported)
			assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain, "the inbox answered")
			assert.Zero(t, zcodePendingSteerCount(a))
			assert.Empty(t, sink.SteerResolutions())
			assert.True(t, a.SupportsSteering())
		})
	}
}

// An acknowledgement that does not state the steer leaves its delivery unknown,
// so the queue neither replays the input nor claims its delivery.
func TestZCodeSteerInputWithAnUnreadableAcknowledgement(t *testing.T) {
	t.Parallel()
	for name, reply := range map[string]func(zcodeTestCommand) string{
		"another command's acknowledgement": func(zcodeTestCommand) string {
			return `{"commandId":"another","status":"accepted","revisionAtDecision":3,"result":{"type":"inputAccepted","delivery":"queue","inputId":"another"}}`
		},
		"an acknowledgement that is not an object": func(zcodeTestCommand) string { return `"accepted"` },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, stdin := newZCodeSteerAgent(t)
			answerZCodeCommand(t, a, stdin, zcodeCommandSendText, 1, reply)
			require.ErrorIs(t, agenttest.SteerInput(a, agent.SteerRequest{Content: "unread"}), agent.ErrDeliveryUncertain)
			assert.Zero(t, zcodePendingSteerCount(a))
			assert.Empty(t, sink.SteerResolutions())
		})
	}
}

func TestZCodeSteerInputThatTheAppServerRefuses(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newZCodeSteerAgent(t)
	go func() {
		req := waitZCodeRequest(t, stdin, MethodCommand)
		a.HandleOutput(zcodeErrorReplyLine(t, zcodeSentRequestID(t, req), -32602, "Invalid params"))
	}()
	err := agenttest.SteerInput(a, agent.SteerRequest{Content: "refused"})
	require.Error(t, err)
	assert.NotErrorIs(t, err, agent.ErrSteeringUnsupported)
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain, "the app-server answered")
	assert.Zero(t, zcodePendingSteerCount(a))
	assert.Empty(t, sink.SteerResolutions())
	assert.True(t, a.SupportsSteering())
}

// A missing inbox method fails the dispatch and leaves steering advertised.
func TestZCodeSteerInputReportsAnAbsentCurrentMethod(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newZCodeSteerAgent(t)
	refuseZCodeRequest(t, a, stdin, MethodCommand, ErrMethodNotFound, "Method not found")
	err := agenttest.SteerInput(a, agent.SteerRequest{Content: "no inbox"})
	require.Error(t, err)
	assert.NotErrorIs(t, err, agent.ErrSteeringUnsupported)
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
	assert.True(t, a.SupportsSteering())
	assert.Equal(t, agent.TurnState{Active: true, Steerable: true}, a.PublishTurnActive())
	assert.Empty(t, sink.SteerResolutions())
	assert.Empty(t, a.steers)
}

func TestZCodeSteerInputPreconditions(t *testing.T) {
	t.Parallel()
	t.Run("no running turn", func(t *testing.T) {
		t.Parallel()
		a, _, stdin := newZCodeSteerAgent(t)
		a.Mu.Lock()
		a.turnActive = false
		a.Mu.Unlock()
		require.ErrorIs(t, agenttest.SteerInput(a, agent.SteerRequest{Content: "idle"}), agent.ErrNoActiveTurn)
		assert.Empty(t, zcodeSentCommands(t, stdin))
	})
	t.Run("a compaction alone", func(t *testing.T) {
		t.Parallel()
		a, _, stdin := newZCodeSteerAgent(t)
		a.Mu.Lock()
		a.turnActive = false
		a.compactionSessionID = a.sessionID
		a.Mu.Unlock()
		require.ErrorIs(t, agenttest.SteerInput(a, agent.SteerRequest{Content: "compacting"}), agent.ErrNoActiveTurn)
		assert.Empty(t, zcodeSentCommands(t, stdin))
	})
	t.Run("a stopped agent", func(t *testing.T) {
		t.Parallel()
		a, _, stdin := newZCodeSteerAgent(t)
		a.SetStoppedForTest(true)
		require.Error(t, agenttest.SteerInput(a, agent.SteerRequest{Content: "stopped"}))
		assert.Empty(t, zcodeSentCommands(t, stdin))
	})
	t.Run("an input above the runtime's limit", func(t *testing.T) {
		t.Parallel()
		a, _, stdin := newZCodeSteerAgent(t)
		require.ErrorIs(t, agenttest.SteerInput(a, agent.SteerRequest{Content: strings.Repeat("x", zcodeSteerInputMaxBytes+1)}), agent.ErrSteeringUnsupported)
		assert.Empty(t, zcodeSentCommands(t, stdin))
	})
}

// A steer carries text: a text attachment inlines into it, and any other one
// sends the input in the next turn.
func TestZCodeSteerInputAttachments(t *testing.T) {
	t.Parallel()
	t.Run("a text attachment inlines", func(t *testing.T) {
		t.Parallel()
		a, _, stdin := newZCodeSteerAgent(t)
		answerZCodeCommand(t, a, stdin, zcodeCommandSendText, 1, zcodeAcceptedSteer("queue"))
		require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{Content: "Read this.", Attachments: []*leapmuxv1.Attachment{{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("the notes")}}}))
		commands := zcodeSentCommands(t, stdin)
		require.Len(t, commands, 1)
		var payload zcodeSendTextPayload
		require.NoError(t, json.Unmarshal(commands[0].Payload, &payload))
		assert.Contains(t, payload.Text, "Read this.")
		assert.Contains(t, payload.Text, "the notes")
		assert.Contains(t, payload.Text, "notes.txt")
	})
	t.Run("an image goes in the next turn", func(t *testing.T) {
		t.Parallel()
		a, _, stdin := newZCodeSteerAgent(t)
		err := agenttest.SteerInput(a, agent.SteerRequest{Content: "Look.", Attachments: []*leapmuxv1.Attachment{{Filename: "shot.png", MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}}}})
		require.ErrorIs(t, err, agent.ErrSteeringUnsupported)
		assert.Empty(t, zcodeSentCommands(t, stdin))
	})
}

// A promoted steer waits for its own turn. Other discards removed it unread.
func TestZCodeSteerDiscard(t *testing.T) {
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
			t.Parallel()
			a, sink, stdin := newZCodeSteerAgent(t)
			commandID := steerZCode(t, a, stdin, 1, 1)
			a.HandleOutput(zcodeSteerDiscardLine(t, 2, tc.reason, zcodePendingInputID(commandID)))
			var receipt agent.NativeInputReceipt
			if tc.reason == zcodeSteerDiscardReasonPromoted {
				require.Empty(t, sink.SteerResolutions())
				a.HandleOutput(zcodeTurnEventLine(t, 3, "turn_2", contracts.ZCodeEventTurnStarted, fmt.Sprintf(`{"inputId":%q}`, commandID)))
				receipt = agent.NativeInputReceipt{SessionID: "sess-1", InputID: commandID, TurnID: "turn_2"}
			}
			assert.Equal(t, []agent.SteerResolution{zcodeTestSteerResolution(1, tc.outcome, receipt)}, sink.SteerResolutions())
			assert.Zero(t, sink.NotificationCount(), "the requeue or the promoted turn is the visible outcome")
			assert.Zero(t, zcodePendingSteerCount(a))
		})
	}
}

// The runtime's own steers (a background task notification, a subagent
// message) keep their rows.
func TestZCodeSteerEventsKeepTheRowsOfTheRuntimesOwnSteers(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newZCodeSteerAgent(t)
	commandID := steerZCode(t, a, stdin, 1, 1)

	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventTurnSteerQueued,
		`{"pendingInputId":"pending_7","input":"[SYSTEM NOTIFICATION - NOT USER INPUT]\nBackground task finished.","delivery":"guide","queueLength":1}`))
	a.HandleOutput(zcodeSteerDrainedLine(t, 3, nil, []string{"pending_7"}))
	assert.Equal(t, 2, sink.NotificationCount())
	a.HandleOutput(zcodeSteerDiscardLine(t, 4, "user_removed", "pending_7"))
	assert.Equal(t, 3, sink.NotificationCount())
	a.HandleOutput(zcodeEventLine(t, 5, contracts.ZCodeEventTurnSteerDeliveryChanged,
		`{"pendingInputId":"pending_7","requestedDelivery":"guide","admittedDelivery":"queue","fallbackReasonCode":"turn_not_steerable"}`))
	assert.Len(t, sink.Messages(), 1, "an input of the runtime's keeps the row of an event no reader knows")

	assert.Empty(t, sink.SteerResolutions())
	assert.Equal(t, 1, zcodePendingSteerCount(a), "LeapMux's steer %s waits", commandID)
}

// A steer that ZCode moved to the ordinary queue stays pending, and the turn
// that runs it as its prompt echoes it.
func TestZCodeSteerMovedToTheQueueEchoesAtItsTurn(t *testing.T) {
	t.Parallel()
	for name, payload := range map[string]func(commandID string) string{
		"by queued input": func(commandID string) string {
			return fmt.Sprintf(`{"pendingInputId":%q,"requestedDelivery":"guide","admittedDelivery":"queue","fallbackReasonCode":"turn_not_steerable"}`, zcodePendingInputID(commandID))
		},
		"by command": func(commandID string) string {
			return fmt.Sprintf(`{"pendingInputId":"other","intent":{"sourceCommandId":%q},"requestedDelivery":"guide","admittedDelivery":"queue"}`, commandID)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, stdin := newZCodeSteerAgent(t)
			commandID := steerZCode(t, a, stdin, 1, 1)
			a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventTurnSteerDeliveryChanged, payload(commandID)))
			assert.Empty(t, sink.Messages(), "LeapMux's own steer writes no row")
			assert.Equal(t, 1, zcodePendingSteerCount(a))
			assert.Empty(t, sink.SteerResolutions())

			a.HandleOutput(zcodeTurnEventLine(t, 3, "turn_2", contracts.ZCodeEventTurnStarted, fmt.Sprintf(`{"turnNumber":2,"inputId":%q}`, commandID)))
			assert.Equal(t, []agent.SteerResolution{zcodeTestSteerEcho(1, commandID, "turn_2")}, sink.SteerResolutions())
			assert.Zero(t, zcodePendingSteerCount(a))
		})
	}
}

// A turn that LeapMux's own send started echoes no steer.
func TestZCodeTurnStartOfAnotherInputLeavesTheSteerPending(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newZCodeSteerAgent(t)
	steerZCode(t, a, stdin, 1, 1)
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventTurnStarted, `{"turnNumber":2,"input":"hi","inputId":"abc123"}`))
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventTurnStarted, `{"turnNumber":3,"input":"hi"}`))
	assert.Empty(t, sink.SteerResolutions())
	assert.Equal(t, 1, zcodePendingSteerCount(a))
}

// An interrupt takes a queued steer back before it stops the turn, because the
// stop would drain it into the conversation, where no model request reads it.
// The withdrawal starts from the newest revision and follows a stale answer.
func TestZCodeInterruptWithdrawsAQueuedSteerBeforeTheStop(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newZCodeSteerAgent(t)
	commandID := steerZCode(t, a, stdin, 1, 1)
	answerZCodeCommand(t, a, stdin, zcodeCommandDeleteQueueItem, 1, zcodeCommandAnswer(zcodeCommandStale, 9))
	answerZCodeCommand(t, a, stdin, zcodeCommandDeleteQueueItem, 2, zcodeCommandAnswer(zcodeCommandAccepted, 9))
	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt(agent.StopContext{}))

	var methods []string
	for _, req := range stdin.Requests(t) {
		methods = append(methods, req.Method)
	}
	assert.Equal(t, []string{MethodCommand, MethodCommand, MethodCommand, MethodSessionStop}, methods, "the steer, two withdrawals, then the stop")
	deletes := zcodeSentCommandsOf(t, stdin, zcodeCommandDeleteQueueItem)
	require.Len(t, deletes, 2)
	for i, revision := range []int64{8, 9} {
		require.NotNil(t, deletes[i].BaseRevision)
		assert.Equal(t, revision, *deletes[i].BaseRevision)
		assert.JSONEq(t, fmt.Sprintf(`{"queueItemId":%q}`, zcodePendingInputID(commandID)), string(deletes[i].Payload))
		assert.True(t, strings.HasPrefix(deletes[i].CommandID, zcodeWithdrawCommandPrefix), deletes[i].CommandID)
	}
	assert.NotEqual(t, deletes[0].CommandID, deletes[1].CommandID, "each attempt is a command of its own")
	assert.Equal(t, []agent.SteerResolution{zcodeTestSteerResolution(1, agent.SteerOutcomeDropped, agent.NativeInputReceipt{})}, sink.SteerResolutions(),
		"the queue runs the steer as the next turn")

	// ZCode then reports the removal, which belongs to the withdrawal.
	a.HandleOutput(zcodeSteerDiscardLine(t, 2, "user_removed", zcodePendingInputID(commandID)))
	assert.Zero(t, sink.NotificationCount())
	assert.Len(t, sink.SteerResolutions(), 1)
}

// A stop that the browser sends as a raw frame takes the steer back too.
func TestZCodeRawStopWithdrawsAQueuedSteerBeforeTheStop(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newZCodeSteerAgent(t)
	commandID := steerZCode(t, a, stdin, 1, 1)
	answerZCodeCommand(t, a, stdin, zcodeCommandDeleteQueueItem, 1, zcodeCommandAnswer(zcodeCommandAccepted, 8))
	go func() {
		deadline := time.Now().Add(zcodeTestRPCTimeout)
		for time.Now().Before(deadline) {
			for _, frame := range stdin.Frames() {
				if strings.Contains(string(frame), `"id":4242`) {
					a.HandleOutput(zcodeReplyLine(t, 4242, json.RawMessage(`{}`)))
					return
				}
			}
			time.Sleep(2 * time.Millisecond)
		}
		t.Errorf("the raw stop was never sent")
	}()
	require.NoError(t, a.SendRawInput(agent.RawInput{Data: []byte(`{"id":4242,"method":"session/stop","params":{"sessionId":"sess-1"}}`)}))
	var methods []string
	for _, req := range stdin.Requests(t) {
		methods = append(methods, req.Method)
	}
	assert.Equal(t, []string{MethodCommand, MethodCommand, MethodSessionStop}, methods)
	assert.Equal(t, []agent.SteerResolution{zcodeTestSteerResolution(1, agent.SteerOutcomeDropped, agent.NativeInputReceipt{})}, sink.SteerResolutions())
	assert.Equal(t, zcodeCommandDeleteQueueItem, zcodeSentCommands(t, stdin)[1].Type, "steer %s", commandID)
}

// A withdrawal that ZCode does not perform leaves the delivery unknown: an
// input that the model read was echoed by its drain, which precedes the
// answer. A steer whose queued input ZCode never stated cannot be addressed.
func TestZCodeWithdrawalThatDoesNotDeleteIsUncertain(t *testing.T) {
	t.Parallel()
	t.Run("a refused deletion", func(t *testing.T) {
		t.Parallel()
		a, sink, stdin := newZCodeSteerAgent(t)
		steerZCode(t, a, stdin, 1, 1)
		answerZCodeCommand(t, a, stdin, zcodeCommandDeleteQueueItem, 1, zcodeCommandAnswer("noop", 9))
		answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
		require.NoError(t, a.Interrupt(agent.StopContext{}))
		assert.Equal(t, []agent.SteerResolution{zcodeTestSteerResolution(1, agent.SteerOutcomeUncertain, agent.NativeInputReceipt{})}, sink.SteerResolutions())
		a.Mu.Lock()
		withdrawn := len(a.withdrawnSteerInputs)
		a.Mu.Unlock()
		assert.Zero(t, withdrawn, "a discard that ZCode reports later is not LeapMux's")
	})
	t.Run("a stale answer each time", func(t *testing.T) {
		t.Parallel()
		a, sink, stdin := newZCodeSteerAgent(t)
		steerZCode(t, a, stdin, 1, 1)
		for attempt := 1; attempt <= zcodeCompareAndSetAttempts; attempt++ {
			answerZCodeCommand(t, a, stdin, zcodeCommandDeleteQueueItem, attempt, zcodeCommandAnswer(zcodeCommandStale, int64(8+attempt)))
		}
		answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
		require.NoError(t, a.Interrupt(agent.StopContext{}))
		assert.Len(t, zcodeSentCommandsOf(t, stdin, zcodeCommandDeleteQueueItem), zcodeCompareAndSetAttempts)
		assert.Equal(t, []agent.SteerResolution{zcodeTestSteerResolution(1, agent.SteerOutcomeUncertain, agent.NativeInputReceipt{})}, sink.SteerResolutions())
	})
	t.Run("an unstated queued input", func(t *testing.T) {
		t.Parallel()
		a, sink, stdin := newZCodeSteerAgent(t)
		answerZCodeCommand(t, a, stdin, zcodeCommandSendText, 1, zcodeAcceptedSteer("queue"))
		require.NoError(t, agenttest.SteerInput(a, zcodeTestSteerRequest(1, "unqueued")))
		answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
		require.NoError(t, a.Interrupt(agent.StopContext{}))
		assert.Empty(t, zcodeSentCommandsOf(t, stdin, zcodeCommandDeleteQueueItem))
		assert.Equal(t, []agent.SteerResolution{zcodeTestSteerResolution(1, agent.SteerOutcomeUncertain, agent.NativeInputReceipt{})}, sink.SteerResolutions())
	})
}

// A turn that ends with a steer still queued (a failed turn keeps it there)
// takes it back off the read loop, and the queue runs it as the next turn.
func TestZCodeTurnEndWithdrawsALeftoverSteer(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newZCodeSteerAgent(t)
	commandID := steerZCode(t, a, stdin, 1, 1)
	answerZCodeCommand(t, a, stdin, zcodeCommandDeleteQueueItem, 1, zcodeCommandAnswer(zcodeCommandAccepted, 9))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventTurnFailed, `{"error":{"message":"probe failure"}}`))
	require.Eventually(t, func() bool { return len(sink.SteerResolutions()) == 1 }, zcodeTestRPCTimeout, 2*time.Millisecond)
	assert.Equal(t, []agent.SteerResolution{zcodeTestSteerResolution(1, agent.SteerOutcomeDropped, agent.NativeInputReceipt{})}, sink.SteerResolutions())
	deletes := zcodeSentCommandsOf(t, stdin, zcodeCommandDeleteQueueItem)
	require.Len(t, deletes, 1)
	assert.JSONEq(t, fmt.Sprintf(`{"queueItemId":%q}`, zcodePendingInputID(commandID)), string(deletes[0].Payload))
}

func TestZCodeTurnEndWithNoPendingSteerSendsNoCommand(t *testing.T) {
	t.Parallel()
	a, _, stdin := newZCodeSteerAgent(t)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventTurnFailed, `{"error":{"message":"probe failure"}}`))
	assert.Empty(t, zcodeSentCommands(t, stdin))
}

// A turn that starts before the withdrawal runs keeps the steers for its own
// end: ZCode may run them in that turn.
func TestZCodeTurnEndWithdrawalSkipsARunningTurn(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newZCodeSteerAgent(t)
	steerZCode(t, a, stdin, 1, 1)
	a.withdrawZCodeSteersAfterTurnEnd()
	assert.Empty(t, zcodeSentCommandsOf(t, stdin, zcodeCommandDeleteQueueItem))
	assert.Empty(t, sink.SteerResolutions())
	assert.Equal(t, 1, zcodePendingSteerCount(a))
}

// A session that a new one replaced runs no turn again, so its steers drop
// and the queue runs them in the new session.
func TestZCodeReplacedSessionDropsItsSteers(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newZCodeSteerAgent(t)
	steerZCode(t, a, stdin, 1, 1)
	steerZCode(t, a, stdin, 2, 2)
	a.dropZCodeSteersOfReplacedSession()
	assert.ElementsMatch(t, []agent.SteerResolution{
		zcodeTestSteerResolution(1, agent.SteerOutcomeDropped, agent.NativeInputReceipt{}),
		zcodeTestSteerResolution(2, agent.SteerOutcomeDropped, agent.NativeInputReceipt{}),
	}, sink.SteerResolutions())
	assert.Zero(t, zcodePendingSteerCount(a))
}

// The process exit owns the queue's recovery, so the agent forgets its steers
// without a report.
func TestZCodeProcessEndForgetsTheSteers(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newZCodeSteerAgent(t)
	steerZCode(t, a, stdin, 1, 1)
	a.resetZCodeSteers()
	assert.Zero(t, zcodePendingSteerCount(a))
	assert.Empty(t, sink.SteerResolutions())
}

func TestZCodeSteersResolveInNativeDrainOrder(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newZCodeSteerAgent(t)
	first := steerZCode(t, a, stdin, 1, 1)
	second := steerZCode(t, a, stdin, 2, 2)
	a.HandleOutput(zcodeSteerDrainedLine(t, 3, []string{second}, nil))
	assert.Equal(t, []agent.SteerResolution{zcodeTestSteerEcho(2, second, "turn_1")}, sink.SteerResolutions())
	a.HandleOutput(zcodeSteerDrainedLine(t, 4, []string{first}, nil))
	assert.Equal(t, []agent.SteerResolution{
		zcodeTestSteerEcho(2, second, "turn_1"),
		zcodeTestSteerEcho(1, first, "turn_1"),
	}, sink.SteerResolutions())
}

func TestZCodeSteerDrainWithAConflictingTurnLeavesTheReplyUnproved(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newZCodeSteerAgent(t)
	commandID := steerZCode(t, a, stdin, 1, 1)
	payload := fmt.Sprintf(`{"queryIds":[%q],"targetTurnId":"turn_1"}`, commandID)
	a.HandleOutput(zcodeTurnEventLine(t, 2, "another-turn", contracts.ZCodeEventTurnSteerDrained, payload))
	assert.Equal(t, []agent.SteerResolution{zcodeTestSteerResolution(1, agent.SteerOutcomeEchoed, agent.NativeInputReceipt{})}, sink.SteerResolutions())
}

func TestZCodeRecoveredTurnKeepsLeadOutcomeAndRetiresSteerReply(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newZCodeSteerAgent(t)
	commandID := steerZCode(t, a, stdin, 1, 1)
	a.HandleOutput(zcodeSteerDrainedLine(t, 2, []string{commandID}, nil))
	echo := zcodeTestSteerEcho(1, commandID, "turn_1")
	require.Equal(t, []agent.SteerResolution{echo}, sink.SteerResolutions())

	a.dispatchMu.Lock()
	defer a.unlockZCodeDispatch()
	a.markZCodeCanonicalRecoveryLocked()
	a.Mu.Lock()
	owner := a.turns["turn_1"]
	a.Mu.Unlock()
	require.NotNil(t, owner)
	a.persistZCodeRecoveryEvidenceForOwner(owner, agent.MessageCompletionComplete,
		[]byte(`{"type":"state.updated","payload":{"reason":"prompt_completed"}}`), false)

	rows := sink.Messages()
	require.NotEmpty(t, rows)
	assert.Equal(t, "lead", rows[len(rows)-1].NativeInputID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, rows[len(rows)-1].InputTurnOutcome)
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		echo.Receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, sink.NativeReplyRetirements())
}

package ohmypi

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSendInputArmsTheTurnAndSendsAFollowUpPrompt(t *testing.T) {
	t.Parallel()
	r := newRig(t)

	require.NoError(t, r.agent.SendInput("say hello", nil))

	prompts := r.commandsOfType(CommandPrompt)
	require.Len(t, prompts, 1)
	assert.Equal(t, "say hello", prompts[0].Payload["message"])
	assert.Equal(t, streamingBehaviorFollowUp, prompts[0].Payload["streamingBehavior"],
		"a new turn queues behind a run omp may have started by itself")
	_, hasImages := prompts[0].Payload["images"]
	assert.False(t, hasImages, "a text prompt carries no image list")

	active, published := r.sink.LastTurnActive()
	require.True(t, published)
	assert.True(t, active, "the turn is armed until its run starts")
}

func TestSendInputRefusesABusyAgent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"agent_start"}`)

	err := r.agent.SendInput("second", nil)
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, &r.sink.Sink, r.agent, err)
	assert.Empty(t, r.commandsOfType(CommandPrompt), "a refused input reaches omp not at all")
}

func TestPublishTurnActiveRisesItsToken(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	agenttest.AssertRisingTurnTokens(t, &r.sink.Sink, r.agent)
}

func TestSendInputForSessionRejectsMissingAndReplacedSessions(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	agenttest.AssertRejectsMissingAndReplacedSessions(t, r.agent)
	assert.Empty(t, r.commandsOfType(CommandPrompt))

	// The current session is the session FILE, the handle the agent reports.
	r.agent.Mu.Lock()
	handle := r.agent.sessionHandleLocked()
	r.agent.Mu.Unlock()
	require.NoError(t, r.agent.SendInputForSession(handle, "for this session", nil))
	assert.Len(t, r.commandsOfType(CommandPrompt), 1)
}

func TestSendInputInlinesTextAndSendsImages(t *testing.T) {
	t.Parallel()
	r := newRig(t)

	require.NoError(t, r.agent.SendInput("look", []*leapmuxv1.Attachment{
		{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("attached text")},
		{Filename: "shot.png", MimeType: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}},
	}))

	prompt := r.commandsOfType(CommandPrompt)[0]
	message, _ := prompt.Payload["message"].(string)
	assert.Contains(t, message, "look")
	assert.Contains(t, message, "attached text", "a text attachment is inlined into the message")
	images, _ := prompt.Payload["images"].([]any)
	require.Len(t, images, 1)
	image, _ := images[0].(map[string]any)
	assert.Equal(t, "image", image["type"])
	assert.Equal(t, "image/png", image["mimeType"])
	assert.Equal(t, "iVBORw==", image["data"])
}

func TestSendInputReleasesTheArmForALocalCommand(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandPrompt {
			return &rigReply{Data: json.RawMessage(`{"agentInvoked":false}`)}
		}
		return nil
	})

	require.NoError(t, r.agent.SendInput("/model", nil))

	active, published := r.sink.LastTurnActive()
	require.True(t, published)
	assert.False(t, active, "a slash command omp answered itself starts no turn")
	assert.Equal(t, []bool{true, false}, r.sink.TurnActives(),
		"the dispatch settles with a publish, so the input queue does not hold its next message")
}

func TestSendInputReturnsARefusal(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandPrompt {
			return &rigReply{Error: "No models available."}
		}
		return nil
	})

	err := r.agent.SendInput("hello", nil)
	require.Error(t, err)
	var refused *commandError
	require.True(t, errors.As(err, &refused))
	assert.Equal(t, "No models available.", refused.Message)
	active, _ := r.sink.LastTurnActive()
	assert.False(t, active, "a refused prompt releases its arm")
}

func TestSendInputReportsAnUncertainDelivery(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := testutil.DeadlineContext(t)
	ack := r.clock.Trap().NewTimer(ompPromptAckTimerTag)
	defer ack.Close()
	r.respond(func(recordedCommand) *rigReply { return &rigReply{Skip: true} })

	result := make(chan error, 1)
	go func() { result <- r.agent.SendInput("hello", nil) }()
	assert.Equal(t, promptAckTimeout, testutil.WaitForTimer(t, ctx, ack))
	r.clock.Advance(promptAckTimeout - time.Nanosecond).MustWait(ctx)
	select {
	case err := <-result:
		t.Fatalf("the wait ended before its limit: %v", err)
	default:
	}
	active, _ := r.sink.LastTurnActive()
	assert.True(t, active, "the turn stays armed while omp can still acknowledge the prompt")

	r.clock.Advance(time.Nanosecond).MustWait(ctx)
	select {
	case err := <-result:
		assert.ErrorIs(t, err, agent.ErrDeliveryUncertain)
		assert.ErrorContains(t, err, "within 10s")
	case <-ctx.Done():
		t.Fatal("the wait did not end at its limit")
	}
	active, _ = r.sink.LastTurnActive()
	assert.False(t, active, "an unacknowledged prompt releases its arm; a run it starts later arms the turn again")
}

func TestSendInputOnAStoppedAgent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.SetStoppedForTest(true)
	assert.ErrorContains(t, r.agent.SendInput("hello", nil), "stopped")
	assert.Empty(t, r.sink.TurnActives(), "a stopped agent arms no turn")
}

// omp exits while the worker waits for the acknowledgement. The exit is a
// failure, not an uncertain delivery: no process is left to run the prompt.
func TestSendInputReleasesTheArmWhenOmpExitsBeforeItsAcknowledgement(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	// The mock clock never ends the acknowledgement wait: only the exit may.
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandPrompt {
			return &rigReply{Skip: true}
		}
		return nil
	})
	result := make(chan error, 1)
	go func() { result <- r.agent.SendInput("hello", nil) }()
	r.waitForCommand(CommandPrompt, 1)

	require.NoError(t, r.stdinR.Close())
	select {
	case err := <-result:
		require.Error(t, err)
		assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
	case <-time.After(30 * time.Second):
		t.Fatal("the input still waits after omp exited")
	}
	active, _ := r.sink.LastTurnActive()
	assert.False(t, active, "the arm the prompt took is released")
}

func TestSendInputReleasesTheArmWhenThePromptCannotBeWritten(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	require.NoError(t, r.stdinW.Close())

	assert.ErrorContains(t, r.agent.SendInput("hello", nil), "write omp prompt")
	assert.Equal(t, []bool{true, false}, r.sink.TurnActives(),
		"the turn is armed before the write and released when the write fails")
}

// A steer that omp refused continues nothing, so an end with `isTerminal:
// false` that follows it still ends the turn.
func TestARefusedSteerContinuesNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandPrompt {
			return &rigReply{Error: "the steer queue is full"}
		}
		return nil
	})
	r.emit(`{"type":"agent_start"}`)

	var refused *commandError
	require.ErrorAs(t, r.agent.SteerInput("guide", nil), &refused)
	active, _ := r.sink.LastTurnActive()
	assert.True(t, active, "a refused steer leaves the running turn alone")

	r.emit(`{"type":"agent_end","isTerminal":false,"messages":[]}`)
	active, _ = r.sink.LastTurnActive()
	assert.False(t, active)
	assert.Len(t, turnEndRows(r.sink.Messages()), 1)
}

func TestPromptInvokesAgent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		data string
		want bool
	}{
		{name: "no data", data: "", want: true},
		{name: "a null", data: "null", want: true},
		{name: "a slash command omp answered itself", data: `{"agentInvoked":false}`, want: false},
		{name: "a prompt omp hands to the model", data: `{"agentInvoked":true}`, want: true},
		{name: "data that states nothing", data: `{}`, want: true},
		{name: "data of another shape", data: `["x"]`, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, promptInvokesAgent(json.RawMessage(tc.data)))
		})
	}
}

func TestPromptPayload(t *testing.T) {
	t.Parallel()
	text := &leapmuxv1.Attachment{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("attached text")}
	other := &leapmuxv1.Attachment{Filename: "more.txt", MimeType: "text/plain", Data: []byte("more text")}
	image := &leapmuxv1.Attachment{Filename: "shot.png", MimeType: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}}
	pdf := &leapmuxv1.Attachment{Filename: "a.pdf", MimeType: "application/pdf", Data: []byte("%PDF")}
	binary := &leapmuxv1.Attachment{Filename: "a.bin", MimeType: "application/x-thing", Data: []byte{0xff, 0xfe}}
	block := func(attachment *leapmuxv1.Attachment) string {
		return providerkit.BuildInlineTextAttachmentBlock(agent.ClassifyAttachments([]*leapmuxv1.Attachment{attachment})[0])
	}

	t.Run("a text attachment with no message opens the message", func(t *testing.T) {
		t.Parallel()
		payload := promptPayload("", []*leapmuxv1.Attachment{text})
		assert.Equal(t, block(text), payload["message"], "no blank line precedes the first block")
		assert.NotContains(t, payload, "images")
	})

	t.Run("text attachments follow the message in order", func(t *testing.T) {
		t.Parallel()
		payload := promptPayload("look", []*leapmuxv1.Attachment{text, nil, other})
		assert.Equal(t, "look\n\n"+block(text)+"\n\n"+block(other), payload["message"])
	})

	t.Run("an image alone keeps the message empty", func(t *testing.T) {
		t.Parallel()
		payload := promptPayload("", []*leapmuxv1.Attachment{image})
		assert.Equal(t, "", payload["message"])
		images, ok := payload["images"].([]map[string]any)
		require.True(t, ok)
		require.Len(t, images, 1)
		assert.Equal(t, "iVBORw==", images[0]["data"])
	})

	t.Run("a PDF and a binary file never reach omp", func(t *testing.T) {
		t.Parallel()
		payload := promptPayload("hi", []*leapmuxv1.Attachment{pdf, binary})
		assert.Equal(t, "hi", payload["message"])
		assert.NotContains(t, payload, "images")
	})
}

func TestSteerInput(t *testing.T) {
	t.Parallel()

	t.Run("refuses when no turn runs", func(t *testing.T) {
		r := newRig(t)
		assert.ErrorIs(t, r.agent.SteerInput("guide", nil), agent.ErrNoActiveTurn)
		assert.Empty(t, r.commandsOfType(CommandPrompt))
	})

	t.Run("sends a steering prompt into the running turn", func(t *testing.T) {
		r := newRig(t)
		r.emit(`{"type":"agent_start"}`)
		before := len(r.sink.TurnActives())

		require.NoError(t, r.agent.SteerInput("guide", nil))

		prompts := r.commandsOfType(CommandPrompt)
		require.Len(t, prompts, 1)
		assert.Equal(t, streamingBehaviorSteer, prompts[0].Payload["streamingBehavior"])
		assert.Len(t, r.sink.TurnActives(), before, "a steer arms nothing: the turn already runs")
	})

	t.Run("supports steering", func(t *testing.T) {
		assert.True(t, (&Agent{}).SupportsSteering())
	})
}

func TestInterrupt(t *testing.T) {
	t.Parallel()

	t.Run("is a no-op with no turn", func(t *testing.T) {
		r := newRig(t)
		require.NoError(t, r.agent.Interrupt())
		assert.Empty(t, r.commandsOfType(CommandAbort))
	})

	t.Run("sends abort and does not wait for its answer", func(t *testing.T) {
		r := newRig(t)
		r.respond(func(command recordedCommand) *rigReply {
			if command.Type == CommandAbort {
				// omp answers an abort only after the run ended.
				return &rigReply{Skip: true}
			}
			return nil
		})
		r.emit(`{"type":"agent_start"}`)

		require.NoError(t, r.agent.Interrupt())
		r.waitForCommand(CommandAbort, 1)
	})

	t.Run("refuses a stopped agent", func(t *testing.T) {
		r := newRig(t)
		r.agent.SetStoppedForTest(true)
		assert.ErrorContains(t, r.agent.Interrupt(), "stopped")
	})

	t.Run("wire format matches the provider classifier", func(t *testing.T) {
		r := newRig(t)
		r.emit(`{"type":"agent_start"}`)
		require.NoError(t, r.agent.Interrupt())
		abort := r.waitForCommand(CommandAbort, 1)[0]
		assert.True(t, ompProvider{}.IsInterrupt(string(abort.Raw)))
	})
}

func TestTheInterruptedTurnReadsAsInterrupted(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"agent_start"}`)
	require.NoError(t, r.agent.Interrupt())

	// omp can end a stopped run on an error when a tool was still running.
	r.emit(`{"type":"agent_end","isTerminal":true,"messages":[{"role":"assistant","stopReason":"error","errorMessage":"This operation was aborted"}]}`)

	messages := r.sink.Messages()
	require.NotEmpty(t, messages)
	last := messages[len(messages)-1]
	assert.True(t, last.TurnEnd)
	assert.Equal(t, agent.MessageCompletionInterrupted, last.Completion)
}

func TestCompactContext(t *testing.T) {
	t.Parallel()

	t.Run("arms the turn and persists omp's answer", func(t *testing.T) {
		r := newRig(t)
		release := make(chan struct{})
		r.respond(func(command recordedCommand) *rigReply {
			if command.Type == contracts.OhMyPiCommandCompact {
				<-release
				return &rigReply{Data: json.RawMessage(`{"summary":"short","tokensBefore":60030}`)}
			}
			return nil
		})

		require.NoError(t, r.agent.CompactContext())
		active, _ := r.sink.LastTurnActive()
		assert.True(t, active, "the input queue holds the next message while omp compacts")
		notifications := r.sink.Notifications()
		require.NotEmpty(t, notifications)
		assert.Equal(t, contracts.NotificationTypeCompacting, notifications[len(notifications)-1][contracts.NotificationFieldType])

		close(release)
		waitFor(t, func() bool {
			active, _ := r.sink.LastTurnActive()
			return !active && r.sink.NotificationCount() == 1
		})
		frame := r.sink.LastNotification().Content
		assert.Contains(t, string(frame), `"command":"compact"`)
		assert.Contains(t, string(frame), `"tokensBefore":60030`)
		assert.Equal(t, agent.NotificationKindCompactionBoundary, ompProvider{}.Classify(frame).Kind)
	})

	t.Run("persists a refusal as a status", func(t *testing.T) {
		r := newRig(t)
		r.respond(func(command recordedCommand) *rigReply {
			if command.Type == contracts.OhMyPiCommandCompact {
				return &rigReply{Error: "Nothing to compact (session too small)"}
			}
			return nil
		})
		require.NoError(t, r.agent.CompactContext())
		waitFor(t, func() bool { return r.sink.NotificationCount() == 1 })
		frame := r.sink.LastNotification().Content
		assert.Contains(t, string(frame), "Nothing to compact")
		assert.Equal(t, agent.NotificationKindStatus, ompProvider{}.Classify(frame).Kind)
		waitFor(t, func() bool {
			active, _ := r.sink.LastTurnActive()
			return !active
		})
	})

	t.Run("refuses a busy agent", func(t *testing.T) {
		r := newRig(t)
		r.emit(`{"type":"agent_start"}`)
		assert.ErrorIs(t, r.agent.CompactContext(), agent.ErrAgentBusy)
		assert.Empty(t, r.commandsOfType(contracts.OhMyPiCommandCompact))
	})

	t.Run("refuses a stopped agent", func(t *testing.T) {
		r := newRig(t)
		r.agent.SetStoppedForTest(true)
		assert.ErrorContains(t, r.agent.CompactContext(), "stopped")
		assert.Empty(t, r.sink.TurnActives(), "a stopped agent arms no turn")
		assert.Empty(t, r.sink.Notifications(), "no compaction notice shows")
	})

	t.Run("an exit before omp answers persists the failure and releases the turn", func(t *testing.T) {
		r := newRig(t)
		r.respond(func(command recordedCommand) *rigReply {
			if command.Type == contracts.OhMyPiCommandCompact {
				return &rigReply{Skip: true}
			}
			return nil
		})
		require.NoError(t, r.agent.CompactContext())
		r.waitForCommand(contracts.OhMyPiCommandCompact, 1)

		require.NoError(t, r.stdinR.Close())
		waitFor(t, func() bool {
			active, _ := r.sink.LastTurnActive()
			return !active
		})
		notifications := r.sink.Notifications()
		require.Len(t, notifications, 2)
		assert.Equal(t, contracts.NotificationTypeCompacting, notifications[0][contracts.NotificationFieldType])
		assert.Equal(t, contracts.NotificationTypeAgentError, notifications[1][contracts.NotificationFieldType],
			"the reader learns why no compaction follows the notice")
		assert.Zero(t, r.sink.NotificationCount(), "no omp frame answered the command")
	})
}

func TestStopPersistsTheUnfinishedOutput(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(
		`{"type":"agent_start"}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Half a sen"}}`,
		`{"type":"tool_execution_start","toolCallId":"call_1","toolName":"bash","args":{"command":"sleep 9"}}`,
		`{"type":"tool_execution_update","toolCallId":"call_1","toolName":"bash","args":{"command":"sleep 9"},"partialResult":{"content":[{"type":"text","text":"partial\n"}],"details":{}}}`,
	)

	r.agent.Stop()

	assert.NotEmpty(t, r.commandsOfType(CommandAbort), "a running turn is aborted before stdin closes")
	var assembled, tool *agenttest.Message
	messages := r.sink.Messages()
	for i, message := range messages {
		switch {
		case message.Closing && message.SpanID == "call_1":
			tool = &messages[i]
		case strings.Contains(string(message.Content), "Half a sen"):
			assembled = &messages[i]
		}
	}
	require.NotNil(t, assembled, "the streamed text is kept")
	assert.Contains(t, string(assembled.Content), string(agent.MessageCompletionInterrupted),
		"the kept text states that the stop cut it short")
	require.NotNil(t, tool, "the running call is closed with its start frame")
	assert.Contains(t, string(tool.Content), `"type":"tool_execution_start"`)
	assert.Contains(t, string(tool.SupplementalContent), "partial")
	active, _ := r.sink.LastTurnActive()
	assert.False(t, active)
}

// omp answers an abort only after the run it stops has ended, so Stop waits for
// that answer at most abortWaitOnStop. The teardown then runs all the same, and
// the unfinished output is kept only after the wait, when no frame can follow.
func TestStopWaitsForTheAbortAtMostItsLimit(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := testutil.DeadlineContext(t)
	abort := r.clock.Trap().NewTimer(providerkit.AwaitResponseTimerTag, CommandAbort)
	defer abort.Close()
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandAbort {
			return &rigReply{Skip: true}
		}
		return nil
	})
	r.emit(
		`{"type":"agent_start"}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Half a sen"}}`,
	)
	r.awaitStatsRead()

	stopped := make(chan struct{})
	go func() {
		r.agent.Stop()
		close(stopped)
	}()
	assert.Equal(t, abortWaitOnStop, testutil.WaitForTimer(t, ctx, abort))
	r.clock.Advance(abortWaitOnStop - time.Nanosecond).MustWait(ctx)
	select {
	case <-stopped:
		t.Fatal("Stop returned before the abort's limit")
	default:
	}
	assert.False(t, r.agent.IsStopped(), "stdin stays open while omp can still answer the abort")
	assert.Empty(t, r.sink.Messages())

	r.clock.Advance(time.Nanosecond).MustWait(ctx)
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("Stop did not go on at the abort's limit")
	}
	assert.True(t, r.agent.IsStopped())
	assert.Len(t, r.commandsOfType(CommandAbort), 1)
	messages := r.sink.Messages()
	require.Len(t, messages, 1)
	assert.Contains(t, string(messages[0].Content), "Half a sen")
	assert.Contains(t, string(messages[0].Content), string(agent.MessageCompletionInterrupted))
}

func TestStopWithNoTurnSendsNoAbort(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.Stop()
	assert.True(t, r.agent.IsStopped())
	assert.Empty(t, r.commandsOfType(CommandAbort), "nothing runs, so nothing is aborted")

	messages := len(r.sink.Messages())
	r.agent.Stop()
	assert.Len(t, r.sink.Messages(), messages, "a second stop persists nothing more")
}

// A stop before a restart discards the output, so the restarted agent does not
// find a copy of the unfinished text and calls in its transcript.
func TestAStopThatDiscardsTheOutputPersistsNoUnfinishedOutput(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(
		`{"type":"agent_start"}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Half a sen"}}`,
		frameBashStart,
	)
	persisted := len(r.sink.Messages())

	r.agent.DiscardOutput()
	r.agent.Stop()

	assert.Len(t, r.sink.Messages(), persisted, "neither the streamed text nor the running call is persisted")
	active, _ := r.sink.LastTurnActive()
	assert.False(t, active)
}

// omp exits with nothing asking it to. Wait keeps what it left unfinished and
// states that the output ended on an error.
func TestAnUnexpectedExitKeepsTheUnfinishedOutputAsAnError(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(
		`{"type":"agent_start"}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Half a sen"}}`,
		frameBashStart,
		frameTaskStart, frameStarted,
	)

	require.NoError(t, r.stdinR.Close())
	require.NoError(t, r.agent.Wait())

	var assembled, tool *agenttest.Message
	messages := r.sink.Messages()
	for i, message := range messages {
		switch {
		case message.Closing && message.SpanID == "call_1":
			tool = &messages[i]
		case strings.Contains(string(message.Content), "Half a sen"):
			assembled = &messages[i]
		}
	}
	require.NotNil(t, assembled, "the streamed text is kept")
	assert.Equal(t, assembledTextRow("Half a sen", agent.MessageCompletionError), assembledRow(t, *assembled))
	require.NotNil(t, tool, "the running call is closed")
	assert.Equal(t, agent.MessageCompletionError, tool.Completion)
	assert.Equal(t, bgtask.StatusFailed, subagentRow(t, r, "Probe").Status, "the subagent ended with the process")
	active, _ := r.sink.LastTurnActive()
	assert.False(t, active)
}

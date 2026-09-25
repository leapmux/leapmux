package kimi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// promptBody is the part of POST .../prompts a test reads.
type promptBody struct {
	Content []map[string]any `json:"content"`
	AgentID string           `json:"agent_id"`
}

func decodePrompt(t *testing.T, request fakeKapRequest) promptBody {
	t.Helper()
	var body promptBody
	require.NoError(t, json.Unmarshal(request.Body, &body))
	return body
}

func TestKimiSendInput(t *testing.T) {
	t.Parallel()

	t.Run("posts the prompt to the main agent", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		require.NoError(t, rig.agent.SendInput("Explain the build.", nil))

		prompts := rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/prompts"))
		require.Len(t, prompts, 1)
		body := decodePrompt(t, prompts[0])
		assert.Empty(t, body.AgentID, "the main agent is the server's default")
		assert.Equal(t, []map[string]any{{"type": "text", "text": "Explain the build."}}, body.Content)

		rig.agent.Mu.Lock()
		active := rig.agent.turnActive
		rig.agent.Mu.Unlock()
		assert.False(t, active, "the turn flag moves on turn.started, never on the POST reply")
	})

	t.Run("refuses input while a turn runs", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, map[string]any{"type": "turn.started", "turnId": 0, "origin": map[string]any{"kind": "user"}})
		err := rig.agent.SendInput("Another message.", nil)
		agenttest.AssertBusyRefusalRepublishesTheTurn(t, &rig.sink.Sink, rig.agent, err)
		var busy *agent.AgentBusyError
		require.ErrorAs(t, err, &busy)
		assert.True(t, busy.ActiveTurnSteerable, "the running main turn takes steering")
		assert.Empty(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), "/prompts")))
	})

	t.Run("validates the session", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		agenttest.AssertRejectsMissingAndReplacedSessions(t, rig.agent)
		require.NoError(t, rig.agent.SendInputForSession(rig.sessionID(), "Hello.", nil))
	})

	t.Run("refuses input once stopped", func(t *testing.T) {
		t.Parallel()
		a := newOfflineKimiAgent(t, &agenttest.Sink{})
		a.SetStoppedForTest(true)
		require.ErrorContains(t, a.SendInput("Hello.", nil), "stopped")
	})

	t.Run("refuses input with no session", func(t *testing.T) {
		t.Parallel()
		a := newOfflineKimiAgent(t, &agenttest.Sink{})
		a.sessionID = ""
		require.ErrorContains(t, a.SendInput("Hello.", nil), "no Kimi Code session")
	})

	t.Run("reports a refusal the server states as a clear failure", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{Code: 40001, Msg: "model not configured"})
		err := rig.agent.SendInput("Hello.", nil)
		require.Error(t, err)
		assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
		assert.Contains(t, err.Error(), "model not configured")
	})

	t.Run("reports a lost reply as an uncertain delivery", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{Drop: true})
		err := rig.agent.SendInput("Hello.", nil)
		require.ErrorIs(t, err, agent.ErrDeliveryUncertain, "the server may have taken the prompt, so the queue must not send it again")
	})

	t.Run("refuses an attachment before it sends", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		err := rig.agent.SendInput("Read this.", []*leapmuxv1.Attachment{{Filename: "spec.pdf", MimeType: "application/pdf", Data: []byte("%PDF")}})
		require.ErrorContains(t, err, "PDF")
		assert.Empty(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), "/prompts")))
	})

	// The current model decides whether an image travels: the server would take
	// an image for a text-only model and drop it on the way to the model.
	t.Run("sends an image to a model that takes images", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		data := []byte{0x89, 0x50, 0x4e, 0x47}
		require.NoError(t, rig.agent.SendInput("Look.", []*leapmuxv1.Attachment{{Filename: "shot.png", MimeType: "image/png", Data: data}}))
		prompts := rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/prompts"))
		require.Len(t, prompts, 1)
		assert.Equal(t, []map[string]any{
			{"type": "text", "text": "Look."},
			{"type": "image", "source": map[string]any{"kind": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString(data)}},
		}, decodePrompt(t, prompts[0]).Content)
	})

	t.Run("refuses an image for a text-only model before it sends", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{Options: options(agent.OptionIDModel, "kimi-text")})
		err := rig.agent.SendInput("Look.", []*leapmuxv1.Attachment{{Filename: "shot.png", MimeType: "image/png", Data: []byte{0x89}}})
		require.ErrorContains(t, err, "does not take images")
		assert.Empty(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), "/prompts")))
	})
}

func TestKimiPublishTurnActiveRaisesItsToken(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineKimiAgent(t, sink)
	agenttest.AssertRisingTurnTokens(t, sink, a)
}

func TestKimiSteerInput(t *testing.T) {
	t.Parallel()

	startTurn := func(t *testing.T, rig *kimiTestRig) {
		t.Helper()
		rig.feed(t, map[string]any{"type": "turn.started", "turnId": 0, "origin": map[string]any{"kind": "user"}})
		rig.fake.setBusy(rig.sessionID(), true)
	}

	t.Run("steers the queued prompt into the running turn", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		startTurn(t, rig)
		require.NoError(t, rig.agent.SteerInput("Use the other file.", nil))
		prompts := rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/prompts"))
		require.Len(t, prompts, 1)
		steers := rig.fake.requestsTo("POST " + kimiItemPath(rig.sessionID(), "prompts", "prompt_1", ":steer"))
		require.Len(t, steers, 1, "the queued prompt is steered by the id the POST returned")
	})

	t.Run("needs a running turn", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		require.ErrorIs(t, rig.agent.SteerInput("Too late.", nil), agent.ErrNoActiveTurn)
		assert.True(t, rig.agent.SupportsSteering())
	})

	t.Run("delivers a prompt that started a turn of its own", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, map[string]any{"type": "turn.started", "turnId": 0, "origin": map[string]any{"kind": "user"}})
		// The server ended the turn before the prompt arrived, so the prompt ran.
		require.NoError(t, rig.agent.SteerInput("Also this.", nil))
		for _, route := range rig.fake.routes() {
			assert.NotContains(t, route, ":steer")
		}
	})

	t.Run("delivers a prompt the turn end launched", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		startTurn(t, rig)
		rig.fake.reply("POST "+kimiItemPath(rig.sessionID(), "prompts", "prompt_1", ":steer"), fakeKapReply{Code: kimiCodePromptNotPending})
		require.NoError(t, rig.agent.SteerInput("Also this.", nil))
		assert.Empty(t, rig.fake.requestsTo("POST "+kimiItemPath(rig.sessionID(), "prompts", "prompt_1", kimiActionAbort)))
	})

	t.Run("withdraws the queued prompt when the steer fails", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		startTurn(t, rig)
		rig.fake.reply("POST "+kimiItemPath(rig.sessionID(), "prompts", "prompt_1", ":steer"), fakeKapReply{Code: 50000, Msg: "boom"})
		err := rig.agent.SteerInput("Also this.", nil)
		require.Error(t, err)
		assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain, "the withdrawn prompt reached nobody")
		assert.Len(t, rig.fake.requestsTo("POST "+kimiItemPath(rig.sessionID(), "prompts", "prompt_1", kimiActionAbort)), 1)
	})

	t.Run("reports an uncertain delivery when the withdrawal fails too", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		startTurn(t, rig)
		rig.fake.reply("POST "+kimiItemPath(rig.sessionID(), "prompts", "prompt_1", ":steer"), fakeKapReply{Code: 50000, Msg: "boom"})
		rig.fake.reply("POST "+kimiItemPath(rig.sessionID(), "prompts", "prompt_1", kimiActionAbort), fakeKapReply{Code: 50000, Msg: "boom"})
		require.ErrorIs(t, rig.agent.SteerInput("Also this.", nil), agent.ErrDeliveryUncertain)
	})

	t.Run("refuses a prompt id the server would not issue", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		startTurn(t, rig)
		rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{Data: map[string]any{"prompt_id": "../x", "status": kimiPromptQueued}})
		require.ErrorContains(t, rig.agent.SteerInput("Also this.", nil), "prompt id")
	})

	t.Run("refuses once stopped", func(t *testing.T) {
		t.Parallel()
		a := newOfflineKimiAgent(t, &agenttest.Sink{})
		a.SetStoppedForTest(true)
		require.ErrorContains(t, a.SteerInput("Hello.", nil), "stopped")
	})

	t.Run("reports a prompt the server refuses and steers nothing", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		startTurn(t, rig)
		rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{Code: 40001, Msg: "prompt too long"})
		err := rig.agent.SteerInput("Also this.", nil)
		require.ErrorContains(t, err, "prompt too long")
		assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain, "the server stated that it took nothing")
		for _, route := range rig.fake.routes() {
			assert.NotContains(t, route, ":steer")
		}
	})

	t.Run("refuses an attachment before it posts", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		startTurn(t, rig)
		err := rig.agent.SteerInput("Read this.", []*leapmuxv1.Attachment{{Filename: "a.bin", MimeType: "application/octet-stream", Data: []byte{0xff, 0x00}}})
		require.ErrorContains(t, err, "binary")
		assert.Empty(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), "/prompts")))
	})
}

// Kimi Code steers only into a turn that a tracked prompt started: the user's
// own, a skill activation, or a plugin command. loopService.steer refuses every
// other turn with PROMPT_NOT_FOUND, and the steered text would wait in the
// server's queue behind that turn.
func TestKimiTurnSteerability(t *testing.T) {
	t.Parallel()

	for _, origin := range []string{contracts.KimiOriginUser, contracts.KimiOriginSkillActivation, contracts.KimiOriginPluginCommand} {
		t.Run("a turn of origin "+origin+" takes a steer", func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": origin}})
			assert.Equal(t, agent.TurnState{Active: true, Steerable: true}, rig.agent.PublishTurnActive())
			var busy *agent.AgentBusyError
			require.ErrorAs(t, rig.agent.SendInput("More.", nil), &busy)
			assert.True(t, busy.ActiveTurnSteerable)
		})
	}

	for _, origin := range []string{
		contracts.KimiOriginSystemTrigger, contracts.KimiOriginTask, contracts.KimiOriginBackgroundTask,
		contracts.KimiOriginCronJob, contracts.KimiOriginRetry, contracts.KimiOriginHookResult, "",
	} {
		t.Run("a turn of origin "+origin+" takes no steer", func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": origin}})
			rig.fake.setBusy(rig.sessionID(), true)
			assert.Equal(t, agent.TurnState{Active: true, Steerable: false}, rig.agent.PublishTurnActive(),
				"the queue waits for the turn to end")
			assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_UNSPECIFIED, rig.sink.TurnKinds()[0], "the turn.started publish states it too")

			var busy *agent.AgentBusyError
			require.ErrorAs(t, rig.agent.SendInput("More.", nil), &busy)
			assert.False(t, busy.ActiveTurnSteerable)

			err := rig.agent.SteerInput("More.", nil)
			require.ErrorAs(t, err, &busy, "the turn runs, so the message waits for it")
			assert.False(t, busy.ActiveTurnSteerable)
			assert.Empty(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), "/prompts")),
				"nothing reaches the server's queue, where it would wait behind the turn")
		})
	}

	t.Run("a turn end clears the steerability with the turn", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": contracts.KimiOriginUser}})
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnEnded, "turnId": 0, "reason": contracts.KimiTurnEndCompleted})
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 1, "origin": map[string]any{"kind": contracts.KimiOriginCronJob}})
		assert.Equal(t, agent.TurnState{Active: true, Steerable: false}, rig.agent.PublishTurnActive())
	})
}

func TestKimiSendRawInput(t *testing.T) {
	t.Parallel()

	t.Run("the raw abort frame interrupts the running turn", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, map[string]any{"type": "turn.started", "turnId": 0, "origin": map[string]any{"kind": "user"}})
		require.NoError(t, rig.agent.SendRawInput([]byte(`{"action":"abort"}`)))
		assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), kimiActionAbort)), 1)
	})

	t.Run("an interrupt with no turn sends nothing", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		require.NoError(t, rig.agent.Interrupt())
		assert.Empty(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), kimiActionAbort)))
	})

	t.Run("anything else is a control response", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		err := rig.agent.SendRawInput([]byte(`{"type":"control_response","response":{"request_id":"approval_1","response":{"decision":"approved"}}}`))
		require.ErrorIs(t, err, errKimiControlGone, "no request of that id is pending")
		require.Error(t, rig.agent.SendRawInput([]byte(`not json`)))
	})

	t.Run("refuses once stopped", func(t *testing.T) {
		t.Parallel()
		a := newOfflineKimiAgent(t, &agenttest.Sink{})
		a.SetStoppedForTest(true)
		require.ErrorContains(t, a.SendRawInput([]byte(`{"action":"abort"}`)), "stopped")
	})
}

func TestIsKimiRawAbort(t *testing.T) {
	t.Parallel()

	assert.True(t, isKimiRawAbort([]byte(`{"action":"abort"}`)))
	for _, other := range []string{``, `not json`, `{}`, `{"action":"compact"}`, `{"type":"abort"}`, `{"method":"session/stop"}`} {
		assert.False(t, isKimiRawAbort([]byte(other)), other)
	}
}

func TestKimiHandleOutputSkipsAFrameThatIsNotJSON(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineKimiAgent(t, sink)
	a.HandleOutput([]byte(`not json`))
	a.HandleOutput([]byte(`{"type":"assistant.delta","session_id":"session_1"}`))
	assert.Zero(t, sink.MessageCount())
	assert.Empty(t, sink.TurnActives())
}

// The stream's dispatcher and HandleOutput both reach the event handlers, and
// dispatchMu serializes them. Frames that arrive from many goroutines at once
// must leave every call closed and publish a repeated request once.
func TestKimiDispatchIsSafeForConcurrentFrames(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.startTurn(t, 0, contracts.KimiOriginUser)

	const calls = 32
	frames := make([][][]byte, calls)
	for i := range calls {
		id := fmt.Sprintf("call_%d", i)
		frames[i] = [][]byte{
			kimiEventFrame(t, "session_1", map[string]any{"type": contracts.KimiEventToolCallStarted, "turnId": 0, "toolCallId": id,
				"name": contracts.KimiToolRead, "args": map[string]any{"path": id}}),
			kimiEventFrame(t, "session_1", map[string]any{"type": contracts.KimiEventToolProgress, "turnId": 0, "toolCallId": id,
				"update": map[string]any{"kind": "stdout", "text": "x"}}),
			kimiEventFrame(t, "session_1", map[string]any{"type": contracts.KimiEventToolResult, "turnId": 0, "toolCallId": id, "output": "ok"}),
		}
	}
	approval := kimiEventFrame(t, "session_1", approvalEvent("approval_1", map[string]any{"kind": "command", "command": "ls"}))

	var wg sync.WaitGroup
	for i := range calls {
		wg.Go(func() {
			for _, frame := range frames[i] {
				rig.agent.HandleOutput(frame)
			}
			rig.agent.HandleOutput(approval)
		})
	}
	wg.Wait()

	assert.Equal(t, 1, rig.sink.PublishedControlCount(), "a request that arrives many times at once publishes once")
	closed := rig.sink.ClosedSpans()
	for i := range calls {
		assert.Contains(t, closed, kimiSpanID("session_1", kimiMainAgentID, 0, fmt.Sprintf("call_%d", i)))
	}
	rig.agent.Mu.Lock()
	uses, open := rig.agent.TurnToolUses, len(rig.agent.runs[kimiMainAgentID].tools)
	rig.agent.Mu.Unlock()
	assert.EqualValues(t, calls, uses, "each result counts once")
	assert.Zero(t, open, "every call that started also closed")
}

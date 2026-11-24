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
	PromptID string           `json:"prompt_id"`
	Content  []map[string]any `json:"content"`
	AgentID  string           `json:"agent_id"`
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
		assert.False(t, active, "the native running header moves the turn flag; the POST reply does not")
	})

	t.Run("refuses input while a turn runs", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.startPublicMainTurn(t, "t0", 0, `{"kind":"user"}`)
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

func TestKimiBlockedPromptReleasesTheTurn(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
		return map[string]any{"prompt_id": decodePrompt(t, request).PromptID, "status": "blocked"}
	}})
	before := len(rig.sink.TurnActives())
	require.NoError(t, rig.agent.SendInput("The hook blocks this prompt.", nil))
	assert.Equal(t, []bool{false}, rig.sink.TurnActives()[before:], "the queue must receive the inactive state before dispatch returns")
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
		rig.startPublicMainTurn(t, "t0", 0, `{"kind":"user"}`)
		rig.fake.setBusy(rig.sessionID(), true)
	}

	t.Run("steers the queued prompt into the running turn", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		startTurn(t, rig)
		receipt, err := rig.agent.SteerInputWithReceipt(agent.SteerRequest{Content: "Use the other file."})
		require.NoError(t, err)
		prompts := rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/prompts"))
		require.Len(t, prompts, 1)
		promptID := decodePrompt(t, prompts[0]).PromptID
		assert.Equal(t, agent.NativeInputReceipt{SessionID: rig.sessionID(), InputID: promptID}, receipt)
		steers := rig.fake.requestsTo("POST " + kimiItemPath(rig.sessionID(), "prompts", promptID, ":steer"))
		require.Len(t, steers, 1, "the queued prompt is steered by the id the POST returned")
	})

	t.Run("needs a running turn", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		require.ErrorIs(t, rig.agent.SteerInput(agent.SteerRequest{Content: "Too late."}), agent.ErrNoActiveTurn)
		assert.True(t, rig.agent.SupportsSteering())
	})

	t.Run("delivers a prompt that started a turn of its own", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.startPublicMainTurn(t, "t0", 0, `{"kind":"user"}`)
		// The server ended the turn before the prompt arrived, so the prompt ran.
		receipt, err := rig.agent.SteerInputWithReceipt(agent.SteerRequest{Content: "Also this."})
		require.NoError(t, err)
		requests := rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/prompts"))
		require.Len(t, requests, 1)
		assert.Equal(t, agent.NativeInputReceipt{SessionID: rig.sessionID(), InputID: decodePrompt(t, requests[0]).PromptID}, receipt)
		for _, route := range rig.fake.routes() {
			assert.NotContains(t, route, ":steer")
		}
	})

	t.Run("delivers a prompt the turn end launched", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		startTurn(t, rig)
		rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
			promptID := decodePrompt(t, request).PromptID
			rig.fake.reply("POST "+kimiItemPath(rig.sessionID(), "prompts", promptID, ":steer"), fakeKapReply{Code: kimiCodePromptNotPending})
			return map[string]any{"prompt_id": promptID, "status": kimiPromptQueued}
		}})
		require.NoError(t, rig.agent.SteerInput(agent.SteerRequest{Content: "Also this."}))
		assert.Empty(t, rig.fake.requestsTo("POST "+kimiItemPath(rig.sessionID(), "prompts", decodePrompt(t, rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/prompts"))[0]).PromptID, kimiActionAbort)))
	})

	t.Run("retains the receipt when the native echo arrives before a steer error", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		startTurn(t, rig)
		native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
		rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
			promptID := decodePrompt(t, request).PromptID
			rig.fake.reply("POST "+kimiItemPath(rig.sessionID(), "prompts", promptID, ":steer"), fakeKapReply{
				Code: 50000, Msg: "steer reply failed", BeforeReply: func() {
					native.endPublicTurn(t, kimiMainAgentID, "t0", 0, "completed")
					native.startPublicTurn(t, kimiMainAgentID, "t1", 1)
					native.conversationFrame(t, `{"type":"item.ended","turn_id":"t1","item":`+
						kimiPublicPromptItem(t, promptID, "opener", promptID)+`}`)
				},
			})
			return map[string]any{"prompt_id": promptID, "status": kimiPromptQueued}
		}})
		receipt, err := rig.agent.SteerInputWithReceipt(agent.SteerRequest{Content: "Also this."})
		require.NoError(t, err)
		requests := rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/prompts"))
		require.Len(t, requests, 1)
		promptID := decodePrompt(t, requests[0]).PromptID
		assert.Equal(t, agent.NativeInputReceipt{SessionID: rig.sessionID(), InputID: promptID}, receipt)
		assert.Empty(t, rig.fake.requestsTo("POST "+kimiItemPath(rig.sessionID(), "prompts", promptID, kimiActionAbort)))
	})

	t.Run("withdraws the queued prompt when the steer fails", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		startTurn(t, rig)
		rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
			promptID := decodePrompt(t, request).PromptID
			rig.fake.reply("POST "+kimiItemPath(rig.sessionID(), "prompts", promptID, ":steer"), fakeKapReply{Code: 50000, Msg: "boom"})
			return map[string]any{"prompt_id": promptID, "status": kimiPromptQueued}
		}})
		receipt, err := rig.agent.SteerInputWithReceipt(agent.SteerRequest{Content: "Also this."})
		require.Error(t, err)
		assert.Empty(t, receipt)
		assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain, "the withdrawn prompt reached nobody")
		rig.agent.dispatchMu.Lock()
		assert.Empty(t, rig.agent.promptSubmissions, "a withdrawn prompt has no pending exact reply")
		rig.agent.finishDispatch()
		assert.Len(t, rig.fake.requestsTo("POST "+kimiItemPath(rig.sessionID(), "prompts", decodePrompt(t, rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/prompts"))[0]).PromptID, kimiActionAbort)), 1)
	})

	t.Run("reports an uncertain delivery when the withdrawal fails too", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		startTurn(t, rig)
		rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
			promptID := decodePrompt(t, request).PromptID
			rig.fake.reply("POST "+kimiItemPath(rig.sessionID(), "prompts", promptID, ":steer"), fakeKapReply{Code: 50000, Msg: "boom"})
			rig.fake.reply("POST "+kimiItemPath(rig.sessionID(), "prompts", promptID, kimiActionAbort), fakeKapReply{Code: 50000, Msg: "boom"})
			return map[string]any{"prompt_id": promptID, "status": kimiPromptQueued}
		}})
		require.ErrorIs(t, rig.agent.SteerInput(agent.SteerRequest{Content: "Also this."}), agent.ErrDeliveryUncertain)
	})

	t.Run("refuses a prompt id the server would not issue", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		startTurn(t, rig)
		rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{Data: map[string]any{"prompt_id": "../x", "status": kimiPromptQueued}})
		require.ErrorContains(t, rig.agent.SteerInput(agent.SteerRequest{Content: "Also this."}), "prompt id")
	})

	t.Run("refuses once stopped", func(t *testing.T) {
		t.Parallel()
		a := newOfflineKimiAgent(t, &agenttest.Sink{})
		a.SetStoppedForTest(true)
		require.ErrorContains(t, a.SteerInput(agent.SteerRequest{Content: "Hello."}), "stopped")
	})

	t.Run("reports a prompt the server refuses and steers nothing", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		startTurn(t, rig)
		rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{Code: 40001, Msg: "prompt too long"})
		err := rig.agent.SteerInput(agent.SteerRequest{Content: "Also this."})
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
		err := rig.agent.SteerInput(agent.SteerRequest{Content: "Read this.", Attachments: []*leapmuxv1.Attachment{{Filename: "a.bin", MimeType: "application/octet-stream", Data: []byte{0xff, 0x00}}}})
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

	for _, test := range []struct {
		name, origin string
		steerable    bool
	}{
		{"human prompt", `{"kind":"user"}`, true},
		{"skill command", `{"kind":"skill","name":"review"}`, true},
		{"plugin command", `{"kind":"plugin_command","plugin_id":"checks","command":"review"}`, true},
		{"system prompt", `{"kind":"system"}`, false},
		{"goal continuation", `{"kind":"goal"}`, false},
		{"scheduler", `{"kind":"cron","job_id":"daily","schedule":"0 9 * * *","recurring":true,"coalesced_count":0,"stale":false}`, false},
		{"retry", `{"kind":"retry"}`, false},
		{"hook", `{"kind":"hook","hook_event":"UserPromptSubmit"}`, false},
		{"an opener has not arrived", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			rig.startPublicMainTurn(t, "t0", 0, test.origin)
			rig.fake.setBusy(rig.sessionID(), true)
			assert.Equal(t, agent.TurnState{Active: true, Steerable: test.steerable}, rig.agent.PublishTurnActive())
			var busy *agent.AgentBusyError
			require.ErrorAs(t, rig.agent.SendInput("More.", nil), &busy)
			assert.Equal(t, test.steerable, busy.ActiveTurnSteerable)
			if !test.steerable {
				assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_UNSPECIFIED, rig.sink.TurnKinds()[0])
				require.ErrorAs(t, rig.agent.SteerInput(agent.SteerRequest{Content: "More."}), &busy)
				assert.False(t, busy.ActiveTurnSteerable)
			}
			assert.Empty(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), "/prompts")))
		})
	}

	t.Run("a turn end clears the steerability with the turn", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.startPublicMainTurn(t, "t0", 0, `{"kind":"user"}`)
		native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
		native.endPublicTurn(t, kimiMainAgentID, "t0", 0, "completed")
		rig.startPublicMainTurn(t, "t1", 1, `{"kind":"cron","job_id":"daily","schedule":"0 9 * * *","recurring":true,"coalesced_count":0,"stale":false}`)
		assert.Equal(t, agent.TurnState{Active: true, Steerable: false}, rig.agent.PublishTurnActive())
	})
}

func TestKimiSendRawInput(t *testing.T) {
	t.Parallel()

	t.Run("the raw abort frame interrupts the running turn", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.startPublicMainTurn(t, "t0", 0, `{"kind":"user"}`)
		require.NoError(t, rig.agent.SendRawInput(agent.RawInput{Data: []byte(`{"action":"abort"}`)}))
		assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), kimiActionAbort)), 1)
	})

	t.Run("an interrupt with no turn sends nothing", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		require.NoError(t, rig.agent.Interrupt(agent.StopContext{}))
		assert.Empty(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), kimiActionAbort)))
	})

	t.Run("anything else is a control response", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		err := rig.agent.SendRawInput(agent.RawInput{Data: []byte(`{"type":"control_response","response":{"request_id":"approval_1","response":{"decision":"approved"}}}`)})
		require.ErrorIs(t, err, errKimiControlGone, "no request of that id is pending")
		require.Error(t, rig.agent.SendRawInput(agent.RawInput{Data: []byte(`not json`)}))
	})

	t.Run("refuses once stopped", func(t *testing.T) {
		t.Parallel()
		a := newOfflineKimiAgent(t, &agenttest.Sink{})
		a.SetStoppedForTest(true)
		require.ErrorContains(t, a.SendRawInput(agent.RawInput{Data: []byte(`{"action":"abort"}`)}), "stopped")
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
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
	const calls = 32
	approval := kimiEventFrame(t, "session_1", approvalEvent("approval_1", map[string]any{"kind": "command", "command": "ls"}))
	var wg sync.WaitGroup
	for i := range calls {
		id := fmt.Sprintf("call_%d", i)
		common := fmt.Sprintf(`"type":"tool_call","item_id":%q,"name":"Read","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"path":%q}`, id, id)
		open := kimiConversationTestFrame(t, rig.sessionID(), kimiMainAgentID, `{"type":"item.started","turn_id":"t0","item":{`+common+`,"status":"running","stage":"executing"}}`)
		progress := kimiConversationTestFrame(t, rig.sessionID(), kimiMainAgentID, fmt.Sprintf(`{"type":"item.delta","turn_id":"t0","item_id":%q,"field":"output","delta":"x"}`, id))
		closed := kimiConversationTestFrame(t, rig.sessionID(), kimiMainAgentID, `{"type":"item.ended","turn_id":"t0","item":{`+common+`,"status":"ended","outcome":"completed","output":"ok"}}`)
		wg.Go(func() {
			rig.agent.HandleOutput(open)
			rig.agent.HandleOutput(progress)
			rig.agent.HandleOutput(closed)
			rig.agent.HandleOutput(closed)
			rig.agent.HandleOutput(approval)
		})
	}
	wg.Wait()
	rig.endPublicTurn(t, kimiMainAgentID, "t0", 0, "completed")
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.Equal(t, 1, rig.sink.PublishedControlCount(), "a request delivered concurrently publishes once")
	rows := rig.sink.Messages()
	require.Len(t, rows, 2*calls+1)
	requests := make(map[string]string, calls)
	results := make(map[string]string, calls)
	for _, row := range rows[:len(rows)-1] {
		view, err := decodeKimiConversationRecord(row.Content)
		require.NoError(t, err)
		require.NotNil(t, view.item)
		if view.record.Role == contracts.KimiConversationRecordRoleRequest {
			assert.NotContains(t, requests, view.item.ItemID)
			requests[view.item.ItemID] = row.SpanID
		} else {
			assert.Equal(t, contracts.KimiConversationRecordRoleResult, view.record.Role)
			assert.True(t, row.Closing)
			assert.NotContains(t, results, view.item.ItemID)
			results[view.item.ItemID] = row.SpanID
		}
	}
	require.Len(t, requests, calls)
	assert.Equal(t, requests, results, "each native tool closes its own span exactly once")
	require.True(t, rows[len(rows)-1].TurnEnd)
	count, present := metadataField(t, rows[len(rows)-1], contracts.MessageMetadataFieldToolUses)
	require.True(t, present)
	assert.JSONEq(t, fmt.Sprint(calls), string(count))
}

package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// piRecordedRequest is one JSONL command written to Pi's stdin.
type piRecordedRequest struct {
	ID      string
	Type    string
	Payload map[string]interface{}
}

// piTestRig wires a PiAgent to an in-memory pipe pair and a fake peer that
// echoes responses for the requests it sees. The peer also records every
// request so tests can assert wire format.
type piTestRig struct {
	agent      *PiAgent
	requests   func() []piRecordedRequest
	cleanup    func()
	cancel     context.CancelFunc
	readPipe   *os.File
	writePipe  *os.File
	respondMu  sync.Mutex
	responder  func(req piRecordedRequest) (data json.RawMessage, success bool, errMsg string)
	stdoutPipe *os.File
}

// newPiTestRig sets up a PiAgent suitable for unit tests. The agent's stdin is
// captured by a goroutine that decodes JSONL commands and either lets the
// supplied responder craft a response, or replies with a generic success.
func newPiTestRig(t *testing.T, sink OutputSink) *piTestRig {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	stdinReader, stdinWriter, err := os.Pipe()
	require.NoError(t, err)
	stdoutReader, stdoutWriter, err := os.Pipe()
	require.NoError(t, err)

	a := &PiAgent{
		processBase: processBase{
			agentID:      "test-agent",
			providerName: "pi",
			stdin:        stdinWriter,
			ctx:          ctx,
			cancel:       cancel,
			processDone:  make(chan struct{}),
			stderrDone:   make(chan struct{}),
			apiTimeout:   2 * time.Second,
		},
		sink:        sink,
		provider:    PiDefaultProvider,
		model:       PiDefaultModel,
		sessionFile: "/tmp/pi-test.jsonl",
	}
	close(a.stderrDone)

	rig := &piTestRig{
		agent:      a,
		cancel:     cancel,
		readPipe:   stdinReader,
		writePipe:  stdinWriter,
		stdoutPipe: stdoutWriter,
	}

	var (
		mu       sync.Mutex
		captured []piRecordedRequest
	)

	go func() {
		scanner := bufio.NewScanner(stdinReader)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			var raw map[string]interface{}
			if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
				continue
			}
			id, _ := raw["id"].(string)
			method, _ := raw["type"].(string)
			rec := piRecordedRequest{ID: id, Type: method, Payload: raw}
			mu.Lock()
			captured = append(captured, rec)
			mu.Unlock()

			rig.respondMu.Lock()
			responder := rig.responder
			rig.respondMu.Unlock()

			data := json.RawMessage(`null`)
			success := true
			errMsg := ""
			if responder != nil {
				data, success, errMsg = responder(rec)
			}

			resp := map[string]interface{}{
				"type":    "response",
				"id":      id,
				"command": method,
				"success": success,
			}
			if data != nil {
				resp["data"] = data
			}
			if errMsg != "" {
				resp["error"] = errMsg
			}
			respBytes, _ := json.Marshal(resp)
			respBytes = append(respBytes, '\n')
			if _, err := stdoutWriter.Write(respBytes); err != nil {
				return
			}
		}
	}()

	// Drive the agent's read loop from the fake stdout. We do not call
	// processBase.readOutput here because it ends with cmd.Wait(), which
	// nil-derefs when no exec.Cmd is attached (rig has no real subprocess).
	go func() {
		scanner := bufio.NewScanner(stdoutReader)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := parseLine(append([]byte(nil), scanner.Bytes()...))
			if a.handlePiResponse(line) {
				continue
			}
			a.handleOutput(line)
		}
	}()

	rig.requests = func() []piRecordedRequest {
		mu.Lock()
		defer mu.Unlock()
		out := make([]piRecordedRequest, len(captured))
		copy(out, captured)
		return out
	}

	rig.cleanup = func() {
		cancel()
		_ = stdinReader.Close()
		_ = stdinWriter.Close()
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
	}
	t.Cleanup(rig.cleanup)
	return rig
}

func (r *piTestRig) setResponder(fn func(req piRecordedRequest) (json.RawMessage, bool, string)) {
	r.respondMu.Lock()
	defer r.respondMu.Unlock()
	r.responder = fn
}

func TestPi_SendPiCommand_RoundTripsResponse(t *testing.T) {
	rig := newPiTestRig(t, noopSink{})
	rig.setResponder(func(req piRecordedRequest) (json.RawMessage, bool, string) {
		assert.Equal(t, "ping", req.Type)
		return json.RawMessage(`{"hello":"world"}`), true, ""
	})

	data, err := rig.agent.sendPiCommand("ping", map[string]any{"x": 1}, time.Second)
	require.NoError(t, err)
	assert.JSONEq(t, `{"hello":"world"}`, string(data))

	reqs := rig.requests()
	require.Equal(t, 1, len(reqs))
	assert.Equal(t, "ping", reqs[0].Type)
	assert.NotEmpty(t, reqs[0].ID, "command must mint a non-empty id")
	assert.Equal(t, float64(1), reqs[0].Payload["x"])
}

func TestPi_SendPiCommand_FailureReturnsError(t *testing.T) {
	rig := newPiTestRig(t, noopSink{})
	rig.setResponder(func(req piRecordedRequest) (json.RawMessage, bool, string) {
		return nil, false, "model not found"
	})

	_, err := rig.agent.sendPiCommand("set_model", map[string]any{
		"provider": "openai-codex",
		"modelId":  "ghost",
	}, time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model not found")
}

func TestPi_SendPiCommand_TimeoutReturnsError(t *testing.T) {
	rig := newPiTestRig(t, noopSink{})
	rig.setResponder(func(req piRecordedRequest) (json.RawMessage, bool, string) {
		// Hold the response forever to force a timeout.
		select {}
	})

	_, err := rig.agent.sendPiCommand(PiCommandGetState, nil, 50*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timeout")
}

func TestPi_SendPiCommand_AssignsUniqueStringIDs(t *testing.T) {
	rig := newPiTestRig(t, noopSink{})
	rig.setResponder(func(req piRecordedRequest) (json.RawMessage, bool, string) {
		return json.RawMessage(`{}`), true, ""
	})

	for i := 0; i < 5; i++ {
		_, err := rig.agent.sendPiCommand("noop", nil, time.Second)
		require.NoError(t, err)
	}

	seen := map[string]bool{}
	for _, r := range rig.requests() {
		assert.NotEmpty(t, r.ID, "every command must mint a non-empty id")
		assert.False(t, seen[r.ID], "ids must be unique: %s", r.ID)
		seen[r.ID] = true
	}
}

func TestPi_HandlePiResponse_RoutesByStringID(t *testing.T) {
	a := &PiAgent{
		processBase: processBase{agentID: "test-agent"},
	}
	ch, release := a.register("leapmux-1")
	defer release()

	consumed := a.handlePiResponse(parseLine([]byte(
		`{"type":"response","id":"leapmux-1","command":"prompt","success":true}`,
	)))
	require.True(t, consumed, "response with pending id should be consumed")

	select {
	case raw := <-ch:
		assert.Contains(t, string(raw), `"id":"leapmux-1"`)
	case <-time.After(time.Second):
		t.Fatal("response was not delivered to pending channel")
	}
}

func TestPi_HandlePiResponse_IgnoresUnknownIDs(t *testing.T) {
	a := &PiAgent{processBase: processBase{agentID: "test-agent"}}
	consumed := a.handlePiResponse(parseLine([]byte(
		`{"type":"response","id":"unknown","command":"prompt","success":true}`,
	)))
	assert.False(t, consumed, "unknown ids should not be consumed")
}

func TestPi_HandlePiResponse_IgnoresNonResponseLines(t *testing.T) {
	a := &PiAgent{processBase: processBase{agentID: "test-agent"}}
	consumed := a.handlePiResponse(parseLine([]byte(`{"type":"agent_start"}`)))
	assert.False(t, consumed)
}

func TestPi_SendInput_FreshTurnOmitsSteer(t *testing.T) {
	rig := newPiTestRig(t, noopSink{})
	gotPayload := make(chan map[string]interface{}, 1)
	rig.setResponder(func(req piRecordedRequest) (json.RawMessage, bool, string) {
		if req.Type == "prompt" {
			gotPayload <- req.Payload
		}
		return nil, true, ""
	})

	require.NoError(t, rig.agent.SendInput("hello", nil))

	select {
	case payload := <-gotPayload:
		assert.Equal(t, "hello", payload["message"])
		_, hasSteer := payload["streamingBehavior"]
		assert.False(t, hasSteer, "fresh turn should not set streamingBehavior")
	case <-time.After(2 * time.Second):
		t.Fatal("prompt command never reached the fake peer")
	}
}

func TestPi_SendInput_DuringTurnSetsSteer(t *testing.T) {
	rig := newPiTestRig(t, noopSink{})
	rig.agent.mu.Lock()
	rig.agent.currentTurnActive = true
	rig.agent.mu.Unlock()

	gotPayload := make(chan map[string]interface{}, 1)
	rig.setResponder(func(req piRecordedRequest) (json.RawMessage, bool, string) {
		if req.Type == "prompt" {
			gotPayload <- req.Payload
		}
		return nil, true, ""
	})

	require.NoError(t, rig.agent.SendInput("steer this", nil))

	select {
	case payload := <-gotPayload:
		assert.Equal(t, "steer", payload["streamingBehavior"])
	case <-time.After(2 * time.Second):
		t.Fatal("prompt command never reached the fake peer")
	}
}

func TestPi_SendInput_StoppedAgentReturnsError(t *testing.T) {
	rig := newPiTestRig(t, noopSink{})
	rig.agent.mu.Lock()
	rig.agent.stopped = true
	rig.agent.mu.Unlock()

	err := rig.agent.SendInput("hello", nil)
	require.Error(t, err)
}

func TestPi_SendInput_WithImageAttachment_BuildsImagesArray(t *testing.T) {
	rig := newPiTestRig(t, noopSink{})
	gotPayload := make(chan map[string]interface{}, 1)
	rig.setResponder(func(req piRecordedRequest) (json.RawMessage, bool, string) {
		if req.Type == "prompt" {
			gotPayload <- req.Payload
		}
		return nil, true, ""
	})

	att := &leapmuxv1.Attachment{
		Filename: "shot.png",
		MimeType: "image/png",
		Data:     []byte("\x89PNGfake"),
	}
	require.NoError(t, rig.agent.SendInput("describe", []*leapmuxv1.Attachment{att}))

	select {
	case payload := <-gotPayload:
		images, ok := payload["images"].([]interface{})
		require.True(t, ok, "images must be an array")
		require.Equal(t, 1, len(images))
		image, ok := images[0].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "image", image["type"])
		assert.Equal(t, "image/png", image["mimeType"])
		assert.NotEmpty(t, image["data"])
	case <-time.After(2 * time.Second):
		t.Fatal("prompt command never reached the fake peer")
	}
}

func TestPi_ApplySessionStats_SkipsStaleResponses(t *testing.T) {
	a := &PiAgent{
		processBase:        processBase{agentID: "test-agent"},
		sessionCostUsd:     1.23,
		sessionCostKnown:   true,
		latestContextUsage: map[string]any{"context_tokens": int64(100)},
		usageGeneration:    2,
	}

	applied := a.applyPiSessionStats(piUsageSnapshot{
		TotalCostUsd: 0.42,
		HasTotalCost: true,
		ContextUsage: map[string]any{"context_tokens": int64(50)},
	}, 1)

	assert.False(t, applied)
	a.mu.Lock()
	defer a.mu.Unlock()
	assert.Equal(t, 1.23, a.sessionCostUsd)
	assert.Equal(t, int64(100), a.latestContextUsage["context_tokens"])
}

func TestPi_RefreshSessionStats_BroadcastsCostAndContextUsage(t *testing.T) {
	sink := &testSink{}
	rig := newPiTestRig(t, sink)
	rig.setResponder(func(req piRecordedRequest) (json.RawMessage, bool, string) {
		if req.Type == "get_session_stats" {
			return json.RawMessage(`{
				"sessionFile":"/tmp/pi-test.jsonl",
				"sessionId":"sess-1",
				"tokens":{"input":1000,"output":100,"cacheRead":200,"cacheWrite":50,"total":1350},
				"cost":0.42,
				"contextUsage":{"tokens":60000,"contextWindow":200000,"percent":30}
			}`), true, ""
		}
		return nil, true, ""
	})

	snap, ok := rig.agent.refreshPiSessionStats(time.Second)
	require.True(t, ok)
	assert.True(t, snap.HasTotalCost)
	assert.Equal(t, 0.42, snap.TotalCostUsd)

	reqs := rig.requests()
	require.Equal(t, 1, len(reqs))
	assert.Equal(t, "get_session_stats", reqs[0].Type)

	require.Equal(t, 1, sink.SessionInfoCount())
	info := sink.LastSessionInfo()
	assert.Equal(t, 0.42, info["total_cost_usd"])
	usage, ok := info["context_usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, int64(60000), usage["context_tokens"])
	assert.Equal(t, int64(200000), usage["context_window"])

	rig.agent.mu.Lock()
	defer rig.agent.mu.Unlock()
	assert.True(t, rig.agent.sessionCostKnown)
	assert.Equal(t, 0.42, rig.agent.sessionCostUsd)
}

func TestPi_ApplyStateResponse_PopulatesFields(t *testing.T) {
	a := &PiAgent{processBase: processBase{agentID: "test-agent"}}
	raw := json.RawMessage(`{
		"model": {"id":"gpt-5.5","provider":"openai-codex"},
		"thinkingLevel":"high",
		"sessionId":"sess-1",
		"sessionFile":"/tmp/foo.jsonl"
	}`)
	a.applyStateResponse(raw)

	assert.Equal(t, "gpt-5.5", a.model)
	assert.Equal(t, "openai-codex", a.provider)
	assert.Equal(t, "high", a.thinkingLevel)
	assert.Equal(t, "sess-1", a.sessionID)
	assert.Equal(t, "/tmp/foo.jsonl", a.sessionFile)
}

func TestPi_ApplyAvailableModels_BuildsCatalog(t *testing.T) {
	a := &PiAgent{processBase: processBase{agentID: "test-agent"}}
	raw := json.RawMessage(`{
		"models":[
			{"id":"gpt-5.5","name":"GPT 5.5","provider":"openai-codex","reasoning":true,"contextWindow":272000},
			{"id":"non-thinking","name":"Plain","provider":"openai","reasoning":false,"contextWindow":128000}
		]
	}`)
	a.applyAvailableModels(raw)

	require.Equal(t, 2, len(a.availableModels))
	assert.Equal(t, "gpt-5.5", a.availableModels[0].Id)
	assert.Equal(t, "GPT 5.5", a.availableModels[0].DisplayName)
	assert.Equal(t, int64(272000), a.availableModels[0].ContextWindow)

	// Reasoning=true model should expose Auto + the full thinking ladder.
	got := make([]string, 0, len(a.availableModels[0].SupportedEfforts))
	for _, e := range a.availableModels[0].SupportedEfforts {
		got = append(got, e.Id)
	}
	assert.Contains(t, got, EffortAuto)
	assert.Contains(t, got, PiThinkingHigh)
	assert.Contains(t, got, PiThinkingXHigh)

	// Non-reasoning model collapses to Auto + Off.
	got = got[:0]
	for _, e := range a.availableModels[1].SupportedEfforts {
		got = append(got, e.Id)
	}
	assert.ElementsMatch(t, []string{EffortAuto, PiThinkingOff}, got)
}

func TestPi_CurrentSettings_ReflectsLocalState(t *testing.T) {
	a := &PiAgent{
		processBase:   processBase{agentID: "test-agent"},
		model:         "gpt-5.5",
		thinkingLevel: "medium",
		provider:      "openai-codex",
	}
	settings := a.CurrentSettings()
	assert.Equal(t, "gpt-5.5", settings.Model)
	assert.Equal(t, "medium", settings.Effort)
	assert.Equal(t, "openai-codex", settings.ExtraSettings[PiExtraProvider])
}

func TestPi_AvailableOptionGroups_IsNil(t *testing.T) {
	a := &PiAgent{processBase: processBase{agentID: "test-agent"}}
	assert.Nil(t, a.AvailableOptionGroups())
}

func TestPi_UpdateSettings_AppliesModelAndThinking(t *testing.T) {
	rig := newPiTestRig(t, &testSink{})
	rig.agent.thinkingLevel = "low"
	rig.agent.model = "gpt-5.4"

	rig.setResponder(func(req piRecordedRequest) (json.RawMessage, bool, string) {
		return nil, true, ""
	})

	ok := rig.agent.UpdateSettings(&leapmuxv1.AgentSettings{
		Model:  "gpt-5.5",
		Effort: "high",
	})
	assert.True(t, ok)

	// Wait briefly for the goroutine writes — UpdateSettings is synchronous
	// so the requests must be observable immediately.
	reqs := rig.requests()
	methodSeen := func(name string) bool {
		for _, r := range reqs {
			if r.Type == name {
				return true
			}
		}
		return false
	}
	assert.True(t, methodSeen("set_model"), "set_model should have been sent")
	assert.True(t, methodSeen("set_thinking_level"), "set_thinking_level should have been sent")

	rig.agent.mu.Lock()
	defer rig.agent.mu.Unlock()
	assert.Equal(t, "gpt-5.5", rig.agent.model)
	assert.Equal(t, "high", rig.agent.thinkingLevel)
}

func TestPi_UpdateSettings_EffortAutoRequiresRestart(t *testing.T) {
	a := &PiAgent{
		processBase:   processBase{agentID: "test-agent"},
		thinkingLevel: "medium",
	}
	ok := a.UpdateSettings(&leapmuxv1.AgentSettings{Effort: EffortAuto})
	assert.False(t, ok, "switching to auto should signal a restart")
}

func TestPi_Stop_SendsAbortBeforeClosingStdin(t *testing.T) {
	rig := newPiTestRig(t, &testSink{})

	abortSeen := make(chan struct{}, 1)
	rig.setResponder(func(req piRecordedRequest) (json.RawMessage, bool, string) {
		if req.Type == "abort" {
			select {
			case abortSeen <- struct{}{}:
			default:
			}
		}
		return nil, true, ""
	})

	// Stop only sends abort when a turn is in flight; mark the rig as active
	// so the abort path runs.
	rig.agent.mu.Lock()
	rig.agent.currentTurnActive = true
	rig.agent.mu.Unlock()

	// processBase.Stop blocks until processDone closes — without a real
	// subprocess, simulate the post-stdin-close exit by closing processDone
	// from a helper goroutine. This isolates the "abort RPC was sent on the
	// way out" assertion.
	go func() {
		time.Sleep(20 * time.Millisecond)
		close(rig.agent.processDone)
	}()
	rig.agent.Stop()

	select {
	case <-abortSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not send abort before tearing down stdin")
	}

	rig.agent.mu.Lock()
	stopped := rig.agent.stopped
	rig.agent.mu.Unlock()
	assert.True(t, stopped, "Stop must mark the agent as stopped")
}

func TestPi_Stop_SkipsAbortWhenIdle(t *testing.T) {
	rig := newPiTestRig(t, &testSink{})

	abortSeen := make(chan struct{}, 1)
	rig.setResponder(func(req piRecordedRequest) (json.RawMessage, bool, string) {
		if req.Type == "abort" {
			select {
			case abortSeen <- struct{}{}:
			default:
			}
		}
		return nil, true, ""
	})

	// currentTurnActive defaults to false; Stop should skip abort entirely so
	// idle agents tear down without burning a 1s timeout each.
	go func() {
		time.Sleep(20 * time.Millisecond)
		close(rig.agent.processDone)
	}()
	rig.agent.Stop()

	select {
	case <-abortSeen:
		t.Fatal("Stop sent abort despite no turn being active")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestPi_ClearContext_RoundtripsNewSessionAndGetState(t *testing.T) {
	sink := &testSink{}
	rig := newPiTestRig(t, sink)

	rig.setResponder(func(req piRecordedRequest) (json.RawMessage, bool, string) {
		switch req.Type {
		case "new_session":
			return json.RawMessage(`{"cancelled":false}`), true, ""
		case "get_state":
			return json.RawMessage(`{
				"model":{"id":"gpt-5.5","provider":"openai-codex"},
				"thinkingLevel":"medium",
				"sessionId":"new-sess",
				"sessionFile":"/tmp/pi-new.jsonl"
			}`), true, ""
		}
		return nil, true, ""
	})

	rig.agent.currentTurnActive = true
	rig.agent.sessionCostUsd = 1.23
	rig.agent.sessionCostKnown = true
	rig.agent.latestContextUsage = map[string]any{"input_tokens": int64(100)}
	id, ok := rig.agent.ClearContext()
	assert.True(t, ok, "ClearContext should report success")
	assert.Equal(t, "/tmp/pi-new.jsonl", id, "returns the new sessionFile path")

	// Confirm both RPCs were issued in order.
	reqs := rig.requests()
	require.GreaterOrEqual(t, len(reqs), 2)
	assert.Equal(t, "new_session", reqs[0].Type)
	assert.Equal(t, "get_state", reqs[1].Type)

	// Local state was refreshed and the sink saw the new session id.
	rig.agent.mu.Lock()
	defer rig.agent.mu.Unlock()
	assert.Equal(t, "/tmp/pi-new.jsonl", rig.agent.sessionFile)
	assert.False(t, rig.agent.currentTurnActive, "ClearContext clears the turn flag")
	assert.False(t, rig.agent.sessionCostKnown, "ClearContext resets Pi usage state")
	assert.Equal(t, 0.0, rig.agent.sessionCostUsd)
	assert.Nil(t, rig.agent.latestContextUsage)
	assert.Contains(t, sink.sessionIDs, "/tmp/pi-new.jsonl")
}

func TestPi_UpdateSettings_BroadcastsRefreshedSettings(t *testing.T) {
	sink := &testSink{}
	rig := newPiTestRig(t, sink)
	rig.agent.thinkingLevel = "low"
	rig.agent.model = "gpt-5.4"

	rig.setResponder(func(req piRecordedRequest) (json.RawMessage, bool, string) {
		return nil, true, ""
	})

	ok := rig.agent.UpdateSettings(&leapmuxv1.AgentSettings{
		Model:  "gpt-5.5",
		Effort: "high",
		ExtraSettings: map[string]string{
			PiExtraProvider: "openai-codex",
		},
	})
	require.True(t, ok)

	require.Equal(t, 1, sink.SettingsRefreshCount(), "settings refresh should be broadcast once")
	last := sink.LastSettingsRefresh()
	assert.Equal(t, "gpt-5.5", last.Model)
	assert.Equal(t, "high", last.Effort)
	assert.Equal(t, "openai-codex", last.ExtraSettings[PiExtraProvider])
}

func TestPi_HandlePiResponse_RoutesNumericIDLeftoverFromJSONRPCMix(t *testing.T) {
	// Sanity check that the IDString helper handles numeric ids too — even
	// though Pi mints string ids itself, defensive callers should not break
	// when a server emits a JSON-RPC-shaped numeric id.
	a := &PiAgent{processBase: processBase{agentID: "test-agent"}}
	ch, release := a.register("42")
	defer release()

	consumed := a.handlePiResponse(parseLine([]byte(
		`{"type":"response","id":42,"command":"prompt","success":true}`,
	)))
	assert.True(t, consumed)
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("numeric id response was not delivered")
	}
}

func TestPi_ProviderForModel_LooksUpCatalog(t *testing.T) {
	a := &PiAgent{
		processBase: processBase{agentID: "test-agent"},
		provider:    "openai-codex",
		modelProviders: map[string]string{
			"gpt-5.5":    "openai-codex",
			"claude-3.5": "anthropic",
		},
	}
	assert.Equal(t, "openai-codex", a.providerForModel("gpt-5.5"))
	assert.Equal(t, "anthropic", a.providerForModel("claude-3.5"))
	assert.Equal(t, "openai-codex", a.providerForModel("unknown"), "fallback to current provider")
}

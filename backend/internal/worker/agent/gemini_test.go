package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

type geminiRecordedRequest struct {
	Method string
	Params map[string]interface{}
}

func newGeminiAgentForRPC(t *testing.T) (*GeminiCLIAgent, func() []geminiRecordedRequest) {
	t.Helper()
	return newGeminiAgentForRPCWithResponder(t, func(string) json.RawMessage { return json.RawMessage(`{}`) })
}

func newGeminiAgentForRPCWithResponder(t *testing.T, respond func(method string) json.RawMessage) (*GeminiCLIAgent, func() []geminiRecordedRequest) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	readPipe, writePipe, err := os.Pipe()
	require.NoError(t, err)

	agent := &GeminiCLIAgent{
		acpBase: acpBase{
			jsonrpcBase: jsonrpcBase{processBase: processBase{
				agentID:     "test-agent",
				stdin:       writePipe,
				ctx:         ctx,
				cancel:      cancel,
				processDone: make(chan struct{}),
				stderrDone:  make(chan struct{}),
			}},
			sessionID: "session-1",
		},
	}
	close(agent.stderrDone)

	var (
		mu       sync.Mutex
		requests []geminiRecordedRequest
	)

	go func() {
		scanner := bufio.NewScanner(readPipe)
		for scanner.Scan() {
			var req struct {
				ID     int64                  `json:"id"`
				Method string                 `json:"method"`
				Params map[string]interface{} `json:"params"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
				continue
			}
			mu.Lock()
			requests = append(requests, geminiRecordedRequest{Method: req.Method, Params: req.Params})
			mu.Unlock()
			if ch, ok := agent.pendingReqs.Load(req.ID); ok {
				body := json.RawMessage(`{}`)
				if respond != nil {
					body = respond(req.Method)
				}
				ch.(chan json.RawMessage) <- body
			}
		}
	}()

	t.Cleanup(func() {
		cancel()
		_ = readPipe.Close()
		_ = writePipe.Close()
	})

	return agent, func() []geminiRecordedRequest {
		mu.Lock()
		defer mu.Unlock()
		out := make([]geminiRecordedRequest, len(requests))
		copy(out, requests)
		return out
	}
}

func installFakeGeminiCLI(t *testing.T, scenario string) {
	t.Helper()

	dir := t.TempDir()
	launcher := filepath.Join(dir, "gemini")
	script := fmt.Sprintf("#!/bin/sh\nLEAPMUX_GEMINI_TEST_SCENARIO=%q exec %q -test.run=TestHelperProcessGeminiCLI --\n", scenario, os.Args[0])
	require.NoError(t, os.WriteFile(launcher, []byte(script), 0o755))

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GO_WANT_HELPER_PROCESS_GEMINI", "1")
}

func TestHelperProcessGeminiCLI(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS_GEMINI") != "1" {
		return
	}

	scanner := bufio.NewScanner(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	defer func() { _ = writer.Flush() }()

	scenario := os.Getenv("LEAPMUX_GEMINI_TEST_SCENARIO")

	writeResponse := func(id json.RawMessage, body string, isError bool) {
		field := "result"
		if isError {
			field = "error"
		}
		_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"%s":%s}`+"\n", string(id), field, body)
		_ = writer.Flush()
	}

	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}

		switch req.Method {
		case "initialize":
			writeResponse(req.ID, `{}`, false)
		case "session/new":
			writeResponse(req.ID, `{"sessionId":"session-new","models":{"currentModelId":"gemini-2.5-pro","availableModels":[{"modelId":"auto","name":"Auto","description":"Automatic"},{"modelId":"gemini-2.5-pro","name":"Gemini 2.5 Pro","description":"Detailed"}]},"modes":{"currentModeId":"default","availableModes":[{"id":"default","name":"Default"},{"id":"plan","name":"Plan"}]}}`, false)
		case "session/load":
			if scenario == "load" {
				writeResponse(req.ID, `{"models":{"currentModelId":"gemini-2.5-flash","availableModels":[{"modelId":"auto","name":"Auto"},{"modelId":"gemini-2.5-flash","name":"Gemini 2.5 Flash"}]},"modes":{"currentModeId":"plan","availableModes":[{"id":"default","name":"Default"},{"id":"plan","name":"Plan"}]}}`, false)
			}
		case "session/set_mode", "session/set_model", "session/prompt":
			writeResponse(req.ID, `{}`, false)
		}
	}
	os.Exit(0)
}

func TestBuildGeminiSessionRequest_NewSession(t *testing.T) {
	method, params := buildACPSessionRequest("", "/workspace", acpMethodSessionNew, acpMethodSessionLoad)
	assert.Equal(t, acpMethodSessionNew, method)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(params, &parsed))
	assert.Equal(t, "/workspace", parsed["cwd"])
	assert.NotContains(t, parsed, "sessionId")
}

func TestBuildGeminiSessionRequest_LoadSession(t *testing.T) {
	method, params := buildACPSessionRequest("session-123", "/workspace", acpMethodSessionNew, acpMethodSessionLoad)
	assert.Equal(t, acpMethodSessionLoad, method)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(params, &parsed))
	assert.Equal(t, "/workspace", parsed["cwd"])
	assert.Equal(t, "session-123", parsed["sessionId"])
}

func TestStartGeminiCLI_NewSessionHandshake(t *testing.T) {
	installFakeGeminiCLI(t, "new")

	provider, err := StartGeminiCLI(context.Background(), Options{
		AgentID:       "gemini-new",
		WorkingDir:    t.TempDir(),
		Shell:         "/bin/sh",
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*GeminiCLIAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	assert.Equal(t, "session-new", agent.sessionID)
	assert.Equal(t, "gemini-2.5-pro", agent.model)
	require.Len(t, agent.AvailableModels(), 2)
	assert.Equal(t, "auto", agent.AvailableModels()[0].GetId())
	require.Len(t, agent.AvailableOptionGroups(), 1)
	assert.Equal(t, "permissionMode", agent.AvailableOptionGroups()[0].GetKey())
	assert.Equal(t, "default", agent.permissionMode)
}

func TestStartGeminiCLI_LoadSessionUsesResumeID(t *testing.T) {
	installFakeGeminiCLI(t, "load")

	provider, err := StartGeminiCLI(context.Background(), Options{
		AgentID:         "gemini-load",
		WorkingDir:      t.TempDir(),
		ResumeSessionID: "session-resume",
		Shell:           "/bin/sh",
		LoginShell:      false,
		AgentProvider:   leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*GeminiCLIAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	assert.Equal(t, "session-resume", agent.sessionID)
	assert.Equal(t, "gemini-2.5-flash", agent.model)
	assert.Equal(t, "plan", agent.permissionMode)
}

func TestGeminiUpdateSettingsSendsLiveACPRequests(t *testing.T) {
	agent, requests := newGeminiAgentForRPC(t)
	agent.availableModes = []*leapmuxv1.AvailableOption{
		{Id: GeminiCLIModeDefault, Name: "Default", IsDefault: true},
		{Id: GeminiCLIModePlan, Name: "Plan"},
	}

	updated := agent.UpdateSettings(&leapmuxv1.AgentSettings{
		Model:          "gemini-2.5-flash",
		PermissionMode: GeminiCLIModePlan,
	})
	require.True(t, updated)
	assert.Equal(t, "gemini-2.5-flash", agent.model)
	assert.Equal(t, GeminiCLIModePlan, agent.permissionMode)

	recorded := requests()
	require.Len(t, recorded, 2)
	assert.Equal(t, "session/set_model", recorded[0].Method)
	assert.Equal(t, "gemini-2.5-flash", recorded[0].Params["modelId"])
	assert.Equal(t, "session/set_mode", recorded[1].Method)
	assert.Equal(t, GeminiCLIModePlan, recorded[1].Params["modeId"])
}

func TestGeminiCancelSessionSendsACPMethod(t *testing.T) {
	agent, requests := newGeminiAgentForRPC(t)

	err := agent.cancelSession()
	require.NoError(t, err)

	require.Eventually(t, func() bool { return len(requests()) == 1 }, time.Second, 10*time.Millisecond)
	assert.Equal(t, "session/cancel", requests()[0].Method)
}

func TestGeminiAvailableOptionGroupsFallsBack(t *testing.T) {
	agent := &GeminiCLIAgent{}
	groups := agent.AvailableOptionGroups()
	require.Len(t, groups, 1)
	assert.Equal(t, "permissionMode", groups[0].GetKey())
	require.Len(t, groups[0].GetOptions(), 4)
	assert.Equal(t, GeminiCLIModeDefault, groups[0].GetOptions()[0].GetId())
	assert.True(t, groups[0].GetOptions()[0].GetIsDefault())
}

// newGeminiAgentWithGate creates a Gemini agent where each prompt response is
// held until the returned gate channel is sent to. This allows tests to
// observe queueing behavior by controlling when turns complete.
func newGeminiAgentWithGate(t *testing.T) (*GeminiCLIAgent, func() []geminiRecordedRequest, chan struct{}) {
	t.Helper()

	gate := make(chan struct{}, 8)
	agent, requests := newGeminiAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionPrompt {
			<-gate // Block until test releases the gate.
		}
		return json.RawMessage(`{}`)
	})
	agent.sink = &testSink{}
	agent.promptFunc = agent.doSendPrompt
	return agent, requests, gate
}

func TestGeminiSendInputQueuesWhenPromptActive(t *testing.T) {
	agent, requests, gate := newGeminiAgentWithGate(t)

	// First message — should start a prompt immediately.
	require.NoError(t, agent.SendInput("first", nil))

	// Give the goroutine time to send the request.
	require.Eventually(t, func() bool { return len(requests()) >= 1 }, time.Second, 10*time.Millisecond)

	// Second and third messages — should be queued, not sent.
	require.NoError(t, agent.SendInput("second", nil))
	require.NoError(t, agent.SendInput("third", nil))

	// Only one prompt request should have been sent so far.
	assert.Len(t, requests(), 1)
	assert.Equal(t, acpMethodSessionPrompt, requests()[0].Method)

	// Release the first prompt.
	gate <- struct{}{}

	// The queued messages should be coalesced and sent as a single prompt.
	require.Eventually(t, func() bool { return len(requests()) >= 2 }, time.Second, 10*time.Millisecond)

	// Release the coalesced prompt.
	gate <- struct{}{}

	// Wait for the agent to finish processing.
	require.Eventually(t, func() bool {
		agent.mu.Lock()
		defer agent.mu.Unlock()
		return !agent.promptActive
	}, time.Second, 10*time.Millisecond)

	recorded := requests()
	require.Len(t, recorded, 2)

	// The second request should contain the coalesced content.
	prompt, ok := recorded[1].Params["prompt"].([]interface{})
	require.True(t, ok, "prompt should be an array")
	require.Len(t, prompt, 1)
	block := prompt[0].(map[string]interface{})
	assert.Equal(t, "second\n\nthird", block["text"])
}

func TestGeminiSendInputNoQueueWhenIdle(t *testing.T) {
	agent, requests, gate := newGeminiAgentWithGate(t)

	// Send a message when no prompt is active.
	require.NoError(t, agent.SendInput("hello", nil))
	gate <- struct{}{} // Release immediately.

	require.Eventually(t, func() bool {
		agent.mu.Lock()
		defer agent.mu.Unlock()
		return !agent.promptActive
	}, time.Second, 10*time.Millisecond)

	// Only one prompt request.
	assert.Len(t, requests(), 1)
}

func TestGeminiCancelDrainsQueueAfterTurnEnds(t *testing.T) {
	agent, requests, gate := newGeminiAgentWithGate(t)

	// Start a prompt.
	require.NoError(t, agent.SendInput("first", nil))
	require.Eventually(t, func() bool { return len(requests()) >= 1 }, time.Second, 10*time.Millisecond)

	// Queue a second message, then cancel.
	require.NoError(t, agent.SendInput("second", nil))
	require.NoError(t, agent.cancelSession())

	// Release the first prompt — the queued message should drain naturally.
	gate <- struct{}{}

	// The queued "second" message should be sent as a follow-up prompt.
	require.Eventually(t, func() bool { return len(requests()) >= 3 }, time.Second, 10*time.Millisecond)

	// Release the follow-up prompt.
	gate <- struct{}{}

	require.Eventually(t, func() bool {
		agent.mu.Lock()
		defer agent.mu.Unlock()
		return !agent.promptActive
	}, time.Second, 10*time.Millisecond)

	recorded := requests()
	assert.Equal(t, acpMethodSessionPrompt, recorded[0].Method) // first prompt
	assert.Equal(t, acpMethodSessionCancel, recorded[1].Method) // cancel notification
	assert.Equal(t, acpMethodSessionPrompt, recorded[2].Method) // queued "second"
}

func TestGeminiStopClearsQueue(t *testing.T) {
	agent, requests, _ := newGeminiAgentWithGate(t)

	// Start a prompt (will block on the gate).
	require.NoError(t, agent.SendInput("first", nil))
	require.Eventually(t, func() bool { return len(requests()) >= 1 }, time.Second, 10*time.Millisecond)

	// Queue a second message.
	require.NoError(t, agent.SendInput("second", nil))

	// Close processDone so processBase.Stop() doesn't block forever
	// (no real subprocess in unit tests).
	close(agent.processDone)
	agent.Stop()

	agent.mu.Lock()
	assert.False(t, agent.promptActive)
	assert.Nil(t, agent.pendingMessages)
	agent.mu.Unlock()
}

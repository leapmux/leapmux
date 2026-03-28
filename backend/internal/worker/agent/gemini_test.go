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
		processBase: processBase{
			agentID:     "test-agent",
			stdin:       writePipe,
			ctx:         ctx,
			cancel:      cancel,
			processDone: make(chan struct{}),
			stderrDone:  make(chan struct{}),
		},
		sessionID: "session-1",
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
	defer writer.Flush()

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
		case "newSession":
			if scenario == "legacy" {
				writeResponse(req.ID, `{"code":-32601,"message":"\"Method not found\": newSession"}`, true)
				continue
			}
			writeResponse(req.ID, `{"sessionId":"session-new","models":{"currentModelId":"gemini-2.5-pro","availableModels":[{"modelId":"auto","name":"Auto","description":"Automatic"},{"modelId":"gemini-2.5-pro","name":"Gemini 2.5 Pro","description":"Detailed"}]},"modes":{"currentModeId":"default","availableModes":[{"id":"default","name":"Default"},{"id":"plan","name":"Plan"}]}}`, false)
		case "loadSession":
			if scenario == "load" {
				writeResponse(req.ID, `{"models":{"currentModelId":"gemini-2.5-flash","availableModels":[{"modelId":"auto","name":"Auto"},{"modelId":"gemini-2.5-flash","name":"Gemini 2.5 Flash"}]},"modes":{"currentModeId":"plan","availableModes":[{"id":"default","name":"Default"},{"id":"plan","name":"Plan"}]}}`, false)
			}
		case "session/new":
			if scenario == "legacy" {
				writeResponse(req.ID, `{"sessionId":"session-legacy","models":{"currentModelId":"auto","availableModels":[{"modelId":"auto","name":"Auto"}]},"modes":{"currentModeId":"default","availableModes":[{"id":"default","name":"Default"},{"id":"plan","name":"Plan"}]}}`, false)
			}
		case "setSessionMode", "unstable_setSessionModel", "prompt":
			if scenario == "legacy" {
				writeResponse(req.ID, fmt.Sprintf(`{"code":-32601,"message":"\"Method not found\": %s"}`, req.Method), true)
				continue
			}
			writeResponse(req.ID, `{}`, false)
		case "session/set_mode", "session/set_model", "session/prompt":
			writeResponse(req.ID, `{}`, false)
		}
	}
	os.Exit(0)
}

func TestBuildGeminiSessionRequest_NewSession(t *testing.T) {
	method, params := buildGeminiSessionRequest("", "/workspace")
	assert.Equal(t, "newSession", method)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(params, &parsed))
	assert.Equal(t, "/workspace", parsed["cwd"])
	assert.NotContains(t, parsed, "sessionId")
}

func TestBuildGeminiSessionRequest_LoadSession(t *testing.T) {
	method, params := buildGeminiSessionRequest("session-123", "/workspace")
	assert.Equal(t, "loadSession", method)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(params, &parsed))
	assert.Equal(t, "/workspace", parsed["cwd"])
	assert.Equal(t, "session-123", parsed["sessionId"])
}

func TestStartGeminiCLI_NewSessionHandshake(t *testing.T) {
	installFakeGeminiCLI(t, "new")

	provider, err := StartGeminiCLI(context.Background(), Options{
		AgentID:     "gemini-new",
		WorkingDir:  t.TempDir(),
		Shell:       "/bin/sh",
		LoginShell:  false,
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

func TestStartGeminiCLI_FallsBackToLegacySessionMethods(t *testing.T) {
	installFakeGeminiCLI(t, "legacy")

	provider, err := StartGeminiCLI(context.Background(), Options{
		AgentID:       "gemini-legacy",
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

	assert.Equal(t, "session-legacy", agent.sessionID)
	assert.True(t, agent.useLegacyMethods)
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
	assert.Equal(t, "unstable_setSessionModel", recorded[0].Method)
	assert.Equal(t, "gemini-2.5-flash", recorded[0].Params["modelId"])
	assert.Equal(t, "setSessionMode", recorded[1].Method)
	assert.Equal(t, GeminiCLIModePlan, recorded[1].Params["modeId"])
}

func TestGeminiUpdateSettingsFallsBackToLegacyMethods(t *testing.T) {
	agent, requests := newGeminiAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		switch method {
		case "unstable_setSessionModel", "setSessionMode":
			return json.RawMessage(`{"code":-32601,"message":"method not found"}`)
		default:
			return json.RawMessage(`{}`)
		}
	})
	agent.availableModes = []*leapmuxv1.AvailableOption{
		{Id: GeminiCLIModeDefault, Name: "Default", IsDefault: true},
		{Id: GeminiCLIModePlan, Name: "Plan"},
	}

	updated := agent.UpdateSettings(&leapmuxv1.AgentSettings{
		Model:          "gemini-2.5-flash",
		PermissionMode: GeminiCLIModePlan,
	})
	require.True(t, updated)
	assert.True(t, agent.useLegacyMethods)
	require.Eventually(t, func() bool { return len(requests()) == 3 }, time.Second, 10*time.Millisecond)
	recorded := requests()
	require.Len(t, recorded, 3)
	assert.Equal(t, "unstable_setSessionModel", recorded[0].Method)
	assert.Equal(t, "session/set_model", recorded[1].Method)
	assert.Equal(t, "session/set_mode", recorded[2].Method)
}

func TestGeminiSendRawInputInterruptUsesLegacyCancelWhenNeeded(t *testing.T) {
	agent, requests := newGeminiAgentForRPC(t)
	agent.useLegacyMethods = true

	err := agent.SendRawInput([]byte(`{"jsonrpc":"2.0","method":"cancel","params":{"sessionId":"session-1"}}`))
	require.NoError(t, err)

	require.Eventually(t, func() bool { return len(requests()) == 1 }, time.Second, 10*time.Millisecond)
	recorded := requests()
	require.Len(t, recorded, 1)
	assert.Equal(t, "session/cancel", recorded[0].Method)
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

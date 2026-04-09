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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

type cursorRecordedRequest struct {
	Method string
	Params map[string]interface{}
}

func newCursorAgentForRPC(t *testing.T) (*CursorCLIAgent, func() []cursorRecordedRequest) {
	t.Helper()
	return newCursorAgentForRPCWithResponder(t, func(string) json.RawMessage { return json.RawMessage(`{}`) })
}

func newCursorAgentForRPCWithResponder(t *testing.T, respond func(method string) json.RawMessage) (*CursorCLIAgent, func() []cursorRecordedRequest) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	readPipe, writePipe, err := os.Pipe()
	require.NoError(t, err)

	agent := &CursorCLIAgent{
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
		requests []cursorRecordedRequest
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
			requests = append(requests, cursorRecordedRequest{Method: req.Method, Params: req.Params})
			mu.Unlock()
			if req.ID != 0 {
				if ch, ok := agent.pendingReqs.Load(req.ID); ok {
					body := json.RawMessage(`{}`)
					if respond != nil {
						body = respond(req.Method)
					}
					ch.(chan json.RawMessage) <- body
				}
			}
		}
	}()

	t.Cleanup(func() {
		cancel()
		_ = readPipe.Close()
		_ = writePipe.Close()
	})

	return agent, func() []cursorRecordedRequest {
		mu.Lock()
		defer mu.Unlock()
		out := make([]cursorRecordedRequest, len(requests))
		copy(out, requests)
		return out
	}
}

func installFakeCursorCLI(t *testing.T, scenario string) {
	t.Helper()

	dir := t.TempDir()
	launcher := filepath.Join(dir, "cursor-agent")
	script := fmt.Sprintf("#!/bin/sh\nLEAPMUX_CURSOR_TEST_SCENARIO=%q exec %q -test.run=TestHelperProcessCursorCLI -- \"$@\"\n", scenario, os.Args[0])
	require.NoError(t, os.WriteFile(launcher, []byte(script), 0o755))

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GO_WANT_HELPER_PROCESS_CURSOR", "1")
}

func TestHelperProcessCursorCLI(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS_CURSOR") != "1" {
		return
	}

	scanner := bufio.NewScanner(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	defer func() { _ = writer.Flush() }()

	scenario := os.Getenv("LEAPMUX_CURSOR_TEST_SCENARIO")

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
		case acpMethodInitialize:
			writeResponse(req.ID, `{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}`, false)
		case acpMethodSessionNew:
			writeResponse(req.ID, `{"sessionId":"cursor-new","models":{"currentModelId":"default[]","availableModels":[{"modelId":"default[]","name":"Auto"},{"modelId":"gpt-5.4[reasoning=medium]","name":"GPT-5.4"}]},"modes":{"currentModeId":"agent","availableModes":[{"id":"agent","name":"Agent"},{"id":"plan","name":"Plan"},{"id":"ask","name":"Ask"}]},"configOptions":[{"id":"mode","currentValue":"agent","options":[{"value":"agent","name":"Agent"},{"value":"plan","name":"Plan"},{"value":"ask","name":"Ask"}]},{"id":"model","currentValue":"default[]","options":[{"value":"default[]","name":"Auto"},{"value":"gpt-5.4[reasoning=medium]","name":"GPT-5.4"}]}]}`, false)
		case acpMethodSessionLoad:
			if scenario == "load" {
				writeResponse(req.ID, `{"models":{"currentModelId":"gpt-5.4[reasoning=medium]","availableModels":[{"modelId":"default[]","name":"Auto"},{"modelId":"gpt-5.4[reasoning=medium]","name":"GPT-5.4"}]},"modes":{"currentModeId":"plan","availableModes":[{"id":"agent","name":"Agent"},{"id":"plan","name":"Plan"},{"id":"ask","name":"Ask"}]}}`, false)
			}
		case acpMethodSessionSetModel, acpMethodSessionSetMode, acpMethodSessionPrompt:
			writeResponse(req.ID, `{}`, false)
		}
	}
	os.Exit(0)
}

func TestStartCursorCLI_NewSessionHandshake(t *testing.T) {
	installFakeCursorCLI(t, "new")

	provider, err := StartCursorCLI(context.Background(), Options{
		AgentID:       "cursor-new",
		WorkingDir:    t.TempDir(),
		Shell:         "/bin/sh",
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*CursorCLIAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	assert.Equal(t, "cursor-new", agent.sessionID)
	assert.Equal(t, "auto", agent.model)
	assert.Equal(t, CursorCLIModeAgent, agent.permissionMode)
	require.Len(t, agent.AvailableModels(), 2)
	assert.Equal(t, "auto", agent.AvailableModels()[0].GetId())
	require.Len(t, agent.AvailableOptionGroups(), 1)
	assert.Equal(t, "permissionMode", agent.AvailableOptionGroups()[0].GetKey())
}

func TestStartCursorCLI_LoadSessionUsesResumeID(t *testing.T) {
	installFakeCursorCLI(t, "load")

	provider, err := StartCursorCLI(context.Background(), Options{
		AgentID:         "cursor-load",
		WorkingDir:      t.TempDir(),
		ResumeSessionID: "cursor-resume",
		Shell:           "/bin/sh",
		LoginShell:      false,
		AgentProvider:   leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*CursorCLIAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	assert.Equal(t, "cursor-resume", agent.sessionID)
	assert.Equal(t, "gpt-5.4[reasoning=medium]", agent.model)
	assert.Equal(t, CursorCLIModePlan, agent.permissionMode)
}

func TestCursorUpdateSettingsSendsLiveACPRequests(t *testing.T) {
	agent, requests := newCursorAgentForRPC(t)
	agent.availableModes = []*leapmuxv1.AvailableOption{
		{Id: CursorCLIModeAgent, Name: "Agent", IsDefault: true},
		{Id: CursorCLIModePlan, Name: "Plan"},
	}

	updated := agent.UpdateSettings(&leapmuxv1.AgentSettings{
		Model:          "auto",
		PermissionMode: CursorCLIModePlan,
	})
	require.True(t, updated)
	assert.Equal(t, "auto", agent.model)
	assert.Equal(t, CursorCLIModePlan, agent.permissionMode)

	recorded := requests()
	require.Len(t, recorded, 2)
	assert.Equal(t, acpMethodSessionSetModel, recorded[0].Method)
	assert.Equal(t, cursorCLIModelAutoWire, recorded[0].Params["modelId"])
	assert.Equal(t, acpMethodSessionSetMode, recorded[1].Method)
	assert.Equal(t, CursorCLIModePlan, recorded[1].Params["modeId"])
}

func TestCursorClearContextReappliesModelAndMode(t *testing.T) {
	agent, requests := newCursorAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			return json.RawMessage(`{"sessionId":"session-2"}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "auto"
	agent.permissionMode = CursorCLIModePlan
	agent.availableModes = []*leapmuxv1.AvailableOption{
		{Id: CursorCLIModeAgent, Name: "Agent", IsDefault: true},
		{Id: CursorCLIModePlan, Name: "Plan"},
	}
	agent.sink = &testSink{}
	agent.reapplySettings = agent.reapplyModelAndMode

	sessionID, ok := agent.ClearContext()
	require.True(t, ok)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "session-2", agent.sessionID)

	// Verify model is preserved with wire format conversion.
	assert.Equal(t, "auto", agent.model)

	recorded := requests()
	require.Len(t, recorded, 3)
	assert.Equal(t, acpMethodSessionNew, recorded[0].Method)
	assert.Equal(t, acpMethodSessionSetModel, recorded[1].Method)
	assert.Equal(t, cursorCLIModelAutoWire, recorded[1].Params["modelId"])
	assert.Equal(t, acpMethodSessionSetMode, recorded[2].Method)
	assert.Equal(t, CursorCLIModePlan, recorded[2].Params["modeId"])
}

func TestBuildCursorCLIModelsNormalizesAuto(t *testing.T) {
	models := []acpModelInfo{
		{ModelID: cursorCLIModelAutoWire, Name: "Auto"},
		{ModelID: "gpt-5.4[reasoning=medium]", Name: "GPT-5.4"},
	}
	result := buildACPModels(models, cursorCLIModelAutoWire, normalizeCursorModelID)
	require.Len(t, result, 2)
	assert.Equal(t, "auto", result[0].Id)
	assert.True(t, result[0].IsDefault)
}

func TestCursorDefaultModelIsAuto(t *testing.T) {
	assert.Equal(t, "auto", DefaultModel(leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR))
}

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

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

type copilotRecordedRequest struct {
	Method string
	Params map[string]interface{}
}

func newCopilotAgentForRPC(t *testing.T) (*CopilotCLIAgent, func() []copilotRecordedRequest) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	readPipe, writePipe, err := os.Pipe()
	require.NoError(t, err)

	agent := &CopilotCLIAgent{
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
		requests []copilotRecordedRequest
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
			requests = append(requests, copilotRecordedRequest{Method: req.Method, Params: req.Params})
			mu.Unlock()
			if req.ID != 0 {
				if ch, ok := agent.pendingReqs.Load(req.ID); ok {
					ch.(chan json.RawMessage) <- json.RawMessage(`{}`)
				}
			}
		}
	}()

	t.Cleanup(func() {
		cancel()
		_ = readPipe.Close()
		_ = writePipe.Close()
	})

	return agent, func() []copilotRecordedRequest {
		mu.Lock()
		defer mu.Unlock()
		out := make([]copilotRecordedRequest, len(requests))
		copy(out, requests)
		return out
	}
}

func installFakeCopilotCLI(t *testing.T, scenario string) {
	t.Helper()

	dir := t.TempDir()
	launcher := filepath.Join(dir, "copilot")
	script := fmt.Sprintf("#!/bin/sh\nLEAPMUX_COPILOT_TEST_SCENARIO=%q exec %q -test.run=TestHelperProcessCopilotCLI --\n", scenario, os.Args[0])
	require.NoError(t, os.WriteFile(launcher, []byte(script), 0o755))

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GO_WANT_HELPER_PROCESS_COPILOT", "1")
}

func TestHelperProcessCopilotCLI(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS_COPILOT") != "1" {
		return
	}

	scanner := bufio.NewScanner(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	defer func() { _ = writer.Flush() }()

	scenario := os.Getenv("LEAPMUX_COPILOT_TEST_SCENARIO")

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
			writeResponse(req.ID, `{"sessionId":"copilot-new","models":{"currentModelId":"gpt-5.4","availableModels":[{"modelId":"gpt-5.4","name":"GPT-5.4","description":"Full"},{"modelId":"gpt-5.4-mini","name":"GPT-5.4 mini","description":"Mini"}]},"modes":{"currentModeId":"https://agentclientprotocol.com/protocol/session-modes#agent","availableModes":[{"id":"https://agentclientprotocol.com/protocol/session-modes#agent","name":"Agent"},{"id":"https://agentclientprotocol.com/protocol/session-modes#plan","name":"Plan"}]},"configOptions":[{"id":"mode","currentValue":"https://agentclientprotocol.com/protocol/session-modes#agent","options":[{"value":"https://agentclientprotocol.com/protocol/session-modes#agent","name":"Agent"},{"value":"https://agentclientprotocol.com/protocol/session-modes#plan","name":"Plan"}]},{"id":"model","currentValue":"gpt-5.4","options":[{"value":"gpt-5.4","name":"GPT-5.4"},{"value":"gpt-5.4-mini","name":"GPT-5.4 mini"}]}]}`, false)
		case acpMethodSessionLoad:
			if scenario == "load" {
				writeResponse(req.ID, `{"models":{"currentModelId":"gpt-5.4-mini","availableModels":[{"modelId":"gpt-5.4-mini","name":"GPT-5.4 mini"}]},"modes":{"currentModeId":"https://agentclientprotocol.com/protocol/session-modes#plan","availableModes":[{"id":"https://agentclientprotocol.com/protocol/session-modes#agent","name":"Agent"},{"id":"https://agentclientprotocol.com/protocol/session-modes#plan","name":"Plan"}]}}`, false)
			}
		case acpMethodSessionSetModel, acpMethodSessionSetMode, acpMethodSessionPrompt:
			writeResponse(req.ID, `{}`, false)
		}
	}
	os.Exit(0)
}

func TestBuildCopilotSessionRequest_NewSession(t *testing.T) {
	method, params := buildACPSessionRequest("", "/workspace", acpMethodSessionNew, acpMethodSessionLoad)
	assert.Equal(t, acpMethodSessionNew, method)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(params, &parsed))
	assert.Equal(t, "/workspace", parsed["cwd"])
	assert.NotContains(t, parsed, "sessionId")
}

func TestBuildCopilotSessionRequest_LoadSession(t *testing.T) {
	method, params := buildACPSessionRequest("session-123", "/workspace", acpMethodSessionNew, acpMethodSessionLoad)
	assert.Equal(t, acpMethodSessionLoad, method)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(params, &parsed))
	assert.Equal(t, "/workspace", parsed["cwd"])
	assert.Equal(t, "session-123", parsed["sessionId"])
}

func TestStartCopilotCLI_NewSessionHandshake(t *testing.T) {
	installFakeCopilotCLI(t, "new")

	provider, err := StartCopilotCLI(context.Background(), Options{
		AgentID:       "copilot-new",
		WorkingDir:    t.TempDir(),
		Shell:         "/bin/sh",
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*CopilotCLIAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	assert.Equal(t, "copilot-new", agent.sessionID)
	assert.Equal(t, "gpt-5.4", agent.model)
	assert.Equal(t, CopilotCLIModeAgent, agent.permissionMode)
	require.Len(t, agent.AvailableModels(), 2)
	assert.Equal(t, "gpt-5.4", agent.AvailableModels()[0].GetId())
	require.Len(t, agent.AvailableOptionGroups(), 1)
	assert.Equal(t, "permissionMode", agent.AvailableOptionGroups()[0].GetKey())
}

func TestStartCopilotCLI_LoadSessionUsesResumeID(t *testing.T) {
	installFakeCopilotCLI(t, "load")

	provider, err := StartCopilotCLI(context.Background(), Options{
		AgentID:         "copilot-load",
		WorkingDir:      t.TempDir(),
		ResumeSessionID: "copilot-resume",
		Shell:           "/bin/sh",
		LoginShell:      false,
		AgentProvider:   leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*CopilotCLIAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	assert.Equal(t, "copilot-resume", agent.sessionID)
	assert.Equal(t, "gpt-5.4-mini", agent.model)
	assert.Equal(t, CopilotCLIModePlan, agent.permissionMode)
}

func TestCopilotUpdateSettingsSendsLiveACPRequests(t *testing.T) {
	agent, requests := newCopilotAgentForRPC(t)
	agent.availableModes = []*leapmuxv1.AvailableOption{
		{Id: CopilotCLIModeAgent, Name: "Agent", IsDefault: true},
		{Id: CopilotCLIModePlan, Name: "Plan"},
	}

	updated := agent.UpdateSettings(&leapmuxv1.AgentSettings{
		Model:          "gpt-5.4-mini",
		PermissionMode: CopilotCLIModePlan,
	})
	require.True(t, updated)
	assert.Equal(t, "gpt-5.4-mini", agent.model)
	assert.Equal(t, CopilotCLIModePlan, agent.permissionMode)

	recorded := requests()
	require.Len(t, recorded, 2)
	assert.Equal(t, acpMethodSessionSetModel, recorded[0].Method)
	assert.Equal(t, "gpt-5.4-mini", recorded[0].Params["modelId"])
	assert.Equal(t, acpMethodSessionSetMode, recorded[1].Method)
	assert.Equal(t, CopilotCLIModePlan, recorded[1].Params["modeId"])
}

func TestCopilotCancelSessionSendsACPMethod(t *testing.T) {
	agent, requests := newCopilotAgentForRPC(t)

	require.NoError(t, agent.cancelSession())
	testutil.AssertEventually(t, func() bool {
		recorded := requests()
		return len(recorded) == 1 && recorded[0].Method == acpMethodSessionCancel
	}, "expected session/cancel notification to be recorded")
}

func TestCopilotAvailableOptionGroupsFallsBack(t *testing.T) {
	agent := &CopilotCLIAgent{}

	groups := agent.AvailableOptionGroups()
	require.Len(t, groups, 1)
	assert.Equal(t, "permissionMode", groups[0].GetKey())
	assert.Equal(t, CopilotCLIModeAgent, groups[0].GetOptions()[0].GetId())
}

func TestDefaultModel_CopilotUsesEnvOverride(t *testing.T) {
	t.Setenv("LEAPMUX_COPILOT_DEFAULT_MODEL", "gpt-5.4-mini")
	assert.Equal(t, "gpt-5.4-mini", DefaultModel(leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT))
}

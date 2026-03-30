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

type gooseRecordedRequest struct {
	Method string
	Params map[string]interface{}
}

func newGooseAgentForRPC(t *testing.T) (*GooseCLIAgent, func() []gooseRecordedRequest) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	readPipe, writePipe, err := os.Pipe()
	require.NoError(t, err)

	agent := &GooseCLIAgent{
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
		requests []gooseRecordedRequest
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
			requests = append(requests, gooseRecordedRequest{Method: req.Method, Params: req.Params})
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

	return agent, func() []gooseRecordedRequest {
		mu.Lock()
		defer mu.Unlock()
		out := make([]gooseRecordedRequest, len(requests))
		copy(out, requests)
		return out
	}
}

func installFakeGooseCLI(t *testing.T, scenario string) {
	t.Helper()

	dir := t.TempDir()
	launcher := filepath.Join(dir, "goose")
	script := fmt.Sprintf("#!/bin/sh\nLEAPMUX_GOOSE_TEST_SCENARIO=%q exec %q -test.run=TestHelperProcessGooseCLI --\n", scenario, os.Args[0])
	require.NoError(t, os.WriteFile(launcher, []byte(script), 0o755))

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GO_WANT_HELPER_PROCESS_GOOSE", "1")
}

func TestHelperProcessGooseCLI(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS_GOOSE") != "1" {
		return
	}

	scanner := bufio.NewScanner(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	defer func() { _ = writer.Flush() }()

	scenario := os.Getenv("LEAPMUX_GOOSE_TEST_SCENARIO")

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
			writeResponse(req.ID, `{"sessionId":"goose-new","models":{"currentModelId":"default-model","availableModels":[{"modelId":"default-model","name":"Default Model","description":"Default"},{"modelId":"fast-model","name":"Fast Model","description":"Fast"}]},"modes":{"currentModeId":"auto","availableModes":[{"id":"auto","name":"Auto"},{"id":"approve","name":"Approve"},{"id":"smart_approve","name":"Smart Approve"},{"id":"chat","name":"Chat"}]},"configOptions":[{"id":"mode","currentValue":"auto","options":[{"value":"auto","name":"Auto"},{"value":"approve","name":"Approve"},{"value":"smart_approve","name":"Smart Approve"},{"value":"chat","name":"Chat"}]},{"id":"model","currentValue":"default-model","options":[{"value":"default-model","name":"Default Model"},{"value":"fast-model","name":"Fast Model"}]}]}`, false)
		case acpMethodSessionLoad:
			if scenario == "load" {
				writeResponse(req.ID, `{"models":{"currentModelId":"fast-model","availableModels":[{"modelId":"fast-model","name":"Fast Model"}]},"modes":{"currentModeId":"approve","availableModes":[{"id":"auto","name":"Auto"},{"id":"approve","name":"Approve"},{"id":"smart_approve","name":"Smart Approve"},{"id":"chat","name":"Chat"}]}}`, false)
			}
		case acpMethodSessionSetModel, acpMethodSessionSetMode, acpMethodSessionPrompt:
			writeResponse(req.ID, `{}`, false)
		}
	}
	os.Exit(0)
}

func TestStartGooseCLI_NewSessionHandshake(t *testing.T) {
	installFakeGooseCLI(t, "new")

	provider, err := StartGooseCLI(context.Background(), Options{
		AgentID:       "goose-new",
		WorkingDir:    t.TempDir(),
		Shell:         "/bin/sh",
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE_CLI,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*GooseCLIAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	assert.Equal(t, "goose-new", agent.sessionID)
	assert.Equal(t, "default-model", agent.model)
	assert.Equal(t, GooseCLIModeAuto, agent.permissionMode)
	require.Len(t, agent.AvailableModels(), 2)
	assert.Equal(t, "default-model", agent.AvailableModels()[0].GetId())
	require.Len(t, agent.AvailableOptionGroups(), 1)
	assert.Equal(t, "permissionMode", agent.AvailableOptionGroups()[0].GetKey())
	// Verify mode names are capitalized (e.g. "smart_approve" → "Smart Approve").
	modeNames := make([]string, 0, len(agent.AvailableOptionGroups()[0].GetOptions()))
	for _, opt := range agent.AvailableOptionGroups()[0].GetOptions() {
		modeNames = append(modeNames, opt.GetName())
	}
	assert.Equal(t, []string{"Auto", "Approve", "Smart Approve", "Chat"}, modeNames)
}

func TestStartGooseCLI_LoadSessionUsesResumeID(t *testing.T) {
	installFakeGooseCLI(t, "load")

	provider, err := StartGooseCLI(context.Background(), Options{
		AgentID:         "goose-load",
		WorkingDir:      t.TempDir(),
		ResumeSessionID: "goose-resume",
		Shell:           "/bin/sh",
		LoginShell:      false,
		AgentProvider:   leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE_CLI,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*GooseCLIAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	assert.Equal(t, "goose-resume", agent.sessionID)
	assert.Equal(t, "fast-model", agent.model)
	assert.Equal(t, GooseCLIModeApprove, agent.permissionMode)
}

func TestGooseUpdateSettingsSendsLiveACPRequests(t *testing.T) {
	agent, requests := newGooseAgentForRPC(t)
	agent.availableModes = []*leapmuxv1.AvailableOption{
		{Id: GooseCLIModeAuto, Name: "Auto", IsDefault: true},
		{Id: GooseCLIModeApprove, Name: "Approve"},
	}

	updated := agent.UpdateSettings(&leapmuxv1.AgentSettings{
		Model:          "fast-model",
		PermissionMode: GooseCLIModeApprove,
	})
	require.True(t, updated)
	assert.Equal(t, "fast-model", agent.model)
	assert.Equal(t, GooseCLIModeApprove, agent.permissionMode)

	recorded := requests()
	require.Len(t, recorded, 2)
	assert.Equal(t, acpMethodSessionSetModel, recorded[0].Method)
	assert.Equal(t, "fast-model", recorded[0].Params["modelId"])
	assert.Equal(t, acpMethodSessionSetMode, recorded[1].Method)
	assert.Equal(t, GooseCLIModeApprove, recorded[1].Params["modeId"])
}

func TestGooseCancelSessionSendsACPMethod(t *testing.T) {
	agent, requests := newGooseAgentForRPC(t)

	require.NoError(t, agent.cancelSession())
	testutil.AssertEventually(t, func() bool {
		recorded := requests()
		return len(recorded) == 1 && recorded[0].Method == acpMethodSessionCancel
	}, "expected session/cancel notification to be recorded")
}

func TestGooseAvailableOptionGroupsFallsBack(t *testing.T) {
	agent := &GooseCLIAgent{}

	groups := agent.AvailableOptionGroups()
	require.Len(t, groups, 1)
	assert.Equal(t, "permissionMode", groups[0].GetKey())
	assert.Equal(t, GooseCLIModeAuto, groups[0].GetOptions()[0].GetId())
}

func TestDefaultModel_GooseUsesEnvOverride(t *testing.T) {
	t.Setenv("LEAPMUX_GOOSE_DEFAULT_MODEL", "custom-model")
	assert.Equal(t, "custom-model", DefaultModel(leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE_CLI))
}

package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type codexRecordedRequest struct {
	Method string
	Params map[string]interface{}
}

func newCodexAgentForRPC(t *testing.T, respond func(method string) json.RawMessage) (*CodexAgent, *testSink, func() []codexRecordedRequest) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	readPipe, writePipe, err := os.Pipe()
	require.NoError(t, err)

	sink := &testSink{}
	agent := &CodexAgent{
		jsonrpcBase: jsonrpcBase{processBase: processBase{
			agentID:     "test-codex",
			stdin:       writePipe,
			ctx:         ctx,
			cancel:      cancel,
			processDone: make(chan struct{}),
			stderrDone:  make(chan struct{}),
		}},
		model:             "gpt-5.4",
		effort:            "high",
		approvalPolicy:    CodexDefaultApprovalPolicy,
		sandboxPolicy:     CodexDefaultSandboxPolicy,
		networkAccess:     CodexDefaultNetworkAccess,
		collaborationMode: CodexDefaultCollaborationMode,
		serviceTier:       CodexDefaultServiceTier,
		sink:              sink,
	}
	close(agent.stderrDone)

	var (
		mu       sync.Mutex
		requests []codexRecordedRequest
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
			requests = append(requests, codexRecordedRequest{Method: req.Method, Params: req.Params})
			mu.Unlock()
			if ch, ok := agent.pendingReqs.Load(req.ID); ok {
				ch.(chan json.RawMessage) <- respond(req.Method)
			}
		}
	}()

	t.Cleanup(func() {
		cancel()
		_ = readPipe.Close()
		_ = writePipe.Close()
	})

	return agent, sink, func() []codexRecordedRequest {
		mu.Lock()
		defer mu.Unlock()
		out := make([]codexRecordedRequest, len(requests))
		copy(out, requests)
		return out
	}
}

func TestCodexRefreshSettingsFromAgent(t *testing.T) {
	agent, sink, requests := newCodexAgentForRPC(t, func(method string) json.RawMessage {
		if method == "config/read" {
			return json.RawMessage(`{
				"config": {
					"model": "gpt-5.2",
					"model_reasoning_effort": "medium",
					"approval_policy": "never",
					"sandbox_mode": "danger-full-access",
					"service_tier": "fast"
				},
				"origins": {}
			}`)
		}
		return json.RawMessage(`{}`)
	})

	agent.refreshSettingsFromAgent()

	assert.Equal(t, "gpt-5.2", agent.model)
	assert.Equal(t, "medium", agent.effort)
	assert.Equal(t, "never", agent.approvalPolicy)
	assert.Equal(t, "danger-full-access", agent.sandboxPolicy)
	assert.Equal(t, "fast", agent.serviceTier)

	recorded := requests()
	require.Len(t, recorded, 1)
	assert.Equal(t, "config/read", recorded[0].Method)

	// Verify settings were broadcast.
	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "gpt-5.2", refresh.Model)
	assert.Equal(t, "medium", refresh.Effort)
	assert.Equal(t, "never", refresh.PermissionMode)
	assert.Equal(t, "danger-full-access", refresh.ExtraSettings[CodexExtraSandboxPolicy])
	assert.Equal(t, "fast", refresh.ExtraSettings[CodexExtraServiceTier])
}

func TestCodexRefreshSettingsFromAgent_NullFields(t *testing.T) {
	agent, _, _ := newCodexAgentForRPC(t, func(method string) json.RawMessage {
		if method == "config/read" {
			return json.RawMessage(`{
				"config": {
					"model": null,
					"model_reasoning_effort": null,
					"approval_policy": null,
					"sandbox_mode": null,
					"service_tier": null
				},
				"origins": {}
			}`)
		}
		return json.RawMessage(`{}`)
	})

	// Set initial values.
	agent.model = "gpt-5.4"
	agent.effort = "high"
	agent.approvalPolicy = "on-request"
	agent.sandboxPolicy = "workspace-write"
	agent.serviceTier = "default"

	agent.refreshSettingsFromAgent()

	// Null fields should not overwrite existing values.
	assert.Equal(t, "gpt-5.4", agent.model)
	assert.Equal(t, "high", agent.effort)
	assert.Equal(t, "on-request", agent.approvalPolicy)
	assert.Equal(t, "workspace-write", agent.sandboxPolicy)
	assert.Equal(t, "default", agent.serviceTier)
}

func TestCodexRefreshSettingsFromAgent_GranularApprovalPolicy(t *testing.T) {
	agent, _, _ := newCodexAgentForRPC(t, func(method string) json.RawMessage {
		if method == "config/read" {
			return json.RawMessage(`{
				"config": {
					"model": "gpt-5.4",
					"approval_policy": {"granular": {"sandbox_approval": true, "rules": true}}
				},
				"origins": {}
			}`)
		}
		return json.RawMessage(`{}`)
	})

	agent.approvalPolicy = "on-request"
	agent.refreshSettingsFromAgent()

	// Granular object should not overwrite the simple string.
	assert.Equal(t, "on-request", agent.approvalPolicy)
}

func TestCodexUpdateSettingsCallsRefresh(t *testing.T) {
	agent, sink, requests := newCodexAgentForRPC(t, func(method string) json.RawMessage {
		if method == "config/read" {
			return json.RawMessage(`{
				"config": {
					"model": "gpt-5.2",
					"model_reasoning_effort": "low",
					"approval_policy": "never",
					"sandbox_mode": "read-only",
					"service_tier": "fast"
				},
				"origins": {}
			}`)
		}
		return json.RawMessage(`{}`)
	})

	updated := agent.UpdateSettings(&leapmuxv1.AgentSettings{
		Model:          "gpt-5.2",
		Effort:         "low",
		PermissionMode: "never",
		ExtraSettings: map[string]string{
			CodexExtraSandboxPolicy: "read-only",
			CodexExtraServiceTier:   "fast",
		},
	})
	require.True(t, updated)

	// After UpdateSettings, refreshSettingsFromAgent should have been called.
	recorded := requests()
	require.Len(t, recorded, 1)
	assert.Equal(t, "config/read", recorded[0].Method)

	// Values should reflect the config/read response.
	assert.Equal(t, "gpt-5.2", agent.model)
	assert.Equal(t, "low", agent.effort)
	assert.Equal(t, "never", agent.approvalPolicy)
	assert.Equal(t, "read-only", agent.sandboxPolicy)
	assert.Equal(t, "fast", agent.serviceTier)

	// Verify settings were broadcast.
	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "gpt-5.2", refresh.Model)
	assert.Equal(t, "low", refresh.Effort)
	assert.Equal(t, "never", refresh.PermissionMode)
}

// TestCodexRefreshSettingsFromAgent_AutoFallsBackToModelDefault verifies
// that when config/read returns null for model_reasoning_effort and the
// agent is in "auto" mode, the refresh falls back to the current model's
// preset default from the model catalog. This mirrors Codex's own
// inference-time behavior, where the CLI uses ModelInfo.default_reasoning_level
// when nothing is explicitly set in config.
func TestCodexRefreshSettingsFromAgent_AutoFallsBackToModelDefault(t *testing.T) {
	agent, sink, _ := newCodexAgentForRPC(t, func(method string) json.RawMessage {
		if method == "config/read" {
			return json.RawMessage(`{
				"config": {
					"model": null,
					"model_reasoning_effort": null
				},
				"origins": {}
			}`)
		}
		return json.RawMessage(`{}`)
	})

	agent.effort = "auto"
	agent.model = "gpt-5.4"
	agent.availableModels = []*leapmuxv1.AvailableModel{
		{Id: "gpt-5.4", DefaultEffort: "high"},
		{Id: "gpt-5.2", DefaultEffort: "medium"},
	}

	agent.refreshSettingsFromAgent()

	assert.Equal(t, "high", agent.effort,
		"auto should fall back to the current model's default from the catalog")

	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, "high", sink.LastSettingsRefresh().Effort,
		"broadcast should carry the resolved effort so the UI updates")
}

// TestCodexRefreshSettingsFromAgent_AutoNoModelCatalogStaysAuto verifies
// the safe fallback: if the model catalog hasn't been populated yet (e.g.
// queryAvailableModels failed), the refresh leaves effort at "auto" rather
// than clobbering it with an empty string.
func TestCodexRefreshSettingsFromAgent_AutoNoModelCatalogStaysAuto(t *testing.T) {
	agent, _, _ := newCodexAgentForRPC(t, func(method string) json.RawMessage {
		if method == "config/read" {
			return json.RawMessage(`{"config": {"model_reasoning_effort": null}, "origins": {}}`)
		}
		return json.RawMessage(`{}`)
	})

	agent.effort = "auto"
	agent.model = "gpt-5.4"
	agent.availableModels = nil

	agent.refreshSettingsFromAgent()

	assert.Equal(t, "auto", agent.effort, "with no catalog, auto stays auto")
}

// TestCodexUpdateSettings_AutoRequiresRestart verifies that switching
// effort to "auto" mid-session signals the caller to restart the agent
// (returns false) rather than writing "auto" into Codex's live session
// config. Codex has no way to clear reasoning_effort at runtime, so a
// fresh process is the only path back to CLI-default behavior.
func TestCodexUpdateSettings_AutoRequiresRestart(t *testing.T) {
	agent, _, requests := newCodexAgentForRPC(t, func(_ string) json.RawMessage {
		return json.RawMessage(`{}`)
	})

	require.Equal(t, "high", agent.effort, "precondition")

	updated := agent.UpdateSettings(&leapmuxv1.AgentSettings{Effort: "auto"})
	require.False(t, updated, "switching to \"auto\" should request a restart")

	assert.Equal(t, "high", agent.effort, "live effort must stay untouched until restart")
	assert.Empty(t, requests(), "no config/read should be issued when restart is requested")
}

// TestCodexUpdateSettings_AutoNoOpWhenAlreadyAuto verifies that when the
// agent is already in "auto", a redundant "auto" update does not trigger
// a restart.
func TestCodexUpdateSettings_AutoNoOpWhenAlreadyAuto(t *testing.T) {
	agent, _, _ := newCodexAgentForRPC(t, func(method string) json.RawMessage {
		if method == "config/read" {
			return json.RawMessage(`{"config": {}, "origins": {}}`)
		}
		return json.RawMessage(`{}`)
	})

	agent.effort = "auto"

	updated := agent.UpdateSettings(&leapmuxv1.AgentSettings{Effort: "auto"})
	require.True(t, updated, "a no-op \"auto\"→\"auto\" should not request a restart")
	assert.Equal(t, "auto", agent.effort)
}

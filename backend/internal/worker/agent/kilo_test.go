package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

type kiloRecordedRequest struct {
	Method string
	Params map[string]interface{}
}

func newKiloAgentForRPC(t *testing.T) (*KiloAgent, func() []kiloRecordedRequest) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	agent := &KiloAgent{
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
		requests []kiloRecordedRequest
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
			requests = append(requests, kiloRecordedRequest{Method: req.Method, Params: req.Params})
			mu.Unlock()
			if ch, ok := agent.pendingReqs.Load(req.ID); ok {
				ch.(chan json.RawMessage) <- json.RawMessage(`{}`)
			}
		}
	}()

	t.Cleanup(func() {
		cancel()
		_ = readPipe.Close()
		_ = writePipe.Close()
	})

	return agent, func() []kiloRecordedRequest {
		mu.Lock()
		defer mu.Unlock()
		out := make([]kiloRecordedRequest, len(requests))
		copy(out, requests)
		return out
	}
}

func TestKiloBuildSessionRequest_NewSession(t *testing.T) {
	method, params := buildACPSessionRequest("", "/workspace", acpMethodSessionNew, openCodeMethodSessionResume)
	assert.Equal(t, acpMethodSessionNew, method)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(params, &parsed))
	assert.Equal(t, "/workspace", parsed["cwd"])
	assert.NotContains(t, parsed, "sessionId")
}

func TestKiloBuildSessionRequest_ResumeSession(t *testing.T) {
	method, params := buildACPSessionRequest("session-123", "/workspace", acpMethodSessionNew, openCodeMethodSessionResume)
	assert.Equal(t, openCodeMethodSessionResume, method)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(params, &parsed))
	assert.Equal(t, "/workspace", parsed["cwd"])
	assert.Equal(t, "session-123", parsed["sessionId"])
}

func TestKiloConfigurePrimaryAgentsUsesSessionCurrentMode(t *testing.T) {
	agent := &KiloAgent{}
	err := agent.configurePrimaryAgents([]acpModeInfo{
		{ID: KiloPrimaryAgentCode, Name: KiloPrimaryAgentCode},
		{ID: OpenCodePrimaryAgentPlan, Name: OpenCodePrimaryAgentPlan},
		{ID: openCodeHiddenCompaction, Name: openCodeHiddenCompaction},
	}, OpenCodePrimaryAgentPlan, "")
	if err != nil {
		t.Fatalf("configurePrimaryAgents: %v", err)
	}

	if agent.currentPrimaryAgent != OpenCodePrimaryAgentPlan {
		t.Fatalf("expected current primary agent %q, got %q", OpenCodePrimaryAgentPlan, agent.currentPrimaryAgent)
	}
	if len(agent.availablePrimaryAgents) != 2 {
		t.Fatalf("expected 2 visible primary agents, got %d", len(agent.availablePrimaryAgents))
	}
}

func TestKiloConfigurePrimaryAgentsRestoresSavedPrimaryAgent(t *testing.T) {
	agent, requests := newKiloAgentForRPC(t)
	err := agent.configurePrimaryAgents([]acpModeInfo{
		{ID: KiloPrimaryAgentCode, Name: KiloPrimaryAgentCode},
		{ID: OpenCodePrimaryAgentPlan, Name: OpenCodePrimaryAgentPlan},
	}, KiloPrimaryAgentCode, OpenCodePrimaryAgentPlan)
	if err != nil {
		t.Fatalf("configurePrimaryAgents: %v", err)
	}

	if agent.currentPrimaryAgent != OpenCodePrimaryAgentPlan {
		t.Fatalf("expected restored primary agent %q, got %q", OpenCodePrimaryAgentPlan, agent.currentPrimaryAgent)
	}
	recorded := requests()
	if len(recorded) != 1 {
		t.Fatalf("expected 1 request, got %d", len(recorded))
	}
	if recorded[0].Method != "session/set_mode" {
		t.Fatalf("expected session/set_mode, got %q", recorded[0].Method)
	}
	if got := recorded[0].Params["modeId"]; got != OpenCodePrimaryAgentPlan {
		t.Fatalf("expected modeId %q, got %#v", OpenCodePrimaryAgentPlan, got)
	}
}

func TestKiloConfigurePrimaryAgentsIgnoresUnknownSavedPrimaryAgent(t *testing.T) {
	agent, requests := newKiloAgentForRPC(t)
	err := agent.configurePrimaryAgents([]acpModeInfo{
		{ID: KiloPrimaryAgentCode, Name: KiloPrimaryAgentCode},
		{ID: OpenCodePrimaryAgentPlan, Name: OpenCodePrimaryAgentPlan},
	}, KiloPrimaryAgentCode, "unknown")
	if err != nil {
		t.Fatalf("configurePrimaryAgents: %v", err)
	}

	if agent.currentPrimaryAgent != KiloPrimaryAgentCode {
		t.Fatalf("expected current primary agent %q, got %q", KiloPrimaryAgentCode, agent.currentPrimaryAgent)
	}
	if len(requests()) != 0 {
		t.Fatalf("expected no session/set_mode request for unknown saved agent")
	}
}

func TestKiloUpdateSettingsSendsSessionSetMode(t *testing.T) {
	agent, requests := newKiloAgentForRPC(t)
	agent.availablePrimaryAgents = []*leapmuxv1.AvailableOption{
		{Id: KiloPrimaryAgentCode, Name: KiloPrimaryAgentCode, IsDefault: true},
		{Id: OpenCodePrimaryAgentPlan, Name: OpenCodePrimaryAgentPlan},
	}
	agent.currentPrimaryAgent = KiloPrimaryAgentCode

	updated := agent.UpdateSettings(&leapmuxv1.AgentSettings{
		ExtraSettings: map[string]string{OpenCodeExtraPrimaryAgent: OpenCodePrimaryAgentPlan},
	})
	if !updated {
		t.Fatalf("expected update to succeed")
	}
	if agent.currentPrimaryAgent != OpenCodePrimaryAgentPlan {
		t.Fatalf("expected current primary agent %q, got %q", OpenCodePrimaryAgentPlan, agent.currentPrimaryAgent)
	}
	recorded := requests()
	if len(recorded) != 1 || recorded[0].Method != "session/set_mode" {
		t.Fatalf("expected one session/set_mode request, got %#v", recorded)
	}
}

func TestKiloCurrentSettingsExposesPrimaryAgent(t *testing.T) {
	agent := &KiloAgent{acpBase: acpBase{model: "openai/gpt-5", currentPrimaryAgent: OpenCodePrimaryAgentPlan}}
	settings := agent.CurrentSettings()
	if settings.GetModel() != "openai/gpt-5" {
		t.Fatalf("expected model to round-trip")
	}
	if got := settings.GetExtraSettings()[OpenCodeExtraPrimaryAgent]; got != OpenCodePrimaryAgentPlan {
		t.Fatalf("expected primaryAgent %q, got %q", OpenCodePrimaryAgentPlan, got)
	}
}

func TestKiloAvailablePrimaryAgentGroupFallsBack(t *testing.T) {
	agent := &KiloAgent{}
	groups := agent.availablePrimaryAgentGroup()
	if len(groups) != 1 {
		t.Fatalf("expected 1 option group, got %d", len(groups))
	}
	if groups[0].Key != OpenCodeExtraPrimaryAgent {
		t.Fatalf("expected key %q, got %q", OpenCodeExtraPrimaryAgent, groups[0].Key)
	}
	if len(groups[0].Options) != 2 {
		t.Fatalf("expected 2 fallback options, got %d", len(groups[0].Options))
	}
	if groups[0].Options[0].Id != KiloPrimaryAgentCode || !groups[0].Options[0].IsDefault {
		t.Fatalf("expected code to be the default fallback option")
	}
}

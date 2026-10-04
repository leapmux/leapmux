package codebuddy

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func TestCodebuddyIsInterrupt(t *testing.T) {
	t.Parallel()

	assert.True(t, codebuddyProvider{}.IsInterrupt(`{"type":"control_request","request":{"subtype":"interrupt"}}`))
	assert.False(t, codebuddyProvider{}.IsInterrupt(`{"type":"control_request","request":{"subtype":"steer"}}`))
	assert.False(t, codebuddyProvider{}.IsInterrupt(`{"action":"abort"}`))
	assert.False(t, codebuddyProvider{}.IsInterrupt(``))
}

func TestCodebuddyPlanModeControl(t *testing.T) {
	t.Parallel()

	p := codebuddyProvider{}
	assert.Equal(t, agent.PlanModeControlEnter, p.PlanModeControl("EnterPlanMode"))
	assert.Equal(t, agent.PlanModeControlExit, p.PlanModeControl("ExitPlanMode"))
	assert.Equal(t, agent.PlanModeControlNone, p.PlanModeControl("Bash"))
}

func TestCodebuddyPlanModePermissionMode(t *testing.T) {
	t.Parallel()

	p := codebuddyProvider{}
	assert.Equal(t, contracts.CodebuddyModePlan, p.PlanModePermissionMode(agent.PlanModeControlEnter))
	assert.Equal(t, contracts.CodebuddyModeAcceptEdits, p.PlanModePermissionMode(agent.PlanModeControlExit))
	assert.Empty(t, p.PlanModePermissionMode(agent.PlanModeControlNone))
}

func TestCodebuddyTurnEndToolUses(t *testing.T) {
	t.Parallel()

	p := codebuddyProvider{}
	count, ok := p.TurnEndToolUses([]byte(`{"type":"result","num_tool_uses":3}`))
	assert.True(t, ok)
	assert.Equal(t, int32(3), count)
	count, ok = p.TurnEndToolUses([]byte(`{"type":"result","num_tool_uses":0}`))
	assert.True(t, ok)
	assert.Zero(t, count)

	_, ok = p.TurnEndToolUses([]byte(`{"type":"result"}`))
	assert.False(t, ok)

	_, ok = p.TurnEndToolUses([]byte(`{`))
	assert.False(t, ok)

	_, ok = p.TurnEndToolUses([]byte(`{"type":"result","num_tool_uses":-1}`))
	assert.False(t, ok)
}

func TestCodebuddyProviderFacts(t *testing.T) {
	t.Parallel()

	agenttest.AssertTokenResumeRule(t, codebuddyProvider{})
	agenttest.AssertWithholdsTheResponseForAMalformedRequest(t, codebuddyProvider{})
	agenttest.AssertPreservesTheResponseWithoutARequest(t, codebuddyProvider{})
	agenttest.AssertChildCapabilities(t, Registration().Plugin, (*Agent)(nil))
}

func TestCodebuddyRegistration(t *testing.T) {
	t.Parallel()

	registration := Registration()
	assert.Equal(t, "LEAPMUX_CODEBUDDY_DEFAULT_MODEL", registration.EnvModelKey)
	assert.Equal(t, "LEAPMUX_CODEBUDDY_DEFAULT_EFFORT", registration.EnvEffortKey)
	assert.True(t, registration.FixedPermissionModes)
	assert.Nil(t, registration.DefaultModels, "the catalog is read from the open session")
}

// TestCodebuddyReadsItsSessionStore seeds a transcript in CodeBuddy's store and
// requires that the plugin finds it.
func TestCodebuddyReadsItsSessionStore(t *testing.T) {
	t.Parallel()
	agenttest.RequireReadsSessionStore(t, codebuddyProvider{}, func(t *testing.T, home, dir string) string {
		projects := filepath.Join(home, ".codebuddy", "projects")
		slug := codebuddyProjectSlug(dir)
		projectDir := filepath.Join(projects, slug)
		require.NoError(t, os.MkdirAll(projectDir, 0o755))
		handle := "session-codebuddy-1"
		transcript := filepath.Join(projectDir, handle+".jsonl")
		content := strings.Join([]string{
			`{"type":"message","sessionId":"` + handle + `","cwd":"` + filepath.ToSlash(dir) + `"}`,
			`{"type":"message","message":{"role":"user","content":[{"type":"input_text","text":"hello codebuddy"}]}}`,
		}, "\n") + "\n"
		require.NoError(t, os.WriteFile(transcript, []byte(content), 0o644))
		return handle
	})
}

// newOfflineAgent builds an agent with no process, for the suites that drive
// only the turn flag and the input-session guard.
func newOfflineAgent(t *testing.T, sink *agenttest.Sink) *Agent {
	return newOfflineAgentWithRuntime(t, sink, nil, nil)
}

func newOfflineAgentWithRuntime(t *testing.T, sink *agenttest.Sink, clock quartz.Clock, processDone chan struct{}) *Agent {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "test-agent", ProviderName: "codebuddy", Ctx: ctx, Cancel: cancel,
			Stdin: agenttest.NopStdin(io.Discard),
			Clock: clock, ProcessDone: processDone,
		}),
		sink:           agent.NewModelProgressResetSink(agent.NewProviderServices(sink)),
		sessionID:      "session-1",
		permissionMode: "default",
		pendingControl: make(map[string]chan<- codebuddyControlResult),
	}
	t.Cleanup(func() {
		cancel()
		a.stopWorkflowArchiveRetries()
	})
	return a
}

func TestCodebuddyPublishTurnActiveRaisesItsToken(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	agenttest.AssertRisingTurnTokens(t, sink, a)
}

func TestCodebuddyBusyRefusalRepublishesTheTurn(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.setTurnActive(true)
	err := a.SendInput("later turn", nil)
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, sink, a, err)
}

func TestCodebuddyRejectsMissingAndReplacedSessions(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	agenttest.AssertRejectsMissingAndReplacedSessions(t, a)
}

// TestCodebuddyTurnFrames pins the frames that may move the turn flag. Only a
// named turn signal moves it; a frame this build has never seen must move
// nothing.
func TestCodebuddyTurnFrames(t *testing.T) {
	t.Parallel()
	agenttest.AssertTurnFrames(t, []agenttest.TurnFrameCase{
		{Name: "assistant arms the turn", Line: `{"type":"assistant","session_id":"s","message":{"content":[]}}`, Moves: true},
		{Name: "result ends the turn", Line: `{"type":"result","num_tool_uses":0}`, Moves: true},
		{Name: "system init is inert", Line: `{"type":"system","subtype":"init","session_id":"s"}`, Moves: false},
		{Name: "unknown type is inert", Line: `{"type":"stream_event"}`, Moves: false},
	}, func(t *testing.T, tc agenttest.TurnFrameCase) []bool {
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		a.HandleOutput([]byte(tc.Line))
		return sink.TurnActiveCalls
	})
}

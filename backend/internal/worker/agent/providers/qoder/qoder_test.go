package qoder

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func TestQoderIsInterrupt(t *testing.T) {
	t.Parallel()

	assert.True(t, qoderProvider{}.IsInterrupt(`{"type":"control_request","request":{"subtype":"interrupt"}}`))
	assert.False(t, qoderProvider{}.IsInterrupt(`{"type":"control_request","request":{"subtype":"set_goal"}}`))
	assert.False(t, qoderProvider{}.IsInterrupt(`{"action":"abort"}`))
	assert.False(t, qoderProvider{}.IsInterrupt(``))
}

func TestQoderPlanModeControl(t *testing.T) {
	t.Parallel()

	p := qoderProvider{}
	assert.Equal(t, agent.PlanModeControlEnter, p.PlanModeControl("EnterPlanMode"))
	assert.Equal(t, agent.PlanModeControlExit, p.PlanModeControl("ExitPlanMode"))
	assert.Equal(t, agent.PlanModeControlNone, p.PlanModeControl("Bash"))
}

func TestQoderTurnEndToolUses(t *testing.T) {
	t.Parallel()

	p := qoderProvider{}
	count, ok := p.TurnEndToolUses([]byte(`{"type":"result","num_tool_uses":2}`))
	assert.True(t, ok)
	assert.Equal(t, int32(2), count)

	_, ok = p.TurnEndToolUses([]byte(`{"type":"result"}`))
	assert.False(t, ok)
}

func TestQoderProviderFacts(t *testing.T) {
	t.Parallel()

	assert.False(t, qoderProvider{}.SupportsChildSteering())
	agenttest.AssertTokenResumeRule(t, qoderProvider{})
	agenttest.AssertWithholdsTheResponseForAMalformedRequest(t, qoderProvider{})
	agenttest.AssertPreservesTheResponseWithoutARequest(t, qoderProvider{})
	agenttest.AssertChildCapabilities(t, Registration().Plugin, (*Agent)(nil))
}

func TestQoderRegistration(t *testing.T) {
	t.Parallel()

	registration := Registration()
	assert.Equal(t, "LEAPMUX_QODER_DEFAULT_MODEL", registration.EnvModelKey)
	assert.Equal(t, "LEAPMUX_QODER_DEFAULT_EFFORT", registration.EnvEffortKey)
	assert.True(t, registration.FixedPermissionModes)
	assert.Nil(t, registration.DefaultModels, "the account decides which models exist")
}

// TestQoderReadsItsSessionStore seeds a transcript in Qoder's store and
// requires that the plugin finds it.
func TestQoderReadsItsSessionStore(t *testing.T) {
	t.Parallel()
	agenttest.RequireReadsSessionStore(t, qoderProvider{}, func(t *testing.T, home, dir string) string {
		projects := filepath.Join(home, ".qoder", "projects")
		slug := mangleQoderPath(dir)
		projectDir := filepath.Join(projects, slug)
		require.NoError(t, os.MkdirAll(projectDir, 0o755))
		handle := "3c33fac3-77c3-4511-8aba-502ed720fdb8"
		transcript := filepath.Join(projectDir, handle+".jsonl")
		content := strings.Join([]string{
			`{"type":"workspace-directories","cwd":"` + filepath.ToSlash(dir) + `"}`,
			`{"type":"user","sessionId":"` + handle + `","message":{"role":"user","content":[{"type":"input_text","text":"hello qoder"}]}}`,
		}, "\n") + "\n"
		require.NoError(t, os.WriteFile(transcript, []byte(content), 0o644))
		return handle
	})
}

// newOfflineAgent builds an agent with no process, for the suites that drive
// only the turn flag and the input-session guard.
func newOfflineAgent(t *testing.T, sink *agenttest.Sink) *Agent {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "test-agent", ProviderName: "qoder", Ctx: ctx, Cancel: cancel,
			Stdin: agenttest.NopStdin(io.Discard),
		}),
		sink:           agent.NewModelProgressResetSink(agent.NewProviderServices(sink)),
		sessionID:      "session-1",
		permissionMode: "default",
		pendingControl: make(map[string]chan<- qoderControlResult),
	}
}

func TestQoderPublishTurnActiveRaisesItsToken(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	agenttest.AssertRisingTurnTokens(t, sink, a)
}

func TestQoderBusyRefusalRepublishesTheTurn(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.setTurnActive(true)
	err := a.SendInput("later turn", nil)
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, sink, a, err)
}

func TestQoderRejectsMissingAndReplacedSessions(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	agenttest.AssertRejectsMissingAndReplacedSessions(t, a)
}

// TestQoderTurnFrames pins the frames that may move the turn flag.
func TestQoderTurnFrames(t *testing.T) {
	t.Parallel()
	agenttest.AssertTurnFrames(t, []agenttest.TurnFrameCase{
		{Name: "assistant arms the turn", Line: `{"type":"assistant","session_id":"s","message":{"content":[]}}`, Moves: true},
		{Name: "result ends the turn", Line: `{"type":"result","num_tool_uses":0}`, Moves: true},
		{Name: "system init is inert", Line: `{"type":"system","subtype":"init","session_id":"s"}`, Moves: false},
		{Name: "stream_event is inert", Line: `{"type":"stream_event"}`, Moves: false},
	}, func(t *testing.T, tc agenttest.TurnFrameCase) []bool {
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		a.HandleOutput([]byte(tc.Line))
		return sink.TurnActiveCalls
	})
}

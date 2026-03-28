package agent

import (
	"bufio"
	"context"
	"os/exec"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubProvider struct {
	groups []*leapmuxv1.AvailableOptionGroup
}

func (s *stubProvider) AgentID() string                                          { return "stub" }
func (s *stubProvider) SendInput(string, []*leapmuxv1.Attachment) error          { return nil }
func (s *stubProvider) SendRawInput([]byte) error                                { return nil }
func (s *stubProvider) Stop()                                                    {}
func (s *stubProvider) IsStopped() bool                                          { return false }
func (s *stubProvider) Wait() error                                              { return nil }
func (s *stubProvider) Stderr() string                                           { return "" }
func (s *stubProvider) CurrentSettings() *leapmuxv1.AgentSettings                { return &leapmuxv1.AgentSettings{} }
func (s *stubProvider) HandleOutput([]byte)                                      {}
func (s *stubProvider) AvailableModels() []*leapmuxv1.AvailableModel             { return nil }
func (s *stubProvider) AvailableOptionGroups() []*leapmuxv1.AvailableOptionGroup { return s.groups }
func (s *stubProvider) UpdateSettings(*leapmuxv1.AgentSettings) bool             { return true }

// startMockAgent wraps mockStart to satisfy the startFunc signature.
func startMockAgent(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
	return mockStart(ctx, opts, sink)
}

func TestManager_StartAndStop(t *testing.T) {
	m := NewManager(nil)
	ctx := context.Background()

	_, err := m.startAgentWith(ctx, Options{
		AgentID:    "s1",
		Model:      "test",
		WorkingDir: t.TempDir(),
	}, noopSink{}, startMockAgent)
	require.NoError(t, err, "StartAgent")

	assert.True(t, m.HasAgent("s1"), "expected HasAgent(s1) = true")

	// Duplicate start should fail.
	_, err = m.startAgentWith(ctx, Options{
		AgentID:    "s1",
		Model:      "test",
		WorkingDir: t.TempDir(),
	}, noopSink{}, startMockAgent)
	assert.Error(t, err, "expected error for duplicate agent")

	// Stop and verify cleanup.
	m.StopAgent("s1")

	// Wait for the background goroutine to clean up.
	testutil.AssertEventually(t, func() bool {
		return !m.HasAgent("s1")
	}, "expected HasAgent(s1) = false after stop")
}

func TestManager_SendInput(t *testing.T) {
	m := NewManager(nil)
	ctx := context.Background()
	sink := &testSink{}

	_, err := m.startAgentWith(ctx, Options{
		AgentID:    "s2",
		Model:      "test",
		WorkingDir: t.TempDir(),
	}, sink, startMockAgent)
	require.NoError(t, err, "StartAgent")
	defer m.StopAgent("s2")

	// SendInput sends a user JSON message; the mock echoes it back.
	// Since it's a simple user text echo, HandleOutput drops it.
	// Send raw assistant NDJSON to verify the full pipeline.
	require.NoError(t, m.SendRawInput("s2", []byte(`{"type":"assistant","message":{"role":"assistant","content":"hi"}}`+"\n")), "SendRawInput")

	testutil.AssertEventually(t, func() bool {
		return sink.MessageCount() > 0
	}, "expected output from agent")
}

func TestManager_SendInputUnknownAgent(t *testing.T) {
	m := NewManager(nil)

	assert.Error(t, m.SendInput("nonexistent", "hello", nil), "expected error for unknown agent")
}

func TestManager_StopAll(t *testing.T) {
	m := NewManager(nil)
	ctx := context.Background()

	for _, id := range []string{"a", "b", "c"} {
		_, err := m.startAgentWith(ctx, Options{
			AgentID:    id,
			Model:      "test",
			WorkingDir: t.TempDir(),
		}, noopSink{}, startMockAgent)
		require.NoError(t, err, "StartAgent(%s)", id)
	}

	m.StopAll()

	// Wait for background cleanup goroutines.
	for _, id := range []string{"a", "b", "c"} {
		id := id
		testutil.AssertEventually(t, func() bool {
			return !m.HasAgent(id)
		}, "HasAgent(%s) = true after StopAll", id)
	}
}

func TestManager_StopAndWaitAgent(t *testing.T) {
	m := NewManager(nil)
	ctx := context.Background()

	_, err := m.startAgentWith(ctx, Options{
		AgentID:    "s1",
		Model:      "test",
		WorkingDir: t.TempDir(),
	}, noopSink{}, startMockAgent)
	require.NoError(t, err, "StartAgent")

	// StopAndWaitAgent should block until the agent is fully removed.
	assert.True(t, m.StopAndWaitAgent("s1"), "expected StopAndWaitAgent to return true")
	assert.False(t, m.HasAgent("s1"), "expected HasAgent(s1) = false immediately after StopAndWaitAgent")

	// A new agent with the same ID should start successfully.
	_, err = m.startAgentWith(ctx, Options{
		AgentID:    "s1",
		Model:      "test",
		WorkingDir: t.TempDir(),
	}, noopSink{}, startMockAgent)
	require.NoError(t, err, "StartAgent after StopAndWaitAgent should succeed")
	m.StopAgent("s1")
}

func TestManager_StopAndWaitAgent_NotRunning(t *testing.T) {
	m := NewManager(nil)
	assert.False(t, m.StopAndWaitAgent("nonexistent"), "expected false for non-running agent")
}

func TestManager_StopUnknownAgent(t *testing.T) {
	m := NewManager(nil)
	// Should not panic.
	m.StopAgent("nonexistent")
}

func TestManager_AgentExitCleanup(t *testing.T) {
	m := NewManager(nil)
	ctx := context.Background()

	// Start an agent that will exit on its own when stdin is closed.
	_, err := m.startAgentWith(ctx, Options{
		AgentID:    "auto-exit",
		Model:      "test",
		WorkingDir: t.TempDir(),
	}, noopSink{}, func(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
		// Create a process that exits immediately.
		ctx2, cancel := context.WithCancel(ctx)
		cmd := exec.CommandContext(ctx2, "true")
		cmd.Dir = opts.WorkingDir

		stdin, _ := cmd.StdinPipe()
		stdout, _ := cmd.StdoutPipe()
		cmd.Stderr = nil

		a := &ClaudeCodeAgent{
			processBase: processBase{
				agentID:     opts.AgentID,
				cmd:         cmd,
				stdin:       stdin,
				ctx:         ctx2,
				cancel:      cancel,
				processDone: make(chan struct{}),
				stderrDone:  make(chan struct{}),
			},
			model:          opts.Model,
			workingDir:     opts.WorkingDir,
			sink:           sink,
			pendingControl: make(map[string]chan<- claudeCodeControlResult),
		}
		close(a.stderrDone)

		if err := cmd.Start(); err != nil {
			cancel()
			return nil, err
		}

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
		go a.readOutputLoop(scanner)
		return a, nil
	})
	require.NoError(t, err, "StartAgent")

	// Wait for the process to exit and cleanup to happen.
	testutil.AssertEventually(t, func() bool {
		return !m.HasAgent("auto-exit")
	}, "expected agent to be cleaned up after exit")
}

func TestManager_AvailableOptionGroupsPrefersRuntimeGroups(t *testing.T) {
	m := NewManager(nil)
	runtimeGroups := []*leapmuxv1.AvailableOptionGroup{{
		Key:   OpenCodeExtraPrimaryAgent,
		Label: "Primary Agent",
		Options: []*leapmuxv1.AvailableOption{
			{Id: "build", Name: "build"},
			{Id: "architect", Name: "architect"},
		},
	}}

	m.mu.Lock()
	m.agents["runtime-agent"] = &stubProvider{groups: runtimeGroups}
	m.mu.Unlock()

	assert.Equal(t, runtimeGroups, m.AvailableOptionGroups("runtime-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE))

	staticGroups := m.AvailableOptionGroups("missing-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE)
	require.Len(t, staticGroups, 1)
	assert.Equal(t, OpenCodeExtraPrimaryAgent, staticGroups[0].Key)
	assert.Equal(t, OpenCodePrimaryAgentBuild, staticGroups[0].Options[0].Id)
}

func TestManager_PreloadCache(t *testing.T) {
	m := NewManager(nil)

	models := []*leapmuxv1.AvailableModel{
		{Id: "gemini-2.5-pro", DisplayName: "Gemini 2.5 Pro"},
		{Id: "gemini-2.5-flash", DisplayName: "Gemini 2.5 Flash"},
	}
	groups := []*leapmuxv1.AvailableOptionGroup{
		{Key: "thinkingBudget", Label: "Thinking Budget", Options: []*leapmuxv1.AvailableOption{
			{Id: "low", Name: "Low"},
			{Id: "high", Name: "High"},
		}},
	}

	// Preload cache for a non-running agent.
	m.PreloadCache("preloaded-agent", models, groups)

	// AvailableModels should return preloaded models (not static defaults).
	got := m.AvailableModels("preloaded-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI)
	require.Len(t, got, 2)
	assert.Equal(t, "gemini-2.5-pro", got[0].GetId())
	assert.Equal(t, "gemini-2.5-flash", got[1].GetId())

	// AvailableOptionGroups should return preloaded groups (not static defaults).
	gotGroups := m.AvailableOptionGroups("preloaded-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI)
	require.Len(t, gotGroups, 1)
	assert.Equal(t, "thinkingBudget", gotGroups[0].GetKey())
	assert.Len(t, gotGroups[0].GetOptions(), 2)
}

func TestManager_PreloadCacheSkipsEmpty(t *testing.T) {
	m := NewManager(nil)

	// Preload with nil slices — should not populate cache.
	m.PreloadCache("empty-agent", nil, nil)

	// Should fall back to static defaults.
	models := m.AvailableModels("empty-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI)
	require.NotEmpty(t, models)
	assert.Equal(t, "auto", models[0].GetId(), "should fall back to static Gemini defaults")
}

func TestManager_AvailableOptionGroupsCachedFallback(t *testing.T) {
	m := NewManager(nil)

	cachedGroups := []*leapmuxv1.AvailableOptionGroup{{
		Key:   "thinkingBudget",
		Label: "Thinking Budget",
		Options: []*leapmuxv1.AvailableOption{
			{Id: "low", Name: "Low"},
		},
	}}

	m.mu.Lock()
	m.cachedOptionGroups["cached-agent"] = cachedGroups
	m.mu.Unlock()

	// Agent is not running — should return cached groups, not static defaults.
	got := m.AvailableOptionGroups("cached-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE)
	require.Len(t, got, 1)
	assert.Equal(t, "thinkingBudget", got[0].GetKey())
}

func TestManager_AvailableModelsFallsBackToGeminiDefaults(t *testing.T) {
	m := NewManager(nil)

	models := m.AvailableModels("missing-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI)
	require.NotEmpty(t, models)
	assert.Equal(t, "auto", models[0].GetId())
	assert.True(t, models[0].GetIsDefault())
}

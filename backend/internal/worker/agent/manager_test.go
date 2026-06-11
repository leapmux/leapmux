//go:build unix

// Depends on helpers in claude_test.go / goose_test.go that spawn /bin/sh.

package agent

import (
	"bufio"
	"context"
	"os/exec"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubProvider struct {
	groups   []*leapmuxv1.AvailableOptionGroup
	settings *leapmuxv1.AgentSettings
}

func (s *stubProvider) AgentID() string                                 { return "stub" }
func (s *stubProvider) SendInput(string, []*leapmuxv1.Attachment) error { return nil }
func (s *stubProvider) SendRawInput([]byte) error                       { return nil }
func (s *stubProvider) Stop()                                           {}
func (s *stubProvider) IsStopped() bool                                 { return false }
func (s *stubProvider) DiscardOutput()                                  {}
func (s *stubProvider) ClearContext() (string, bool)                    { return "", false }
func (s *stubProvider) Wait() error                                     { return nil }
func (s *stubProvider) Stderr() string                                  { return "" }
func (s *stubProvider) CurrentSettings() *leapmuxv1.AgentSettings {
	if s.settings != nil {
		return s.settings
	}
	return &leapmuxv1.AgentSettings{}
}
func (s *stubProvider) HandleOutput([]byte)                                      {}
func (s *stubProvider) AvailableModels() []*leapmuxv1.AvailableModel             { return nil }
func (s *stubProvider) AvailableOptionGroups() []*leapmuxv1.AvailableOptionGroup { return s.groups }
func (s *stubProvider) UpdateSettings(*leapmuxv1.AgentSettings) bool             { return true }
func (s *stubProvider) Interrupt() error                                         { return nil }

// startMockAgent wraps mockStart to satisfy the startFunc signature.
func startMockAgent(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
	return mockStart(ctx, opts, sink)
}

func TestManager_SetOnExit_FiresOnStop(t *testing.T) {
	m := NewManager(func(string, int, error) {
		// Original handler: should be replaced by SetOnExit below.
		t.Error("original onExit should not be called after SetOnExit")
	})

	exited := make(chan string, 1)
	m.SetOnExit(func(agentID string, _ int, _ error) {
		exited <- agentID
	})

	_, err := m.startAgentWith(context.Background(), Options{
		AgentID:    "s-exit",
		Model:      "test",
		WorkingDir: t.TempDir(),
	}, noopSink{}, startMockAgent)
	require.NoError(t, err)

	m.StopAgent("s-exit")

	select {
	case got := <-exited:
		assert.Equal(t, "s-exit", got)
	case <-time.After(2 * time.Second):
		t.Fatal("exit handler did not fire after stop")
	}
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

func TestManager_LockAgent_ComposesStopAndStart(t *testing.T) {
	m := NewManager(nil)
	ctx := context.Background()

	_, err := m.startAgentWith(ctx, Options{AgentID: "r1", Model: "test", WorkingDir: t.TempDir()}, noopSink{}, startMockAgent)
	require.NoError(t, err)

	unlock := m.LockAgent("r1")
	m.stopAndWait("r1", false)
	_, err = m.startAgentWith(ctx, Options{AgentID: "r1", Model: "test", WorkingDir: t.TempDir()}, noopSink{}, startMockAgent)
	unlock()
	require.NoError(t, err, "restart composed under LockAgent should succeed")
	assert.True(t, m.HasAgent("r1"))
	m.StopAgent("r1")
}

func TestManager_LockAgent_SerializesConcurrentRestarts(t *testing.T) {
	m := NewManager(nil)
	ctx := context.Background()

	_, err := m.startAgentWith(ctx, Options{AgentID: "race", Model: "test", WorkingDir: t.TempDir()}, noopSink{}, startMockAgent)
	require.NoError(t, err)

	restart := func() error {
		unlock := m.LockAgent("race")
		defer unlock()
		m.stopAndWait("race", false)
		_, err := m.startAgentWith(ctx, Options{AgentID: "race", Model: "test", WorkingDir: t.TempDir()}, noopSink{}, startMockAgent)
		return err
	}

	errCh := make(chan error, 2)
	go func() { errCh <- restart() }()
	go func() { errCh <- restart() }()

	// Both must succeed: the lock serializes them so neither trips the
	// "agent already running" guard.
	assert.NoError(t, <-errCh)
	assert.NoError(t, <-errCh)
	m.StopAgent("race")
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
	}, noopSink{}, func(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
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
		Key:   OptionGroupKeyPrimaryAgent,
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
	assert.Equal(t, OptionGroupKeyPrimaryAgent, staticGroups[0].Key)
	assert.Equal(t, OpenCodePrimaryAgentBuild, staticGroups[0].Options[0].Id)
}

func TestManager_CurrentSettings(t *testing.T) {
	m := NewManager(nil)

	// No agent registered → nil, so callers fall back to their own value.
	assert.Nil(t, m.CurrentSettings("missing-agent"))

	// Registered agent → the provider's in-memory confirmed settings, letting
	// callers read back the effort the agent actually confirmed (e.g. an
	// ultracode request downgraded to xhigh) without a DB round-trip.
	m.mu.Lock()
	m.agents["running-agent"] = &stubProvider{settings: &leapmuxv1.AgentSettings{Effort: "xhigh"}}
	m.mu.Unlock()

	got := m.CurrentSettings("running-agent")
	require.NotNil(t, got)
	assert.Equal(t, "xhigh", got.GetEffort())
}

func TestManager_PreloadCache(t *testing.T) {
	m := NewManager(nil)

	models := []*leapmuxv1.AvailableModel{
		{Id: "sonnet-4.5", DisplayName: "Sonnet 4.5"},
		{Id: "gpt-5", DisplayName: "GPT-5"},
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
	got := m.AvailableModels("preloaded-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR)
	require.Len(t, got, 2)
	assert.Equal(t, "sonnet-4.5", got[0].GetId())
	assert.Equal(t, "gpt-5", got[1].GetId())

	// AvailableOptionGroups should return preloaded groups (not static defaults).
	gotGroups := m.AvailableOptionGroups("preloaded-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR)
	require.Len(t, gotGroups, 1)
	assert.Equal(t, "thinkingBudget", gotGroups[0].GetKey())
	assert.Len(t, gotGroups[0].GetOptions(), 2)
}

func TestManager_PreloadCacheSkipsEmpty(t *testing.T) {
	m := NewManager(nil)

	// Preload with nil slices — should not populate cache.
	m.PreloadCache("empty-agent", nil, nil)

	// Should fall back to static defaults.
	models := m.AvailableModels("empty-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR)
	require.NotEmpty(t, models)
	assert.Equal(t, "auto", models[0].GetId(), "should fall back to static Cursor defaults")
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

func TestManager_AvailableModelsFallsBackToCursorDefaults(t *testing.T) {
	m := NewManager(nil)

	models := m.AvailableModels("missing-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR)
	require.NotEmpty(t, models)
	assert.Equal(t, "auto", models[0].GetId())
	assert.True(t, models[0].GetIsDefault())
}

// TestWithDefaultModelMarked_PreservesACPCurrentModel verifies that for a
// provider with no configured default (registered with nil defaultModels, like
// the ACP providers that self-mark the currently-selected model in
// buildACPModels), withDefaultModelMarked leaves the per-agent IsDefault badge in
// place instead of promoting the first entry. Regression guard: defaultModelForList
// must return "" rather than falling through to "mark the first entry" when there
// is no configured default to anchor on.
func TestWithDefaultModelMarked_PreservesACPCurrentModel(t *testing.T) {
	t.Setenv("LEAPMUX_OPENCODE_DEFAULT_MODEL", "") // hermetic: ignore any ambient override
	opencode := leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE
	require.Empty(t, DefaultModel(opencode), "precondition: opencode has no configured default")

	// buildACPModels marks the active model; here that's the 2nd entry.
	models := []*leapmuxv1.AvailableModel{
		{Id: "anthropic/claude-x", DisplayName: "Claude X"},
		{Id: "openai/gpt-y", DisplayName: "GPT Y", IsDefault: true},
		{Id: "xai/grok-z", DisplayName: "Grok Z"},
	}

	got := withDefaultModelMarked(models, opencode)
	require.Len(t, got, 3)
	assert.False(t, got[0].GetIsDefault(), "first entry must not be promoted")
	assert.True(t, got[1].GetIsDefault(), "current model keeps its badge")
	assert.False(t, got[2].GetIsDefault())

	// An operator override still wins and moves the badge to the override target.
	t.Setenv("LEAPMUX_OPENCODE_DEFAULT_MODEL", "xai/grok-z")
	got = withDefaultModelMarked(models, opencode)
	require.Len(t, got, 3)
	assert.False(t, got[1].GetIsDefault(), "override clears the per-agent badge")
	assert.True(t, got[2].GetIsDefault(), "override target is marked")
}

// TestWithDefaultModelMarked_SentinelIsClaudeOnly verifies the DefaultModelSentinel
// ("default") badge rule is scoped to Claude Code. A non-Claude ACP provider whose
// live list happens to contain a model literally id'd "default" must NOT have its
// self-marked current-model badge hijacked onto that entry -- the sentinel is a
// Claude-Code concept, so for other providers "default" is just another model id.
func TestWithDefaultModelMarked_SentinelIsClaudeOnly(t *testing.T) {
	t.Setenv("LEAPMUX_OPENCODE_DEFAULT_MODEL", "") // hermetic: ignore any ambient override
	t.Setenv("LEAPMUX_CLAUDE_DEFAULT_MODEL", "")
	opencode := leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE
	require.Empty(t, DefaultModel(opencode), "precondition: opencode has no configured default")

	// An ACP list containing a model id'd "default" with a DIFFERENT self-marked
	// current model. The self-marked model must keep the badge.
	models := []*leapmuxv1.AvailableModel{
		{Id: "default", DisplayName: "Some Local Default Model"},
		{Id: "openai/gpt-y", DisplayName: "GPT Y", IsDefault: true},
	}
	got := withDefaultModelMarked(models, opencode)
	require.Len(t, got, 2)
	assert.False(t, got[0].GetIsDefault(), "non-Claude 'default'-id model is not treated as the sentinel")
	assert.True(t, got[1].GetIsDefault(), "the ACP self-marked current model keeps its badge")

	// Same list under Claude Code: the sentinel rule DOES apply, so the "default"
	// entry is badged.
	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	got = withDefaultModelMarked(models, claude)
	require.Len(t, got, 2)
	assert.True(t, got[0].GetIsDefault(), "Claude Code badges the 'default' sentinel entry")
	assert.False(t, got[1].GetIsDefault())
}

// TestWithDefaultModelMarked_PreservesProviderDefaultWhenConfiguredAbsent verifies
// that for a provider WITH a configured default (e.g. Codex), when that configured
// default is absent from an account-specific list, withDefaultModelMarked respects
// a default the provider already designated on the list itself (Codex's
// queryAvailableModels copies the CLI's isDefault) rather than promoting the first
// entry. Regression guard: the step-3 fallback must not move the badge off the
// model the CLI marked just because the registry default isn't offered.
func TestWithDefaultModelMarked_PreservesProviderDefaultWhenConfiguredAbsent(t *testing.T) {
	t.Setenv("LEAPMUX_CODEX_DEFAULT_MODEL", "") // hermetic: ignore any ambient override
	codex := leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	configured := DefaultModel(codex)
	require.NotEmpty(t, configured, "precondition: codex has a configured default")

	// An account-specific list that does NOT contain the configured default, with
	// the CLI's own default marked on the 2nd (non-first) entry.
	models := []*leapmuxv1.AvailableModel{
		{Id: "codex-mini", DisplayName: "Mini"},
		{Id: "codex-pro", DisplayName: "Pro", IsDefault: true},
	}
	require.Nil(t, FindAvailableModel(models, configured), "precondition: configured default absent from list")

	got := withDefaultModelMarked(models, codex)
	require.Len(t, got, 2)
	assert.False(t, got[0].GetIsDefault(), "first entry must not be promoted over the CLI-marked default")
	assert.True(t, got[1].GetIsDefault(), "the provider-marked default keeps its badge")

	// Sanity: with NO entry pre-marked, the badge still falls back to the first
	// entry so the picker always shows a default.
	unmarked := []*leapmuxv1.AvailableModel{{Id: "codex-mini"}, {Id: "codex-pro"}}
	got = withDefaultModelMarked(unmarked, codex)
	require.Len(t, got, 2)
	assert.True(t, got[0].GetIsDefault(), "no designated default -> first entry marked")
	assert.False(t, got[1].GetIsDefault())
}

// TestWithDefaultModelMarked_EnvOverrideAbsentFallsThrough verifies that an operator
// default-model override pointing at a model the (account-specific) list does not
// contain -- or naming it with a different spelling than the catalog stores -- does
// NOT strip every entry's badge. defaultModelForList honors the override only when it
// resolves to a model in the list (by exact id or provider-normalized alias);
// otherwise it falls through the ladder so the picker still shows a default.
func TestWithDefaultModelMarked_EnvOverrideAbsentFallsThrough(t *testing.T) {
	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE

	// A Claude list with the account-default sentinel present.
	models := []*leapmuxv1.AvailableModel{
		{Id: DefaultModelSentinel, DisplayName: "Default (recommended)", IsDefault: true},
		{Id: "sonnet", DisplayName: "Sonnet"},
	}

	// Override names a model the account does not offer -> falls through to the
	// sentinel (Claude's list-designated default); the badge is preserved.
	t.Setenv("LEAPMUX_CLAUDE_DEFAULT_MODEL", "opus[1m]")
	got := withDefaultModelMarked(models, claude)
	require.Len(t, got, 2)
	assert.True(t, got[0].GetIsDefault(), "absent override falls through to the sentinel")
	assert.False(t, got[1].GetIsDefault())
	require.NotEmpty(t, markedModelID(got), "some entry stays badged")

	// Override names a PRESENT model with a fully-qualified spelling: it resolves to
	// the catalog's normalized alias and wins, moving the badge off the sentinel.
	present := []*leapmuxv1.AvailableModel{
		{Id: DefaultModelSentinel, DisplayName: "Default (recommended)", IsDefault: true},
		{Id: "opus[1m]", DisplayName: "Opus (1M context)"},
	}
	t.Setenv("LEAPMUX_CLAUDE_DEFAULT_MODEL", "claude-opus-4-8[1m]")
	got = withDefaultModelMarked(present, claude)
	require.Len(t, got, 2)
	assert.False(t, got[0].GetIsDefault(), "override resolves to the present alias; sentinel loses the badge")
	assert.True(t, got[1].GetIsDefault(), "fully-qualified override matches the normalized opus[1m]")
}

// TestWithDefaultModelMarked_ClaudeNoSentinelFallsBackToFirst covers the documented
// Claude branch of defaultModelForList step 3: a Claude CLI reporting concrete models
// but NO "default" sentinel (and no operator override) falls back to badging the first
// model so the picker still shows a default.
func TestWithDefaultModelMarked_ClaudeNoSentinelFallsBackToFirst(t *testing.T) {
	t.Setenv("LEAPMUX_CLAUDE_DEFAULT_MODEL", "")
	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	require.Equal(t, DefaultModelSentinel, DefaultModel(claude), "precondition: Claude's configured default is the sentinel")

	models := []*leapmuxv1.AvailableModel{
		{Id: "opus", DisplayName: "Opus"},
		{Id: "sonnet", DisplayName: "Sonnet"},
	}
	require.Nil(t, FindAvailableModel(models, DefaultModelSentinel), "precondition: no sentinel in the list")

	got := withDefaultModelMarked(models, claude)
	require.Len(t, got, 2)
	assert.True(t, got[0].GetIsDefault(), "no sentinel -> first concrete model badged")
	assert.False(t, got[1].GetIsDefault())
}

// TestFirstAndMarkedModelID verifies the small id-extraction helpers tolerate nil
// entries and return the right id (or "").
func TestFirstAndMarkedModelID(t *testing.T) {
	models := []*leapmuxv1.AvailableModel{
		nil,
		{Id: "a"},
		{Id: "b", IsDefault: true},
		nil,
		{Id: "c", IsDefault: true},
	}
	assert.Equal(t, "a", firstModelID(models), "first non-nil id, skipping nils")
	assert.Equal(t, "b", markedModelID(models), "first IsDefault id")

	none := []*leapmuxv1.AvailableModel{nil, {Id: "x"}, {Id: "y"}}
	assert.Equal(t, "", markedModelID(none), "no marked entry -> empty")
	assert.Equal(t, "x", firstModelID(none))

	assert.Equal(t, "", firstModelID([]*leapmuxv1.AvailableModel{nil, nil}), "all-nil -> empty, no panic")
	assert.Equal(t, "", markedModelID(nil))
	assert.Equal(t, "", firstModelID(nil))
}

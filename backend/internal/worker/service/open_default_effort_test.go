package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestOpenAgentAppliesSafePermissionDefaultsToNewSessions(t *testing.T) {
	cases := []struct {
		name     string
		provider leapmuxv1.AgentProvider
		want     map[string]string
	}{
		{
			name:     "claude auto",
			provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			want:     map[string]string{agent.OptionIDPermissionMode: agent.PermissionModeAuto},
		},
		{
			name:     "goose smart approve",
			provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
			want:     map[string]string{agent.OptionIDPermissionMode: contracts.GooseModeSmartApprove},
		},
		{
			name:     "copilot assisted approval",
			provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
			want: map[string]string{
				contracts.CopilotPermissionGroupAssistedApproval: contracts.CopilotPermissionValueOn,
				contracts.CopilotPermissionGroupAllowAll:         contracts.CopilotPermissionValueOff,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, d, w := setupTestService(t)
			started := make(chan agent.Options, 1)
			svc.startAgentFn = func(_ context.Context, opts agent.Options, _ agent.OutputSink) (map[string]string, error) {
				started <- opts
				return opts.Options, nil
			}

			dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
				WorkingDir:    t.TempDir(),
				AgentProvider: tc.provider,
			}, w)
			require.Empty(t, w.errors)

			select {
			case opts := <-started:
				for id, value := range tc.want {
					assert.Equal(t, value, opts.Get(id))
				}
			case <-time.After(5 * time.Second):
				t.Fatal("startAgentFn did not run within 5 seconds")
			}
		})
	}
}

func TestOpenAgentDoesNotApplySafePermissionDefaultsToResumedSessions(t *testing.T) {
	cases := []struct {
		name       string
		provider   leapmuxv1.AgentProvider
		safeOption string
		safeValue  string
	}{
		{"claude auto", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, agent.OptionIDPermissionMode, agent.PermissionModeAuto},
		{"goose smart approve", leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, agent.OptionIDPermissionMode, contracts.GooseModeSmartApprove},
		{"copilot assisted approval", leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT, contracts.CopilotPermissionGroupAssistedApproval, contracts.CopilotPermissionValueOn},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, d, w := setupTestService(t)
			started := make(chan agent.Options, 1)
			svc.startAgentFn = func(_ context.Context, opts agent.Options, _ agent.OutputSink) (map[string]string, error) {
				started <- opts
				return opts.Options, nil
			}

			dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
				WorkingDir:     t.TempDir(),
				AgentProvider:  tc.provider,
				AgentSessionId: "resume-session",
			}, w)
			require.Empty(t, w.errors)

			select {
			case opts := <-started:
				assert.NotEqual(t, tc.safeValue, opts.Get(tc.safeOption))
			case <-time.After(5 * time.Second):
				t.Fatal("startAgentFn did not run within 5 seconds")
			}
		})
	}
}

func TestOpenAgentExplicitPermissionOptionsOverrideSafeDefaults(t *testing.T) {
	svc, d, w := setupTestService(t)
	started := make(chan agent.Options, 1)
	svc.startAgentFn = func(_ context.Context, opts agent.Options, _ agent.OutputSink) (map[string]string, error) {
		started <- opts
		return opts.Options, nil
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
		Options: map[string]string{
			contracts.CopilotPermissionGroupAllowAll: contracts.CopilotPermissionValueOn,
		},
	}, w)
	require.Empty(t, w.errors)

	select {
	case opts := <-started:
		assert.Equal(t, contracts.CopilotPermissionValueOn, opts.Get(contracts.CopilotPermissionGroupAllowAll))
		assert.Equal(t, contracts.CopilotPermissionValueOff, opts.Get(contracts.CopilotPermissionGroupAssistedApproval))
	case <-time.After(5 * time.Second):
		t.Fatal("startAgentFn did not run within 5 seconds")
	}
}

// TestOpenAgent_DefaultsEffortToAuto verifies that when the OpenAgent
// request omits the effort, the backend fills it in with the "auto"
// sentinel so the agent binary picks its own default (rather than pinning
// LeapMux to a specific effort name that older CLIs may not recognize).
func TestOpenAgent_DefaultsEffortToAuto(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t)

	var capturedMu sync.Mutex
	var captured agent.Options
	done := make(chan struct{})
	svc.startAgentFn = func(_ context.Context, opts agent.Options, _ agent.OutputSink) (map[string]string, error) {
		capturedMu.Lock()
		captured = opts
		capturedMu.Unlock()
		close(done)
		return map[string]string{}, nil
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)

	require.Empty(t, w.errors, "OpenAgent should succeed")
	require.Len(t, w.responses, 1)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("startAgentFn not invoked within 5s")
	}

	capturedMu.Lock()
	effort := captured.Effort()
	capturedMu.Unlock()
	assert.Equal(t, "auto", effort,
		"agent.Options.Effort should default to \"auto\" (CLI picks its own default)")

	var resp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.NotNil(t, resp.GetAgent())

	// The "auto" sentinel is persisted as the effort option. (It is not surfaced
	// as a standalone effort option group because the account-default model has no
	// concrete effort tiers, but it is recorded so the launch reproduces it.)
	dbAgent, err := svc.Queries.GetAgentByID(ctx, resp.GetAgent().GetId())
	require.NoError(t, err)
	assert.Equal(t, "auto", loadOptions(dbAgent.Options, dbAgent.AgentProvider)[agent.OptionIDEffort],
		"the agent's effort should default to the \"auto\" sentinel")
}

// TestOpenAgent_RespectsEnvOverride verifies that when
// LEAPMUX_CLAUDE_DEFAULT_EFFORT is set, the backend injects that value
// instead of the "auto" sentinel. This is the documented escape hatch for
// users who want to pin a specific effort across workspaces.
func TestOpenAgent_RespectsEnvOverride(t *testing.T) {
	t.Setenv("LEAPMUX_CLAUDE_DEFAULT_EFFORT", "high")

	svc, d, w := setupTestService(t)

	var capturedMu sync.Mutex
	var captured agent.Options
	done := make(chan struct{})
	svc.startAgentFn = func(_ context.Context, opts agent.Options, _ agent.OutputSink) (map[string]string, error) {
		capturedMu.Lock()
		captured = opts
		capturedMu.Unlock()
		close(done)
		return map[string]string{}, nil
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)

	require.Empty(t, w.errors, "OpenAgent should succeed")

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("startAgentFn not invoked within 5s")
	}

	capturedMu.Lock()
	effort := captured.Effort()
	capturedMu.Unlock()
	assert.Equal(t, "high", effort,
		"env var LEAPMUX_CLAUDE_DEFAULT_EFFORT should override the \"auto\" default")
}

// TestOpenAgent_PreservesExplicitEffort verifies that an effort specified
// on the OpenAgent request passes through untouched, even when the env var
// override is set (explicit request wins).
func TestOpenAgent_PreservesExplicitEffort(t *testing.T) {
	t.Setenv("LEAPMUX_CLAUDE_DEFAULT_EFFORT", "high")

	svc, d, w := setupTestService(t)

	var capturedMu sync.Mutex
	var captured agent.Options
	done := make(chan struct{})
	svc.startAgentFn = func(_ context.Context, opts agent.Options, _ agent.OutputSink) (map[string]string, error) {
		capturedMu.Lock()
		captured = opts
		capturedMu.Unlock()
		close(done)
		return map[string]string{}, nil
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Options:       map[string]string{agent.OptionIDEffort: "medium"},
	}, w)

	require.Empty(t, w.errors, "OpenAgent should succeed")

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("startAgentFn not invoked within 5s")
	}

	capturedMu.Lock()
	effort := captured.Effort()
	capturedMu.Unlock()
	assert.Equal(t, "medium", effort,
		"explicit effort in OpenAgent request should win over env var override")
}

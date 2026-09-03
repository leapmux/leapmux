package service

import (
	"context"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// openAgentAndCapture dispatches OpenAgent and returns the launch options the service
// handed startAgentFn. It fails the test when the RPC reports an error or the start never
// runs, so a caller asserts on the options alone. The service and the response writer come
// back for the cases that also read the response or the persisted row.
//
// Six tests in this file drove the same capture-and-await skeleton by hand, in two
// spellings (a buffered channel, and a mutex plus a done channel). One helper is the only
// way the timeout, the error check and the locking stay identical across them.
func openAgentAndCapture(t *testing.T, req *leapmuxv1.OpenAgentRequest) (*Service, *testResponseWriter, agent.Options) {
	t.Helper()
	svc, d, w := setupTestService(t)
	started := make(chan agent.Options, 1)
	svc.startAgentFn = func(_ context.Context, opts agent.Options, _ agent.OutputSink) (map[string]string, error) {
		started <- opts
		return opts.Options, nil
	}

	dispatch(d, "OpenAgent", req, w)
	require.Empty(t, w.errors, "OpenAgent should succeed")

	select {
	case opts := <-started:
		return svc, w, opts
	case <-time.After(5 * time.Second):
		t.Fatal("startAgentFn did not run within 5 seconds")
		return nil, nil, agent.Options{}
	}
}

func TestOpenAgentAppliesSafePermissionDefaultsToNewSessions(t *testing.T) {
	cases := []struct {
		name     string
		provider leapmuxv1.AgentProvider
		want     map[string]string
	}{
		{
			name:     "claude auto",
			provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			want:     map[string]string{agent.OptionIDPermissionMode: contracts.ClaudeModeAuto},
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
			_, _, opts := openAgentAndCapture(t, &leapmuxv1.OpenAgentRequest{
				WorkingDir:    t.TempDir(),
				AgentProvider: tc.provider,
			})
			for id, value := range tc.want {
				assert.Equal(t, value, opts.Get(id))
			}
		})
	}
}

// A resumed session receives no safe default: it keeps whatever it had, and falls back to
// the PROVIDER's own default for an axis it never stored.
//
// The assertions are exact values rather than "not the safe value". Goose's provider
// default and its safe default are the same mode, so a NotEqual could not tell "the safe
// default was skipped" from "the safe default was applied", and it passed on the very
// value this change exists to avoid -- Goose's earlier fallback was `auto`, which is the
// mode Goose's own BYPASS preset selects.
//
// resolveLaunchOptions' own decision (a resumed request marks NO id) is asserted in
// options_test.go. What reaches the provider here is the launch funnel's re-derivation
// from the resulting values, which is all a later restart can read.
func TestOpenAgentDoesNotApplySafePermissionDefaultsToResumedSessions(t *testing.T) {
	cases := []struct {
		name        string
		provider    leapmuxv1.AgentProvider
		option      string
		wantResumed string
		// wantDefaulted is what the launch funnel derives from the resulting VALUE, which
		// is the only thing a relaunch can read. It is true only where the provider's own
		// fallback happens to equal its safe default.
		wantDefaulted bool
	}{
		{"claude falls back to default", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, agent.OptionIDPermissionMode, contracts.ClaudeModeDefault, false},
		{"goose falls back to smart approve, never its bypass mode", leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, agent.OptionIDPermissionMode, contracts.GooseModeSmartApprove, true},
		{"copilot leaves assisted approval unset", leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT, contracts.CopilotPermissionGroupAssistedApproval, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, opts := openAgentAndCapture(t, &leapmuxv1.OpenAgentRequest{
				WorkingDir:     t.TempDir(),
				AgentProvider:  tc.provider,
				AgentSessionId: "resume-session",
			})
			assert.Equal(t, tc.wantResumed, opts.Get(tc.option))
			assert.Equal(t, tc.wantDefaulted, opts.NewSessionDefaultOptionIDs[tc.option])
		})
	}
}

func TestOpenAgentExplicitPermissionOptionsOverrideSafeDefaults(t *testing.T) {
	_, _, opts := openAgentAndCapture(t, &leapmuxv1.OpenAgentRequest{
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
		Options: map[string]string{
			contracts.CopilotPermissionGroupAllowAll: contracts.CopilotPermissionValueOn,
		},
	})
	assert.Equal(t, contracts.CopilotPermissionValueOn, opts.Get(contracts.CopilotPermissionGroupAllowAll),
		"the explicit request wins over the safe default")
	// Requesting Allow All leaves Assisted Approval at its safe default: clearing that
	// axis costs a process restart, and Allow All already supersedes it.
	assert.Equal(t, contracts.CopilotPermissionValueOn, opts.Get(contracts.CopilotPermissionGroupAssistedApproval))
}

// TestOpenAgent_DefaultsEffortToAuto verifies that when the OpenAgent
// request omits the effort, the backend fills it in with the "auto"
// sentinel so the agent binary picks its own default (rather than pinning
// LeapMux to a specific effort name that older CLIs may not recognize).
func TestOpenAgent_DefaultsEffortToAuto(t *testing.T) {
	ctx := context.Background()
	svc, w, captured := openAgentAndCapture(t, &leapmuxv1.OpenAgentRequest{
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	})
	require.Len(t, w.responses, 1)

	assert.Equal(t, "auto", captured.Effort(),
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

	_, _, captured := openAgentAndCapture(t, &leapmuxv1.OpenAgentRequest{
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	})
	assert.Equal(t, "high", captured.Effort(),
		"env var LEAPMUX_CLAUDE_DEFAULT_EFFORT should override the \"auto\" default")
}

// TestOpenAgent_PreservesExplicitEffort verifies that an effort specified
// on the OpenAgent request passes through untouched, even when the env var
// override is set (explicit request wins).
func TestOpenAgent_PreservesExplicitEffort(t *testing.T) {
	t.Setenv("LEAPMUX_CLAUDE_DEFAULT_EFFORT", "high")

	_, _, captured := openAgentAndCapture(t, &leapmuxv1.OpenAgentRequest{
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Options:       map[string]string{agent.OptionIDEffort: "medium"},
	})
	assert.Equal(t, "medium", captured.Effort(),
		"explicit effort in OpenAgent request should win over env var override")
}

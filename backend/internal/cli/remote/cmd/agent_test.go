package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
)

// TestResolveProvider_KnownAliasShortCircuits pins the happy path:
// when the user passes a recognised --provider value the helper maps
// to the enum without calling the worker. Without this, every
// `tab open --type=agent --provider=...` invocation would pay an
// extra ListAvailableProviders RPC for no benefit.
func TestResolveProvider_KnownAliasShortCircuits(t *testing.T) {
	called := false
	list := func(context.Context, *remote.Client, string, string, proto.Message, proto.Message) error {
		called = true
		return nil
	}
	got, err := resolveProvider(context.Background(), nil, "worker-A", "claude-code", list)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, got)
	assert.False(t, called, "known alias must skip the worker RPC")
}

// TestResolveProvider_UnknownAliasReturnsInvalidRequest pins the
// hard-fail path: a typo'd --provider must NOT silently fall back to
// Claude Code (the legacy behaviour) and must NOT trigger a worker
// query. The error envelope lists every alias the parser accepts so
// the user can fix the typo without grepping the source.
func TestResolveProvider_UnknownAliasReturnsInvalidRequest(t *testing.T) {
	called := false
	list := func(context.Context, *remote.Client, string, string, proto.Message, proto.Message) error {
		called = true
		return nil
	}
	out := withCapturedStdout(t, func() {
		_, err := resolveProvider(context.Background(), nil, "worker-A", "not-a-provider", list)
		require.Error(t, err)
	})
	assert.False(t, called, "unknown alias must not fire the worker RPC")
	var env struct {
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Equal(t, "invalid_request", env.Error["code"])
	assert.Contains(t, env.Error["message"], "not-a-provider")
	// The error message must surface canonical aliases so the user can
	// recover without an extra round-trip.
	assert.Contains(t, env.Error["message"], "Claude Code")
	assert.Contains(t, env.Error["message"], "Codex")
}

// TestResolveProvider_EmptyAutoPicksWhenWorkerHasOne pins the silent
// success path: a single installed provider on the worker means
// `tab open --type=agent` needs no --provider flag at all. Mirrors
// the frontend picker auto-selecting when only one option exists.
func TestResolveProvider_EmptyAutoPicksWhenWorkerHasOne(t *testing.T) {
	list := func(_ context.Context, _ *remote.Client, _, method string, _ proto.Message, out proto.Message) error {
		assert.Equal(t, "ListAvailableProviders", method)
		resp, ok := out.(*leapmuxv1.ListAvailableProvidersResponse)
		require.True(t, ok)
		resp.Providers = []leapmuxv1.AgentProvider{leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX}
		return nil
	}
	got, err := resolveProvider(context.Background(), nil, "worker-A", "", list)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, got)
}

// TestResolveProvider_EmptyAmbiguousReturnsTypedError pins the disambig
// path: when the worker reports more than one installed provider the
// CLI must NOT pick one arbitrarily. The envelope's `code` is the
// stable `ambiguous_provider` so scripts can branch without parsing the
// message, and the message itself lists the candidate display names.
func TestResolveProvider_EmptyAmbiguousReturnsTypedError(t *testing.T) {
	list := func(_ context.Context, _ *remote.Client, _, _ string, _ proto.Message, out proto.Message) error {
		resp := out.(*leapmuxv1.ListAvailableProvidersResponse)
		resp.Providers = []leapmuxv1.AgentProvider{
			leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		}
		return nil
	}
	out := withCapturedStdout(t, func() {
		_, err := resolveProvider(context.Background(), nil, "worker-A", "", list)
		require.Error(t, err)
	})
	var env struct {
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Equal(t, "ambiguous_provider", env.Error["code"])
	assert.Contains(t, env.Error["message"], "Claude Code")
	assert.Contains(t, env.Error["message"], "Codex")
}

// TestResolveProvider_EmptyEmptyWorkerReturnsTypedError pins the
// no-providers-installed case. The worker has nothing to spawn from;
// the CLI must say so explicitly with `no_providers_installed` rather
// than letting the user's tab open fail downstream with a generic
// worker-side error.
func TestResolveProvider_EmptyEmptyWorkerReturnsTypedError(t *testing.T) {
	list := func(_ context.Context, _ *remote.Client, _, _ string, _ proto.Message, out proto.Message) error {
		resp := out.(*leapmuxv1.ListAvailableProvidersResponse)
		resp.Providers = nil
		return nil
	}
	out := withCapturedStdout(t, func() {
		_, err := resolveProvider(context.Background(), nil, "worker-A", "", list)
		require.Error(t, err)
	})
	var env struct {
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Equal(t, "no_providers_installed", env.Error["code"])
}

// TestResolveProvider_EmptyPropagatesCodedRPCError pins that an RPC
// failure during ListAvailableProviders preserves the underlying code
// (e.g. "channel_open_failed") rather than collapsing every error to
// `rpc_failed`. Without this, network and authn problems become
// indistinguishable in scripts.
func TestResolveProvider_EmptyPropagatesCodedRPCError(t *testing.T) {
	list := func(context.Context, *remote.Client, string, string, proto.Message, proto.Message) error {
		return &codedRPCError{Code: "channel_open_failed", Cause: errors.New("noise handshake failed")}
	}
	out := withCapturedStdout(t, func() {
		_, err := resolveProvider(context.Background(), nil, "worker-A", "", list)
		require.Error(t, err)
	})
	var env struct {
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Equal(t, "channel_open_failed", env.Error["code"])
	assert.Contains(t, env.Error["message"], "noise handshake failed")
}

// TestRunAgentInterrupt_RequiresAgentID pins the early-validation
// path: missing --agent-id surfaces invalid_request without spinning
// up a transport. Without this, scripts that forget the flag would
// see a network error rather than a clear hint.
func TestRunAgentInterrupt_RequiresAgentID(t *testing.T) {
	clearRemoteEnv(t)
	out := withCapturedStdout(t, func() {
		err := RunAgentInterrupt(fakeCmdCtx{}, []string{"--hub", "https://stub"})
		require.Error(t, err)
	})
	var env struct {
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Equal(t, "invalid_request", env.Error["code"])
}

// TestRunAgentSend_RequiresMessageOrStdin pins the second invalid-
// args branch: --tab-id is set, but no --message and --stdin not
// provided. The CLI must surface this clearly so scripts don't send
// empty turns to the agent by accident.
func TestRunAgentSend_RequiresMessageOrStdin(t *testing.T) {
	clearRemoteEnv(t)
	out := withCapturedStdout(t, func() {
		err := RunAgentSend(fakeCmdCtx{}, []string{"--hub", "https://stub", "--tab-id", "ag-1"})
		require.Error(t, err)
	})
	var env struct {
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Equal(t, "invalid_request", env.Error["code"])
	assert.Contains(t, env.Error["message"], "message")
	assert.Contains(t, env.Error["message"], "stdin")
}

// TestRunTabRename_RequiresTitle pins the early-validation path on
// the new universal `tab rename` command. Missing --title surfaces
// invalid_request without any RPC traffic.
func TestRunTabRename_RequiresTitle(t *testing.T) {
	t.Setenv("LEAPMUX_HUB", "")
	t.Setenv("LEAPMUX_REMOTE_TAB_ID", "tab-1")
	t.Setenv("LEAPMUX_REMOTE_TAB_TYPE", "agent")
	out := withCapturedStdout(t, func() {
		err := RunTabRename(fakeCmdCtx{}, []string{"--hub", "https://stub"})
		require.Error(t, err)
	})
	var env struct {
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Equal(t, "invalid_request", env.Error["code"])
	assert.Contains(t, env.Error["message"], "--title")
}

// TestApplyPermissionMode_NoOpOnEmptyMode pins the "user didn't pass
// --permission-mode" path: no RPC fires and the helper returns "" so
// the caller emits the regular agent envelope.
func TestApplyPermissionMode_NoOpOnEmptyMode(t *testing.T) {
	called := false
	apply := func(context.Context, *remote.Client, string, string, proto.Message, proto.Message) error {
		called = true
		return nil
	}
	got := applyPermissionMode(context.Background(), nil, "worker-A", "agent-1", "", apply)
	assert.Equal(t, "", got)
	assert.False(t, called, "empty --permission-mode must not fire UpdateAgentSettings")
}

// TestApplyPermissionMode_BuildsRequestWithJustPermissionMode pins the
// payload shape: model/effort/extras stay empty so the worker's
// "empty == no change" semantics leave provider-driven defaults
// untouched. A regression that populated those fields too would
// silently clobber the agent's model the first time the user passes
// --permission-mode after `agent open` already succeeded.
func TestApplyPermissionMode_BuildsRequestWithJustPermissionMode(t *testing.T) {
	var captured *leapmuxv1.UpdateAgentSettingsRequest
	apply := func(_ context.Context, _ *remote.Client, workerID, method string, in proto.Message, _ proto.Message) error {
		assert.Equal(t, "worker-A", workerID)
		assert.Equal(t, "UpdateAgentSettings", method)
		req, ok := in.(*leapmuxv1.UpdateAgentSettingsRequest)
		require.True(t, ok, "in must be UpdateAgentSettingsRequest, got %T", in)
		captured = req
		return nil
	}
	got := applyPermissionMode(context.Background(), nil, "worker-A", "agent-1", "default", apply)
	assert.Equal(t, "", got, "successful apply returns empty string")
	require.NotNil(t, captured)
	assert.Equal(t, "agent-1", captured.GetAgentId())
	settings := captured.GetSettings()
	require.NotNil(t, settings)
	assert.Equal(t, "default", settings.GetPermissionMode())
	assert.Equal(t, "", settings.GetModel(), "model must stay empty so worker treats it as 'no change'")
	assert.Equal(t, "", settings.GetEffort(), "effort must stay empty so worker treats it as 'no change'")
	assert.Empty(t, settings.GetExtraSettings())
}

// TestApplyPermissionMode_FailureReturnsErrorMessage covers the
// non-fatal error path. The agent is up; we just couldn't apply the
// permission mode. Returning the error message lets the caller fold
// it into the JSON envelope as `permission_mode_apply_error` so
// scripts can decide whether to retry.
func TestApplyPermissionMode_FailureReturnsErrorMessage(t *testing.T) {
	apply := func(context.Context, *remote.Client, string, string, proto.Message, proto.Message) error {
		return errors.New("worker rejected: unknown permission mode")
	}
	got := applyPermissionMode(context.Background(), nil, "worker-A", "agent-1", "yolo", apply)
	assert.Contains(t, got, "worker rejected")
	assert.Contains(t, got, "unknown permission mode")
}

// TestRunAgentSendControlResponse_RequiresAgentAndContent pins the
// invalid-args path. The control_response payload has no safe default
// (the agent treats absent content as "no decision provided"), so
// missing --content must hard-fail rather than silently send "{}".
func TestRunAgentSendControlResponse_RequiresAgentAndContent(t *testing.T) {
	clearRemoteEnv(t)
	out := withCapturedStdout(t, func() {
		err := RunAgentSendControlResponse(fakeCmdCtx{}, []string{"--hub", "https://stub", "--tab-id", "ag-1"})
		require.Error(t, err)
	})
	var env struct {
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Equal(t, "invalid_request", env.Error["code"])
	assert.Contains(t, env.Error["message"], "content")
}

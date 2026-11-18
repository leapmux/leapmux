package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
)

// TestResolveProvider_KnownAliasShortCircuits pins the happy path:
// when the user passes a recognised --provider value the helper maps
// to the enum without calling the worker. Without this, every
// `tab open --type=agent --provider=...` invocation would pay an
// extra ListAvailableProviders RPC for no benefit.
func TestResolveProvider_KnownAliasShortCircuits(t *testing.T) {
	called := false
	list := func(context.Context, string, proto.Message, proto.Message) error {
		called = true
		return nil
	}
	got, err := resolveProvider(context.Background(), "claude-code", list)
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
	list := func(context.Context, string, proto.Message, proto.Message) error {
		called = true
		return nil
	}
	_, err := resolveProvider(context.Background(), "not-a-provider", list)
	assert.False(t, called, "unknown alias must not fire the worker RPC")
	env := failureOf(t, err)
	assert.Equal(t, "usage", env.Error["code"])
	assert.Contains(t, env.Error["message"], "not-a-provider")
	// The hint must list the accepted values so the user can recover
	// without an extra round-trip.
	assert.Contains(t, env.Error["hints"], "claude-code")
	assert.Contains(t, env.Error["hints"], "codex")
}

// TestResolveProvider_EmptyAutoPicksWhenWorkerHasOne pins the silent
// success path: a single installed provider on the worker means
// `tab open --type=agent` needs no --provider flag at all. Mirrors
// the frontend picker auto-selecting when only one option exists.
func TestResolveProvider_EmptyAutoPicksWhenWorkerHasOne(t *testing.T) {
	list := func(_ context.Context, method string, _ proto.Message, out proto.Message) error {
		assert.Equal(t, leapmuxv1connect.WorkerAgentServiceListAvailableProvidersProcedure, method)
		resp, ok := out.(*leapmuxv1.ListAvailableProvidersResponse)
		require.True(t, ok)
		resp.Providers = []leapmuxv1.AgentProvider{leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX}
		return nil
	}
	got, err := resolveProvider(context.Background(), "", list)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, got)
}

// TestResolveProvider_EmptyAmbiguousReturnsTypedError pins the disambig
// path: when the worker reports more than one installed provider the
// CLI must NOT pick one arbitrarily. The envelope's `code` is the
// stable `ambiguous_provider` so scripts can branch without parsing the
// message, and the message itself lists the candidate display names.
func TestResolveProvider_EmptyAmbiguousReturnsTypedError(t *testing.T) {
	list := func(_ context.Context, _ string, _ proto.Message, out proto.Message) error {
		resp := out.(*leapmuxv1.ListAvailableProvidersResponse)
		resp.Providers = []leapmuxv1.AgentProvider{
			leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		}
		return nil
	}
	_, err := resolveProvider(context.Background(), "", list)
	env := failureOf(t, err)
	assert.Equal(t, "usage", env.Error["code"])
	assert.Contains(t, env.Error["message"], "Claude Code")
	assert.Contains(t, env.Error["message"], "Codex")
}

// TestResolveProvider_EmptyEmptyWorkerReturnsTypedError pins the
// no-providers-installed case. The worker has nothing to spawn from;
// the CLI must say so explicitly with `no_providers_installed` rather
// than letting the user's tab open fail downstream with a generic
// worker-side error.
func TestResolveProvider_EmptyEmptyWorkerReturnsTypedError(t *testing.T) {
	list := func(_ context.Context, _ string, _ proto.Message, out proto.Message) error {
		resp := out.(*leapmuxv1.ListAvailableProvidersResponse)
		resp.Providers = nil
		return nil
	}
	_, err := resolveProvider(context.Background(), "", list)
	env := failureOf(t, err)
	assert.Equal(t, "conflict", env.Error["code"])
}

// TestResolveProvider_EmptyPropagatesCodedRPCError pins that an RPC
// failure during ListAvailableProviders preserves the underlying code
// (e.g. "channel_open_failed") rather than collapsing every error to
// `rpc_failed`. Without this, network and authn problems become
// indistinguishable in scripts.
func TestResolveProvider_EmptyPropagatesCodedRPCError(t *testing.T) {
	list := func(context.Context, string, proto.Message, proto.Message) error {
		return &codedRPCError{Code: "channel_open_failed", Cause: errors.New("noise handshake failed")}
	}
	_, err := resolveProvider(context.Background(), "", list)
	env := failureOf(t, err)
	assert.Equal(t, "worker_offline", env.Error["code"])
	assert.Contains(t, env.Error["message"], "noise handshake failed")
}

// A command that acts on an agent requires the agent: a bare one must never
// act on the agent that runs it. The refusal is a usage error, before any
// transport exists.
func TestAgentInterrupt_RequiresTheAgent(t *testing.T) {
	clearRemoteEnv(t)
	o := runControl(t, "agent", "interrupt", "--hub", "https://stub.invalid")
	f := o.requireFailure(t, "usage")
	assert.Contains(t, f.Message, "<agent>")
}

// `agent send` refuses an empty message, so a script cannot send an empty
// turn by accident.
func TestAgentSend_RequiresTheText(t *testing.T) {
	clearRemoteEnv(t)
	for name, args := range map[string][]string{
		"no text":    {"agent", "send", fullTestID("ag-1")},
		"empty text": {"agent", "send", fullTestID("ag-1"), "   "},
	} {
		t.Run(name, func(t *testing.T) {
			o := runControl(t, append(args, "--hub", "https://stub.invalid")...)
			o.requireFailure(t, "usage")
		})
	}
}

// The text is an argument or --file, never both.
func TestAgentSend_RefusesTextAndFileTogether(t *testing.T) {
	clearRemoteEnv(t)
	o := runControl(t, "agent", "send", fullTestID("ag-1"), "hi", "--file", "prompt.md", "--hub", "https://stub.invalid")
	f := o.requireFailure(t, "usage")
	assert.Contains(t, f.Message, "not both")
}

// A word that starts with a dash is a flag, and an unknown flag fails: a
// misspelled flag never reaches the agent as text.
func TestAgentSend_AnUnknownFlagNeverBecomesText(t *testing.T) {
	clearRemoteEnv(t)
	o := runControl(t, "agent", "send", fullTestID("ag-1"), "--wiat", "--hub", "https://stub.invalid")
	f := o.requireFailure(t, "usage")
	assert.Contains(t, f.Message, "--wiat")
}

// `tab rename` requires the title.
func TestTabRename_RequiresTheTitle(t *testing.T) {
	clearRemoteEnv(t)
	o := runControl(t, "tab", "rename", "self", "--hub", "https://stub.invalid")
	f := o.requireFailure(t, "usage")
	assert.Contains(t, f.Message, "<title>")
}

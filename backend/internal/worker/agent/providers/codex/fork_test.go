package codex

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexProviderSupportsFork(t *testing.T) {
	t.Parallel()
	assert.True(t, Registration().Plugin.SupportsFork())
}

func TestCodexForkThread(t *testing.T) {
	t.Parallel()

	const timeout = 5 * time.Second

	t.Run("sends thread/fork with target cwd and adopts returned thread ID", func(t *testing.T) {
		t.Parallel()
		a, _, requests := newCodexAgentForRPC(t, func(method string) agenttest.RPCReply {
			require.Equal(t, "thread/fork", method)
			return agenttest.RPCReply{Result: json.RawMessage(`{
				"thread":{"id":"thread-forked"},
				"model":"gpt-5.6-sol",
				"reasoningEffort":"high",
				"approvalPolicy":"on-request",
				"approvalsReviewer":"user",
				"activePermissionProfile":{"id":":workspace","extends":null}
			}`)}
		})

		params := codexThreadParams(
			"gpt-5.6-luna",
			"/work/worktree-branch",
			codexPermissionSettings{mode: DefaultPermissionMode, profile: contracts.CodexPermissionProfileWorkspace},
			ServiceTierFast,
		)
		thread, err := a.forkThread(params, "thread-source", timeout)
		require.NoError(t, err)
		assert.Equal(t, "thread-forked", thread.ID)

		// Caller's params map must not be mutated with threadId.
		assert.NotContains(t, params, "threadId")

		a.applyThreadResult(thread)
		assert.Equal(t, "gpt-5.6-sol", a.model)

		sent := requests()
		require.Len(t, sent, 1)
		assert.Equal(t, "thread/fork", sent[0].Method)
		assert.Equal(t, "thread-source", sent[0].Params["threadId"])
		assert.Equal(t, "/work/worktree-branch", sent[0].Params["cwd"])
		assert.Equal(t, "gpt-5.6-luna", sent[0].Params["model"])
		assert.Equal(t, ModeAskForApproval, sent[0].Params["approvalPolicy"])
		assert.Equal(t, contracts.CodexPermissionProfileWorkspace, sent[0].Params["permissions"])
		assert.Equal(t, ServiceTierFast, sent[0].Params["serviceTier"])
		assert.NotContains(t, sent[0].Params, "excludeTurns")
		assert.NotContains(t, sent[0].Params, "historyMode")
	})

	t.Run("omits model when account default sentinel is requested", func(t *testing.T) {
		t.Parallel()
		a, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
			return agenttest.RPCReply{Result: json.RawMessage(`{"thread":{"id":"thread-forked"},"model":"gpt-5.6-sol"}`)}
		})
		params := codexThreadParams(
			agent.DefaultModelSentinel,
			"/work/tree",
			codexPermissionSettings{mode: DefaultPermissionMode, profile: contracts.CodexPermissionProfileWorkspace},
			contracts.CodexOptionDefaultServiceTier,
		)
		thread, err := a.forkThread(params, "thread-source", timeout)
		require.NoError(t, err)
		assert.Equal(t, "thread-forked", thread.ID)

		sent := requests()
		require.Len(t, sent, 1)
		assert.Equal(t, "thread/fork", sent[0].Method)
		assert.NotContains(t, sent[0].Params, "model")
	})

	t.Run("fails when thread/fork returns an RPC error", func(t *testing.T) {
		t.Parallel()
		a, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"source thread missing"}`)}
		})
		_, err := a.forkThread(map[string]interface{}{"cwd": "/work"}, "thread-source", timeout)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "thread-source")
		assert.Contains(t, err.Error(), "source thread missing")
		require.Len(t, requests(), 1)
	})

	t.Run("fails when thread/fork returns the source thread ID unchanged", func(t *testing.T) {
		t.Parallel()
		a, _, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
			return agenttest.RPCReply{Result: json.RawMessage(`{"thread":{"id":"thread-source"},"model":"gpt-5.6-sol"}`)}
		})
		_, err := a.forkThread(map[string]interface{}{"cwd": "/work"}, "thread-source", timeout)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "source thread ID")
	})

	t.Run("fails when thread/fork omits thread ID", func(t *testing.T) {
		t.Parallel()
		a, _, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
			return agenttest.RPCReply{Result: json.RawMessage(`{"thread":{},"model":"gpt-5.6-sol"}`)}
		})
		_, err := a.forkThread(map[string]interface{}{"cwd": "/work"}, "thread-source", timeout)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no thread ID")
	})
}

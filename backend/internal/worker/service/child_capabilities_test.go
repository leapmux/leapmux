package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

type storedChildOperationsProvider struct{ agent.ProviderDefaults }

func (storedChildOperationsProvider) ChildCapabilities(options optionmap.Map) agent.ChildCapabilities {
	accepts := options["operationPolicy"] == "interactive"
	return agent.ChildCapabilities{AcceptsMessages: accepts, AcceptsInterrupt: accepts}
}

func TestChildCapabilitiesUseTheStoredOptions(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name, options string
		accepts       bool
	}{
		{name: "interactive", options: `{"operationPolicy":"interactive"}`, accepts: true},
		{name: "one shot", options: `{"operationPolicy":"one-shot"}`},
		{name: "unknown", options: `{"operationPolicy":"unknown"}`},
		{name: "absent", options: `{}`},
		{name: "empty", options: `{"operationPolicy":""}`},
		{name: "invalid value", options: `{"operationPolicy":0}`},
		{name: "malformed", options: `{broken`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
			registry := registryWithPlugin(t, provider, storedChildOperationsProvider{})
			svc, _, _ := setupTestService(t, withRegistry(registry))
			ctx := context.Background()
			require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{ID: "root", WorkingDir: t.TempDir(), HomeDir: t.TempDir(), AgentProvider: provider}))
			sink := svc.Output.NewSink("root", provider)
			childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "spawn", ProviderChildKey: "native-child", Title: "Child"})
			require.NoError(t, err)
			_, err = svc.DB.ExecContext(ctx, "UPDATE agents SET options = ? WHERE id = ?", testCase.options, childID)
			require.NoError(t, err)
			child, err := svc.Queries.GetAgentByID(ctx, childID)
			require.NoError(t, err)
			info := svc.agentToProto(&child, false, nil)
			assert.Equal(t, testCase.accepts, info.AcceptsMessages)
			assert.Equal(t, testCase.accepts, info.AcceptsInterrupt)
			queue := agentInputQueueAdapter{svc: svc}
			assert.Equal(t, testCase.accepts, queue.AcceptsKind(childID, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE))
			assert.True(t, queue.AcceptsKind(childID, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK))
		})
	}
}

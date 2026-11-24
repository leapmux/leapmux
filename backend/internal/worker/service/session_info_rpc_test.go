package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func TestGetAgentSessionInfo_ReturnsCachedMetricsAndEmptyForClosedOrIdle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "ag-live",
		WorkingDir:    "/tmp",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	// Before any sink exists, returns empty JSON object.
	dispatch(d, leapmuxv1connect.WorkerAgentServiceGetAgentSessionInfoProcedure, &leapmuxv1.GetAgentSessionInfoRequest{
		AgentId: "ag-live",
	}, w)
	require.Len(t, w.responses, 1)
	var emptyResp leapmuxv1.GetAgentSessionInfoResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &emptyResp))
	assert.Equal(t, "{}", emptyResp.GetSessionInfoJson())

	// Seed a live sink with session info.
	sink := &agentOutputSink{
		h:       svc.Output,
		agentID: "ag-live",
		lastSessionInfo: map[string][]byte{
			contracts.SessionInfoKeyTotalCostUsd: []byte(`1.2345`),
			contracts.SessionInfoKeyContextUsage: []byte(`{"percentage":42.5}`),
		},
	}
	svc.Output.sinksByAgent.Store("ag-live", sink)

	w2 := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerAgentServiceGetAgentSessionInfoProcedure, &leapmuxv1.GetAgentSessionInfoRequest{
		AgentId: "ag-live",
	}, w2)
	require.Len(t, w2.responses, 1)
	var resp leapmuxv1.GetAgentSessionInfoResponse
	require.NoError(t, proto.Unmarshal(w2.responses[0].GetPayload(), &resp))
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(resp.GetSessionInfoJson()), &parsed))
	assert.InDelta(t, 1.2345, parsed[contracts.SessionInfoKeyTotalCostUsd], 1e-6)
}

func TestListAgents_IncludeDescendantsReturnsUnopenedSubagents(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "root-1",
		WorkingDir:    "/tmp",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.NoError(t, svc.Queries.CreateChildAgent(ctx, db.CreateChildAgentParams{
		ID:            "child-1",
		ParentAgentID: sql.NullString{String: "root-1", Valid: true},
		WorkingDir:    "/tmp",
		Title:         "Subagent 1",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.NoError(t, svc.Queries.CreateChildAgent(ctx, db.CreateChildAgentParams{
		ID:            "grandchild-1",
		ParentAgentID: sql.NullString{String: "child-1", Valid: true},
		WorkingDir:    "/tmp",
		Title:         "Grandchild 1",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	dispatch(d, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{
		TabIds:             []string{"root-1"},
		IncludeDescendants: true,
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListAgentsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	ids := make([]string, 0, len(resp.GetAgents()))
	for _, a := range resp.GetAgents() {
		ids = append(ids, a.GetId())
	}
	assert.ElementsMatch(t, []string{"root-1", "child-1", "grandchild-1"}, ids)
}

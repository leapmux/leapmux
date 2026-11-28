package service

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func TestAnswerControl_BlocksRootAgentFromAnsweringOwnChildSubagent(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	ctx := context.Background()

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "root-1",
		Title:         "Root",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		WorkingDir:    t.TempDir(),
	}))
	require.NoError(t, svc.Queries.CreateChildAgent(ctx, db.CreateChildAgentParams{
		ID:            "child-1",
		Title:         "Child",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		WorkingDir:    t.TempDir(),
		ParentAgentID: sql.NullString{String: "root-1", Valid: true},
	}))

	// RIPPLE-5: A root agent calling AnswerControl on its own virtual subagent must be denied.
	payload, err := proto.Marshal(&leapmuxv1.AnswerControlRequest{
		AgentId:    "child-1",
		RequestId:  "req-child",
		ClaimToken: "claim-1",
		Decision: &leapmuxv1.ControlDecision{
			Kind: &leapmuxv1.ControlDecision_Permission{
				Permission: &leapmuxv1.PermissionDecision{OptionId: "allow"},
			},
		},
	})
	require.NoError(t, err)
	w := &testResponseWriter{}
	dispatchTestRequest(d, ctx, tabCaller("root-1"), &leapmuxv1.InnerRpcRequest{
		Method:  leapmuxv1connect.WorkerAgentServiceAnswerControlProcedure,
		Payload: payload,
	}, w)
	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(codes.PermissionDenied), w.errors[0].code)
}

func TestAnswerControl_RecoversDeliveredRecordingAfterRequestDisappears(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	ctx := context.Background()

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-rec",
		Title:         "Recover",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		WorkingDir:    t.TempDir(),
	}))
	sink := newTestRootOutputSink(t, svc.Output, "agent-rec", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	reqPayload := []byte(`{"id":"req-delivered","method":"item/commandExecution/requestApproval","params":{"command":"pwd"}}`)
	require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: "req-delivered", Payload: reqPayload}))
	stored := requireLookupControlRequest(t, ctx, svc.Output, "agent-rec", "req-delivered")

	respContent := []byte(`{"id":"req-delivered","result":{"decision":"accept"}}`)
	deliveryID, err := svc.Output.claimControlResponseAnswer(db.StoreControlResponseAnswerParams{
		AgentID:         "agent-rec",
		RequestID:       "req-delivered",
		ClaimToken:      stored.ClaimToken,
		RequestPayload:  stored.Payload,
		ResponseContent: respContent,
		ResolvedContent: respContent,
	})
	require.NoError(t, err)
	require.NotEmpty(t, deliveryID)
	require.NoError(t, setControlDeliveryState(svc.Queries, deliveryID, storedStatePending, storedStateDelivered))
	// Remove the pending blocking request (simulating agent restart before finalization).
	svc.Output.controls.drainBlocking("agent-rec")

	// REMOVALS-2: Retrying AnswerControl via RPC must resume finalization and reach COMPLETED.
	w := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerAgentServiceAnswerControlProcedure, &leapmuxv1.AnswerControlRequest{
		AgentId:    "agent-rec",
		RequestId:  "req-delivered",
		ClaimToken: stored.ClaimToken,
		Decision: &leapmuxv1.ControlDecision{
			Kind: &leapmuxv1.ControlDecision_Permission{
				Permission: &leapmuxv1.PermissionDecision{OptionId: "accept"},
			},
		},
	}, w)
	require.Empty(t, w.errors)
	var resp leapmuxv1.AnswerControlResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetError())
	assert.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED, resp.GetState())
}

func TestControlRPC_MapsStorageErrorToInternalNotInvalidArgument(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	ctx := context.Background()

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-db-err",
		Title:         "DBErr",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		WorkingDir:    t.TempDir(),
	}))
	_, err := svc.DB.ExecContext(ctx, `DROP TABLE control_response_payloads`)
	require.NoError(t, err)
	_, err = svc.Queries.CloseAgent(ctx, "agent-db-err")
	require.NoError(t, err)

	// SCAN-14: RecordControlResponse must map storage errors to Internal, not InvalidArgument.
	wRec := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerAgentServiceRecordControlResponseProcedure, &leapmuxv1.RecordControlResponseRequest{
		AgentId:    "agent-db-err",
		RequestId:  "req-1",
		ClaimToken: "claim-1",
	}, wRec)
	require.Len(t, wRec.errors, 1)
	assert.Equal(t, int32(codes.Internal), wRec.errors[0].code)

	// SCAN-14: AnswerControl must map storage errors to Internal, not InvalidArgument.
	wAns := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerAgentServiceAnswerControlProcedure, &leapmuxv1.AnswerControlRequest{
		AgentId:    "agent-db-err",
		RequestId:  "req-1",
		ClaimToken: "claim-1",
		Decision: &leapmuxv1.ControlDecision{
			Kind: &leapmuxv1.ControlDecision_Permission{
				Permission: &leapmuxv1.PermissionDecision{OptionId: "allow"},
			},
		},
	}, wAns)
	require.Len(t, wAns.errors, 1)
	assert.Equal(t, int32(codes.Internal), wAns.errors[0].code)
}

func TestCompleteControlDelivery_HonorsCanceledContext(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	// SWEEP-1: completeControlDelivery must honor ctx on the GetControlDelivery fallback too.
	_, err := completeControlDelivery(canceledCtx, svc.Queries, "missing-delivery")
	require.ErrorIs(t, err, context.Canceled)
}

func TestAnswerControl_PropagatesLookupErrorOnRPC(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	ctx := context.Background()

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-lookup-err",
		Title:         "LookupErr",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		WorkingDir:    t.TempDir(),
	}))
	// Drop control_requests so LookupControlRequest fails while control_response_payloads still exists.
	_, err := svc.DB.ExecContext(ctx, `DROP TABLE control_requests`)
	require.NoError(t, err)

	// REMOVALS-1 / REMOVALS-5: AnswerControl RPC must never drop LookupControlRequest errors.
	w := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerAgentServiceAnswerControlProcedure, &leapmuxv1.AnswerControlRequest{
		AgentId:    "agent-lookup-err",
		RequestId:  "req-missing",
		ClaimToken: "claim-1",
		Decision: &leapmuxv1.ControlDecision{
			Kind: &leapmuxv1.ControlDecision_Permission{
				Permission: &leapmuxv1.PermissionDecision{OptionId: "accept"},
			},
		},
	}, w)
	require.Empty(t, w.errors)
	var resp leapmuxv1.AnswerControlResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.NotEmpty(t, resp.GetError(), "LookupControlRequest failure must be surfaced in AnswerControlResponse.Error")
}

func TestAnswerControl_RPCValidationAndDeliveryPaths(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	ctx := context.Background()

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-rpc-flow",
		Title:         "RPCFlow",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		WorkingDir:    t.TempDir(),
	}))
	sink := newTestRootOutputSink(t, svc.Output, "agent-rpc-flow", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	reqPayload := []byte(`{"jsonrpc":"2.0","id":"req-rpc","method":"item/commandExecution/requestApproval","params":{"command":"pwd"}}`)
	identity, valid := agent.JSONRPCControlRequestIdentity(reqPayload)
	require.True(t, valid)
	requestID := identity.Key
	require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: requestID, Payload: reqPayload}))
	stored := requireLookupControlRequest(t, ctx, svc.Output, "agent-rpc-flow", requestID)

	// Missing decision must fail at RPC boundary with InvalidArgument.
	wBad := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerAgentServiceAnswerControlProcedure, &leapmuxv1.AnswerControlRequest{
		AgentId:    "agent-rpc-flow",
		RequestId:  requestID,
		ClaimToken: stored.ClaimToken,
	}, wBad)
	require.Len(t, wBad.errors, 1)
	assert.Equal(t, int32(codes.InvalidArgument), wBad.errors[0].code)

	// Valid decision delivers through sendControlResponseFn and completes via RPC dispatch.
	var delivered []byte
	svc.sendControlResponseFn = func(agentID string, answer agent.RawInput) error {
		assert.Equal(t, "agent-rpc-flow", agentID)
		delivered = append([]byte(nil), answer.Data...)
		return nil
	}
	wOK := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerAgentServiceAnswerControlProcedure, &leapmuxv1.AnswerControlRequest{
		AgentId:    "agent-rpc-flow",
		RequestId:  requestID,
		ClaimToken: stored.ClaimToken,
		Decision: &leapmuxv1.ControlDecision{
			Kind: &leapmuxv1.ControlDecision_Permission{
				Permission: &leapmuxv1.PermissionDecision{OptionId: "accept"},
			},
		},
	}, wOK)
	require.Empty(t, wOK.errors)
	var resp leapmuxv1.AnswerControlResponse
	require.NoError(t, proto.Unmarshal(wOK.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetError())
	assert.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED, resp.GetState())
	assert.Contains(t, string(delivered), `"accept"`)
}

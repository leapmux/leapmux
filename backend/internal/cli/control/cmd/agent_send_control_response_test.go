package cmd

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

type controlResponseDispatcher struct {
	mu       sync.Mutex
	requests []*leapmuxv1.SendControlResponseRequest
	response *leapmuxv1.SendControlResponseResponse
}

func (dispatcher *controlResponseDispatcher) DispatchWith(_ context.Context, _ channel.Caller, request *leapmuxv1.InnerRpcRequest, writer channel.ResponseWriter) {
	if request.Method != "SendControlResponse" {
		_ = writer.SendError(int32(codes.Unimplemented), "unexpected method")
		return
	}
	var response leapmuxv1.SendControlResponseRequest
	if err := proto.Unmarshal(request.Payload, &response); err != nil {
		_ = writer.SendError(int32(codes.InvalidArgument), err.Error())
		return
	}
	dispatcher.mu.Lock()
	dispatcher.requests = append(dispatcher.requests, &response)
	dispatcher.mu.Unlock()
	payload, err := proto.Marshal(dispatcher.response)
	if err != nil {
		_ = writer.SendError(int32(codes.Internal), err.Error())
		return
	}
	_ = writer.SendResponse(&leapmuxv1.InnerRpcResponse{Payload: payload})
}

func TestRunAgentSendControlResponsePreservesIdentityAndChecksCompletion(t *testing.T) {
	for _, test := range []struct {
		name       string
		recordOnly bool
		state      leapmuxv1.ControlResponseState
		detail     string
		failed     bool
	}{
		{"answer", false, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED, "", false},
		{"record", true, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED, "", false},
		{"check", true, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY, "", false},
		{"unknown", false, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_UNSPECIFIED, "", true},
		{"record failure", true, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_DELIVERED, "storage unavailable", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := &controlResponseDispatcher{response: &leapmuxv1.SendControlResponseResponse{State: test.state, Error: test.detail}}
			startSpawnIPC(t, &recordingHub{locateTab: agentTab()}, dispatcher)
			args := []string{"--tab-id", "agent-2", "--request-id", "jsonrpc:9007199254740993", "--claim-token", "instance"}
			content := ` {"id":9007199254740993,"result":{"enabled":false,"count":0,"text":""}} `
			if test.recordOnly {
				args = append(args, "--record-only")
			} else {
				args = append(args, "--content", content)
			}
			output := withCapturedStdout(t, func() {
				err := RunAgentSendControlResponse(fakeCmdCtx{}, args)
				require.Equal(t, test.failed, err != nil)
			})
			var envelope struct {
				Data  map[string]string `json:"data"`
				Error map[string]string `json:"error"`
			}
			require.NoError(t, json.Unmarshal(output, &envelope))
			if test.failed {
				require.Equal(t, "control_response_incomplete", envelope.Error["code"])
			} else {
				require.Equal(t, "agent-2", envelope.Data["agent_id"])
				require.Equal(t, test.state.String(), envelope.Data["state"])
			}
			dispatcher.mu.Lock()
			defer dispatcher.mu.Unlock()
			require.Len(t, dispatcher.requests, 1)
			request := dispatcher.requests[0]
			require.Equal(t, "jsonrpc:9007199254740993", request.RequestId)
			require.Equal(t, "instance", request.ClaimToken)
			require.Equal(t, test.recordOnly, request.RecordOnly)
			if test.recordOnly {
				require.Empty(t, request.Content)
			} else {
				require.Equal(t, content, string(request.Content))
			}
		})
	}
}

func TestRunAgentSendControlResponseSeparatesPlanSettings(t *testing.T) {
	for _, clearContext := range []string{"false", "true"} {
		t.Run(clearContext, func(t *testing.T) {
			dispatcher := &controlResponseDispatcher{response: &leapmuxv1.SendControlResponseResponse{
				State: leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED,
			}}
			startSpawnIPC(t, &recordingHub{locateTab: agentTab()}, dispatcher)
			content := ` {"response":{"request_id":"plan","response":{"behavior":"allow"}},"large":9007199254740993} `
			withCapturedStdout(t, func() {
				require.NoError(t, RunAgentSendControlResponse(fakeCmdCtx{}, []string{
					"--tab-id", "agent-2", "--request-id", "plan", "--claim-token", "claim", "--content", content,
					"--plan-permission-mode=", "--plan-clear-context=" + clearContext,
				}))
			})
			dispatcher.mu.Lock()
			requests := append([]*leapmuxv1.SendControlResponseRequest(nil), dispatcher.requests...)
			dispatcher.mu.Unlock()
			require.Len(t, requests, 1)
			require.Equal(t, content, string(requests[0].Content))
			require.NotNil(t, requests[0].PlanApproval)
			require.Empty(t, requests[0].PlanApproval.PermissionMode)
			require.Equal(t, clearContext == "true", requests[0].PlanApproval.ClearContext)
		})
	}
}

func TestRunAgentSendControlResponseRejectsPlanSettingsDuringRecording(t *testing.T) {
	dispatcher := &controlResponseDispatcher{}
	startSpawnIPC(t, &recordingHub{locateTab: agentTab()}, dispatcher)
	output := withCapturedStdout(t, func() {
		require.Error(t, RunAgentSendControlResponse(fakeCmdCtx{}, []string{
			"--tab-id", "agent-2", "--request-id", "plan", "--claim-token", "claim",
			"--record-only", "--plan-clear-context=false",
		}))
	})
	require.Contains(t, string(output), "--record-only cannot include plan settings")
	dispatcher.mu.Lock()
	count := len(dispatcher.requests)
	dispatcher.mu.Unlock()
	require.Zero(t, count)
}

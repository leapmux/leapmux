package cmd

import (
	"context"
	"encoding/base64"
	"slices"
	"sync"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
)

// rpcCall is one call that a fake received: the procedure and the request
// bytes as they arrived.
type rpcCall struct {
	procedure string
	payload   []byte
}

// rpcHub is a controlipc.HubClient that records each call and answers it
// from answers. A procedure without an answer is refused. mu guards calls.
type rpcHub struct {
	answers map[string]proto.Message
	mu      sync.Mutex
	calls   []rpcCall
}

func (h *rpcHub) CallInner(_ context.Context, _ userid.UserID, procedure string, payload []byte) ([]byte, error) {
	h.mu.Lock()
	h.calls = append(h.calls, rpcCall{procedure: procedure, payload: slices.Clone(payload)})
	h.mu.Unlock()
	answer, ok := h.answers[procedure]
	if !ok {
		return nil, connect.NewError(connect.CodePermissionDenied, "delegation token cannot call this procedure: "+procedure)
	}
	return proto.Marshal(answer)
}

// callsOf returns the calls of procedure that the hub received.
func (h *rpcHub) callsOf(procedure string) []rpcCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []rpcCall
	for _, call := range h.calls {
		if call.procedure == procedure {
			out = append(out, call)
		}
	}
	return out
}

// rpcDispatcher is a fake Worker that records each call and answers it with
// answer, or with the in-band error refusal when it is not zero. mu guards
// calls.
type rpcDispatcher struct {
	answer  proto.Message
	refusal codes.Code
	mu      sync.Mutex
	calls   []rpcCall
}

func (d *rpcDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	d.mu.Lock()
	d.calls = append(d.calls, rpcCall{procedure: req.GetMethod(), payload: slices.Clone(req.GetPayload())})
	d.mu.Unlock()
	if d.refusal != codes.OK {
		_ = rw.SendError(int32(d.refusal), "no such agent")
		return
	}
	writeProtoResponse(rw, d.answer)
}

func (d *rpcDispatcher) recorded() []rpcCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.calls)
}

// startRPCIPC serves hub and disp behind the socket of a tab on worker-A.
func startRPCIPC(t *testing.T, hub *rpcHub, disp controlipc.LocalDispatcher) {
	t.Helper()
	startSpawnIPC(t, &recordingHub{}, disp, func(r *controlipc.Router) { r.Hub = hub })
	t.Setenv("LEAPMUX_CONTROL_WORKER_ID", "worker-A")
}

// marshaled returns the bytes of msg as the CLI sends them.
func marshaled(t *testing.T, msg proto.Message) []byte {
	t.Helper()
	raw, err := proto.Marshal(msg)
	require.NoError(t, err)
	return raw
}

// listTabsAnswer is a ListTabs response of one terminal tab.
func listTabsAnswer() *leapmuxv1.ListTabsResponse {
	return &leapmuxv1.ListTabsResponse{Tabs: []*leapmuxv1.WorkspaceTab{{
		Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "t-1"},
		WorkerId:    "worker-A",
		WorkspaceId: "ws-1",
	}}}
}

const listTabsText = `{
  "tabs": [
    {
      "tab": {
        "tab_type": "TAB_TYPE_TERMINAL",
        "tab_id": "t-1"
      },
      "worker_id": "worker-A",
      "workspace_id": "ws-1"
    }
  ]
}
`

func TestRPCOverIPC_CallsAHubProcedure(t *testing.T) {
	hub := &rpcHub{answers: map[string]proto.Message{leapmuxv1connect.WorkspaceServiceListTabsProcedure: listTabsAnswer()}}
	startRPCIPC(t, hub, nil)

	o := runControlText(t, "rpc", "leapmux.v1.WorkspaceService/ListTabs", `{"workspace_ids": ["ws-1"]}`)
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, listTabsText, o.Stdout)
	calls := hub.callsOf(leapmuxv1connect.WorkspaceServiceListTabsProcedure)
	require.Len(t, calls, 1)
	assert.Equal(t, marshaled(t, &leapmuxv1.ListTabsRequest{WorkspaceIds: []string{"ws-1"}}), calls[0].payload)
}

func TestRPCOverIPC_PrintsTheResponseAsAnEnvelope(t *testing.T) {
	hub := &rpcHub{answers: map[string]proto.Message{leapmuxv1connect.WorkspaceServiceListTabsProcedure: listTabsAnswer()}}
	startRPCIPC(t, hub, nil)

	o := runControl(t, "rpc", "WorkspaceService/ListTabs")
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.JSONEq(t, `{"data":{"tabs":[{"tab":{"tab_type":"TAB_TYPE_TERMINAL","tab_id":"t-1"},"worker_id":"worker-A","workspace_id":"ws-1"}]}}`, o.Stdout)
	calls := hub.callsOf(leapmuxv1connect.WorkspaceServiceListTabsProcedure)
	require.Len(t, calls, 1)
	assert.Empty(t, calls[0].payload, "a command line without a request sends {}")
}

func TestRPCOverIPC_ReadsTheRequestFromStdin(t *testing.T) {
	hub := &rpcHub{answers: map[string]proto.Message{leapmuxv1connect.WorkspaceServiceListTabsProcedure: &leapmuxv1.ListTabsResponse{}}}
	startRPCIPC(t, hub, nil)

	// protobuf JSON takes the JSON name of a field too.
	o := runControlIn(t, `{"workspaceIds": ["ws-1", "ws-2"]}`, "rpc", "/leapmux.v1.WorkspaceService/ListTabs", "-")
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, "{}\n", o.Stdout)
	calls := hub.callsOf(leapmuxv1connect.WorkspaceServiceListTabsProcedure)
	require.Len(t, calls, 1)
	assert.Equal(t, marshaled(t, &leapmuxv1.ListTabsRequest{WorkspaceIds: []string{"ws-1", "ws-2"}}), calls[0].payload)
}

func TestRPCOverIPC_ReportsTheRefusalOfTheHub(t *testing.T) {
	hub := &rpcHub{}
	startRPCIPC(t, hub, nil)

	f := runControl(t, "rpc", "WorkspaceService/ListTabs").requireFailure(t, cli.CodePermissionDenied)
	assert.Contains(t, f.Message, "delegation token cannot call this procedure")
}

func TestRPCOverIPC_RefusesAHubProcedureThatTheSocketDoesNotRelay(t *testing.T) {
	hub := &rpcHub{}
	startRPCIPC(t, hub, nil)

	f := runControl(t, "rpc", "leapmux.v1.SystemService/GetTimeouts").requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "does not relay leapmux.v1.SystemService/GetTimeouts")
	require.NotEmpty(t, f.Hints)
	assert.Contains(t, f.Hints[0], "--hub <url>")
	assert.Empty(t, hub.callsOf(leapmuxv1connect.SystemServiceGetTimeoutsProcedure), "nothing reached the hub")
}

func TestRPCOverIPC_CallsAWorkerProcedure(t *testing.T) {
	hub := &rpcHub{answers: map[string]proto.Message{
		leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure: &leapmuxv1.GetWorkerResponse{Worker: &leapmuxv1.Worker{Id: "worker-A"}},
	}}
	disp := &rpcDispatcher{answer: &leapmuxv1.ListAgentsResponse{Agents: []*leapmuxv1.AgentInfo{{Id: "a-1", Title: "Fix the build"}}}}
	startRPCIPC(t, hub, disp)

	data := runControl(t, "rpc", "leapmux.v1.WorkerAgentService/ListAgents", `{"tab_ids": ["a-1"]}`, "--worker", "self").Data(t)
	assert.Equal(t, map[string]any{"agents": []any{map[string]any{"id": "a-1", "title": "Fix the build"}}}, data)
	calls := disp.recorded()
	require.Len(t, calls, 1)
	assert.Equal(t, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, calls[0].procedure)
	assert.Equal(t, marshaled(t, &leapmuxv1.ListAgentsRequest{TabIds: []string{"a-1"}}), calls[0].payload)
}

func TestRPCOverIPC_KeepsTheCodeOfARefusalOfTheWorker(t *testing.T) {
	hub := &rpcHub{}
	disp := &rpcDispatcher{refusal: codes.NotFound}
	startRPCIPC(t, hub, disp)

	f := runControl(t, "rpc", "WorkerAgentService/ListAgents", "--worker", "self").requireFailure(t, cli.CodeNotFound)
	assert.Contains(t, f.Message, "no such agent")
}

func TestRPCSendsNativeControlAnswerBytesOverBothTransports(t *testing.T) {
	for _, transport := range tabOpenTransports {
		t.Run(transport.name, func(t *testing.T) {
			worker := &rpcDispatcher{answer: &leapmuxv1.AnswerControlResponse{
				State: leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED,
			}}
			hub := &recordingHub{workers: workerRows("worker-A")}
			hubFlag := transport.start(t, hub, worker)
			payload := []byte(`{"id":"jsonrpc:9007199254740993","jsonrpc":"2.0","result":{}}`)
			input := `{"agent_id":"a-1","request_id":"jsonrpc:9007199254740993","claim_token":"claim-1","native_response":{"content":"` +
				base64.StdEncoding.EncodeToString(payload) + `"}}`
			args := []string{"rpc", "WorkerAgentService/AnswerControl", "-", "--worker", "worker-A", "--json"}
			out := runControlIn(t, input, append(args, testHubArgs(hubFlag)...)...)
			assert.Equal(t, "CONTROL_RESPONSE_STATE_COMPLETED", out.Data(t)["state"])
			calls := worker.recorded()
			require.Len(t, calls, 1)
			assert.Equal(t, leapmuxv1connect.WorkerAgentServiceAnswerControlProcedure, calls[0].procedure)
			var sent leapmuxv1.AnswerControlRequest
			require.NoError(t, proto.Unmarshal(calls[0].payload, &sent))
			assert.Equal(t, "a-1", sent.GetAgentId())
			assert.Equal(t, "jsonrpc:9007199254740993", sent.GetRequestId())
			assert.Equal(t, "claim-1", sent.GetClaimToken())
			assert.Nil(t, sent.GetDecision())
			assert.Equal(t, payload, sent.GetNativeResponse().GetContent())
		})
	}
}

func TestRPCOverHub_CallsAHubProcedureWithTheLoginOfTheUser(t *testing.T) {
	hub := &recordingHub{locateTab: &leapmuxv1.WorkspaceTab{
		Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "t-1"},
		WorkerId:    "worker-A",
		WorkspaceId: "ws-1",
	}}
	disp := &rpcDispatcher{}
	harness := startHubChannelHarness(t, hub, disp)

	o := runControlText(t, "rpc", "leapmux.v1.WorkspaceService/ListTabs", "--hub", harness.url)
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, listTabsText, o.Stdout)
	assert.Contains(t, hub.called(), leapmuxv1connect.WorkspaceServiceListTabsProcedure)
	assert.Empty(t, disp.recorded(), "a hub procedure reaches no Worker")
}

// A login of the user reaches every hub procedure, so the CLI sends one that
// the socket of a tab does not relay. This hub does not serve it.
func TestRPCOverHub_SendsAProcedureThatTheSocketDoesNotRelay(t *testing.T) {
	harness := startHubChannelHarness(t, &recordingHub{}, &rpcDispatcher{})

	f := runControl(t, "rpc", "leapmux.v1.SystemService/GetTimeouts", "--hub", harness.url).requireFailure(t, cli.CodeVersionMismatch)
	assert.NotContains(t, f.Message, "does not relay")
}

func TestRPCOverHub_CallsAWorkerProcedureOverTheEncryptedChannel(t *testing.T) {
	disp := &rpcDispatcher{answer: &leapmuxv1.ListAgentsResponse{}}
	harness := startHubChannelHarness(t, &recordingHub{workers: workerRows("worker-A")}, disp)

	o := runControlText(t, "rpc", "WorkerAgentService/ListAgents", `{"tab_ids": ["a-1", "a-2"]}`, "--worker", "worker-A", "--hub", harness.url)
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, "{}\n", o.Stdout)
	calls := disp.recorded()
	require.Len(t, calls, 1)
	assert.Equal(t, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, calls[0].procedure)
	assert.Equal(t, marshaled(t, &leapmuxv1.ListAgentsRequest{TabIds: []string{"a-1", "a-2"}}), calls[0].payload)
	assert.Equal(t, 1, harness.channelOpens())
}

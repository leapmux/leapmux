package cmd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// A retry of `agent open --request-id` sends the request of the first run
// again: the same tab id, and the same input id for its first message. The
// Worker answers a repeated creation of one tab id with the agent that the
// first creation made, and it queues a repeated input once, so the retry
// opens no second agent. A run without a request id opens a new one.
func TestAgentOpenOverIPC_ARetryWithTheRequestIDRepeatsTheFirstRequest(t *testing.T) {
	for _, transport := range tabOpenTransports {
		t.Run(transport.name, func(t *testing.T) {
			state := newStateBuilder().workspace("ws-1", "root-1").leafNode("root-1", "", "m").st
			hub, dispatcher := &recordingHub{workers: workerRows("worker-A"), materialized: state}, &creationPublicationDispatcher{}
			hubFlag := transport.start(t, hub, dispatcher)
			open := func(extra ...string) map[string]any {
				return runControl(t, append([]string{
					"agent", "open", "--hub", hubFlag, "--provider", "claude", "--workspace", "ws-1", "--tile", "root-1",
					"--worker", "worker-A", "--dir", "/src/app", "--message", "Review the diff",
				}, extra...)...).Data(t)
			}

			first := open("--request-id", "review-1")
			again := open("--request-id", "review-1")
			wantTab := requestScopedID("review-1", requestScopeAgentOpen, "worker-A")
			assert.Equal(t, wantTab, first["id"])
			assert.Equal(t, first, again, "the retry prints the agent of the first run")

			calls := dispatcher.recorded()
			require.Len(t, calls, 4)
			firstOpen, retriedOpen := calls[0].in.(*leapmuxv1.OpenAgentRequest), calls[2].in.(*leapmuxv1.OpenAgentRequest)
			assert.Equal(t, wantTab, firstOpen.GetCreation().GetTabId())
			assert.True(t, proto.Equal(firstOpen, retriedOpen), "the retry repeats the creation exactly:\nfirst %v\nretry %v", firstOpen, retriedOpen)
			firstMessage, retriedMessage := calls[1].in.(*leapmuxv1.EnqueueAgentInputRequest), calls[3].in.(*leapmuxv1.EnqueueAgentInputRequest)
			assert.Equal(t, requestScopedID("review-1", requestScopeAgentOpenMessage, "worker-A"), firstMessage.GetInputId())
			assert.Equal(t, wantTab, firstMessage.GetAgentId())
			assert.True(t, proto.Equal(firstMessage, retriedMessage), "the retry repeats the first message exactly")
			assert.NotEqual(t, firstMessage.GetInputId(), firstOpen.GetCreation().GetTabId(), "the tab and its first message take ids of their own")

			other := open("--request-id", "review-2")
			plain := open()
			again = open()
			distinct := map[any]bool{first["id"]: true, other["id"]: true, plain["id"]: true, again["id"]: true}
			assert.Len(t, distinct, 4, "another request id, or none, opens another agent")
		})
	}
}

// conflictingOpenDispatcher refuses every OpenAgent as the Worker refuses a
// tab id that an earlier creation with other arguments took, and serves every
// other call as creationPublicationDispatcher does.
type conflictingOpenDispatcher struct {
	*creationPublicationDispatcher
}

func (d conflictingOpenDispatcher) DispatchWith(ctx context.Context, caller channel.Caller, r *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	if r.GetMethod() == leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure {
		_ = w.SendError(int32(codes.AlreadyExists), "tab creation conflicts with an earlier creation")
		return
	}
	d.creationPublicationDispatcher.DispatchWith(ctx, caller, r, w)
}

// A conflict of `agent open --request-id` states that the request id may
// belong to a request with other arguments. A conflict without a request id
// states no such cause.
func TestAgentOpenOverIPC_AConflictStatesTheCauseThatTheRequestIDGives(t *testing.T) {
	state := newStateBuilder().workspace("ws-1", "root-1").leafNode("root-1", "", "m").st
	startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A"), materialized: state}, conflictingOpenDispatcher{&creationPublicationDispatcher{}})
	open := func(extra ...string) cliFailure {
		return runControl(t, append([]string{
			"agent", "open", "--provider", "claude", "--workspace", "ws-1", "--tile", "root-1",
			"--worker", "worker-A", "--dir", "/src/app",
		}, extra...)...).requireFailure(t, cli.CodeConflict)
	}

	f := open("--request-id", "review-1")
	assert.Contains(t, f.Message, "conflicts with an earlier creation")
	require.NotEmpty(t, f.Hints)
	assert.Contains(t, f.Hints[len(f.Hints)-1], `--request-id "review-1"`)

	for _, hint := range open().Hints {
		assert.NotContains(t, hint, "--request-id", "without a request id the conflict has another cause")
	}
}

package service

import (
	"context"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type watchUpdatingBindWriter struct {
	*testResponseWriter
	update []byte
}

func (writer *watchUpdatingBindWriter) BindStream(controller channel.StreamController) (func(), bool) {
	controller.OnClientFrame(writer.update)
	return writer.testResponseWriter.BindStream(controller)
}

func TestWatchStartupDoesNotReplaceANewerRevision(t *testing.T) {
	svc, dispatcher, writer := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	update, err := proto.Marshal(&leapmuxv1.WatchEventsRequest{UpdateId: 2, Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}})
	require.NoError(t, err)
	initial, err := proto.Marshal(&leapmuxv1.WatchEventsRequest{UpdateId: 1, Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY}}})
	require.NoError(t, err)
	updating := &watchUpdatingBindWriter{testResponseWriter: writer, update: update}
	dispatcher.DispatchWith(context.Background(), channel.LocalAgentCaller(userid.MustNew("user-1")), &leapmuxv1.InnerRpcRequest{Method: "WatchEvents", Payload: initial}, updating)
	require.Eventually(t, func() bool {
		ack := lastWatchUpdateAck(t, writer)
		return ack != nil && ack.UpdateId == 2
	}, time.Second, time.Millisecond)
	require.Equal(t, leapmuxv1.WatchMode_WATCH_MODE_FULL, svc.Watchers.AgentModesForChannel(writer.channelID)["agent-1"])
}

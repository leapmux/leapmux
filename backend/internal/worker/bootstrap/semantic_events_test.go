package bootstrap

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func TestWiringFinalizesTheEventJournalAfterAdmittedHandlersJoin(t *testing.T) {
	const owner = "semantic-owner"
	w, _ := wireForTestWith(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, func(p *Params) {
		p.SeedRegisteredBy = owner
	})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	const agentID = "late-publication"
	require.NoError(t, w.Service.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: agentID, WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	w.dispatcher.Handle("finish-admitted-control-answer", channel.Registration{},
		func(context.Context, channel.Caller, *leapmuxv1.InnerRpcRequest, channel.ResponseWriter) {
			close(entered)
			<-release
			w.Service.Watchers.BroadcastAgentEvent(agentID, &leapmuxv1.AgentEvent{
				AgentId: agentID,
				Event: &leapmuxv1.AgentEvent_ControlCancel{ControlCancel: &leapmuxv1.AgentControlCancelRequest{
					RequestId: "finished answer", ClaimToken: "instance",
					ResponseState: leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED,
				}},
			})
		})
	w.dispatcher.DispatchAsync(ctx, channel.LocalAgentCaller(userid.MustNew(owner)),
		&leapmuxv1.InnerRpcRequest{Method: "finish-admitted-control-answer"}, &wiringStubWriter{})
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the plain handler did not enter")
	}
	w.Shutdown()
	unblock()
	w.WaitHandlers()

	journal, err := w.Service.Queries.GetSemanticEventJournal(ctx)
	require.NoError(t, err)
	assert.Zero(t, journal.Dirty)
	records, err := w.Service.Queries.ListSemanticEventEntriesAfter(ctx, db.ListSemanticEventEntriesAfterParams{
		TabType: int64(leapmuxv1.TabType_TAB_TYPE_AGENT), TabID: agentID, PageSize: 10,
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	var fact leapmuxv1.SemanticTabFact
	require.NoError(t, proto.Unmarshal(records[0].Fact, &fact))
	assert.Equal(t, "finished answer", fact.GetAgentRequestEnded().GetRequestId())
}

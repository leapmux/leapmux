package dirac

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

type diracCloseSink struct {
	*agenttest.Sink
	closed    chan bgtask.Status
	closeGate <-chan struct{}
}

func (s *diracCloseSink) CloseBackgroundTask(rowKey string, status bgtask.Status) error {
	if err := s.Sink.CloseBackgroundTask(rowKey, status); err != nil {
		return err
	}
	s.closed <- status
	if s.closeGate != nil {
		<-s.closeGate
	}
	return nil
}

func newDiracSubagentAgentForRPC(t *testing.T, root string) (*Agent, *diracCloseSink, *quartz.Mock) {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	sink := &diracCloseSink{Sink: &agenttest.Sink{}, closed: make(chan bgtask.Status, 16)}
	a, _ := acptest.NewAgentForRPCWithResponder(t,
		func() *Agent { return &Agent{root: root} },
		func(a *Agent) *acp.Base { return &a.Base },
		func(method string) agenttest.RPCReply {
			if method == acp.MethodSessionNew {
				return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2"}`)}
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		},
	)
	a.SetSinkForTest(agent.NewProviderServices(sink))
	*a.HooksForTest() = a.configure(clock)
	return a, sink, clock
}

func waitDiracChildClose(t *testing.T, ctx context.Context, sink *diracCloseSink) bgtask.Status {
	t.Helper()
	select {
	case status := <-sink.closed:
		return status
	case <-ctx.Done():
		t.Fatal("the Dirac child row did not close")
		return bgtask.StatusUnspecified
	}
}

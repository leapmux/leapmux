package muse

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStopAndWaitJoinLogGapEnrichment(t *testing.T) {
	for _, stop := range []bool{false, true} {
		name := "wait"
		if stop {
			name = "stop"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				a, base := testAgent(t)
				done := make(chan struct{})
				ctx, cancel := context.WithCancel(t.Context())
				t.Cleanup(cancel)
				a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "muse-gap", ProviderName: "Muse Code", Ctx: ctx, Cancel: cancel, Stdin: &agenttest.Stdin{}, ProcessDone: done})
				base.UpdateSessionID("session")
				original := a.sink
				failing := &museEnrichmentFailureSink{ProviderServices: original}
				a.sink = agent.NewOrderedProviderServices(agent.OutputQueueFor(original), failing)
				state := a.sessions["session"]
				state.sink = a.sink
				state.log.subscriptionID = 1
				origin := logRecord("origin", "assistant_tool_calls_committed", "message_id", 1, "\"tool_calls\":[{\"call_id\":\"call\",\"name\":\"bash\",\"args\":\"{\\\"command\\\":\\\"exit 7\\\"}\"}]")
				result := logRecord("result", "tool_result_batch_committed", "batch_id", 2, "\"results\":[{\"tool_call_index\":0,\"tool_call_id\":\"call\",\"text\":\"{\\\"exit_code\\\":7}\"}]")
				feed(t, a, contracts.MuseMethodLogRecord, map[string]any{"subscriptionId": 1, "record": origin})
				completedNativeTool(t, a)
				entered, release := make(chan struct{}), make(chan struct{})
				var count atomic.Int64
				failing.onEnrich = func(change agent.MessageEnrichment) (bool, error) {
					count.Add(1)
					close(entered)
					<-release
					return original.EnrichMessage(change)
				}
				a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
					require.Equal(t, methodLogPage, method)
					raw, err := json.Marshal(map[string]any{"records": []json.RawMessage{result}, "nextCursor": map[string]any{"stream": nativeStream{Kind: "session", ID: "session"}, "after": nativePosition{ID: "result", Sequence: 2}}})
					require.NoError(t, err)
					return agenttest.RPCReply{Result: raw}
				}})
				feed(t, a, methodLogGap, map[string]any{"subscriptionId": 1, "after": nativePosition{ID: "origin", Sequence: 1}, "next": nativePosition{ID: "result", Sequence: 2}})
				<-entered
				close(done)
				returned := make(chan struct{})
				go func() {
					defer close(returned)
					if stop {
						a.Stop()
					} else {
						assert.NoError(t, a.Wait())
					}
				}()
				synctest.Wait()
				select {
				case <-returned:
					t.Error("lifecycle completion returned while an admitted log-gap enrichment still ran")
				default:
				}
				close(release)
				<-returned
				synctest.Wait()
				assert.Equal(t, int64(1), count.Load())
			})
		})
	}
}

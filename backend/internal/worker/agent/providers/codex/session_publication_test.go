package codex

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type codexInputProcessStateServices struct {
	agent.ProviderServices
	publications atomic.Int32
	onPublish    func()
}

func (services *codexInputProcessStateServices) PublishInputProcessState() {
	services.publications.Add(1)
	if services.onPublish != nil {
		services.onPublish()
	}
	services.ProviderServices.PublishInputProcessState()
}

func TestCodexAuthenticationPublishesProcessStateOnceBeforeRouting(t *testing.T) {
	for _, scenario := range []string{"unrouted child", "retired main", "retired child", "ordinary failure", "stopped process", "exited process"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			a, recorded, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			})
			a.threadID = "main-thread"
			a.inputAuthenticationChecked = true
			feedCodexLines(a, `{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"current-turn"}}}`)
			services := &codexInputProcessStateServices{ProviderServices: a.sink}
			services.onPublish = func() {
				stateUnlocked := a.Mu.TryLock()
				assert.True(t, stateUnlocked, "process-state publication reads provider state without its mutex")
				if stateUnlocked {
					a.Mu.Unlock()
				}
				retryUnlocked := a.retryOfferMu.TryLock()
				assert.True(t, retryUnlocked, "process-state publication follows retry-offer withdrawal")
				if retryUnlocked {
					a.retryOfferMu.Unlock()
				}
			}
			a.sink = services
			threadID := "unrouted-child"
			if scenario == "retired main" || scenario == "retired child" {
				threadID = scenario
				a.retiredCodexThreads = map[string]struct{}{threadID: {}}
			}
			ended := scenario == "stopped process" || scenario == "exited process"
			if ended {
				a.SimulateExitForTest()
			}
			if scenario == "stopped process" {
				a.Stop()
			}
			info := "unauthorized"
			if scenario == "ordinary failure" {
				info = "serverOverloaded"
			}
			completion := fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":%q,"turn":{"id":"old-turn","status":"failed","error":{"message":"request failed","codexErrorInfo":%q}}}}`, threadID, info)
			feedCodexLines(a, completion)
			feedCodexLines(a, completion)
			var expected int32
			if !ended && scenario != "ordinary failure" {
				expected = 1
				require.True(t, recoveryRequired(a))
				assert.Equal(t, "current-turn", a.mainTurn.id, "process health publication cannot end its parent's turn")
			}
			assert.Equal(t, expected, services.publications.Load(), "one health transition publishes once even when output has no route")
			assert.EqualValues(t, expected, recorded.InputProcessStatePublishes())
			assert.Empty(t, requests(), "publishing process health performs no native request")
		})
	}
}

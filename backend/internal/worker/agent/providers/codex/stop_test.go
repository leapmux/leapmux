package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type codexCommandTestClient struct {
	list      func(string, *string, int) (json.RawMessage, error)
	terminate func(string, string) (json.RawMessage, error)
}

func (client codexCommandTestClient) ListCommandProcesses(threadID string, cursor *string, limit int) (json.RawMessage, error) {
	if client.list == nil {
		return nil, fmt.Errorf("unexpected list request")
	}
	return client.list(threadID, cursor, limit)
}

func (client codexCommandTestClient) TerminateCommandProcess(threadID, processID string) (json.RawMessage, error) {
	if client.terminate == nil {
		return nil, fmt.Errorf("unexpected termination request")
	}
	return client.terminate(threadID, processID)
}

type codexStopWriter func([]byte) (int, error)

func TestCodexCommandStopCombinesRefusalAndRemainingProcess(t *testing.T) {
	t.Parallel()
	listCalls := 0
	err := stopCodexCommandProcesses("thread", []codexCommandProcess{{ItemID: "a", ProcessID: "1"}, {ItemID: "b", ProcessID: "2"}}, codexCommandTestClient{
		list: func(threadID string, cursor *string, limit int) (json.RawMessage, error) {
			listCalls++
			return json.RawMessage(`{"data":[{"itemId":"b","processId":"2"}]}`), nil
		},
		terminate: func(threadID, processID string) (json.RawMessage, error) {
			if processID == "1" {
				return nil, fmt.Errorf("termination refused for 1")
			}
			return json.RawMessage(`{"terminated":false}`), nil
		},
	})
	require.ErrorContains(t, err, "termination refused for 1")
	require.ErrorContains(t, err, `native command process "2" remains active`)
	assert.Equal(t, 1, listCalls)
}

func (write codexStopWriter) Write(raw []byte) (int, error) { return write(raw) }

type capturedCodexStopRequest struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func TestCodexInterruptChecksExitedCommandsInOneFinalScan(t *testing.T) {
	t.Parallel()
	for _, remaining := range []bool{false, true} {
		t.Run(fmt.Sprintf("remaining=%t", remaining), func(t *testing.T) {
			t.Parallel()
			var listCalls, terminateCalls atomic.Int32
			a, _, _ := newCommandStopAgent(t, func(request capturedCodexStopRequest) agenttest.RPCReply {
				switch request.Method {
				case codexListCommandProcessesMethod:
					listCalls.Add(1)
					var params struct {
						Cursor string `json:"cursor"`
					}
					require.NoError(t, json.Unmarshal(request.Params, &params))
					if terminateCalls.Load() == 0 {
						if params.Cursor == "" {
							return agenttest.RPCReply{Result: json.RawMessage(`{"data":[{"itemId":"a","processId":"41"},{"itemId":"b","processId":"42"}],"nextCursor":"tail"}`)}
						}
						return agenttest.RPCReply{Result: json.RawMessage(`{"data":[{"itemId":"c","processId":"43"}]}`)}
					}
					assert.Equal(t, int32(3), terminateCalls.Load(), "the final scan follows every termination response")
					if params.Cursor == "" {
						return agenttest.RPCReply{Result: json.RawMessage(`{"data":[{"itemId":"unrelated","processId":"99"}],"nextCursor":"tail"}`)}
					}
					if remaining {
						return agenttest.RPCReply{Result: json.RawMessage(`{"data":[{"itemId":"a","processId":"41"}]}`)}
					}
					return agenttest.RPCReply{Result: json.RawMessage(`{"data":[]}`)}
				case codexTerminateCommandProcessMethod:
					terminateCalls.Add(1)
					return agenttest.RPCReply{Result: json.RawMessage(`{"terminated":false}`)}
				default:
					return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
				}
			})
			for _, item := range []string{"a", "b", "c"} {
				rememberCommandForStop(t, a, "root-thread", "current-turn", item, contracts.CodexItemTypeCommandExecution)
			}
			err := a.Interrupt(agent.StopContext{})
			if remaining {
				require.ErrorContains(t, err, `native command process "41" remains active`)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, int32(3), terminateCalls.Load())
			assert.Equal(t, int32(4), listCalls.Load(), "read two initial pages and two final pages")
		})
	}
}

func newCommandStopAgent(t *testing.T, reply func(capturedCodexStopRequest) agenttest.RPCReply) (*Agent, *agenttest.ControlSink, func() []capturedCodexStopRequest) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	var a *Agent
	var mu sync.Mutex
	var requests []capturedCodexStopRequest
	writer := codexStopWriter(func(raw []byte) (int, error) {
		var request capturedCodexStopRequest
		if err := json.Unmarshal(raw, &request); err != nil {
			return 0, err
		}
		if request.Method == "" {
			return len(raw), nil
		}
		mu.Lock()
		requests = append(requests, request)
		mu.Unlock()
		if !a.Deliver(request.ID, agenttest.JSONRPCResponse(request.ID, reply(request))) {
			return 0, fmt.Errorf("no pending native request %d", request.ID)
		}
		return len(raw), nil
	})
	sink := &agenttest.ControlSink{}
	a = &Agent{
		JSONRPCProcess: providerkit.JSONRPCProcess{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "command-stop", ProviderName: "codex", Ctx: ctx, Cancel: cancel,
			Stdin: agenttest.NopStdin(writer), APITimeout: 30 * time.Second,
		})},
		sink: agent.NewProviderServices(sink), threadID: "root-thread", mainTurn: codexMainTurn{id: "current-turn"},
	}
	return a, sink, func() []capturedCodexStopRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]capturedCodexStopRequest(nil), requests...)
	}
}

func rememberCommandForStop(t *testing.T, a *Agent, threadID, turnID, itemID, itemType string) {
	t.Helper()
	params, err := json.Marshal(map[string]any{
		"threadId": threadID, "turnId": turnID,
		"item": map[string]string{"id": itemID, "type": itemType},
	})
	require.NoError(t, err)
	childID := threadID
	if threadID == a.threadID {
		childID = ""
	}
	a.rememberCodexIncompleteTool(itemID, itemType, childID, params)
}

func TestCodexInterruptStopsOnlyCommandsFromItsTurn(t *testing.T) {
	t.Parallel()
	a, _, captured := newCommandStopAgent(t, func(request capturedCodexStopRequest) agenttest.RPCReply {
		switch request.Method {
		case "thread/backgroundTerminals/list":
			return agenttest.RPCReply{Result: json.RawMessage(`{"data":[{"itemId":"current-command","processId":"41"},{"itemId":"older-command","processId":"42"},{"itemId":"child-command","processId":"43"}],"nextCursor":null}`)}
		case "thread/backgroundTerminals/terminate":
			return agenttest.RPCReply{Result: json.RawMessage(`{"terminated":true}`)}
		default:
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
	})
	rememberCommandForStop(t, a, "root-thread", "current-turn", "current-command", contracts.CodexItemTypeCommandExecution)
	rememberCommandForStop(t, a, "root-thread", "older-turn", "older-command", contracts.CodexItemTypeCommandExecution)
	rememberCommandForStop(t, a, "child-thread", "child-turn", "child-command", contracts.CodexItemTypeCommandExecution)
	rememberCommandForStop(t, a, "root-thread", "current-turn", "file-change", contracts.CodexItemTypeFileChange)

	require.NoError(t, a.Interrupt(agent.StopContext{}))
	requests := captured()
	require.Len(t, requests, 3, "an interrupted model turn must also stop its native command process")
	assert.Equal(t, "turn/interrupt", requests[0].Method)
	assert.Equal(t, "thread/backgroundTerminals/list", requests[1].Method)
	assert.Equal(t, "thread/backgroundTerminals/terminate", requests[2].Method)
	assert.JSONEq(t, `{"threadId":"root-thread","processId":"41"}`, string(requests[2].Params))
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	assert.Len(t, captured(), 3, "the same stop must not repeat native requests")
}

func TestCodexCommandListValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, response, failure string
	}{
		{"empty", `{"data":[]}`, ""},
		{"zero", `{"data":[{"itemId":"a","processId":"0"}]}`, ""},
		{"negative", `{"data":[{"itemId":"a","processId":"-1"}]}`, ""},
		{"missing data", `{}`, "omits its data"},
		{"null data", `{"data":null}`, "omits its data"},
		{"invalid JSON", `{`, "read native command list"},
		{"missing owner", `{"data":[{"processId":"1"}]}`, "invalid identity"},
		{"missing process", `{"data":[{"itemId":"a"}]}`, "invalid identity"},
		{"small process", `{"data":[{"itemId":"a","processId":"-2147483649"}]}`, "invalid identity"},
		{"large process", `{"data":[{"itemId":"a","processId":"2147483648"}]}`, "invalid identity"},
		{"noncanonical process", `{"data":[{"itemId":"a","processId":"01"}]}`, "invalid identity"},
		{"duplicate", `{"data":[{"itemId":"a","processId":"1"},{"itemId":"a","processId":"1"}]}`, ""},
		{"conflicting owner", `{"data":[{"itemId":"a","processId":"1"},{"itemId":"b","processId":"1"}]}`, "conflicting owners"},
		{"empty continuation", `{"data":[],"nextCursor":"next"}`, "invalid continuation"},
		{"blank cursor", `{"data":[{"itemId":"a","processId":"1"}],"nextCursor":""}`, "invalid continuation"},
		{"cycle", `{"data":[{"itemId":"a","processId":"1"}],"nextCursor":"next"}`, "repeats a cursor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := readCodexCommandProcesses("thread", codexCommandTestClient{list: func(threadID string, cursor *string, limit int) (json.RawMessage, error) {
				assert.Equal(t, "thread", threadID)
				return json.RawMessage(tc.response), nil
			}})
			if tc.failure == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.failure)
			}
		})
	}
}

func TestCodexCommandTerminationValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, response, list, failure string
	}{
		{"terminated", `{"terminated":true}`, "", ""},
		{"already exited", `{"terminated":false}`, `{"data":[]}`, ""},
		{"still active", `{"terminated":false}`, `{"data":[{"itemId":"a","processId":"1"}]}`, "remains active"},
		{"missing result", `{}`, "", "omits its result"},
		{"null result", `{"terminated":null}`, "", "omits its result"},
		{"invalid result", `{"terminated":"true"}`, "", "read native command termination"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var methods []string
			err := stopCodexCommandProcesses("thread", []codexCommandProcess{{ItemID: "a", ProcessID: "1"}}, codexCommandTestClient{
				list: func(threadID string, cursor *string, limit int) (json.RawMessage, error) {
					methods = append(methods, codexListCommandProcessesMethod)
					return json.RawMessage(tc.list), nil
				},
				terminate: func(threadID, processID string) (json.RawMessage, error) {
					methods = append(methods, codexTerminateCommandProcessMethod)
					return json.RawMessage(tc.response), nil
				},
			})
			if tc.failure == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.failure)
			}
			assert.Equal(t, codexTerminateCommandProcessMethod, methods[0])
			if tc.list != "" {
				assert.Equal(t, []string{codexTerminateCommandProcessMethod, codexListCommandProcessesMethod}, methods)
			} else {
				assert.Len(t, methods, 1)
			}
		})
	}
}

func TestCodexCommandStopCollectsPagesBeforeTermination(t *testing.T) {
	t.Parallel()
	var a *Agent
	var pages int
	a, _, _ = newCommandStopAgent(t, func(request capturedCodexStopRequest) agenttest.RPCReply {
		switch request.Method {
		case "turn/interrupt":
			// Native completion can clear these records before the reply arrives.
			a.Mu.Lock()
			clear(a.incompleteTools)
			a.Mu.Unlock()
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		case codexListCommandProcessesMethod:
			pages++
			if pages == 1 {
				return agenttest.RPCReply{Result: json.RawMessage(`{"data":[{"itemId":"a","processId":"1"}],"nextCursor":"second"}`)}
			}
			assert.JSONEq(t, `{"threadId":"root-thread","cursor":"second","limit":100}`, string(request.Params))
			return agenttest.RPCReply{Result: json.RawMessage(`{"data":[{"itemId":"a","processId":"2"}]}`)}
		default:
			assert.Equal(t, 2, pages, "collect every page before changing the list")
			return agenttest.RPCReply{Result: json.RawMessage(`{"terminated":true}`)}
		}
	})
	rememberCommandForStop(t, a, "root-thread", "current-turn", "a", contracts.CodexItemTypeCommandExecution)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
}

func TestCodexInterruptWithdrawsApprovalsAfterAcceptedStopWithCleanupFailure(t *testing.T) {
	t.Parallel()
	a, sink, _ := newCommandStopAgent(t, func(request capturedCodexStopRequest) agenttest.RPCReply {
		if request.Method == codexListCommandProcessesMethod {
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	rememberCommandForStop(t, a, "root-thread", "current-turn", "a", contracts.CodexItemTypeCommandExecution)
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","id":7,"method":"item/commandExecution/requestApproval","params":{}}`))
	request := sink.LastPublishedControl()
	require.NotEmpty(t, request.RequestID)
	require.ErrorContains(t, a.Interrupt(agent.StopContext{}), "omits its data")
	assert.Contains(t, sink.CanceledControls(), request.RequestID)
	assert.False(t, a.OutstandingControlForTest(request.RequestID))
}

func TestCodexCommandStopUsesOnlyTheChildTurn(t *testing.T) {
	t.Parallel()
	a, _, captured := newCommandStopAgent(t, func(request capturedCodexStopRequest) agenttest.RPCReply {
		if request.Method == codexListCommandProcessesMethod {
			assert.JSONEq(t, `{"threadId":"child-thread","limit":100}`, string(request.Params))
			return agenttest.RPCReply{Result: json.RawMessage(`{"data":[{"itemId":"child-command","processId":"7"},{"itemId":"parent-command","processId":"8"}]}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{"terminated":true}`)}
	})
	a.collabChildren = map[string]*codexChildState{"child-thread": {spawnCorrelationID: "spawn", turnID: "child-turn"}}
	rememberCommandForStop(t, a, "root-thread", "current-turn", "parent-command", contracts.CodexItemTypeCommandExecution)
	rememberCommandForStop(t, a, "child-thread", "child-turn", "child-command", contracts.CodexItemTypeCommandExecution)
	require.NoError(t, a.InterruptChild("child-thread", agent.StopContext{}))
	requests := captured()
	require.Len(t, requests, 3)
	assert.JSONEq(t, `{"threadId":"child-thread","processId":"7"}`, string(requests[2].Params))
}

func TestCodexCommandStopLeavesCommandsAloneAfterRefusedInterrupt(t *testing.T) {
	t.Parallel()
	a, _, captured := newCommandStopAgent(t, func(request capturedCodexStopRequest) agenttest.RPCReply {
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"the turn cannot stop"}`)}
	})
	rememberCommandForStop(t, a, "root-thread", "current-turn", "command", contracts.CodexItemTypeCommandExecution)
	require.ErrorContains(t, a.Interrupt(agent.StopContext{}), "the turn cannot stop")
	requests := captured()
	require.Len(t, requests, 1)
	assert.Equal(t, "turn/interrupt", requests[0].Method)
}

func TestCodexCommandListCapsAnEndlessCursorStream(t *testing.T) {
	t.Parallel()
	calls := 0
	_, err := readCodexCommandProcesses("thread", codexCommandTestClient{list: func(threadID string, cursor *string, limit int) (json.RawMessage, error) {
		calls++
		return json.RawMessage(fmt.Sprintf(`{"data":[{"itemId":"a","processId":"1"}],"nextCursor":"page-%d"}`, calls)), nil
	}})
	require.ErrorContains(t, err, "exceeds the page limit")
	assert.Equal(t, codexCommandPageLimit, calls)
}

func TestCodexCommandStopReportsEveryFailedTermination(t *testing.T) {
	t.Parallel()
	a, _, captured := newCommandStopAgent(t, func(request capturedCodexStopRequest) agenttest.RPCReply {
		if request.Method == codexListCommandProcessesMethod {
			return agenttest.RPCReply{Result: json.RawMessage(`{"data":[{"itemId":"command","processId":"1"},{"itemId":"command","processId":"2"}]}`)}
		}
		if request.Method == codexTerminateCommandProcessMethod {
			var params codexCommandProcess
			assert.NoError(t, json.Unmarshal(request.Params, &params))
			return agenttest.RPCReply{Error: json.RawMessage(fmt.Sprintf(`{"code":-32000,"message":"cannot stop process %s"}`, params.ProcessID))}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	rememberCommandForStop(t, a, "root-thread", "current-turn", "command", contracts.CodexItemTypeCommandExecution)
	err := a.Interrupt(agent.StopContext{})
	require.ErrorContains(t, err, "cannot stop process 1")
	require.ErrorContains(t, err, "cannot stop process 2")
	assert.Len(t, captured(), 4)
}

func TestCodexCommandStopIncludesACommandAnnouncedBeforeTheInterruptReply(t *testing.T) {
	t.Parallel()
	var a *Agent
	var captured func() []capturedCodexStopRequest
	a, _, captured = newCommandStopAgent(t, func(request capturedCodexStopRequest) agenttest.RPCReply {
		switch request.Method {
		case "turn/interrupt":
			// A native startup notification can arrive while the stop request waits for its reply.
			rememberCommandForStop(t, a, "root-thread", "current-turn", "late-command", contracts.CodexItemTypeCommandExecution)
			a.Mu.Lock()
			clear(a.incompleteTools)
			a.Mu.Unlock()
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		case codexListCommandProcessesMethod:
			return agenttest.RPCReply{Result: json.RawMessage(`{"data":[{"itemId":"late-command","processId":"9"}]}`)}
		default:
			return agenttest.RPCReply{Result: json.RawMessage(`{"terminated":true}`)}
		}
	})
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	requests := captured()
	require.Len(t, requests, 3)
	assert.Equal(t, codexTerminateCommandProcessMethod, requests[2].Method)
	assert.JSONEq(t, `{"threadId":"root-thread","processId":"9"}`, string(requests[2].Params))
}

func TestCodexCommandListRetainsSignedProcessIDs(t *testing.T) {
	t.Parallel()
	processes, err := readCodexCommandProcesses("thread", codexCommandTestClient{list: func(threadID string, cursor *string, limit int) (json.RawMessage, error) {
		return json.RawMessage(`{"data":[{"itemId":"zero","processId":"0"},{"itemId":"lowest","processId":"-2147483648"},{"itemId":"highest","processId":"2147483647"},{"itemId":"zero","processId":"0"}]}`), nil
	}})
	require.NoError(t, err)
	assert.Equal(t, map[string]codexCommandProcess{
		"0":           {ItemID: "zero", ProcessID: "0"},
		"-2147483648": {ItemID: "lowest", ProcessID: "-2147483648"},
		"2147483647":  {ItemID: "highest", ProcessID: "2147483647"},
	}, processes)
}

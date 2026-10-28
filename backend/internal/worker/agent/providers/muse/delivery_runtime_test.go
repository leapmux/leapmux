package muse

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func requireFailedParentInputProjection(t *testing.T, a *Agent, root *agenttest.Sink, childID string, scenario nativeParentInputCase) {
	t.Helper()
	state := a.sessions[scenario.ChildSessionID]
	require.NotNil(t, state.projection)
	require.Error(t, state.projection.failed)
	assert.Equal(t, childViewPrefixFailed, state.childViewPrefix)
	require.NotNil(t, state.acceptedViewCursor)
	assert.Equal(t, "accepted-prefix", *state.acceptedViewCursor)
	rows := root.Child(childID).Messages()
	require.Len(t, rows, 1, "incomplete proof admits neither the parent input nor its child answer")
	require.Len(t, root.Messages(), 1)
	assert.Equal(t, []byte(scenario.Answer), root.Messages()[0].Content)
	assert.Equal(t, scenario.ChildSessionID, root.Messages()[0].AgentSessionID)
	var supplemental map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(root.Messages()[0].SupplementalContent, &supplemental))
	assert.Contains(t, supplemental, contracts.MuseSupplementFieldViewProjectionUnavailable)
	require.Len(t, root.LeapMuxNotifications(), 1)
}

func TestNativeParentInputProofFailureWithholdsTheAnswer(t *testing.T) {
	for _, scenario := range nativeParentInputCases(t) {
		for _, failure := range []string{"missing capability", "denied stream", "duplicate stream", "foreign stream", "parent sequence gap", "parent cursor mismatch", "child sequence gap"} {
			t.Run(scenario.Name+"/"+failure, func(t *testing.T) {
				a, root, childID, _ := nativeParentInputRuntime(t, scenario)
				if failure == "missing capability" {
					a.handshake.GrantedCapabilities = nil
				}
				if failure == "parent sequence gap" || failure == "parent cursor mismatch" {
					a.sessions[scenario.ParentSessionID].log = newNativeLog(scenario.ParentSessionID)
				}
				if failure == "child sequence gap" {
					a.sessions[scenario.ChildSessionID].log = newNativeLog(scenario.ChildSessionID)
				}
				a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
					var params struct{ SessionID string }
					require.NoError(t, json.Unmarshal(raw, &params))
					records := scenario.ParentRecords
					if params.SessionID == scenario.ChildSessionID {
						records = scenario.ChildRecords
					} else {
						require.Equal(t, scenario.ParentSessionID, params.SessionID)
					}
					last, err := decodeNativeRecord(records[len(records)-1], params.SessionID)
					require.NoError(t, err)
					tail := nativePosition{ID: last.ID, Sequence: last.Sequence}
					var result any
					switch method {
					case methodLogStreams:
						if failure == "denied stream" {
							return agenttest.RPCReply{Error: json.RawMessage("{\"code\":-32000,\"message\":\"native proof denied\"}")}
						}
						stream := nativeStream{Kind: "session", ID: params.SessionID}
						if failure == "foreign stream" {
							stream.ID = "foreign-session"
						}
						streams := []any{map[string]any{"stream": stream, "tail": tail}}
						if failure == "duplicate stream" {
							streams = append(streams, streams[0])
						}
						result = map[string]any{"streams": streams}
					case methodLogPage:
						page := slices.Clone(records)
						if failure == "parent sequence gap" || failure == "child sequence gap" {
							require.Greater(t, len(page), 2)
							page = append(page[:1], page[2:]...)
						}
						if failure == "parent cursor mismatch" {
							tail.ID = "different-last-record"
						}
						result = map[string]any{"records": page, "nextCursor": map[string]any{"stream": nativeStream{Kind: "session", ID: params.SessionID}, "after": tail}}
					default:
						t.Errorf("unexpected native proof method %s", method)
						return agenttest.RPCReply{Error: json.RawMessage("{\"code\":-32000,\"message\":\"unexpected proof method\"}")}
					}
					encoded, err := json.Marshal(result)
					require.NoError(t, err)
					return agenttest.RPCReply{Result: encoded}
				}})
				a.HandleOutput(scenario.Answer)
				a.viewWork.CloseAndWait()
				requireFailedParentInputProjection(t, a, root, childID, scenario)
			})
		}
	}
}

type parentInputRefusalWriter struct {
	agent.ProviderServices
	attempts int
}

func (writer *parentInputRefusalWriter) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	if source == leapmuxv1.MessageSource_MESSAGE_SOURCE_USER {
		writer.attempts++
		return assert.AnError
	}
	return writer.ProviderServices.PersistMessage(source, content, span)
}

func TestNativeParentInputHardRefusalCannotReleaseItsAnswer(t *testing.T) {
	for _, scenario := range nativeParentInputCases(t) {
		t.Run(scenario.Name, func(t *testing.T) {
			a, root, childID, _ := nativeParentInputRuntime(t, scenario)
			state := a.sessions[scenario.ChildSessionID]
			refused := &parentInputRefusalWriter{ProviderServices: state.sink}
			state.sink = refused
			a.HandleOutput(scenario.Answer)
			a.viewWork.CloseAndWait()
			requireFailedParentInputProjection(t, a, root, childID, scenario)
			assert.Equal(t, 1, refused.attempts)
			assert.ErrorIs(t, state.projection.failed, assert.AnError)
			require.Less(t, state.projection.classifiedThrough.Sequence, state.log.contiguousThrough.Sequence)
		})
	}
}

func TestNativeParentInputRetainedWriteKeepsItsAnswerBehindTheInput(t *testing.T) {
	for _, scenario := range nativeParentInputCases(t) {
		t.Run(scenario.Name, func(t *testing.T) {
			a, root, childID, original := nativeParentInputRuntime(t, scenario)
			child := root.Child(childID)
			child.PersistErr = assert.AnError
			a.HandleOutput(scenario.Answer)
			a.viewWork.CloseAndWait()
			queue := agent.OutputQueueFor(a.sessions[scenario.ChildSessionID].sink)
			require.True(t, queue.HasPending())
			require.Nil(t, a.sessions[scenario.ChildSessionID].projection.failed)
			attempts := child.Messages()
			require.Greater(t, len(attempts), 1)
			for _, attempt := range attempts[1:] {
				assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, attempt.Source)
				assert.Equal(t, []byte(original), attempt.Content, "the answer cannot reach storage while its input is refused")
			}
			child.PersistErr = nil
			require.NoError(t, queue.Flush())
			attempts = child.Messages()
			require.GreaterOrEqual(t, len(attempts), 4)
			assert.Equal(t, []byte(original), attempts[len(attempts)-2].Content)
			assert.Equal(t, []byte(scenario.Answer), attempts[len(attempts)-1].Content)
			count := 0
			for _, attempt := range attempts {
				if slices.Equal(attempt.Content, scenario.Answer) {
					count++
				}
			}
			assert.Equal(t, 1, count)
			assert.Empty(t, root.Messages())
		})
	}
}

func TestNativeParentInputDeadlineRetainsItsBlockedAnswer(t *testing.T) {
	for _, scenario := range nativeParentInputCases(t) {
		t.Run(scenario.Name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				scenario.ParentRecords = slices.Clone(scenario.ParentRecords)
				receipt, err := decodeNativeRecord(scenario.Records[1], scenario.ParentSessionID)
				require.NoError(t, err)
				for index, raw := range scenario.ParentRecords {
					record, err := decodeNativeRecord(raw, scenario.ParentSessionID)
					require.NoError(t, err)
					if record.ID == receipt.ID {
						scenario.ParentRecords[index] = rewriteNativeInputJSON(t, raw, json.RawMessage("\"unrelated_event\""), "payload", "event", "kind")
					}
				}
				clock := testutil.NewQuartzMock(t)
				a, root, childID, _ := nativeParentInputRuntimeWithClock(t, scenario, clock)
				created, stopped := testutil.NewTimerTraps(t, clock, childProjectionTimerTag)
				a.HandleOutput(scenario.Answer)
				ctx := testutil.DeadlineContext(t)
				delay := testutil.WaitForTimer(t, ctx, created)
				synctest.Wait()
				state := a.sessions[scenario.ChildSessionID]
				a.stateMu.Lock()
				failed := state.projection.failed
				a.stateMu.Unlock()
				require.NoError(t, failed, "complete native evidence without a matching receipt must wait for the operation deadline")
				testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopped)
				a.viewWork.CloseAndWait()
				requireFailedParentInputProjection(t, a, root, childID, scenario)
				assert.Contains(t, fmt.Sprint(state.projection.failed), "deadline exceeded")
			})
		})
	}
}

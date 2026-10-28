package muse

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type nativeParentInputCase struct {
	Name             string            `json:"name"`
	ParentSessionID  string            `json:"parentSessionId"`
	ChildSessionID   string            `json:"childSessionId"`
	SubagentID       string            `json:"subagentId"`
	RunID            string            `json:"runId"`
	InitialObjective string            `json:"initialObjective"`
	ExpectedBody     string            `json:"expectedBody"`
	Original         json.RawMessage   `json:"original"`
	Records          []json.RawMessage `json:"records"`
	ParentRecords    []json.RawMessage `json:"parentRecords"`
	ChildRecords     []json.RawMessage `json:"childRecords"`
	Answer           json.RawMessage   `json:"answer"`
}

func nativeParentInputCases(t *testing.T) []nativeParentInputCase {
	t.Helper()
	raw, err := os.ReadFile(testutil.RepoPath(t, "testdata", "muse_parent_input_conformance.json"))
	require.NoError(t, err)
	var fixture struct {
		Cases []nativeParentInputCase `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(raw, &fixture))
	require.NotEmpty(t, fixture.Cases)
	return fixture.Cases
}

func rewriteNativeInputJSON(t *testing.T, raw json.RawMessage, value json.RawMessage, path ...string) json.RawMessage {
	t.Helper()
	require.NotEmpty(t, path)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields))
	if len(path) == 1 {
		if value == nil {
			delete(fields, path[0])
		} else {
			fields[path[0]] = value
		}
	} else {
		fields[path[0]] = rewriteNativeInputJSON(t, fields[path[0]], value, path[1:]...)
	}
	result, err := json.Marshal(fields)
	require.NoError(t, err)
	return result
}

func parentInputSubject(t *testing.T, scenario nativeParentInputCase, transform func(json.RawMessage) json.RawMessage) (*nativeLog, storedRecord, nativeParentInputTarget) {
	t.Helper()
	log := newNativeLog(scenario.ParentSessionID)
	drain, err := decodeNativeRecord(scenario.Original, scenario.ParentSessionID)
	require.NoError(t, err)
	for _, raw := range scenario.ParentRecords {
		if transform != nil {
			raw = transform(raw)
		}
		if raw != nil {
			require.NoError(t, log.add(raw))
		}
	}
	return log, log.records[drain.ID], nativeParentInputTarget{ParentSessionID: scenario.ParentSessionID, ChildSessionID: scenario.ChildSessionID, SubagentID: scenario.SubagentID, RunID: scenario.RunID}
}

func TestNativeParentInputMatcherPreservesTheNativeEvidence(t *testing.T) {
	for _, scenario := range nativeParentInputCases(t) {
		t.Run(scenario.Name, func(t *testing.T) {
			log, drain, target := parentInputSubject(t, scenario, nil)
			result := matchNativeParentInput(log, drain, target)
			require.Equal(t, nativeParentInputMatched, result.Status, result.Reason)
			assert.Equal(t, drain.raw, result.Original)
			require.Len(t, result.Records, 5)
			for index, record := range scenario.Records {
				assert.JSONEq(t, string(record), string(result.Records[index]))
			}
			assert.NotEmpty(t, result.IdempotencyKey)
			assert.NotEmpty(t, result.InboxID)
			assert.Equal(t, result.IdempotencyKey, matchNativeParentInput(log, drain, target).IdempotencyKey)
		})
	}
}

func TestNativeParentInputMatcherUsesEvidenceInsteadOfOptionalMetadata(t *testing.T) {
	for _, scenario := range nativeParentInputCases(t) {
		for _, change := range []struct {
			name  string
			path  []string
			value json.RawMessage
		}{
			{name: "absent caller", path: []string{"payload", "event", "delivery_snapshot", "source", "caller"}},
			{name: "independent version metadata", path: []string{"payload", "event", "delivery_snapshot", "source", "schema_version"}, value: json.RawMessage(`3`)},
			{name: "absent causation", path: []string{"causation_id"}},
			{name: "unknown causation", path: []string{"causation_id"}, value: json.RawMessage(`null`)},
			{name: "absent owner run", path: []string{"payload", "event", "delivery_snapshot", "source", "parent_run_id"}},
			{name: "different owner run", path: []string{"payload", "event", "delivery_snapshot", "source", "parent_run_id"}, value: json.RawMessage(`"different-owner-run"`)},
			{name: "independent disposition", path: []string{"payload", "event", "delivery_snapshot", "disposition"}, value: json.RawMessage(`"interrupt"`)},
		} {
			t.Run(scenario.Name+"/"+change.name, func(t *testing.T) {
				identity, err := decodeNativeRecord(scenario.Original, scenario.ParentSessionID)
				require.NoError(t, err)
				log, drain, target := parentInputSubject(t, scenario, func(raw json.RawMessage) json.RawMessage {
					record, err := decodeNativeRecord(raw, scenario.ParentSessionID)
					require.NoError(t, err)
					if record.ID == identity.ID {
						return rewriteNativeInputJSON(t, raw, change.value, change.path...)
					}
					return raw
				})
				result := matchNativeParentInput(log, drain, target)
				assert.Equal(t, nativeParentInputMatched, result.Status, result.Reason)
			})
		}
	}
}

func TestNativeParentInputMatcherDistinguishesMissingAndContradictoryEvidence(t *testing.T) {
	for _, scenario := range nativeParentInputCases(t) {
		t.Run(scenario.Name, func(t *testing.T) {
			var batch nativeRecord
			require.NoError(t, json.Unmarshal(scenario.Records[1], &batch))
			log, drain, target := parentInputSubject(t, scenario, func(raw json.RawMessage) json.RawMessage {
				record, err := decodeNativeRecord(raw, scenario.ParentSessionID)
				require.NoError(t, err)
				if record.ID == batch.ID {
					return nil
				}
				return raw
			})
			assert.Equal(t, nativeParentInputPending, matchNativeParentInput(log, drain, target).Status)
			require.NoError(t, log.add(scenario.Records[1]))
			assert.Equal(t, nativeParentInputMatched, matchNativeParentInput(log, drain, target).Status)
			conflict := rewriteNativeInputJSON(t, scenario.Records[1], json.RawMessage(`"contradictory-batch"`), "payload", "event", "batch_id")
			require.Error(t, log.add(conflict))
			assert.Equal(t, nativeParentInputInvalid, matchNativeParentInput(log, drain, target).Status)
		})
	}
}

func TestNativeParentInputMatcherRequiresPositiveHumanProvenance(t *testing.T) {
	for _, caller := range []struct {
		name  string
		value json.RawMessage
		want  nativeParentInputStatus
	}{
		{name: "human", value: json.RawMessage(`{"kind":"user","actor_id":"msp-wire"}`), want: nativeParentInputHuman},
		{name: "child agent", value: json.RawMessage(`{"kind":"child_agent","actor_id":"child"}`), want: nativeParentInputInvalid},
		{name: "null", value: json.RawMessage(`null`), want: nativeParentInputInvalid},
		{name: "empty actor", value: json.RawMessage(`{"kind":"parent_agent","actor_id":""}`), want: nativeParentInputInvalid},
	} {
		t.Run(caller.name, func(t *testing.T) {
			scenario := nativeParentInputCases(t)[0]
			identity, err := decodeNativeRecord(scenario.Original, scenario.ParentSessionID)
			require.NoError(t, err)
			log, drain, target := parentInputSubject(t, scenario, func(raw json.RawMessage) json.RawMessage {
				record, err := decodeNativeRecord(raw, scenario.ParentSessionID)
				require.NoError(t, err)
				if record.ID == identity.ID {
					return rewriteNativeInputJSON(t, raw, caller.value, "payload", "event", "delivery_snapshot", "source", "caller")
				}
				return raw
			})
			assert.Equal(t, caller.want, matchNativeParentInput(log, drain, target).Status)
		})
	}
}

func nativeParentInputRuntime(t *testing.T, scenario nativeParentInputCase) (*Agent, *agenttest.Sink, string, json.RawMessage) {
	t.Helper()
	return nativeParentInputRuntimeWithClock(t, scenario, nil)
}

func nativeParentInputRuntimeWithClock(t *testing.T, scenario nativeParentInputCase, clock quartz.Clock) (*Agent, *agenttest.Sink, string, json.RawMessage) {
	t.Helper()
	a, root := testAgentWithClock(t, clock)
	root.UpdateSessionID(scenario.ParentSessionID)
	a.sessionID = scenario.ParentSessionID
	parent := &sessionState{sink: a.sink, items: make(map[string]*itemState), completed: make(map[string]turnCompletionState), log: newNativeLog(scenario.ParentSessionID)}
	a.sessions = map[string]*sessionState{scenario.ParentSessionID: parent}
	drain, err := decodeNativeRecord(scenario.Original, scenario.ParentSessionID)
	require.NoError(t, err)
	var original json.RawMessage
	for _, raw := range scenario.ParentRecords {
		record, err := parent.log.addRecord(raw)
		require.NoError(t, err)
		if record.ID == drain.ID {
			original = raw
		}
	}
	require.NotEmpty(t, original)
	key := museChildRowKey(scenario.ParentSessionID, scenario.SubagentID)
	id, err := a.sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "spawn", ProviderChildKey: key, AgentSessionID: scenario.ChildSessionID, Title: scenario.InitialObjective})
	require.NoError(t, err)
	require.NoError(t, a.sink.PersistChildPrompt(id, scenario.InitialObjective))
	cursor := "accepted-prefix"
	child := &sessionState{sink: a.sink.ChildSink(id), childID: id, childKey: key, parentSessionID: scenario.ParentSessionID, subagentID: scenario.SubagentID, items: make(map[string]*itemState), completed: make(map[string]turnCompletionState), log: newNativeLog(scenario.ChildSessionID), childViewPrefix: childViewPrefixReady, acceptedViewCursor: &cursor}
	for _, raw := range scenario.ChildRecords {
		require.NoError(t, child.log.add(raw))
	}
	a.sessions[scenario.ChildSessionID] = child
	a.handshake.GrantedCapabilities = []string{"rawLog"}
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		if method != methodLogStreams {
			t.Errorf("unexpected native provenance method %s", method)
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"unexpected method"}`)}
		}
		var params struct {
			SessionID string `json:"sessionId"`
		}
		require.NoError(t, json.Unmarshal(raw, &params))
		records := scenario.ParentRecords
		if params.SessionID == scenario.ChildSessionID {
			records = scenario.ChildRecords
		} else {
			assert.Equal(t, scenario.ParentSessionID, params.SessionID)
		}
		record, err := decodeNativeRecord(records[len(records)-1], params.SessionID)
		require.NoError(t, err)
		reply, err := json.Marshal(map[string]any{"streams": []any{map[string]any{"stream": nativeStream{Kind: "session", ID: params.SessionID}, "tail": nativePosition{ID: record.ID, Sequence: record.Sequence}}}})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: reply}
	}})
	return a, root, id, original
}

func TestNativeParentInputPersistsBeforeItsChildAnswer(t *testing.T) {
	for _, scenario := range nativeParentInputCases(t) {
		t.Run(scenario.Name, func(t *testing.T) {
			a, root, id, original := nativeParentInputRuntime(t, scenario)
			a.HandleOutput(scenario.Answer)
			a.viewWork.CloseAndWait()
			rows := root.Child(id).Messages()
			require.Equal(t, 3, len(rows), "the native parent instruction must appear between the opening assignment and the answer")
			assert.Equal(t, []byte(original), rows[1].Content)
			assert.Equal(t, scenario.ChildSessionID, rows[1].AgentSessionID)
			assert.Equal(t, []byte(scenario.Answer), rows[2].Content)
			var supplement struct {
				Records []json.RawMessage `json:"nativeRecords"`
			}
			require.NoError(t, json.Unmarshal(rows[1].SupplementalContent, &supplement))
			require.Len(t, supplement.Records, 5)
			for index, record := range scenario.Records {
				assert.JSONEq(t, string(record), string(supplement.Records[index]))
			}
		})
	}
}

func TestNativeParentInputMatcherKeepsExactCommandRetriesOneDelivery(t *testing.T) {
	for _, scenario := range nativeParentInputCases(t) {
		for _, conflicting := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/conflicting=%v", scenario.Name, conflicting), func(t *testing.T) {
				log, drain, target := parentInputSubject(t, scenario, nil)
				original := matchNativeParentInput(log, drain, target)
				require.Equal(t, nativeParentInputMatched, original.Status)
				tail := log.contiguousThrough.Sequence
				var origin, receipt map[string]any
				require.NoError(t, json.Unmarshal(scenario.Records[0], &origin))
				require.NoError(t, json.Unmarshal(scenario.Records[1], &receipt))
				origin["id"], origin["sequence"] = "retried-origin", tail+1
				receipt["id"], receipt["sequence"] = "retried-receipt", tail+2
				originEvent := origin["payload"].(map[string]any)["event"].(map[string]any)
				originEvent["message_id"] = "retried-batch"
				call := originEvent["tool_calls"].([]any)[0].(map[string]any)
				call["id"], call["call_id"] = "retried-message", "retried-call"
				if conflicting {
					var args map[string]any
					require.NoError(t, json.Unmarshal([]byte(call["args"].(string)), &args))
					args["message"] = "a conflicting retry body"
					encoded, err := json.Marshal(args)
					require.NoError(t, err)
					call["args"] = string(encoded)
				}
				resultEvent := receipt["payload"].(map[string]any)["event"].(map[string]any)
				resultEvent["batch_id"] = "retried-batch"
				entry := resultEvent["results"].([]any)[0].(map[string]any)
				entry["tool_call_id"] = "retried-call"
				var result map[string]any
				require.NoError(t, json.Unmarshal([]byte(entry["text"].(string)), &result))
				result["status"] = "duplicate"
				encoded, err := json.Marshal(result)
				require.NoError(t, err)
				entry["text"] = string(encoded)
				for _, record := range []map[string]any{origin, receipt} {
					raw, err := json.Marshal(record)
					require.NoError(t, err)
					require.NoError(t, log.add(raw))
				}
				matched := matchNativeParentInput(log, drain, target)
				if conflicting {
					assert.Equal(t, nativeParentInputInvalid, matched.Status, matched.Reason)
				} else {
					require.Equal(t, nativeParentInputMatched, matched.Status, matched.Reason)
					assert.Equal(t, original.IdempotencyKey, matched.IdempotencyKey)
					assert.Equal(t, original.Records, matched.Records)
				}
			})
		}
	}
}

func earlierProvedChildText(t *testing.T, a *Agent, scenario nativeParentInputCase) (*sessionState, childInputMarker, json.RawMessage) {
	t.Helper()
	var answer struct {
		Params itemParams `json:"params"`
	}
	require.NoError(t, json.Unmarshal(scenario.Answer, &answer))
	state := a.sessions[scenario.ChildSessionID]
	state.projection = newChildProjection()
	var marker *childInputMarker
	for sequence := int64(1); sequence <= state.log.contiguousThrough.Sequence; sequence++ {
		next, err := decodeChildInputMarker(state.log, state.log.records[state.log.sequences[sequence]])
		require.NoError(t, err)
		if next != nil && next.runID == scenario.RunID {
			marker = next
		}
	}
	require.NotNil(t, marker)
	require.Greater(t, marker.position.Sequence, int64(1))
	first := state.log.records[state.log.sequences[1]].record
	params := itemParams{SessionID: scenario.ChildSessionID, SourceRange: nativeSourceRange{Stream: first.Stream, First: nativePosition{ID: first.ID, Sequence: first.Sequence}, Last: nativePosition{ID: first.ID, Sequence: first.Sequence}}, Item: nativeItem{ID: answer.Params.Item.ID, Kind: contracts.MuseItemKindAgentMessage, Status: contracts.MuseItemStatusInProgress, Revision: 1, TurnID: &scenario.RunID, Text: "Earlier proved text"}}
	raw, err := json.Marshal(map[string]any{"method": contracts.MuseMethodItemUpdated, "params": params})
	require.NoError(t, err)
	state.items[params.Item.ID] = &itemState{params: params, raw: raw}
	return state, *marker, raw
}

func TestNativeParentInputWaitsForEarlierUnfinishedProvedText(t *testing.T) {
	for _, scenario := range nativeParentInputCases(t) {
		t.Run(scenario.Name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				a, root, id, _ := nativeParentInputRuntime(t, scenario)
				state, marker, _ := earlierProvedChildText(t, a, scenario)
				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(context.Canceled)
				done := make(chan error, 1)
				go func() { done <- a.proveChildInputMarker(ctx, scenario.ChildSessionID, state, marker) }()
				synctest.Wait()
				assert.Equal(t, 1, len(root.Child(id).Messages()), "a later input must not precede earlier unfinished native text")
				var result error
				early := false
				select {
				case result = <-done:
					early = true
					t.Error("the unresolved earlier text must keep the operation pending")
				default:
				}
				cancel(context.DeadlineExceeded)
				synctest.Wait()
				if !early {
					result = <-done
				}
				require.ErrorIs(t, result, context.DeadlineExceeded)
			})
		})
	}
}

func TestNativeParentInputRequiresTheFinalRevisionOfItsEarlierText(t *testing.T) {
	for _, scenario := range nativeParentInputCases(t) {
		for _, mode := range []string{"own final revision", "another item", "old revision", "earlier source", "foreign source", "different stream"} {
			t.Run(scenario.Name+"/"+mode, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					a, root, id, original := nativeParentInputRuntime(t, scenario)
					state, marker, partial := earlierProvedChildText(t, a, scenario)
					ctx, cancel := context.WithCancelCause(t.Context())
					defer cancel(context.Canceled)
					done := make(chan error, 1)
					go func() { done <- a.proveChildInputMarker(ctx, scenario.ChildSessionID, state, marker) }()
					synctest.Wait()
					require.Equal(t, 1, len(root.Child(id).Messages()))
					future := scenario.Answer
					switch mode {
					case "another item":
						future = rewriteNativeInputJSON(t, future, json.RawMessage(`"different-item"`), "params", "item", "itemId")
					case "old revision":
						future = rewriteNativeInputJSON(t, future, json.RawMessage(`1`), "params", "item", "revision")
					case "earlier source":
						first := state.log.records[state.log.sequences[1]].record
						position, err := json.Marshal(nativePosition{ID: first.ID, Sequence: first.Sequence})
						require.NoError(t, err)
						future = rewriteNativeInputJSON(t, future, position, "params", "sourceRange", "first")
						future = rewriteNativeInputJSON(t, future, position, "params", "sourceRange", "last")
					case "foreign source":
						future = rewriteNativeInputJSON(t, future, json.RawMessage(`"different-source"`), "params", "sourceRange", "first", "id")
					case "different stream":
						future = rewriteNativeInputJSON(t, future, json.RawMessage(`"different-child"`), "params", "sourceRange", "stream", "id")
					}
					a.stateMu.Lock()
					state.projection.frames = append(state.projection.frames, childProjectionFrame{raw: future, serial: 1})
					state.projection.bufferedBytes = len(future)
					state.projection.signal()
					a.stateMu.Unlock()
					synctest.Wait()
					if mode == "own final revision" {
						require.NoError(t, <-done)
						require.Equal(t, 2, len(root.Child(id).Messages()))
						assert.Equal(t, []byte(original), root.Child(id).Messages()[1].Content)
						require.NoError(t, a.applyChildProjectionSegment(scenario.ChildSessionID, state, []childProjectionFrame{{raw: future, serial: 1}}))
						require.Equal(t, 3, len(root.Child(id).Messages()))
						assert.Equal(t, []byte(scenario.Answer), root.Child(id).Messages()[2].Content)
					} else {
						assert.Equal(t, 1, len(root.Child(id).Messages()), "unrelated or unproved completion cannot authorize the later input")
						cancel(context.DeadlineExceeded)
						synctest.Wait()
						failure := <-done
						require.ErrorIs(t, failure, context.DeadlineExceeded)
						a.failChildProjection(scenario.ChildSessionID, state, failure)
						assert.True(t, slices.ContainsFunc(root.Messages(), func(message agenttest.Message) bool { return slices.Equal(message.Content, partial) }), "strict failure retains the earlier proved native revision")
					}
				})
			})
		}
	}
}

func TestNativeParentInputMatcherRequiresProofForEitherOwnedAgentKind(t *testing.T) {
	for _, scenario := range nativeParentInputCases(t) {
		for _, missing := range []int{-1, 0, 1} {
			t.Run(fmt.Sprintf("%s/missing=%d", scenario.Name, missing), func(t *testing.T) {
				drainIdentity, err := decodeNativeRecord(scenario.Original, scenario.ParentSessionID)
				require.NoError(t, err)
				omitted := ""
				if missing >= 0 {
					var record nativeRecord
					require.NoError(t, json.Unmarshal(scenario.Records[missing], &record))
					omitted = record.ID
				}
				caller, err := json.Marshal(map[string]string{"kind": "child_agent", "actor_id": scenario.ParentSessionID})
				require.NoError(t, err)
				log, drain, target := parentInputSubject(t, scenario, func(raw json.RawMessage) json.RawMessage {
					record, err := decodeNativeRecord(raw, scenario.ParentSessionID)
					require.NoError(t, err)
					if record.ID == omitted {
						return nil
					}
					if record.ID == drainIdentity.ID {
						return rewriteNativeInputJSON(t, raw, caller, "payload", "event", "delivery_snapshot", "source", "caller")
					}
					return raw
				})
				result := matchNativeParentInput(log, drain, target)
				want := nativeParentInputMatched
				if missing >= 0 {
					want = nativeParentInputPending
				}
				assert.Equal(t, want, result.Status, result.Reason)
			})
		}
	}
}

func TestNativeWorkflowChildOwnershipUsesItsIndependentPayloadVersion(t *testing.T) {
	raw, err := os.ReadFile(testutil.RepoPath(t, "testdata", "muse_workflow_child_ownership.json"))
	require.NoError(t, err)
	var fixture struct {
		ParentSessionID string            `json:"parentSessionId"`
		ChildSessionID  string            `json:"childSessionId"`
		SubagentID      string            `json:"subagentId"`
		RunID           string            `json:"runId"`
		Records         []json.RawMessage `json:"records"`
	}
	require.NoError(t, json.Unmarshal(raw, &fixture))
	log := newNativeLog(fixture.ParentSessionID)
	for _, record := range fixture.Records {
		require.NoError(t, log.add(record))
	}
	proved, err := initialChildInputProof(log, nativeParentInputTarget{ParentSessionID: fixture.ParentSessionID, ChildSessionID: fixture.ChildSessionID, SubagentID: fixture.SubagentID, RunID: fixture.RunID})
	require.NoError(t, err)
	assert.True(t, proved, "the actual workflow spawn payload version does not change its proven owner")
}

func TestNativeParentInputMatcherChecksThePayloadVersionIndependently(t *testing.T) {
	for _, scenario := range nativeParentInputCases(t) {
		for _, version := range []struct {
			name  string
			value json.RawMessage
			want  nativeParentInputStatus
		}{
			{"independent positive version", json.RawMessage(`4`), nativeParentInputMatched},
			{"zero", json.RawMessage(`0`), nativeParentInputInvalid},
			{"negative", json.RawMessage(`-1`), nativeParentInputInvalid},
			{"fraction", json.RawMessage(`1.5`), nativeParentInputInvalid},
		} {
			t.Run(scenario.Name+"/"+version.name, func(t *testing.T) {
				log, drain, target := parentInputSubject(t, scenario, func(raw json.RawMessage) json.RawMessage {
					return rewriteNativeInputJSON(t, raw, version.value, "payload_schema_version")
				})
				result := matchNativeParentInput(log, drain, target)
				assert.Equal(t, version.want, result.Status, result.Reason)
			})
		}
	}
}
